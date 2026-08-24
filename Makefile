# moedex v0.1 — build, test, and the full-corpus parity gate.
#
# `make verify` is the master gate (AC-F1): health + build + unit suite
# (round-trip is a unit test) + full-corpus parity (+ Zoekt if installed). It
# exits non-zero on any hard-gate failure; exit 0 means the DoD is met.

# Corpus root: override with `make verify MOEDEX_CORPUS=/path`.
MOEDEX_CORPUS ?= $(HOME)/.moedex-managed
export MOEDEX_CORPUS

SEED       ?= 20260622
REPORT     ?= PARITY-REPORT.md
PARITYARGS ?=
# Repo-local scratch (stable across the long run, unlike macOS $TMPDIR which the
# OS may purge mid-run). Dot-prefixed so `go build/vet ./...` skips it.
WORK       ?= $(CURDIR)/.parity-work

GOBIN := $(shell go env GOPATH)/bin

# Canonical builds bind dirty binaries to the exact Git-visible worktree that
# produced them. The linker value is intentionally computed lazily so non-build
# targets do not pay for it.
SOURCE_DIGEST = $(shell go run ./internal/version/buildmeta)
VERSION_LDFLAGS = -X moedex/internal/version.SourceDigest=$(SOURCE_DIGEST)
GO_BUILD = go build -ldflags "$(VERSION_LDFLAGS)"

# Canonical install location for the moedex binaries. ONE directory holds them all
# so PATH can never resolve a stale shadow (the skew that caused the index-loss
# incident). Override with `make install BINDIR=/somewhere/bin`.
BINDIR       ?= $(HOME)/.local/bin
# The operational binary set installed by `make install` (moedex-serve is built
# separately because it has a pure-Go vs -tags onnx variant). moedex-parity (CI)
# and scale (dev, generic name) are intentionally excluded.
INSTALL_CMDS := moedex moedex-index moedex-corpus moedex-mcp

.PHONY: verify parity graph-eval graph-eval-private setup setup-lsp build vet test roundtrip health clean build-dense test-dense build-simd vet-simd build-lsp test-lsp vet-lsp bench-setops bench-real bench-latency install install-dense install-bins install-finish require-source-digest require-clean

# Real-index benchmark knobs.
BENCHOUT  ?= $(CURDIR)/.bench
BENCHTIME ?= 20x
BENCHN    ?= 20

## verify: master gate — everything must pass for DoD.
verify: health roundtrip parity
	@echo "=== make verify: ALL GATES GREEN ==="

## health: build + vet + full unit suite (AC-A1/A2/A3).
health: build vet test

## graph-eval: hermetic production-handler graph quality and context-budget gate.
graph-eval:
	go test ./internal/eval -run '^TestHermeticGraphGoldGate$$' -count=1 -v

## graph-eval-private: reviewed >=30-query self-hosted tier. Requires
## MOEDEX_GRAPH_EVAL_SHARDS and MOEDEX_GRAPH_GOLD.
graph-eval-private: graph-eval
	go test ./internal/eval -run '^TestPrivateGraphGoldGate$$' -count=1 -v

require-source-digest:
	@test -n "$(SOURCE_DIGEST)" || { echo "unable to compute source digest"; exit 1; }

require-clean:
	@go run ./internal/version/buildmeta -check-clean

build: require-source-digest
	@echo "=== go build ./... (AC-A1) ==="
	$(GO_BUILD) ./...

vet:
	@echo "=== go vet ./... (AC-A2) ==="
	go vet ./...

test:
	@echo "=== go test ./... (AC-A3) ==="
	bash -o pipefail -c 'go test ./... 2>&1 | tee test.log'
	@if grep -q -- '--- SKIP' test.log; then \
		echo "WARNING: skipped tests present (env-gated skips are acceptable; see report):"; \
		grep -- '--- SKIP' test.log || true; \
	fi

