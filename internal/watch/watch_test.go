package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitEvent(t *testing.T, ch <-chan Event, timeout time.Duration) (Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(timeout):
		return Event{}, false
	}
}

func TestIsAudio(t *testing.T) {
	if !isAudio("/x/song.MP3") {
		t.Error("mp3 should be audio")
	}
	if isAudio("/x/readme.txt") {
		t.Error("txt should not be audio")
	}
}

func TestWatchErrorOnMissingRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Watch(ctx, filepath.Join(t.TempDir(), "does", "not", "exist"), 10*time.Millisecond); err == nil {
		t.Error("expected error watching a missing root")
	}
}

func TestWatchUpsertAndRemove(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := Watch(ctx, dir, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	// A non-audio file exercises the isAudio "continue" branch.
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mp3 := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(mp3, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, ok := waitEvent(t, ch, 3*time.Second)
	if !ok {
		t.Fatal("expected an upsert event")
	}
	if e.Op != OpUpsert || filepath.Base(e.Path) != "track.mp3" {
		t.Fatalf("unexpected event: %+v", e)
	}

	if err := os.Remove(mp3); err != nil {
		t.Fatal(err)
	}
	// Drain until we observe the remove (extra write events may arrive first).
	deadline := time.After(4 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Op == OpRemove {
				return
			}
		case <-deadline:
			t.Fatal("expected a remove event")
		}
	}
}

func TestWatchFlushInterruptedByCancel(t *testing.T) {
	saved := outBuffer
	outBuffer = 0 // unbuffered: flush blocks on send with no reader
	defer func() { outBuffer = saved }()

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := Watch(ctx, dir, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Give the debounce timer time to fire so flush blocks on the send, then
	// cancel to hit the back-pressure ctx.Done branch inside flush.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// The output channel must be closed once the goroutine returns.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("expected channel to close after cancel")
		}
	}
}
