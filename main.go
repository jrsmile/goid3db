// Command goid3db is a CLI/TUI music library indexer and player built to scale
// to ~1,000,000 audio files: a permanent SQLite index, an in-memory fzf-style
// fuzzy search over all ID3/metadata fields, instant incremental indexing via a
// recursive filesystem watcher, and playback to a selectable output device
// (default soundcard or Bluetooth sink).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jrsmile/goid3db/internal/audio"
	"github.com/jrsmile/goid3db/internal/index"
	"github.com/jrsmile/goid3db/internal/model"
	"github.com/jrsmile/goid3db/internal/scan"
	"github.com/jrsmile/goid3db/internal/search"
	"github.com/jrsmile/goid3db/internal/tui"
	"github.com/jrsmile/goid3db/internal/watch"
)

// External, hard-to-test steps are indirected through package variables so the
// unit tests can stub hardware/TTY dependencies and force every error branch.
var (
	openIndex       = index.Open
	runIndexLibrary = indexLibrary
	loadAllTracks   = func(ix *index.Index, ctx context.Context) ([]model.Track, error) {
		return ix.LoadAll(ctx)
	}
	newAudioEngine = audio.New
	listDevices    = func(e *audio.Engine) ([]audio.Device, error) { return e.Devices() }
	startWatch     = watch.Watch
	scanLibrary    = scan.Scan
	runProgram     = func(m tea.Model) error {
		_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
		return err
	}
	runFn = run

	// osExit is indirected so tests can invoke main without terminating the
	// test process.
	osExit = os.Exit

	// batchSize is the number of upserts per transaction. It is a variable so
	// tests can exercise the mid-loop commit path without millions of files.
	batchSize = 2000
)

func main() {
	osExit(realMain(os.Args[1:]))
}

// realMain parses flags and runs the application, returning a process exit code.
// It is separated from main so it can be unit-tested without calling os.Exit.
func realMain(args []string) int {
	fs := flag.NewFlagSet("goid3db", flag.ContinueOnError)
	root := fs.String("root", ".", "music library root directory")
	db := fs.String("db", "goid3db.sqlite", "path to the index database")
	workers := fs.Int("workers", runtime.GOMAXPROCS(0)*2, "number of scan workers")
	noWatch := fs.Bool("no-watch", false, "disable the filesystem watcher")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := runFn(*root, *db, *workers, !*noWatch); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func run(root, db string, workers int, doWatch bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ix, err := openIndex(db)
	if err != nil {
		return err
	}
	defer ix.Close()

	if err := runIndexLibrary(ctx, ix, root, workers); err != nil {
		return err
	}

	fmt.Print("Loading index into memory… ")
	tracks, err := loadAllTracks(ix, ctx)
	if err != nil {
		return err
	}
	matcher := search.New(tracks, runtime.GOMAXPROCS(0))
	fmt.Printf("%d tracks ready.\n", matcher.Len())

	engine, err := newAudioEngine()
	if err != nil {
		return fmt.Errorf("audio init: %w", err)
	}
	defer engine.Close()
	devices, err := listDevices(engine)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not list audio devices:", err)
	}

	if doWatch {
		events, err := startWatch(ctx, root, 300*time.Millisecond)
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: watcher disabled:", err)
		} else {
			go handleEvents(ctx, events, ix, matcher)
		}
	}

	return runProgram(tui.New(matcher, engine, devices))
}

// indexLibrary performs the initial/refresh scan, skipping files whose
// mod-time and size are unchanged, and upserts results in batched transactions.
func indexLibrary(ctx context.Context, ix *index.Index, root string, workers int) error {
	return indexInto(ctx, indexLib{ix}, root, workers)
}

// library is the minimal index surface used by indexInto. Abstracting it lets
// tests drive every error branch deterministically.
type library interface {
	PathStats(ctx context.Context) (map[string]index.Stat, error)
	BeginBatch(ctx context.Context) (batchWriter, error)
	Count(ctx context.Context) (int64, error)
}

// batchWriter is the subset of *index.Writer used during indexing.
type batchWriter interface {
	Put(ctx context.Context, t *model.Track) error
	Commit() error
}

// indexLib adapts the concrete *index.Index to the library interface.
type indexLib struct{ ix *index.Index }

func (l indexLib) PathStats(ctx context.Context) (map[string]index.Stat, error) {
	return l.ix.PathStats(ctx)
}

func (l indexLib) BeginBatch(ctx context.Context) (batchWriter, error) {
	w, err := l.ix.BeginBatch(ctx)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (l indexLib) Count(ctx context.Context) (int64, error) { return l.ix.Count(ctx) }

func indexInto(ctx context.Context, lib library, root string, workers int) error {
	stats, err := lib.PathStats(ctx)
	if err != nil {
		return err
	}
	skip := func(path string, mod, size int64) bool {
		s, ok := stats[path]
		return ok && s.ModTime == mod && s.Size == size
	}

	fmt.Printf("Indexing %s …\n", root)
	results := scanLibrary(ctx, scan.Options{Root: root, Workers: workers, Skip: skip})

	writer, err := lib.BeginBatch(ctx)
	if err != nil {
		return err
	}
	var added, errs, sinceCommit int
	for r := range results {
		if r.Err != nil {
			errs++
			continue
		}
		if r.Track == nil {
			continue
		}
		if err := writer.Put(ctx, r.Track); err != nil {
			errs++
			continue
		}
		added++
		sinceCommit++
		if sinceCommit >= batchSize {
			if err := writer.Commit(); err != nil {
				return err
			}
			if writer, err = lib.BeginBatch(ctx); err != nil {
				return err
			}
			sinceCommit = 0
			fmt.Printf("\r  indexed %d new files…", added)
		}
	}
	if err := writer.Commit(); err != nil {
		return err
	}

	total, _ := lib.Count(ctx)
	fmt.Printf("\r  indexed %d new files, %d total, %d errors\n", added, total, errs)
	return nil
}

// handleEvents applies watcher events to both the durable index and the
// in-memory matcher so new/changed files appear "instantly".
func handleEvents(ctx context.Context, events <-chan watch.Event, ix *index.Index, matcher *search.Matcher) {
	for ev := range events {
		switch ev.Op {
		case watch.OpUpsert:
			fi, err := os.Stat(ev.Path)
			if err != nil {
				continue
			}
			t, err := scan.ParseFile(ev.Path, fi.ModTime().Unix(), fi.Size())
			if err != nil {
				continue
			}
			if err := ix.Upsert(ctx, t); err != nil {
				continue
			}
			matcher.Upsert(*t)
		case watch.OpRemove:
			_ = ix.Delete(ctx, ev.Path)
			matcher.RemoveByPath(ev.Path)
		}
	}
}
