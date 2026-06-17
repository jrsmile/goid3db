package tui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrsmile/goid3db/internal/audio"
	"github.com/jrsmile/goid3db/internal/model"
	"github.com/jrsmile/goid3db/internal/search"
)

// --- synthetic media helpers ---

func ssafe(n int) []byte {
	return []byte{byte((n >> 21) & 0x7f), byte((n >> 14) & 0x7f), byte((n >> 7) & 0x7f), byte(n & 0x7f)}
}

func id3Frame(id string, data []byte) []byte {
	b := []byte(id)
	b = append(b, ssafe(len(data))...)
	b = append(b, 0, 0)
	return append(b, data...)
}

func id3Tag(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	hdr := append([]byte("ID3"), 0x04, 0x00, 0x00)
	hdr = append(hdr, ssafe(len(body))...)
	return append(hdr, body...)
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 30), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func apicFrame(pic []byte) []byte {
	data := []byte{0x03}
	data = append(data, []byte("image/png")...)
	data = append(data, 0x00, 0x03, 0x00)
	data = append(data, pic...)
	return id3Frame("APIC", data)
}

func artMP3(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, id3Tag(apicFrame(pngBytes(t))), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeWAV(t *testing.T, dir, name string) string {
	t.Helper()
	const sampleRate, channels, bits, frames = 44100, 2, 16, 64
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
	w16(1)
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
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkTrack(id int64, path, title, artist, album, genre string, year int) model.Track {
	t := model.Track{ID: id, Path: path, Title: title, Artist: artist, Album: album, Genre: genre, Year: year}
	t.BuildHaystack()
	return t
}

func upd(m Model, msg tea.Msg) (Model, tea.Cmd) {
	nm, c := m.Update(msg)
	return nm.(Model), c
}

func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

type unknownMsg struct{}

func TestNewDeviceSelection(t *testing.T) {
	engine, err := audio.New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	matcher := search.New([]model.Track{mkTrack(1, "/a.mp3", "T", "A", "Al", "G", 2000)}, 2)

	// First device non-default, second default -> covers both branches of the
	// devIdx selection loop.
	devs := []audio.Device{{Name: "a", IsDefault: false}, {Name: "b", IsDefault: true}}
	m := New(matcher, engine, devs)
	if m.devIdx != 1 {
		t.Errorf("expected default device index 1, got %d", m.devIdx)
	}
}

func TestUpdateFlow(t *testing.T) {
	dir := t.TempDir()
	wav := writeWAV(t, dir, "money.wav")
	art := artMP3(t, dir, "time.mp3")

	tracks := []model.Track{
		mkTrack(1, wav, "Money", "Pink Floyd", "Dark Side", "Rock", 1973),
		mkTrack(2, art, "Time", "Pink Floyd", "Dark Side", "Rock", 1973),
		mkTrack(3, filepath.Join(dir, "missing.mp3"), "", "", "", "", 0),
	}
	matcher := search.New(tracks, 4)
	engine, err := audio.New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	devices, err := engine.Devices()
	if err != nil {
		t.Fatal(err)
	}

	m := New(matcher, engine, devices)

	// Init + command helpers.
	_ = m.Init()
	_ = tickEvery()()
	if _, ok := m.doSearch()().(searchResultMsg); !ok {
		t.Fatal("doSearch should yield a searchResultMsg")
	}

	// Window size makes the art panel viable.
	m, _ = upd(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Populate hits via a matching search result.
	hits := []search.Hit{
		{Track: &tracks[0], Score: 10},
		{Track: &tracks[1], Score: 9},
		{Track: &tracks[2], Score: 8},
	}
	m, cmd := upd(m, searchResultMsg{query: "", hits: hits})
	if cmd != nil {
		if msg := cmd(); msg != nil { // loadArtCmd for wav (no art) -> lines nil
			m, _ = upd(m, msg)
		}
	}

	// Move down onto the art track and execute its load command.
	m, cmd = upd(m, key(tea.KeyDown))
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m, _ = upd(m, msg) // matching artLoadedMsg -> sets artLines
		}
	}
	if len(m.artLines) == 0 {
		t.Error("expected album art lines to be loaded")
	}

	// Re-requesting art for the already-loaded selection is a no-op.
	if m.loadArtCmd() != nil {
		t.Error("already-loaded art should yield nil command")
	}
	// A too-narrow window disables the art panel even with a selection.
	narrow := m
	narrow.width = 50
	if narrow.loadArtCmd() != nil {
		t.Error("narrow window should yield nil art command")
	}

	// Non-matching artLoadedMsg is ignored.
	m, _ = upd(m, artLoadedMsg{path: "/somewhere/else.mp3", lines: []string{"x"}})

	// View with art panel + selection rendering.
	_ = m.View()

	// Navigation bounds.
	m, _ = upd(m, key(tea.KeyUp))    // cursor 1 -> 0
	m, _ = upd(m, key(tea.KeyUp))    // cursor 0, no-op
	m, _ = upd(m, key(tea.KeyCtrlK)) // alias, no-op at top
	m, _ = upd(m, key(tea.KeyDown))  // 0 -> 1
	m, _ = upd(m, key(tea.KeyCtrlJ)) // 1 -> 2
	m, _ = upd(m, key(tea.KeyDown))  // cursor 2, no-op at bottom

	// Play the bad-path track -> error branch.
	m, _ = upd(m, key(tea.KeyEnter))
	if !m.statusErr {
		t.Error("expected play error status")
	}

	// Play a valid track on the default device -> success + waitForFinish.
	m, _ = upd(m, key(tea.KeyUp)) // back to art track (valid mp3? no audio) -> use wav
	m, _ = upd(m, key(tea.KeyUp)) // cursor 0 = wav
	m2, cmd := upd(m, key(tea.KeyEnter))
	if cmd == nil || m2.player == nil {
		t.Fatal("expected a player and finish command")
	}
	// Pause + view paused state.
	m2, _ = upd(m2, key(tea.KeyCtrlP))
	_ = m2.View()
	// Finish the track to cover waitForFinish's closure, then deliver the msg.
	m2.player.Stop()
	if fmsg := cmd(); fmsg != nil {
		m2, _ = upd(m2, fmsg) // trackFinishedMsg -> clears nowPlaying
	}

	// ctrl+p with no player.
	m2, _ = upd(m2, key(tea.KeyCtrlP))

	// Stop clears now-playing.
	m2, _ = upd(m2, key(tea.KeyCtrlS))

	// Toggle album art off then on.
	m2, _ = upd(m2, key(tea.KeyCtrlA)) // off
	m2, _ = upd(m2, key(tea.KeyCtrlA)) // on

	// Device cycling.
	m2, _ = upd(m2, key(tea.KeyTab))

	// Typing forwards to the input and re-searches.
	m2, _ = upd(m2, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("money")})

	// Stale search result is ignored.
	m2, _ = upd(m2, searchResultMsg{query: "stale-query", hits: hits})

	// Cursor clamp when fewer results arrive.
	m2.cursor = 5
	m2, _ = upd(m2, searchResultMsg{query: m2.input.Value(), hits: nil})
	if m2.cursor != 0 {
		t.Errorf("expected cursor clamped to 0, got %d", m2.cursor)
	}

	// Tick triggers a re-search batch.
	m2, _ = upd(m2, tickMsg{})

	// Unknown message type falls through to the input.
	m2, _ = upd(m2, unknownMsg{})

	// Quit paths.
	_, qc := upd(m2, key(tea.KeyCtrlC))
	if qc == nil {
		t.Error("ctrl+c should return a quit command")
	}
	_, qc = upd(m2, key(tea.KeyEsc))
	if qc == nil {
		t.Error("esc should return a quit command")
	}
}