## roundtrip: persistence save/load identity (AC-C1).
roundtrip:
	@echo "=== round-trip (AC-C1) ==="
	go test ./internal/parity/ ./internal/graph/diskgraph/ -run 'RoundTrip|MoedexEqualsGold|CaseInsensitive' -count=1

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

## setup-lsp: install the language servers the navigation arm (ADR 0017, the
## -tags lsp daemon) drives — gopls, csharp-ls (+ .NET SDK), the TS/Python/CSS/HTML
## servers, sql-language-server, rust-analyzer. Idempotent. CFML is opt-in (it
## builds external source): `scripts/install-lsp-servers.sh --with-cfml`. Verify
## afterward with `moedex-index doctor` (the "lsp navigation servers" section).
setup-lsp:
	scripts/install-lsp-servers.sh

## install: build every operational binary and install it to BINDIR (default
## ~/.local/bin), with the PURE-GO moedex-serve, then remove any stale moedex-*
## shadow from GOPATH/bin so PATH can't resolve an old build. For the warm daemon
## use `install-dense` (the dense serve). Installation requires a clean worktree;
## binaries are VCS-stamped by `go build` plus the canonical source digest wiring
## (see `<bin> -version`).
install: install-bins
	@echo "=== moedex-serve (pure-Go) -> $(BINDIR) ==="
	@$(GO_BUILD) -o "$(BINDIR)/moedex-serve" ./cmd/moedex-serve
	@$(MAKE) --no-print-directory install-finish

## install-dense: like install, but moedex-serve is the warm-daemon build with the
## in-process ONNX embedder AND the LSP navigation arm (-tags "onnx lsp"): it
## serves search_context PLUS find_definition/find_references/find_implementations
## (ADR 0017). Running it needs the ONNX Runtime dylib at ONNXRUNTIME_LIB_PATH and
## the language servers on PATH (gopls, csharp-ls, …) — see deploy/com.moedex.serve.plist.
install-dense: install-bins
	@echo '=== moedex-index (-tags "onnx lsp") -> $(BINDIR) ==='
	@$(GO_BUILD) -tags "onnx lsp" -o "$(BINDIR)/moedex-index" ./cmd/moedex-index
	@echo '=== moedex-serve (-tags "onnx lsp") -> $(BINDIR) ==='
	@$(GO_BUILD) -tags "onnx lsp" -o "$(BINDIR)/moedex-serve" ./cmd/moedex-serve
	@$(MAKE) --no-print-directory install-finish

# install-bins / install-finish are internal helpers for install / install-dense.
install-bins: require-clean require-source-digest
	@mkdir -p "$(BINDIR)"
	@for c in $(INSTALL_CMDS); do \
		echo "=== $$c -> $(BINDIR) ==="; \
		$(GO_BUILD) -o "$(BINDIR)/$$c" ./cmd/$$c || exit 1; \
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

## build-dense: build moedex-index and moedex-serve with the in-process ONNX embedder (-tags onnx).
## Embeds the st-codesearch-distilroberta code model (int8, ~78MB) into the binary.
## The default build stays pure-Go with zero ML deps; only this target pulls them
## in. Run requires the ONNX Runtime shared library at run time (ONNXRUNTIME_LIB_PATH).
build-dense: require-source-digest
	@echo "=== go build -tags onnx ./cmd/moedex-index ==="
	$(GO_BUILD) -tags onnx -o moedex-index-dense ./cmd/moedex-index
	@echo "=== go build -tags onnx ./cmd/moedex-serve ==="
	$(GO_BUILD) -tags onnx -o moedex-serve-dense ./cmd/moedex-serve
	@echo "built ./moedex-index-dense and ./moedex-serve-dense (set ONNXRUNTIME_LIB_PATH)"

