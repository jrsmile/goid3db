package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cilium/ebpf/ringbuf"
	"github.com/rjeczalik/notify"
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

// scriptRec is one item a scriptedReader hands back from ReadInto.
type scriptRec struct {
	sample []byte
	err    error
}

// scriptedReader is a fake ringReader letting tests drive the watcher loop
// without privileges. ReadInto returns queued records, blocks when empty, and
// reports ErrClosed once Close is called.
type scriptedReader struct {
	ch     chan scriptRec
	closed chan struct{}
	once   sync.Once
}

func newScriptedReader(buf int) *scriptedReader {
	return &scriptedReader{ch: make(chan scriptRec, buf), closed: make(chan struct{})}
}

func (r *scriptedReader) push(rec scriptRec) { r.ch <- rec }

func (r *scriptedReader) ReadInto(rec *ringbuf.Record) error {
	select {
	case sr := <-r.ch:
		if sr.err != nil {
			return sr.err
		}
		rec.RawSample = sr.sample
		return nil
	case <-r.closed:
		return ringbuf.ErrClosed
	}
}

func (r *scriptedReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

// mkSample builds a ring buffer payload with an absolute (already resolved)
// pathname so decode does not need /proc.
func mkSample(op byte, name string) []byte {
	b := make([]byte, 12+len(name)+1)
	b[0] = op
	copy(b[12:], name)
	return b
}

// startOrSkip starts a watcher, skipping the test when no watcher can be
// started (which should not happen for a valid directory).
func startOrSkip(t *testing.T, ctx context.Context, dir string, debounce time.Duration) <-chan Event {
	t.Helper()
	ch, err := Watch(ctx, dir, debounce)
	if err != nil {
		t.Skipf("watcher unavailable: %v", err)
	}
	return ch
}

// withGeteuid forces the effective uid Watch sees, so the eBPF (root) and
// inotify (non-root) dispatch branches can be exercised deterministically.
func withGeteuid(t *testing.T, uid int) {
	t.Helper()
	saved := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = saved })
}

func TestIsAudio(t *testing.T) {
	if !isAudio("/x/song.MP3") {
		t.Error("mp3 should be audio")
	}
	if isAudio("/x/readme.txt") {
		t.Error("txt should not be audio")
	}
}

func TestUnderRoot(t *testing.T) {
	root := "/music"
	if !underRoot(root, "/music") {
		t.Error("root itself should be under root")
	}
	if !underRoot(root, "/music/a/b.mp3") {
		t.Error("descendant should be under root")
	}
	if underRoot(root, "/musichall/x.mp3") {
		t.Error("sibling prefix should not be under root")
	}
	if underRoot(root, "/other/x.mp3") {
		t.Error("unrelated path should not be under root")
	}
}

func TestResolvePath(t *testing.T) {
	// Absolute names are returned unchanged.
	if got := resolvePath(1, atFDCWD, "/abs/song.mp3"); got != "/abs/song.mp3" {
		t.Errorf("absolute path not preserved: %q", got)
	}
	// A relative name against this process's cwd resolves under that directory.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	pid := int32(os.Getpid())
	got := resolvePath(pid, atFDCWD, "track.mp3")
	if got != filepath.Join(cwd, "track.mp3") {
		// /proc/<pid>/cwd may itself be a symlink target; compare resolved dirs.
		gotDir, _ := filepath.EvalSymlinks(filepath.Dir(got))
		wantDir, _ := filepath.EvalSymlinks(cwd)
		if filepath.Base(got) != "track.mp3" || gotDir != wantDir {
			t.Errorf("resolvePath(cwd) = %q, want under %q", got, cwd)
		}
	}
	// An invalid pid/fd yields an empty string.
	if got := resolvePath(-1, 999999, "track.mp3"); got != "" {
		t.Errorf("expected empty path for bad fd, got %q", got)
	}
}

