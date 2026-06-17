// Package scan walks the filesystem concurrently and parses audio metadata
// into model.Track values. Tag parsing is I/O bound, so a worker pool keeps the
// CPU and disk busy at the 1M-file scale.
package scan

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/dhowden/tag"
	"github.com/jrsmile/goid3db/internal/model"
)

// AudioExts are the file extensions treated as audio files.
var AudioExts = map[string]bool{
	".mp3": true, ".m4a": true, ".mp4": true,
	".flac": true, ".ogg": true, ".oga": true, ".opus": true,
}

// Skip reports whether a file at path with the given fs info already matches
// the index and can be skipped.
type Skip func(path string, modTime, size int64) bool

// Result carries a parsed track or the error encountered for a path.
type Result struct {
	Track *model.Track
	Err   error
	Path  string
}

// Options configure a Scan.
type Options struct {
	Root    string
	Workers int  // defaults to GOMAXPROCS
	Skip    Skip // optional; return true to skip an unchanged file
}

// Scan walks opts.Root and streams parsed tracks on the returned channel. The
// channel is closed when the walk completes or ctx is cancelled.
func Scan(ctx context.Context, opts Options) <-chan Result {
	if opts.Workers <= 0 {
		opts.Workers = runtime.GOMAXPROCS(0)
	}
	paths := make(chan string, opts.Workers*2)
	results := make(chan Result, opts.Workers*2)

	// Producer: walk the tree and enqueue candidate audio files.
	go produce(ctx, opts.Root, paths)

	// Workers: stat + parse.
	var wg sync.WaitGroup
	wg.Add(opts.Workers)
	for i := 0; i < opts.Workers; i++ {
		go func() {
			defer wg.Done()
			worker(ctx, paths, results, opts.Skip)
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	return results
}

// produce walks root and enqueues candidate audio paths until the walk ends or
// ctx is cancelled.
func produce(ctx context.Context, root string, paths chan<- string) {
	defer close(paths)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // ignore unreadable entries, keep walking
		}
		if d.IsDir() {
			return nil
		}
		if !AudioExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		select {
		case <-ctx.Done():
			return filepath.SkipAll
		case paths <- p:
		}
		return nil
	})
}

// worker drains paths, parses each file and forwards results, stopping early
// when ctx is cancelled.
func worker(ctx context.Context, paths <-chan string, results chan<- Result, skip Skip) {
	for p := range paths {
		if ctx.Err() != nil {
			return
		}
		r := parse(p, skip)
		if r == nil {
			continue // skipped
		}
		if !emit(ctx, results, *r) {
			return
		}
	}
}

// emit sends r unless ctx is cancelled first; it reports whether the send
// happened.
func emit(ctx context.Context, results chan<- Result, r Result) bool {
	select {
	case <-ctx.Done():
		return false
	case results <- r:
		return true
	}
}

// parse reads metadata for a single file. Returns nil if the file was skipped.
func parse(path string, skip Skip) *Result {
	fi, err := os.Stat(path)
	if err != nil {
		return &Result{Path: path, Err: err}
	}
	modTime, size := fi.ModTime().Unix(), fi.Size()
	if skip != nil && skip(path, modTime, size) {
		return nil
	}
	t, err := ParseFile(path, modTime, size)
	if err != nil {
		return &Result{Path: path, Err: err}
	}
	return &Result{Track: t, Path: path}
}

// ParseFile opens a single file and extracts its tags into a model.Track.
func ParseFile(path string, modTime, size int64) (*model.Track, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	t := &model.Track{
		Path:    path,
		ModTime: modTime,
		Size:    size,
		Folder:  filepath.Base(filepath.Dir(path)),
	}

	m, err := tag.ReadFrom(f)
	if err != nil {
		// Unreadable/missing tags: still index by filename so it's searchable.
		base := filepath.Base(path)
		t.Title = strings.TrimSuffix(base, filepath.Ext(base))
		t.BuildHaystack()
		return t, nil
	}

	t.Title = m.Title()
	t.Album = m.Album()
	t.Artist = m.Artist()
	t.Genre = m.Genre()
	t.Year = m.Year()
	if t.Title == "" {
		base := filepath.Base(path)
		t.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	t.BuildHaystack()
	return t, nil
}
