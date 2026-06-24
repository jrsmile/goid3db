// Package watch provides recursive filesystem watching so that newly added or
// changed audio files are reflected in the index "instantly".
//
// When the process runs with root privileges it uses eBPF (via cilium/ebpf):
// syscall tracepoints report opens with write intent (creates/modifications),
// unlinks (deletions) and renames (moves) across the whole system. User space
// reconstructs the absolute path from the syscall pathname and the calling
// process's /proc entry, then filters by file extension and watch root. This
// scales to arbitrarily deep trees without per-directory watch descriptors.
//
// Loading eBPF programs requires elevated privileges (root or CAP_BPF +
// CAP_PERFMON). When goid3db runs unprivileged, or when the eBPF programs cannot
// be loaded, Watch transparently falls back to a recursive inotify watcher
// (rjeczalik/notify) that delivers the same debounced Events.
package watch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cilium/ebpf/ringbuf"
	"github.com/jrsmile/goid3db/internal/scan"
)

// ringReader abstracts the eBPF ring buffer so the watcher loop can be tested
// without privileges. *ringbuf.Reader satisfies it.
type ringReader interface {
	ReadInto(*ringbuf.Record) error
	Close() error
}

// setup loads and attaches the eBPF programs and returns a ring buffer reader
// plus a cleanup function. It is a package variable so tests can substitute a
// fake implementation in place of the real, privilege-requiring kernel setup.
var setup = setupBPF

// getwd resolves the working directory used to absolutize a relative root. It
// is a package variable so tests can exercise the failure branch.
var getwd = os.Getwd

// geteuid reports the effective user id. It is a package variable so tests can
// force the privileged (eBPF) and unprivileged (inotify) dispatch branches.
var geteuid = os.Geteuid

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

// These mirror the constants in bpf/watch.bpf.c.
const (
	opRemove = 1
	atFDCWD  = -100
)

// Watch begins watching root and delivers debounced Events on the returned
// channel until ctx is cancelled. When running as root it uses the eBPF
// watcher; otherwise (or if eBPF cannot be loaded) it falls back to a recursive
// inotify watcher. The buffer should be generous since large copies can burst
// many events.
func Watch(ctx context.Context, root string, debounce time.Duration) (<-chan Event, error) {
	if !filepath.IsAbs(root) {
		wd, err := getwd()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(wd, root)
	}
	root = filepath.Clean(root)
	fi, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("watch: %s is not a directory", root)
	}

	// Only the eBPF watcher needs privileges; attempt it only when running as
	// root and fall back to inotify if the programs cannot be loaded.
	if geteuid() == 0 {
		if rd, cleanup, err := setup(); err == nil {
			out := make(chan Event, outBuffer)
			go run(ctx, root, debounce, rd, cleanup, out)
			return out, nil
		}
	}

	return watchInotify(ctx, root, debounce)
}

// run drains the ring buffer, coalesces rapid repeated events per path, and
// delivers debounced Events until ctx is cancelled.
func run(ctx context.Context, root string, debounce time.Duration, rd ringReader, cleanup func(), out chan<- Event) {
	defer close(out)
	defer cleanup()

	// Closing the reader unblocks the blocking ReadInto below.
	go func() {
		<-ctx.Done()
		_ = rd.Close()
	}()

	raw := make(chan Event, 4096)
	go func() {
		defer close(raw)
		var rec ringbuf.Record
		for {
			if err := rd.ReadInto(&rec); err != nil {
				if errors.Is(err, ringbuf.ErrClosed) {
					return
				}
				continue
			}
			ev, ok := decode(root, rec.RawSample)
			if !ok {
				continue
			}
			select {
			case raw <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

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
		case ev, ok := <-raw:
			if !ok {
				return
			}
			pending[ev.Path] = ev.Op
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
}

// decode turns a raw ring buffer record into an Event, resolving the (possibly
// relative) syscall path and filtering by audio extension and watch root.
func decode(root string, sample []byte) (Event, bool) {
	if len(sample) < 12 {
		return Event{}, false
	}
	op := sample[0]
	pid := int32(binary.LittleEndian.Uint32(sample[4:8]))
	dfd := int32(binary.LittleEndian.Uint32(sample[8:12]))

	nameBytes := sample[12:]
	if i := bytes.IndexByte(nameBytes, 0); i >= 0 {
		nameBytes = nameBytes[:i]
	}
	name := string(nameBytes)
	if name == "" {
		return Event{}, false
	}

	path := resolvePath(pid, dfd, name)
	if path == "" {
		return Event{}, false
	}
	path = filepath.Clean(path)

	if !isAudio(path) || !underRoot(root, path) {
		return Event{}, false
	}

	o := OpUpsert
	if op == opRemove {
		o = OpRemove
	}
	return Event{Op: o, Path: path}, true
}

// resolvePath reconstructs an absolute path from a (possibly relative) syscall
// pathname using the calling process's cwd or the referenced directory fd.
func resolvePath(pid, dfd int32, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	var base string
	var err error
	if dfd == atFDCWD {
		base, err = os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	} else {
		base, err = os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, dfd))
	}
	if err != nil {
		return ""
	}
	return filepath.Join(base, name)
}

// underRoot reports whether path lies within the watched root directory.
func underRoot(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

func isAudio(p string) bool {
	return scan.AudioExts[strings.ToLower(filepath.Ext(p))]
}