func TestEmptyDevicesAndDefaultPlayback(t *testing.T) {
	dir := t.TempDir()
	wav := writeWAV(t, dir, "ok.wav")
	tracks := []model.Track{mkTrack(1, wav, "Song", "", "", "", 0)}
	matcher := search.New(tracks, 2)
	engine, err := audio.New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	m := New(matcher, engine, nil) // no devices
	m, _ = upd(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = upd(m, searchResultMsg{query: "", hits: []search.Hit{{Track: &tracks[0], Score: 1}}})

	// tab with no devices is a no-op.
	m, _ = upd(m, key(tea.KeyTab))

	// Enter plays on the default device (dev == nil branch).
	m2, cmd := upd(m, key(tea.KeyEnter))
	if cmd == nil || m2.player == nil {
		t.Fatal("expected default-device playback")
	}
	m2.player.Stop()
	_ = cmd()
	_ = m2.View() // exercises "default" output label
}

func TestViewAndHelpers(t *testing.T) {
	matcher := search.New(nil, 1)
	m := New(matcher, nil, nil)

	// Empty results -> "no matches", empty status / now-playing.
	if got := m.View(); got == "" {
		t.Error("view should render")
	}

	// Enter with no results hits the playSelected out-of-range guard.
	if _, c := upd(m, key(tea.KeyEnter)); c != nil {
		t.Error("play with no selection should be a no-op")
	}

	// Status rendering (error and non-error).
	m.setStatus("oops", true)
	_ = m.View()
	m.setStatus("ok", false)
	_ = m.View()

	// visibleRows branches.
	m.height = 10
	if r := m.visibleRows(); r != 5 {
		t.Errorf("small height should clamp to 5, got %d", r)
	}
	m.height = 300
	if r := m.visibleRows(); r != m.limit {
		t.Errorf("large height should clamp to limit, got %d", r)
	}

	// artDims branches.
	m.showArt = true
	m.width = 120
	if c, _ := m.artDims(); c != 28 {
		t.Errorf("wide window should clamp art cols to 28, got %d", c)
	}
	m.width = 80
	if c, _ := m.artDims(); c != 20 {
		t.Errorf("expected 20 art cols, got %d", c)
	}
	m.width = 50
	if c, _ := m.artDims(); c != 0 {
		t.Errorf("narrow window should disable art, got %d", c)
	}
	m.showArt = false
	if c, _ := m.artDims(); c != 0 {
		t.Errorf("art off should disable art, got %d", c)
	}

	// loadArtCmd guards: no selection, and art disabled.
	if cmd := m.loadArtCmd(); cmd != nil {
		t.Error("no selection should yield nil art command")
	}

	// truncate branches.
	if truncate("short", 40) != "short" {
		t.Error("short string unchanged")
	}
	if truncate("abcdef", 1) != "a" {
		t.Error("n<=1 truncation")
	}
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Errorf("ellipsis truncation, got %q", got)
	}

	// displayName branches.
	if displayName(&model.Track{Title: "T", Artist: "A"}) != "A – T" {
		t.Error("artist + title")
	}
	if displayName(&model.Track{Title: "T"}) != "T" {
		t.Error("title only")
	}
	if displayName(&model.Track{Path: "/p.mp3"}) != "/p.mp3" {
		t.Error("path fallback")
	}

	// meta branches.
	if meta(&model.Track{Album: "Al", Genre: "G", Year: 1999}) == "" {
		t.Error("full meta")
	}
	if meta(&model.Track{}) != "" {
		t.Error("empty meta")
	}
}
