package watch

import (
	"errors"
	"fmt"
	"os"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

// setupBPF loads the eBPF objects, attaches the syscall tracepoints and opens
// the ring buffer reader. It returns the reader plus a cleanup function that
// detaches everything. This is the only part of the watcher that talks to the
// kernel and therefore requires elevated privileges (root or CAP_BPF +
// CAP_PERFMON); it is excluded from the coverage requirement because it cannot
// run in an unprivileged test environment.
func setupBPF() (ringReader, func(), error) {
	// Older kernels require a raised RLIMIT_MEMLOCK to load BPF objects.
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, nil, err
	}

	objs := &watchObjects{}
	if err := loadWatchObjects(objs, nil); err != nil {
		return nil, nil, fmt.Errorf("watch: load eBPF objects: %w", err)
	}

	var links []link.Link
	cleanup := func() {
		for _, l := range links {
			_ = l.Close()
		}
		_ = objs.Close()
	}

	// Attach the syscall tracepoints. The "at" variants exist on every arch;
	// the legacy ones may be absent, so attaching them is best-effort.
	tps := []struct {
		name     string
		prog     *ebpf.Program
		required bool
	}{
		{"sys_enter_openat", objs.OnOpenat, true},
		{"sys_enter_unlinkat", objs.OnUnlinkat, true},
		{"sys_enter_renameat2", objs.OnRenameat2, true},
		{"sys_enter_openat2", objs.OnOpenat2, false},
		{"sys_enter_open", objs.OnOpen, false},
		{"sys_enter_creat", objs.OnCreat, false},
		{"sys_enter_unlink", objs.OnUnlink, false},
		{"sys_enter_renameat", objs.OnRenameat, false},
		{"sys_enter_rename", objs.OnRename, false},
	}
	for _, tp := range tps {
		l, err := link.Tracepoint("syscalls", tp.name, tp.prog, nil)
		if err != nil {
			if !tp.required && errors.Is(err, os.ErrNotExist) {
				continue
			}
			cleanup()
			return nil, nil, fmt.Errorf("watch: attach %s: %w", tp.name, err)
		}
		links = append(links, l)
	}

	rd, err := ringbuf.NewReader(objs.Events)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("watch: open ringbuf: %w", err)
	}

	return rd, cleanup, nil
}
