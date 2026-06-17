package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// synchsafe encodes n as a 4-byte ID3v2 synch-safe integer (7 bits per byte).
func synchsafe(n int) []byte {
	return []byte{byte((n >> 21) & 0x7f), byte((n >> 14) & 0x7f), byte((n >> 7) & 0x7f), byte(n & 0x7f)}
}

// textFrame builds a UTF-8 ID3v2.4 text frame.
func textFrame(id, text string) []byte {
	data := append([]byte{0x03}, []byte(text)...) // 0x03 = UTF-8
	b := []byte(id)
	b = append(b, synchsafe(len(data))...)
	b = append(b, 0, 0) // flags
	return append(b, data...)
}

// buildID3 assembles a minimal valid ID3v2.4 tag from the given frames.
func buildID3(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	hdr := append([]byte("ID3"), 0x04, 0x00, 0x00) // v2.4, no flags
	hdr = append(hdr, synchsafe(len(body))...)
	return append(hdr, body...)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseFileWithTags(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tagged.mp3")
	writeFile(t, p, buildID3(
		textFrame("TIT2", "Real Title"),
		textFrame("TPE1", "Real Artist"),
		textFrame("TALB", "Real Album"),
		textFrame("TCON", "Jazz"),
		textFrame("TDRC", "1994"),
	))

	tr, err := ParseFile(p, 1, 2)
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	if tr.Title != "Real Title" || tr.Artist != "Real Artist" || tr.Album != "Real Album" {
		t.Errorf("tags not parsed: %+v", tr)
	}
	if tr.Haystack == "" {
		t.Error("expected haystack to be built")
	}
}

func TestParseFileTaggedNoTitleUsesFilename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "No Title Track.mp3")
	// Tag present but no TIT2 -> Title falls back to the filename.
	writeFile(t, p, buildID3(textFrame("TPE1", "Someone")))

	tr, err := ParseFile(p, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "No Title Track" {
		t.Errorf("expected filename title, got %q", tr.Title)
	}
	if tr.Artist != "Someone" {
		t.Errorf("expected artist from tag, got %q", tr.Artist)
	}
}

func TestParseFileOpenError(t *testing.T) {
	if _, err := ParseFile(filepath.Join(t.TempDir(), "missing.mp3"), 0, 0); err == nil {
		t.Error("expected error opening missing file")
	}
}

func TestParseStatError(t *testing.T) {
	r := parse(filepath.Join(t.TempDir(), "missing.mp3"), nil)
	if r == nil || r.Err == nil {
		t.Errorf("expected stat error result, got %+v", r)
	}
}

func TestParseUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks do not apply")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "locked.mp3")
	writeFile(t, p, []byte("body"))
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o644)
	// Stat succeeds but Open fails -> parse surfaces a ParseFile error.
	r := parse(p, nil)
	if r == nil || r.Err == nil {
		t.Errorf("expected open-permission error, got %+v", r)
	}
}

func TestProduceSkipsOnCancel(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, filepath.Join(dir, "f"+string(rune('a'+i))+".mp3"), []byte("x"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled -> first audio entry triggers SkipAll
	paths := make(chan string, 1)
	done := make(chan struct{})
	go func() { produce(ctx, dir, paths); close(done) }()
	for range paths {
	}
	<-done
}

func TestProduceWalkError(t *testing.T) {
	paths := make(chan string)
	done := make(chan struct{})
	// A non-existent root makes WalkDir invoke the callback with an error.
	go func() { produce(context.Background(), filepath.Join(t.TempDir(), "nope"), paths); close(done) }()
	for range paths {
	}
	<-done
}

func TestWorkerStopsOnCancel(t *testing.T) {
	paths := make(chan string, 1)
	paths <- "whatever.mp3"
	close(paths)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// ctx already cancelled -> worker returns before parsing.
	worker(ctx, paths, make(chan Result, 1), nil)
}

func TestWorkerStopsWhenEmitBlocked(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "song.mp3")
	writeFile(t, p, []byte("x"))
	paths := make(chan string, 1)
	paths <- p
	close(paths)

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result) // unbuffered, no reader -> emit blocks
	reached := make(chan struct{})
	// Skip runs inside parse, after the ctx.Err() guard has already passed for
	// this item, so signalling here guarantees the worker proceeds to emit.
	skip := func(string, int64, int64) bool { close(reached); return false }

	done := make(chan struct{})
	go func() { worker(ctx, paths, results, skip); close(done) }()
	<-reached
	cancel() // emit is (or will be) blocked; ctx.Done unblocks it -> return
	<-done
}

func TestEmitBranches(t *testing.T) {
	// Live context: the send succeeds.
	ch := make(chan Result, 1)
	if !emit(context.Background(), ch, Result{Path: "a"}) {
		t.Error("expected emit to send on live context")
	}
	// Cancelled context: the send is abandoned.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if emit(ctx, make(chan Result), Result{Path: "b"}) {
		t.Error("expected emit to abandon send on cancelled context")
	}
}

func TestScanDefaultWorkers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.mp3"), []byte("x"))
	n := 0
	// Workers unset (0) exercises the GOMAXPROCS default branch.
	for range Scan(context.Background(), Options{Root: dir}) {
		n++
	}
	if n != 1 {
		t.Errorf("expected 1 result, got %d", n)
	}
}

func TestWorkerEmitsAndSkips(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.mp3")
	skip := filepath.Join(dir, "skip.mp3")
	writeFile(t, keep, []byte("x"))
	writeFile(t, skip, []byte("x"))

	paths := make(chan string, 2)
	paths <- skip
	paths <- keep
	close(paths)

	results := make(chan Result, 2)
	skipFn := func(p string, _, _ int64) bool { return p == skip }
	worker(context.Background(), paths, results, skipFn)
	close(results)

	var got []string
	for r := range results {
		got = append(got, r.Path)
	}
	if len(got) != 1 || got[0] != keep {
		t.Errorf("expected only the kept file, got %v", got)
	}
}
