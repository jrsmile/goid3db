# goid3db Makefile
# CGO is required for the miniaudio (malgo) audio backend.

BINARY      := goid3db
PKG         := ./...
COVERPROFILE := cover.out
ROOT        ?= .
export CGO_ENABLED := 1

GO          := go
GOFLAGS     ?=

.DEFAULT_GOAL := build

.PHONY: all build run clean fmt vet tidy \
        test test-race cover cover-html coverage-check \
        bench bench-2m help

## build: compile the goid3db binary
build:
	$(GO) build $(GOFLAGS) -o $(BINARY) .

## run: build and run against ROOT (override: make run ROOT=/music)
run: build
	./$(BINARY) -root $(ROOT)

## fmt: format all Go sources
fmt:
	$(GO) fmt $(PKG)

## vet: run go vet
vet:
	$(GO) vet $(PKG)

## tidy: sync go.mod/go.sum
tidy:
	$(GO) mod tidy

## test: run the full test suite
test:
	$(GO) test $(GOFLAGS) -count=1 $(PKG)

## test-race: run tests with the race detector
test-race:
	$(GO) test $(GOFLAGS) -race -count=1 $(PKG)

## cover: run tests and write a coverage profile
cover:
	$(GO) test $(GOFLAGS) -covermode=set -coverprofile=$(COVERPROFILE) -count=1 $(PKG)
	$(GO) tool cover -func=$(COVERPROFILE) | tail -1

## cover-html: open the coverage profile in a browser
cover-html: cover
	$(GO) tool cover -html=$(COVERPROFILE)

## coverage-check: fail if any package is below 100% statement coverage
coverage-check: cover
	@gaps=$$($(GO) tool cover -func=$(COVERPROFILE) | grep -vE '100.0%$$' || true); \
	if [ -n "$$gaps" ]; then \
		echo "Coverage below 100%:"; echo "$$gaps"; exit 1; \
	else \
		echo "All packages at 100% coverage."; \
	fi

## bench: run all benchmarks with memory stats
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./internal/search/

## bench-2m: run only the 2,000,000-track search benchmarks
bench-2m:
	$(GO) test -run '^$$' -bench 'Search2M|SearchFiltered2M' -benchmem ./internal/search/

## all: format, vet, test and build
all: fmt vet test build

## clean: remove build and coverage artifacts
clean:
	rm -f $(BINARY) $(COVERPROFILE)

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'
