package watch

import (
	"context"
	"path/filepath"
	"time"

	"github.com/rjeczalik/notify"
)

// notifyWatch and notifyStop wrap the rjeczalik/notify entry points so tests can
// substitute them (in particular to exercise the setup-error branch).
var (
	notifyWatch = notify.Watch
	notifyStop  = notify.Stop
)

// watchInotify is the fallback watcher used when eBPF is unavailable, e.g. the
// process is not running with root / CAP_BPF privileges. It uses rjeczalik/notify
// for native recursive inotify watching and emits the same debounced Events as
// the eBPF path, so callers cannot tell the two apart.
func watchInotify(ctx context.Context, root string, debounce time.Duration) (<-chan Event, error) {
	raw := make(chan notify.EventInfo, 4096)
	// "..." requests recursive watching of the whole subtree.
	if err := notifyWatch(filepath.Join(root, "..."), raw,
		notify.Create, notify.Write, notify.Remove, notify.Rename); err != nil {
		return nil, err
	}

	out := make(chan Event, outBuffer)
	go func() {
		defer notifyStop(raw)
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