## test-dense: run the onnx-tagged embedder, semantic-graph, and indexer tests.
## Real model tests skip if the runtime lib is absent. The indexer suite disables
## its automatic semantic pass because package tests build many fixture corpora;
## server tests exercise that pass directly. Set ONNXRUNTIME_LIB_PATH to run the
## bundled-model assertions.
test-dense:
	@echo "=== go test -tags onnx (dense + semantic graph) ==="
	MOEDEX_GRAPH_SIMILAR_TOP_K=0 go test -tags onnx ./internal/embed/ ./internal/server/ ./cmd/moedex-index/ -count=1

## build-simd: cross-build the whole tree with the optional native AVX2 set-ops
## kernel (internal/setops). amd64-only — the kernel uses the experimental
## simd/archsimd intrinsics, reachable only under GOEXPERIMENT=simd. The default
## `make build` stays pure Go on every arch (the kernel falls back to goIntersect
## anywhere this triple isn't satisfied), and go.mod is untouched (archsimd ships
## with the toolchain, it is not a module dependency).
build-simd: require-source-digest
	@echo "=== GOEXPERIMENT=simd GOARCH=amd64 go build -tags moedex_simd ./... ==="
	GOEXPERIMENT=simd GOOS=$(shell go env GOOS) GOARCH=amd64 $(GO_BUILD) -tags moedex_simd ./...
	@echo "built (amd64). The native kernel runs only on an amd64 CPU with AVX2."

## vet-simd: type-check the build-tagged SIMD kernel + its differential test
## without an amd64 host (cross-compile the test binary). Proves the archsimd
## code compiles. Running it requires an amd64 CPU (or Rosetta on Apple Silicon).
vet-simd:
	@echo "=== cross-compile SIMD test binary (linux/amd64) ==="
	GOEXPERIMENT=simd GOOS=linux GOARCH=amd64 go test -tags moedex_simd -c -o /dev/null ./internal/setops/
	@echo "SIMD kernel + differential test compile OK (amd64)."

## build-lsp: build the experimental LSP-precise navigation arm (-tags lsp),
## its demo CLI, and the indexer whose graph-sidecar pass systematically runs
## find-references. The client is pure stdlib; language servers are external
## binaries supplied on PATH. The default build stays pure-Go and does not
## compile or launch this arm.
build-lsp: require-source-digest
	@echo "=== go build -tags lsp ./cmd/moedex-nav ==="
	$(GO_BUILD) -tags lsp -o moedex-nav ./cmd/moedex-nav
	@echo "=== go build -tags lsp ./cmd/moedex-index ==="
	$(GO_BUILD) -tags lsp -o moedex-index-lsp ./cmd/moedex-index
	@echo "built ./moedex-nav and ./moedex-index-lsp (language servers required at run time)"

## test-lsp: run the lsp-tagged navigation tests under the race detector. They
## drive a real gopls against self-contained throwaway Go modules (no corpus, no
## network) and skip if gopls is not on PATH. -race is load-bearing here: the
## Pool concurrency tests (ADR 0017 Condition 2) prove parallel-lane safety.
## internal/server also proves the Phase 9 systematic, rate-limited graph pass,
## including a real gopls interface-dispatch edge when gopls is available.
test-lsp:
	@echo "=== go test -tags lsp -race ./internal/navigate/ ./internal/server/ ./cmd/moedex-serve/ ==="
	go test -tags lsp -race ./internal/navigate/ ./internal/server/ ./cmd/moedex-serve/ -count=1

## vet-lsp: type-check the lsp-tagged navigation arm + cmd without running gopls.
vet-lsp:
	@echo "=== go vet -tags lsp ./internal/navigate/ ./internal/server/ ./cmd/moedex-nav/ ./cmd/moedex-index/ ./cmd/moedex-serve/ ==="
	go vet -tags lsp ./internal/navigate/ ./internal/server/ ./cmd/moedex-nav/ ./cmd/moedex-index/ ./cmd/moedex-serve/
	@echo "LSP navigation and systematic graph arms compile OK."

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
