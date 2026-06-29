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

# Canonical install location for the moedex binaries. ONE directory holds them all
# so PATH can never resolve a stale shadow (the skew that caused the index-loss
# incident). Override with `make install BINDIR=/somewhere/bin`.
BINDIR       ?= $(HOME)/.local/bin
# The operational binary set installed by `make install` (moedex-serve is built
# separately because it has a pure-Go vs -tags onnx variant). moedex-parity (CI)
# and scale (dev, generic name) are intentionally excluded.
INSTALL_CMDS := moedex moedex-index moedex-corpus moedex-mcp

.PHONY: verify parity setup build vet test roundtrip health clean build-dense test-dense build-simd vet-simd build-lsp test-lsp vet-lsp bench-setops bench-real bench-latency install install-dense install-bins install-finish

# Real-index benchmark knobs.
BENCHOUT  ?= $(CURDIR)/.bench
BENCHTIME ?= 20x
BENCHN    ?= 20

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

## install: build every operational binary and install it to BINDIR (default
## ~/.local/bin), with the PURE-GO moedex-serve, then remove any stale moedex-*
## shadow from GOPATH/bin so PATH can't resolve an old build. For the warm daemon
## use `install-dense` (the dense serve). Binaries are VCS-stamped by `go build`
## (see `<bin> -version`).
install: install-bins
	@echo "=== moedex-serve (pure-Go) -> $(BINDIR) ==="
	@go build -o "$(BINDIR)/moedex-serve" ./cmd/moedex-serve
	@$(MAKE) --no-print-directory install-finish

## install-dense: like install, but moedex-serve is the warm-daemon build with the
## in-process ONNX embedder AND the LSP navigation arm (-tags "onnx lsp"): it
## serves search_context PLUS find_definition/find_references/find_implementations
## (ADR 0017). Running it needs the ONNX Runtime dylib at ONNXRUNTIME_LIB_PATH and
## the language servers on PATH (gopls, csharp-ls, …) — see deploy/com.moedex.serve.plist.
install-dense: install-bins
	@echo '=== moedex-serve (-tags "onnx lsp") -> $(BINDIR) ==='
	@go build -tags "onnx lsp" -o "$(BINDIR)/moedex-serve" ./cmd/moedex-serve
	@$(MAKE) --no-print-directory install-finish

# install-bins / install-finish are internal helpers for install / install-dense.
install-bins:
	@mkdir -p "$(BINDIR)"
	@for c in $(INSTALL_CMDS); do \
		echo "=== $$c -> $(BINDIR) ==="; \
		go build -o "$(BINDIR)/$$c" ./cmd/$$c || exit 1; \
	done

install-finish:
	@if [ "$(GOBIN)" != "$(BINDIR)" ]; then \
		for c in $(INSTALL_CMDS) moedex-serve; do \
			if [ -e "$(GOBIN)/$$c" ]; then \
				echo "removing stale shadow $(GOBIN)/$$c"; \
				rm -f "$(GOBIN)/$$c"; \
			fi; \
		done; \
	fi
	@echo "installed moedex binaries into $(BINDIR) -- verify with: moedex-index doctor"

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

## build-lsp: build the experimental LSP-precise navigation arm (-tags lsp) and
## its demo CLI moedex-nav. This is the ADR 0017 Condition-1 spike: type-resolved
## go-to-def / find-references / find-implementations driven by a real language
## server (gopls) out of process. It pulls NO new go.mod deps (the JSON-RPC client
## is pure stdlib); gopls is an external binary supplied on PATH. The default
## `make build` stays pure-Go and does not compile this arm.
build-lsp:
	@echo "=== go build -tags lsp ./cmd/moedex-nav ==="
	go build -tags lsp -o moedex-nav ./cmd/moedex-nav
	@echo "built ./moedex-nav (needs gopls on PATH; try: ./moedex-nav -verb refs FILE:LINE:COL)"

## test-lsp: run the lsp-tagged navigation tests under the race detector. They
## drive a real gopls against self-contained throwaway Go modules (no corpus, no
## network) and skip if gopls is not on PATH. -race is load-bearing here: the
## Pool concurrency tests (ADR 0017 Condition 2) prove parallel-lane safety.
test-lsp:
	@echo "=== go test -tags lsp -race ./internal/navigate/ ==="
	go test -tags lsp -race ./internal/navigate/ -count=1

## vet-lsp: type-check the lsp-tagged navigation arm + cmd without running gopls.
vet-lsp:
	@echo "=== go vet -tags lsp ./internal/navigate/ ./cmd/moedex-nav/ ==="
	go vet -tags lsp ./internal/navigate/ ./cmd/moedex-nav/
	@echo "LSP navigation arm + moedex-nav compile OK."

## bench-setops: pure-Go set-ops benchmarks on the host arch (the always-built
## baseline the amd64 SIMD kernel is compared against).
bench-setops:
	go test ./internal/setops/ -run '^$$' -bench 'BenchmarkIntersect|BenchmarkUnion' -benchmem -count=2

## bench-real: real-index macro benchmarks for the agent query path — end-to-end
## search_context, per-arm decomposition (lexical/path/symbol), and context
## assembly — against a prebuilt shard dir (default ~/.moedex-index/shards,
## override MOEDEX_BENCH_SHARDS). Opt-in: skips with no shard dir, so CI never
## needs the multi-GB index. The corpus loads ONCE per run (~cold-start cost).
## Writes CPU+mem profiles to $(BENCHOUT) for `go tool pprof`.
bench-real:
	@mkdir -p "$(BENCHOUT)"
	go test ./internal/server -run '^$$' \
		-bench 'BenchmarkSearchContext_Real|BenchmarkRankArms|BenchmarkContextwin_Assemble' \
		-benchmem -benchtime=$(BENCHTIME) -timeout 30m \
		-cpuprofile "$(BENCHOUT)/cpu.prof" -memprofile "$(BENCHOUT)/mem.prof" \
		| tee "$(BENCHOUT)/bench.txt"
	@echo "profiles -> $(BENCHOUT)/cpu.prof  $(BENCHOUT)/mem.prof"
	@echo "inspect  -> go tool pprof -top -nodecount=25 $(BENCHOUT)/cpu.prof"

## bench-latency: end-to-end p50/p95/p99 latency against the RUNNING warm daemon
## (HTTP round trip, the user-perceived number). N samples/query via BENCHN.
bench-latency:
	scripts/bench-latency.sh $(BENCHN)

clean:
	rm -f test.log moedex-serve-dense moedex-nav
	rm -rf "$(WORK)" "$${TMPDIR:-/tmp}/moedex-parity"
