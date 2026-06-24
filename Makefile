# moedex v0.1 — build, test, and the full-corpus parity gate.
#
# `make verify` is the master gate (AC-F1): health + build + unit suite
# (round-trip is a unit test) + full-corpus parity (+ Zoekt if installed). It
# exits non-zero on any hard-gate failure; exit 0 means the DoD is met.

# Corpus root: override with `make verify MOEDEX_CORPUS=/path`.
MOEDEX_CORPUS ?= $(HOME)/TCGitlab
export MOEDEX_CORPUS

SEED       ?= 20260622
REPORT     ?= PARITY-REPORT.md
PARITYARGS ?=
# Repo-local scratch (stable across the long run, unlike macOS $TMPDIR which the
# OS may purge mid-run). Dot-prefixed so `go build/vet ./...` skips it.
WORK       ?= $(CURDIR)/.parity-work

GOBIN := $(shell go env GOPATH)/bin

.PHONY: verify parity setup build vet test roundtrip health clean build-dense test-dense build-simd vet-simd bench-setops

## verify: master gate — everything must pass for DoD.
verify: health roundtrip parity
	@echo "=== make verify: ALL GATES GREEN ==="

## health: build + vet + full unit suite (AC-A1/A2/A3).
health: build vet test

build:
	@echo "=== go build ./... (AC-A1) ==="
	go build ./...

vet:
	@echo "=== go vet ./... (AC-A2) ==="
	go vet ./...

test:
	@echo "=== go test ./... (AC-A3) ==="
	go test ./... 2>&1 | tee test.log
	@if grep -q -- '--- SKIP' test.log; then \
		echo "WARNING: skipped tests present (env-gated skips are acceptable; see report):"; \
		grep -- '--- SKIP' test.log || true; \
	fi

## roundtrip: persistence save/load identity (AC-C1).
roundtrip:
	@echo "=== round-trip (AC-C1) ==="
	go test ./internal/parity/ -run 'RoundTrip|MoedexEqualsGold|CaseInsensitive' -count=1

## parity: full-corpus exact-match parity gate over MOEDEX_CORPUS (AC-B/D/E/F).
## Builds the index (sharded), runs the battery vs ripgrep + gold (+ Zoekt),
## writes $(REPORT), and exits non-zero on any hard-gate failure.
parity: build
	@echo "=== full-corpus parity over $(MOEDEX_CORPUS) ==="
	go run ./cmd/moedex-parity -corpus "$(MOEDEX_CORPUS)" -seed $(SEED) -report "$(REPORT)" -work "$(WORK)" $(PARITYARGS)

## setup: install the Zoekt differential oracle (one-time, needs network).
setup:
	@echo "=== installing Zoekt (AC-E1) ==="
	go install github.com/sourcegraph/zoekt/cmd/zoekt-index@latest
	go install github.com/sourcegraph/zoekt/cmd/zoekt@latest
	@echo "installed into $(GOBIN)"

## build-dense: build moedex-serve with the in-process ONNX embedder (-tags onnx).
## Embeds the st-codesearch-distilroberta code model (int8, ~78MB) into the binary.
## The default build stays pure-Go with zero ML deps; only this target pulls them
## in. Run requires the ONNX Runtime shared library at run time (ONNXRUNTIME_LIB_PATH).
build-dense:
	@echo "=== go build -tags onnx ./cmd/moedex-serve ==="
	go build -tags onnx -o moedex-serve-dense ./cmd/moedex-serve
	@echo "built ./moedex-serve-dense (run with -mcp -embed onnx; set ONNXRUNTIME_LIB_PATH)"

## test-dense: run the onnx-tagged embedder test. Skips if the runtime lib is
## absent. Set ONNXRUNTIME_LIB_PATH to the libonnxruntime shared library.
test-dense:
	@echo "=== go test -tags onnx ./internal/embed/ ==="
	go test -tags onnx ./internal/embed/ -count=1

## build-simd: cross-build the whole tree with the optional native AVX2 set-ops
## kernel (internal/setops). amd64-only — the kernel uses the experimental
## simd/archsimd intrinsics, reachable only under GOEXPERIMENT=simd. The default
## `make build` stays pure Go on every arch (the kernel falls back to goIntersect
## anywhere this triple isn't satisfied), and go.mod is untouched (archsimd ships
## with the toolchain, it is not a module dependency).
build-simd:
	@echo "=== GOEXPERIMENT=simd GOARCH=amd64 go build -tags moedex_simd ./... ==="
	GOEXPERIMENT=simd GOOS=$(shell go env GOOS) GOARCH=amd64 go build -tags moedex_simd ./...
	@echo "built (amd64). The native kernel runs only on an amd64 CPU with AVX2."

## vet-simd: type-check the build-tagged SIMD kernel + its differential test
## without an amd64 host (cross-compile the test binary). Proves the archsimd
## code compiles. Running it requires an amd64 CPU (or Rosetta on Apple Silicon).
vet-simd:
	@echo "=== cross-compile SIMD test binary (linux/amd64) ==="
	GOEXPERIMENT=simd GOOS=linux GOARCH=amd64 go test -tags moedex_simd -c -o /dev/null ./internal/setops/
	@echo "SIMD kernel + differential test compile OK (amd64)."

## bench-setops: pure-Go set-ops benchmarks on the host arch (the always-built
## baseline the amd64 SIMD kernel is compared against).
bench-setops:
	go test ./internal/setops/ -run '^$$' -bench 'BenchmarkIntersect|BenchmarkUnion' -benchmem -count=2

clean:
	rm -f test.log moedex-serve-dense
	rm -rf "$(WORK)" "$${TMPDIR:-/tmp}/moedex-parity"
