// Package watch provides recursive filesystem watching so that newly added or
// changed audio files are reflected in the index "instantly". It uses
// rjeczalik/notify for native recursive watching, which scales to deep trees
// far better than watching each directory individually.
package watch

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/jrsmile/goid3db/internal/scan"
	"github.com/rjeczalik/notify"
)

// Op describes what happened to a path.
type Op uint8

const (
	// OpUpsert means the file was created or modified and should be reindexed.
	OpUpsert Op = iota
	// OpRemove means the file was deleted or moved away.
	OpRemove
)

// Event is a debounced filesystem change for a single audio file.
type Event struct {
	Op   Op
	Path string
}

// outBuffer is the capacity of the delivery channel. It is a variable (not a
// const) so tests can shrink it to deterministically exercise the back-pressure
// path where a flush is interrupted by context cancellation.
var outBuffer = 1024

// Watch begins recursively watching root and delivers debounced Events on the
// returned channel until ctx is cancelled. The buffer should be generous since
// large copies can burst many events.
func Watch(ctx context.Context, root string, debounce time.Duration) (<-chan Event, error) {
	raw := make(chan notify.EventInfo, 4096)
	// "..." requests recursive watching of the whole subtree.
	if err := notify.Watch(filepath.Join(root, "..."), raw,
		notify.Create, notify.Write, notify.Remove, notify.Rename); err != nil {
		return nil, err
	}

	out := make(chan Event, outBuffer)
	go func() {
		defer notify.Stop(raw)
		defer close(out)

		// Coalesce rapid repeated events per path (e.g. streamed writes).
		pending := make(map[string]Op)
		var timer *time.Timer
		var timerC <-chan time.Time

		flush := func() {
			for p, op := range pending {
				select {
				case <-ctx.Done():
					return
				case out <- Event{Op: op, Path: p}:
				}
			}
			pending = make(map[string]Op)
			timerC = nil
		}

		for {
			select {
			case <-ctx.Done():
				return
			case ei := <-raw:
				p := ei.Path()
				if !isAudio(p) {
					continue
				}
				switch ei.Event() {
				case notify.Remove, notify.Rename:
					pending[p] = OpRemove
				default: // Create, Write
					pending[p] = OpUpsert
				}
				if timer == nil {
					timer = time.NewTimer(debounce)
				} else {
					timer.Reset(debounce)
				}
				timerC = timer.C
			case <-timerC:
				flush()
			}
		}
	}()

	return out, nil
}

func isAudio(p string) bool {
	return scan.AudioExts[strings.ToLower(filepath.Ext(p))]
}
