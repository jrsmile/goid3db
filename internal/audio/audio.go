// Package audio handles decoding and playback with explicit output-device
// selection, so the user can route sound to either the default soundcard or a
// Bluetooth sink (which the OS exposes as just another playback device).
//
// Decoding uses gopxl/beep (MP3/FLAC/OGG/WAV); the raw PCM is pushed into a
// miniaudio (gen2brain/malgo) playback device. malgo is used instead of plain
// oto because it can enumerate and pin a specific output device, which oto
// cannot.
package audio

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gen2brain/malgo"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"
)

// Engine owns the miniaudio context. Create one per process.
type Engine struct {
	ctx *malgo.AllocatedContext
}

// Device is a selectable playback endpoint (soundcard, Bluetooth sink, ...).
type Device struct {
	Name      string
	IsDefault bool
	id        malgo.DeviceID
}

// External malgo entry points are indirected through package variables so that
// tests can substitute the software "null" backend and deterministically
// exercise the error paths without real audio hardware.
var (
	initContext = malgo.InitContext
	listDevices = func(ctx *malgo.AllocatedContext) ([]malgo.DeviceInfo, error) {
		return ctx.Devices(malgo.Playback)
	}
	initDevice  = malgo.InitDevice
	startDevice = func(d *malgo.Device) error { return d.Start() }
)

// New initializes the audio engine using the platform's default backend.
func New() (*Engine, error) {
	return NewWithBackends()
}

// NewWithBackends initializes the audio engine with the given miniaudio
// backends (empty = auto-detect). Tests pass malgo.BackendNull to run without
// hardware.
func NewWithBackends(backends ...malgo.Backend) (*Engine, error) {
	ctx, err := initContext(backends, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}
	return &Engine{ctx: ctx}, nil
}

// Close releases the engine.
func (e *Engine) Close() {
	if e.ctx != nil {
		_ = e.ctx.Uninit()
		e.ctx.Free()
	}
}

// Devices lists available playback devices. The default device is flagged so
// the UI can pre-select it; Bluetooth speakers appear here once connected.
func (e *Engine) Devices() ([]Device, error) {
	infos, err := listDevices(e.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(infos))
	for _, di := range infos {
		out = append(out, Device{
			Name:      strings.TrimRight(di.Name(), "\x00 "),
			IsDefault: di.IsDefault != 0,
			id:        di.ID,
		})
	}
	return out, nil
}

// Player represents one playing track. It is safe for concurrent control calls.
type Player struct {
	mu       sync.Mutex
	device   *malgo.Device
	streamer beep.StreamSeekCloser
	format   beep.Format
	scratch  [][2]float64
	paused   bool
	finished bool
	done     chan struct{}
}

// Play decodes path and starts playback on dev (nil = default device).
func (e *Engine) Play(path string, dev *Device) (*Player, error) {
	streamer, format, err := decode(path)
	if err != nil {
		return nil, err
	}

	p := &Player{
		streamer: streamer,
		format:   format,
		done:     make(chan struct{}),
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatF32
	cfg.Playback.Channels = 2
	cfg.SampleRate = uint32(format.SampleRate)
	if dev != nil && !dev.IsDefault {
		id := dev.id
		cfg.Playback.DeviceID = id.Pointer()
	}

	device, err := initDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{
		Data: p.onData,
	})
	if err != nil {
		streamer.Close()
		return nil, fmt.Errorf("init device: %w", err)
	}
	p.device = device

	if err := startDevice(device); err != nil {
		device.Uninit()
		streamer.Close()
		return nil, fmt.Errorf("start device: %w", err)
	}
	return p, nil
}

// onData is the realtime callback that fills the output buffer with PCM.
func (p *Player) onData(out, _ []byte, frames uint32) {
	n := int(frames)
	if cap(p.scratch) < n {
		p.scratch = make([][2]float64, n)
	}
	buf := p.scratch[:n]

	p.mu.Lock()
	paused := p.paused
	finished := p.finished
	p.mu.Unlock()

	read := 0
	if !paused && !finished {
		read, _ = p.streamer.Stream(buf)
		if read < n {
			p.markFinished()
		}
	}

	// Write float32 little-endian interleaved stereo; silence for the remainder.
	o := out
	for i := 0; i < n; i++ {
		var l, r float64
		if i < read {
			l, r = buf[i][0], buf[i][1]
		}
		binary.LittleEndian.PutUint32(o[0:4], math.Float32bits(float32(l)))
		binary.LittleEndian.PutUint32(o[4:8], math.Float32bits(float32(r)))
		o = o[8:]
	}
}

func (p *Player) markFinished() {
	p.mu.Lock()
	if !p.finished {
		p.finished = true
		close(p.done)
	}
	p.mu.Unlock()
}

// TogglePause flips between playing and paused.
func (p *Player) TogglePause() {
	p.mu.Lock()
	p.paused = !p.paused
	p.mu.Unlock()
}

// Paused reports the current pause state.
func (p *Player) Paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// Done is closed when the track finishes naturally.
func (p *Player) Done() <-chan struct{} { return p.done }

// Stop halts playback and releases resources. It is idempotent: calling it more
// than once (or concurrently with natural completion) is safe.
func (p *Player) Stop() {
	p.markFinished()
	p.mu.Lock()
	dev := p.device
	p.device = nil
	p.mu.Unlock()
	if dev != nil {
		_ = dev.Stop()
		dev.Uninit()
	}
	if p.streamer != nil {
		p.streamer.Close()
	}
}

func decode(path string) (beep.StreamSeekCloser, beep.Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, beep.Format{}, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return mp3.Decode(f)
	case ".flac":
		return flac.Decode(f)
	case ".ogg", ".oga":
		return vorbis.Decode(f)
	case ".wav":
		return wav.Decode(f)
	default:
		f.Close()
		return nil, beep.Format{}, fmt.Errorf("unsupported audio format: %s", filepath.Ext(path))
	}
}
