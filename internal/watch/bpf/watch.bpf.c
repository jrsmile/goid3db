// watch.bpf.c is a CO-RE eBPF program that reports filesystem changes to user
// space so goid3db can incrementally re-index audio files. It replaces the old
// inotify-based watcher.
//
// It attaches to syscall tracepoints and emits one record per relevant
// operation:
//
//   - openat/openat2/open/creat with write intent (O_WRONLY/O_RDWR/O_CREAT/
//     O_TRUNC) -> OpUpsert: a file is being created or modified.
//   - unlink/unlinkat -> OpRemove.
//   - rename/renameat/renameat2 -> OpRemove for the source and OpUpsert for the
//     destination (covers moves into and out of the watched tree).
//
// Only the (possibly relative) syscall pathname plus the calling pid and dirfd
// are captured; user space reconstructs the absolute path via /proc and filters
// by file extension and watch root. Keeping kernel-side logic minimal makes the
// program portable across kernels.
//
//go:build ignore

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_tracing.h>

#define PATH_MAX 4096
#define AT_FDCWD -100

/* open(2) flags (octal, as in <fcntl.h>). */
#define O_WRONLY 00000001
#define O_RDWR 00000002
#define O_CREAT 00000100
#define O_TRUNC 00001000
#define WRITE_INTENT (O_WRONLY | O_RDWR | O_CREAT | O_TRUNC)

#define OP_UPSERT 0
#define OP_REMOVE 1

char LICENSE[] SEC("license") = "GPL";

struct event {
	__u8 op;
	__u8 _pad[3];
	__s32 pid;
	__s32 dfd;
	char name[PATH_MAX];
};

/* Force BTF emission of struct event for bpf2go's -type flag. */
struct event *unused_event __attribute__((unused));

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 1 << 24); /* 16 MiB */
} events SEC(".maps");

static __always_inline void emit(__u8 op, __s32 dfd, const char *uname)
{
	struct event *e = bpf_ringbuf_reserve(&events, sizeof(*e), 0);
	if (!e)
		return;

	long n = bpf_probe_read_user_str(e->name, sizeof(e->name), uname);
	if (n <= 1) { /* empty or unreadable pathname */
		bpf_ringbuf_discard(e, 0);
		return;
	}
	e->op = op;
	e->pid = bpf_get_current_pid_tgid() >> 32;
	e->dfd = dfd;
	bpf_ringbuf_submit(e, 0);
}

SEC("tracepoint/syscalls/sys_enter_openat")
int on_openat(struct trace_event_raw_sys_enter *ctx)
{
	long flags = (long)ctx->args[2];
	if (flags & WRITE_INTENT)
		emit(OP_UPSERT, (__s32)ctx->args[0], (const char *)ctx->args[1]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_openat2")
int on_openat2(struct trace_event_raw_sys_enter *ctx)
{
	struct open_how how = {};
	bpf_probe_read_user(&how, sizeof(how), (void *)ctx->args[2]);
	if (how.flags & WRITE_INTENT)
		emit(OP_UPSERT, (__s32)ctx->args[0], (const char *)ctx->args[1]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_open")
int on_open(struct trace_event_raw_sys_enter *ctx)
{
	long flags = (long)ctx->args[1];
	if (flags & WRITE_INTENT)
		emit(OP_UPSERT, AT_FDCWD, (const char *)ctx->args[0]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_creat")
int on_creat(struct trace_event_raw_sys_enter *ctx)
{
	emit(OP_UPSERT, AT_FDCWD, (const char *)ctx->args[0]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_unlinkat")
int on_unlinkat(struct trace_event_raw_sys_enter *ctx)
{
	emit(OP_REMOVE, (__s32)ctx->args[0], (const char *)ctx->args[1]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_unlink")
int on_unlink(struct trace_event_raw_sys_enter *ctx)
{
	emit(OP_REMOVE, AT_FDCWD, (const char *)ctx->args[0]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_renameat2")
int on_renameat2(struct trace_event_raw_sys_enter *ctx)
{
	emit(OP_REMOVE, (__s32)ctx->args[0], (const char *)ctx->args[1]);
	emit(OP_UPSERT, (__s32)ctx->args[2], (const char *)ctx->args[3]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_renameat")
int on_renameat(struct trace_event_raw_sys_enter *ctx)
{
	emit(OP_REMOVE, (__s32)ctx->args[0], (const char *)ctx->args[1]);
	emit(OP_UPSERT, (__s32)ctx->args[2], (const char *)ctx->args[3]);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_rename")
int on_rename(struct trace_event_raw_sys_enter *ctx)
{
	emit(OP_REMOVE, AT_FDCWD, (const char *)ctx->args[0]);
	emit(OP_UPSERT, AT_FDCWD, (const char *)ctx->args[1]);
	return 0;
}