func TestDecode(t *testing.T) {
	root := "/music"

	mk := func(op uint8, pid, dfd int32, name string) []byte {
		b := make([]byte, 12+len(name)+1)
		b[0] = op
		b[4] = byte(pid) // small values fit in the low byte (little-endian)
		b[8] = byte(dfd)
		copy(b[12:], name)
		return b
	}

	if ev, ok := decode(root, mk(0, 0, 0, "/music/a/song.mp3")); !ok ||
		ev.Op != OpUpsert || ev.Path != "/music/a/song.mp3" {
		t.Errorf("upsert decode failed: %+v ok=%v", ev, ok)
	}
	if ev, ok := decode(root, mk(opRemove, 0, 0, "/music/a/song.mp3")); !ok ||
		ev.Op != OpRemove {
		t.Errorf("remove decode failed: %+v ok=%v", ev, ok)
	}
	if _, ok := decode(root, mk(0, 0, 0, "/music/a/note.txt")); ok {
		t.Error("non-audio should be dropped")
	}
	if _, ok := decode(root, mk(0, 0, 0, "/other/song.mp3")); ok {
		t.Error("path outside root should be dropped")
	}
	if _, ok := decode(root, mk(0, 0, 0, "")); ok {
		t.Error("empty name should be dropped")
	}
	if _, ok := decode(root, []byte{1, 2, 3}); ok {
		t.Error("short sample should be dropped")
	}
	if _, ok := decode(root, mk(0, -1, 0, "x.mp3")); ok {
		t.Error("unresolvable relative path should be dropped")
	}
}

func TestWatchErrorOnMissingRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Watch(ctx, filepath.Join(t.TempDir(), "does", "not", "exist"), 10*time.Millisecond); err == nil {
		t.Error("expected error watching a missing root")
	}
}

func TestWatchErrorOnNonDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.mp3")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Watch(ctx, file, 10*time.Millisecond); err == nil {
		t.Error("expected error watching a non-directory")
	}
}

