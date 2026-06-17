package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrsmile/goid3db/internal/audio"
	"github.com/jrsmile/goid3db/internal/index"
	"github.com/jrsmile/goid3db/internal/model"
	"github.com/jrsmile/goid3db/internal/scan"
	"github.com/jrsmile/goid3db/internal/search"
	"github.com/jrsmile/goid3db/internal/watch"
)

// --- helpers ---

func guardVars(t *testing.T) {
	t.Helper()
	a, b, c, d, e, f, g, h, i, j, k := openIndex, runIndexLibrary, loadAllTracks,
		newAudioEngine, listDevices, startWatch, scanLibrary, runProgram, runFn, batchSize, osExit
	t.Cleanup(func() {
		openIndex, runIndexLibrary, loadAllTracks, newAudioEngine, listDevices,
			startWatch, scanLibrary, runProgram, runFn, batchSize, osExit = a, b, c, d, e, f, g, h, i, j, k
	})
}

func ssafe(n int) []byte {
	return []byte{byte((n >> 21) & 0x7f), byte((n >> 14) & 0x7f), byte((n >> 7) & 0x7f), byte(n & 0x7f)}
}

func id3MP3(t *testing.T, dir, name string) string {
	t.Helper()
	// Minimal ID3v2.4 tag with a TIT2 (title) frame so dhowden/tag parses it.
	data := append([]byte{0x03}, []byte("Song")...)
	frame := append([]byte("TIT2"), ssafe(len(data))...)
	frame = append(frame, 0, 0)
	frame = append(frame, data...)
	hdr := append([]byte("ID3"), 0x04, 0x00, 0x00)
	hdr = append(hdr, ssafe(len(frame))...)
	hdr = append(hdr, frame...)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, hdr, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func tempDB(t *testing.T) string { return filepath.Join(t.TempDir(), "index.sqlite") }

// --- realMain ---

func TestRealMainParseError(t *testing.T) {
	if code := realMain([]string{"-totally-unknown-flag"}); code != 2 {
		t.Fatalf("parse error should exit 2, got %d", code)
	}
}

func TestRealMainSuccess(t *testing.T) {
	guardVars(t)
	runFn = func(string, string, int, bool) error { return nil }
	if code := realMain([]string{"-no-watch"}); code != 0 {
		t.Fatalf("success should exit 0, got %d", code)
	}
}

func TestRealMainRunError(t *testing.T) {
	guardVars(t)
	runFn = func(string, string, int, bool) error { return errors.New("boom") }
	if code := realMain(nil); code != 1 {
		t.Fatalf("run error should exit 1, got %d", code)
	}
}

func TestMainInProcess(t *testing.T) {
	guardVars(t)
	savedArgs := os.Args
	t.Cleanup(func() { os.Args = savedArgs })

	var code int
	osExit = func(c int) { code = c }
	runFn = func(string, string, int, bool) error { return nil }
	os.Args = []string{"goid3db", "-no-watch"}
	main()
	if code != 0 {
		t.Fatalf("main should exit 0, got %d", code)
	}
}

// --- run ---

func TestRunOpenError(t *testing.T) {
	guardVars(t)
	openIndex = func(string) (*index.Index, error) { return nil, errors.New("open failed") }
	if err := run(".", tempDB(t), 1, false); err == nil {
		t.Fatal("expected open error")
	}
}

func TestRunIndexError(t *testing.T) {
	guardVars(t)
	runIndexLibrary = func(context.Context, *index.Index, string, int) error { return errors.New("idx") }
	if err := run(t.TempDir(), tempDB(t), 1, false); err == nil {
		t.Fatal("expected index error")
	}
}

func TestRunLoadError(t *testing.T) {
	guardVars(t)
	runIndexLibrary = func(context.Context, *index.Index, string, int) error { return nil }
	loadAllTracks = func(*index.Index, context.Context) ([]model.Track, error) {
		return nil, errors.New("load")
	}
	if err := run(t.TempDir(), tempDB(t), 1, false); err == nil {
		t.Fatal("expected load error")
	}
}

func TestRunAudioError(t *testing.T) {
	guardVars(t)
	runIndexLibrary = func(context.Context, *index.Index, string, int) error { return nil }
	loadAllTracks = func(*index.Index, context.Context) ([]model.Track, error) { return nil, nil }
	newAudioEngine = func() (*audio.Engine, error) { return nil, errors.New("audio") }
	if err := run(t.TempDir(), tempDB(t), 1, false); err == nil {
		t.Fatal("expected audio error")
	}
}

func TestRunWatchAndDevicesWarn(t *testing.T) {
	guardVars(t)
	runIndexLibrary = func(context.Context, *index.Index, string, int) error { return nil }
	loadAllTracks = func(*index.Index, context.Context) ([]model.Track, error) { return nil, nil }
	listDevices = func(*audio.Engine) ([]audio.Device, error) { return nil, errors.New("no devices") }
	runProgram = func(tea.Model) error { return nil }
	// doWatch=true with a real, existing root succeeds and starts handleEvents.
	if err := run(t.TempDir(), tempDB(t), 1, true); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestRunWatchError(t *testing.T) {
	guardVars(t)
	runIndexLibrary = func(context.Context, *index.Index, string, int) error { return nil }
	loadAllTracks = func(*index.Index, context.Context) ([]model.Track, error) { return nil, nil }
	startWatch = func(context.Context, string, time.Duration) (<-chan watch.Event, error) {
		return nil, errors.New("watch failed")
	}
	runProgram = func(tea.Model) error { return nil }
	if err := run(t.TempDir(), tempDB(t), 1, true); err != nil {
		t.Fatalf("watcher error should only warn, got %v", err)
	}
}

func TestRunNoWatchProgramError(t *testing.T) {
	guardVars(t)
	runIndexLibrary = func(context.Context, *index.Index, string, int) error { return nil }
	loadAllTracks = func(*index.Index, context.Context) ([]model.Track, error) { return nil, nil }
	runProgram = func(tea.Model) error { return errors.New("program") }
	if err := run(t.TempDir(), tempDB(t), 1, false); err == nil {
		t.Fatal("expected program error")
	}
}

// --- indexInto (deterministic via fakes) ---

type fakeWriter struct {
	putErr    error
	commitErr []error
	commitN   int
}

func (w *fakeWriter) Put(context.Context, *model.Track) error { return w.putErr }
func (w *fakeWriter) Commit() error {
	var e error
	if w.commitN < len(w.commitErr) {
		e = w.commitErr[w.commitN]
	}
	w.commitN++
	return e
}

type fakeLib struct {
	stats    map[string]index.Stat
	statsErr error
	begins   []error
	beginN   int
	writer   *fakeWriter
	count    int64
}

func (l *fakeLib) PathStats(context.Context) (map[string]index.Stat, error) {
	return l.stats, l.statsErr
}
func (l *fakeLib) BeginBatch(context.Context) (batchWriter, error) {
	var e error
	if l.beginN < len(l.begins) {
		e = l.begins[l.beginN]
	}
	l.beginN++
	if e != nil {
		return nil, e
	}
	return l.writer, nil
}
func (l *fakeLib) Count(context.Context) (int64, error) { return l.count, nil }

func feed(rs ...scan.Result) func(context.Context, scan.Options) <-chan scan.Result {
	return func(_ context.Context, opts scan.Options) <-chan scan.Result {
		if opts.Skip != nil {
			opts.Skip("p", 1, 2) // matches stats -> true
			opts.Skip("q", 9, 9) // missing -> false
		}
		ch := make(chan scan.Result, len(rs))
		for _, r := range rs {
			ch <- r
		}
		close(ch)
		return ch
	}
}

func TestIndexIntoPathStatsError(t *testing.T) {
	guardVars(t)
	lib := &fakeLib{statsErr: errors.New("stats")}
	if err := indexInto(context.Background(), lib, ".", 1); err == nil {
		t.Fatal("expected path-stats error")
	}
}

func TestIndexIntoBeginError(t *testing.T) {
	guardVars(t)
	scanLibrary = feed()
	lib := &fakeLib{begins: []error{errors.New("begin")}}
	if err := indexInto(context.Background(), lib, ".", 1); err == nil {
		t.Fatal("expected begin-batch error")
	}
}

func TestIndexIntoHappyPath(t *testing.T) {
	guardVars(t)
	batchSize = 1
	tr := model.Track{Path: "/a.mp3", Title: "A"}
	tr2 := model.Track{Path: "/b.mp3", Title: "B"}
	scanLibrary = feed(
		scan.Result{Err: errors.New("bad file")}, // errs++
		scan.Result{},                            // Track nil -> continue
		scan.Result{Track: &tr},                  // Put + mid commit
		scan.Result{Track: &tr2},                 // Put + mid commit
	)
	lib := &fakeLib{stats: map[string]index.Stat{"p": {ModTime: 1, Size: 2}}, writer: &fakeWriter{}, count: 2}
	if err := indexInto(context.Background(), lib, ".", 1); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestIndexIntoPutError(t *testing.T) {
	guardVars(t)
	tr := model.Track{Path: "/a.mp3"}
	scanLibrary = feed(scan.Result{Track: &tr})
	lib := &fakeLib{writer: &fakeWriter{putErr: errors.New("put")}}
	if err := indexInto(context.Background(), lib, ".", 1); err != nil {
		t.Fatalf("put error should be counted, not returned: %v", err)
	}
}

func TestIndexIntoMidCommitError(t *testing.T) {
	guardVars(t)
	batchSize = 1
	tr := model.Track{Path: "/a.mp3"}
	scanLibrary = feed(scan.Result{Track: &tr})
	lib := &fakeLib{writer: &fakeWriter{commitErr: []error{errors.New("commit")}}}
	if err := indexInto(context.Background(), lib, ".", 1); err == nil {
		t.Fatal("expected mid-loop commit error")
	}
}

func TestIndexIntoMidBeginError(t *testing.T) {
	guardVars(t)
	batchSize = 1
	tr := model.Track{Path: "/a.mp3"}
	scanLibrary = feed(scan.Result{Track: &tr})
	lib := &fakeLib{begins: []error{nil, errors.New("begin2")}, writer: &fakeWriter{}}
	if err := indexInto(context.Background(), lib, ".", 1); err == nil {
		t.Fatal("expected mid-loop begin error")
	}
}

func TestIndexIntoFinalCommitError(t *testing.T) {
	guardVars(t)
	batchSize = 2000
	tr := model.Track{Path: "/a.mp3"}
	scanLibrary = feed(scan.Result{Track: &tr})
	lib := &fakeLib{writer: &fakeWriter{commitErr: []error{errors.New("final")}}}
	if err := indexInto(context.Background(), lib, ".", 1); err == nil {
		t.Fatal("expected final commit error")
	}
}

// --- indexLibrary adapter (real index) ---

func TestIndexLibraryReal(t *testing.T) {
	dir := t.TempDir()
	id3MP3(t, dir, "song.mp3")
	ix, err := index.Open(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ctx := context.Background()
	if err := indexLibrary(ctx, ix, dir, 1); err != nil {
		t.Fatalf("first index: %v", err)
	}
	// Second pass exercises the skip path for unchanged files.
	if err := indexLibrary(ctx, ix, dir, 1); err != nil {
		t.Fatalf("second index: %v", err)
	}
}

// --- handleEvents ---

func TestHandleEvents(t *testing.T) {
	dir := t.TempDir()
	good := id3MP3(t, dir, "good.mp3")
	garbage := filepath.Join(dir, "garbage.mp3")
	if err := os.WriteFile(garbage, []byte("not audio at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	matcher := search.New(nil, 1)
	ctx := context.Background()

	ev := make(chan watch.Event, 8)
	ev <- watch.Event{Op: watch.OpUpsert, Path: good}                     // success
	ev <- watch.Event{Op: watch.OpUpsert, Path: filepath.Join(dir, "no")} // stat error
	ev <- watch.Event{Op: watch.OpUpsert, Path: garbage}                  // fallback parse
	ev <- watch.Event{Op: watch.OpRemove, Path: good}                     // remove
	close(ev)
	handleEvents(ctx, ev, ix, matcher)

	// Upsert error branch: a closed index makes ix.Upsert fail.
	ix.Close()
	ev2 := make(chan watch.Event, 1)
	ev2 <- watch.Event{Op: watch.OpUpsert, Path: good}
	close(ev2)
	handleEvents(ctx, ev2, ix, matcher)
}

func TestHandleEventsParseError(t *testing.T) {
	dir := t.TempDir()
	// A file that stat succeeds on but cannot be opened (no read perm) makes
	// scan.ParseFile fail, exercising the parse-error continue branch.
	noperm := filepath.Join(dir, "noperm.mp3")
	if err := os.WriteFile(noperm, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	ix, err := index.Open(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	ev := make(chan watch.Event, 1)
	ev <- watch.Event{Op: watch.OpUpsert, Path: noperm}
	close(ev)
	handleEvents(context.Background(), ev, ix, search.New(nil, 1))
}

// --- default injectables / adapters ---

type quitModel struct{}

func (quitModel) Init() tea.Cmd                       { return tea.Quit }
func (quitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return quitModel{}, tea.Quit }
func (quitModel) View() string                        { return "" }

func TestDefaultInjectables(t *testing.T) {
	ix, err := index.Open(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	// Default loadAllTracks closure.
	if _, err := loadAllTracks(ix, context.Background()); err != nil {
		t.Fatalf("loadAllTracks: %v", err)
	}
	// Default listDevices closure against a real engine.
	if eng, err := newAudioEngine(); err == nil {
		_, _ = listDevices(eng)
		eng.Close()
	}
	// Default runProgram closure with a model that quits immediately.
	if err := runProgram(quitModel{}); err != nil {
		t.Logf("runProgram returned: %v", err)
	}
}

func TestIndexLibBeginBatchError(t *testing.T) {
	ix, err := index.Open(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ix.Close()
	if _, err := (indexLib{ix}).BeginBatch(context.Background()); err == nil {
		t.Fatal("expected begin-batch error on closed index")
	}
}
