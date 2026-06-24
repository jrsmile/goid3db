package watch

// Generate the eBPF object and its Go loader from watch.bpf.c. Run via
// `go generate ./internal/watch/...` (or `make generate`). Requires clang and
// the libbpf headers; the generated *_bpfel.go/*_bpfeb.go and embedded objects
// are committed so that ordinary `go build` does not need a BPF toolchain.
//
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -target bpfel,bpfeb -type event watch ./bpf/watch.bpf.c -- -I./bpf -Wall -O2