func TestWatchUpsertAndRemove(t *testing.T) {
	withGeteuid(t, 1000) // exercise the inotify fallback path
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := startOrSkip(t, ctx, dir, 20*time.Millisecond)

	// A non-audio file exercises the isAudio filter.
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

	withGeteuid(t, 1000) // exercise the inotify fallback path
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := startOrSkip(t, ctx, dir, 20*time.Millisecond)

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

// withSetup temporarily replaces the package setup hook and restores it.
func withSetup(t *testing.T, fn func() (ringReader, func(), error)) {
	t.Helper()
	saved := setup
	setup = fn
	t.Cleanup(func() { setup = saved })
}

func TestWatchEBPFFallsBackToInotify(t *testing.T) {
	withGeteuid(t, 0) // pretend to be root so the eBPF path is attempted
	withSetup(t, func() (ringReader, func(), error) {
		return nil, nil, errors.New("no privileges")
	})
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := Watch(ctx, dir, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("expected inotify fallback, got error: %v", err)
	}

	// The fallback watcher must still deliver events.
	mp3 := filepath.Join(dir, "fallback.mp3")
	if err := os.WriteFile(mp3, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if e, ok := waitEvent(t, ch, 3*time.Second); !ok || e.Op != OpUpsert {
		t.Fatalf("expected upsert from inotify fallback, got %+v ok=%v", e, ok)
	}
}

func TestWatchInotifyError(t *testing.T) {
	withGeteuid(t, 1000) // force the inotify path
	saved := notifyWatch
	notifyWatch = func(string, chan<- notify.EventInfo, ...notify.Event) error {
		return errors.New("inotify failed")
	}
	defer func() { notifyWatch = saved }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Watch(ctx, t.TempDir(), 10*time.Millisecond); err == nil {
		t.Error("expected error when inotify setup fails")
	}
}

func TestWatchGetwdError(t *testing.T) {
	savedWd := getwd
	getwd = func() (string, error) { return "", errors.New("no cwd") }
	defer func() { getwd = savedWd }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Watch(ctx, "relative/path", 10*time.Millisecond); err == nil {
		t.Error("expected error when getwd fails")
	}
}

func TestWatchRelativeRootSuccess(t *testing.T) {
	withGeteuid(t, 0) // force the eBPF path
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	savedWd := getwd
	getwd = func() (string, error) { return parent, nil }
	defer func() { getwd = savedWd }()

	rd := newScriptedReader(4)
	var cleaned int32
	withSetup(t, func() (ringReader, func(), error) {
		return rd, func() { atomic.StoreInt32(&cleaned, 1) }, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Watch(ctx, "music", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	song := filepath.Join(parent, "music", "track.mp3")
	rd.push(scriptRec{sample: mkSample(0, song)})
	e, ok := waitEvent(t, ch, time.Second)
	if !ok || e.Op != OpUpsert || e.Path != song {
		t.Fatalf("unexpected event: %+v ok=%v", e, ok)
	}

	cancel()
	for {
		if _, ok := waitEvent(t, ch, 2*time.Second); !ok {
			break
		}
	}
	if atomic.LoadInt32(&cleaned) != 1 {
		t.Error("cleanup not invoked")
	}
}

// TestRunDeliversCoalescesAndCloses exercises run's happy path plus the
// decode-failure, transient-error, timer-reset and reader-closed branches.
func TestRunDeliversCoalescesAndCloses(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "song.mp3")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rd := newScriptedReader(8)
	var cleaned int32
	out := make(chan Event, 16)
	done := make(chan struct{})
	go func() {
		run(ctx, root, 10*time.Millisecond, rd, func() { atomic.StoreInt32(&cleaned, 1) }, out)
		close(done)
	}()

	rd.push(scriptRec{sample: []byte{1, 2, 3}})      // too short -> decode drops it
	rd.push(scriptRec{err: errors.New("transient")}) // non-closed error -> continue
	rd.push(scriptRec{sample: mkSample(0, song)})    // first event -> timer starts
	rd.push(scriptRec{sample: mkSample(0, song)})    // second event -> timer.Reset

	e, ok := waitEvent(t, out, time.Second)
	if !ok || e.Op != OpUpsert || e.Path != song {
		t.Fatalf("unexpected event: %+v ok=%v", e, ok)
	}

	// Closing the reader directly makes ReadInto report ErrClosed, which closes
	// the internal channel and returns run via the !ok branch.
	rd.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after reader close")
	}
	if _, ok := <-out; ok {
		t.Error("out channel should be closed")
	}
	if atomic.LoadInt32(&cleaned) != 1 {
		t.Error("cleanup not invoked")
	}
}

// TestRunFlushInterruptedByCancel covers the flush back-pressure branch where
// the context is cancelled while a debounced event is blocked on delivery.
func TestRunFlushInterruptedByCancel(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "a.mp3")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rd := newScriptedReader(4)
	out := make(chan Event) // unbuffered with no reader: flush blocks on send
	done := make(chan struct{})
	go func() {
		run(ctx, root, 10*time.Millisecond, rd, func() {}, out)
		close(done)
	}()

	rd.push(scriptRec{sample: mkSample(0, song)})
	time.Sleep(150 * time.Millisecond) // let the debounce timer fire into flush
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after cancel during flush")
	}
}

// TestRunReadBackpressureCancel covers the reader goroutine's ctx.Done branch
// where delivery into the internal channel is blocked because the main loop is
// stuck in a back-pressured flush.
func TestRunReadBackpressureCancel(t *testing.T) {
	root := t.TempDir()
	song := filepath.Join(root, "b.mp3")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rd := newScriptedReader(8192)
	out := make(chan Event) // unbuffered, no reader: main loop blocks in flush
	done := make(chan struct{})
	go func() {
		run(ctx, root, 10*time.Millisecond, rd, func() {}, out)
		close(done)
	}()

	// One event drives the main loop into a blocked flush.
	rd.push(scriptRec{sample: mkSample(0, song)})
	time.Sleep(150 * time.Millisecond)

	// Overfill the internal channel (cap 4096) so the reader goroutine blocks
	// on send while the main loop is parked in flush.
	for i := 0; i < 4200; i++ {
		rd.push(scriptRec{sample: mkSample(0, song)})
	}
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after back-pressure cancel")
	}
}
