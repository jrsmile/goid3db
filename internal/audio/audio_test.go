package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/malgo"
)

// fakeStreamer is a deterministic beep.StreamSeekCloser used to drive onData.
type fakeStreamer struct {
	remaining int
	closed    bool
}

func (f *fakeStreamer) Stream(s [][2]float64) (int, bool) {
	if f.remaining <= 0 {
		return 0, false
	}
	n := len(s)
	if n > f.remaining {
		n = f.remaining
	}
	for i := 0; i < n; i++ {
		s[i] = [2]float64{0.25, -0.25}
	}
	f.remaining -= n
	return n, n > 0
}
func (f *fakeStreamer) Err() error     { return nil }
func (f *fakeStreamer) Len() int       { return 100 }
func (f *fakeStreamer) Position() int  { return 0 }
func (f *fakeStreamer) Seek(int) error { return nil }
func (f *fakeStreamer) Close() error   { f.closed = true; return nil }

func writeWAV(t *testing.T, path string, frames int) {
	t.Helper()
	const sampleRate, channels, bits = 44100, 2, 16
	dataSize := frames * channels * bits / 8
	var b bytes.Buffer
	le := binary.LittleEndian
	w16 := func(v uint16) { _ = binary.Write(&b, le, v) }
	w32 := func(v uint32) { _ = binary.Write(&b, le, v) }
	b.WriteString("RIFF")
	w32(uint32(36 + dataSize))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	w32(16)
	w16(1) // PCM
	w16(channels)
	w32(sampleRate)
	w32(sampleRate * channels * bits / 8)
	w16(channels * bits / 8)
	w16(bits)
	b.WriteString("data")
	w32(uint32(dataSize))
	for i := 0; i < frames*channels; i++ {
		w16(0)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOnDataPlaybackAndFinish(t *testing.T) {
	p := &Player{streamer: &fakeStreamer{remaining: 4}, done: make(chan struct{})}
	out := make([]byte, 8*8) // 8 frames * 8 bytes/frame
	// First call streams 4 frames then finishes (read < n), filling the rest
	// with silence; closes Done.
	p.onData(out, nil, 8)
	select {
	case <-p.Done():
	default:
		t.Fatal("expected Done to be closed after underrun")
	}
	if !p.finished {
		t.Error("expected player marked finished")
	}
	// Second call: already finished -> no streaming, scratch reused (no grow).
	p.onData(out, nil, 8)
}

func TestOnDataPausedSilence(t *testing.T) {
	p := &Player{streamer: &fakeStreamer{remaining: 100}, done: make(chan struct{})}
	p.TogglePause()
	if !p.Paused() {
		t.Fatal("expected paused")
	}
	out := make([]byte, 8*4)
	p.onData(out, nil, 4) // paused -> all silence, not finished
	if p.finished {
		t.Error("paused playback should not finish")
	}
}

func TestStopNilDeviceAndStreamer(t *testing.T) {
	// Stop with no device and no streamer hits the nil guards.
	(&Player{done: make(chan struct{})}).Stop()
	// Stop with a streamer but no device closes the streamer.
	fs := &fakeStreamer{remaining: 1}
	p := &Player{streamer: fs, done: make(chan struct{})}
	p.Stop()
	if !fs.closed {
		t.Error("expected streamer closed on Stop")
	}
}

func TestMarkFinishedIdempotent(t *testing.T) {
	p := &Player{done: make(chan struct{})}
	p.markFinished()
	p.markFinished() // second call must not re-close the channel
}

func TestDecode(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "ok.wav")
	writeWAV(t, wav, 16)
	if _, _, err := decode(wav); err != nil {
		t.Errorf("expected wav to decode, got %v", err)
	}
	// Each branch is reached; encoded formats error on junk but the line runs.
	for _, ext := range []string{".mp3", ".flac", ".ogg", ".oga"} {
		p := filepath.Join(dir, "junk"+ext)
		if err := os.WriteFile(p, []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, _ = decode(p)
	}
	unsupported := filepath.Join(dir, "x.xyz")
	if err := os.WriteFile(unsupported, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decode(unsupported); err == nil {
		t.Error("expected unsupported-format error")
	}
	if _, _, err := decode(filepath.Join(dir, "missing.wav")); err == nil {
		t.Error("expected open error for missing file")
	}
}

func TestEngineLifecycle(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatalf("engine init: %v", err)
	}
	defer e.Close()

	if _, err := e.Devices(); err != nil {
		t.Fatalf("Devices: %v", err)
	}

	dir := t.TempDir()
	wav := filepath.Join(dir, "play.wav")
	writeWAV(t, wav, 64)

	// Happy path on the default device.
	if p, err := e.Play(wav, nil); err != nil {
		t.Fatalf("Play: %v", err)
	} else {
		p.Stop()
	}

	// Non-default device exercises the DeviceID branch (init may still fail).
	if p, err := e.Play(wav, &Device{Name: "x", IsDefault: false}); err == nil {
		p.Stop()
	}

	// Decode error.
	if _, err := e.Play(filepath.Join(dir, "nope.wav"), nil); err == nil {
		t.Error("expected decode error")
	}
}

func TestCloseNilContext(t *testing.T) {
	(&Engine{}).Close()
}

func TestNewContextError(t *testing.T) {
	orig := initContext
	initContext = func([]malgo.Backend, malgo.ContextConfig, malgo.LogProc) (*malgo.AllocatedContext, error) {
		return nil, errors.New("boom")
	}
	defer func() { initContext = orig }()
	if _, err := New(); err == nil {
		t.Error("expected context init error")
	}
}

func TestDevicesError(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	orig := listDevices
	listDevices = func(*malgo.AllocatedContext) ([]malgo.DeviceInfo, error) {
		return nil, errors.New("enum failed")
	}
	defer func() { listDevices = orig }()
	if _, err := e.Devices(); err == nil {
		t.Error("expected devices error")
	}
}

func TestPlayInitDeviceError(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	dir := t.TempDir()
	wav := filepath.Join(dir, "x.wav")
	writeWAV(t, wav, 16)

	orig := initDevice
	initDevice = func(malgo.Context, malgo.DeviceConfig, malgo.DeviceCallbacks) (*malgo.Device, error) {
		return nil, errors.New("init device failed")
	}
	defer func() { initDevice = orig }()
	if _, err := e.Play(wav, nil); err == nil {
		t.Error("expected init device error")
	}
}

func TestPlayStartDeviceError(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	dir := t.TempDir()
	wav := filepath.Join(dir, "x.wav")
	writeWAV(t, wav, 16)

	orig := startDevice
	startDevice = func(*malgo.Device) error { return errors.New("start failed") }
	defer func() { startDevice = orig }()
	if _, err := e.Play(wav, nil); err == nil {
		t.Error("expected start device error")
	}
}
