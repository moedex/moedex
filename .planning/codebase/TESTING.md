# Testing Patterns

**Analysis Date:** 2026-08-24

## Test Framework

**Runner:**
- Go's built-in `testing` package (Go 1.26 toolchain). No config file — behavior is driven entirely by the `Makefile` and build tags.
- 211 `*_test.go` files against 386 total `.go` files in `internal/` + `cmd/`; 1052 `TestXxx` functions, 30 `BenchmarkXxx` functions.

**Assertion Library:**
- **None.** Zero third-party test dependencies — no testify, no gomega, no gomock. Assertions are hand-written `if got != want { t.Errorf(...) }` plus `reflect.DeepEqual` for composites.
- Counts: 2030 `t.Fatalf`, 1383 `t.Errorf`, 1047 `t.Fatal`, 144 `t.Error`.

**Run Commands:**
```bash
make health                                    # build + vet + full unit suite — the fast gate; run before pushing
make test                                      # go test ./... ; tees to test.log and warns on any --- SKIP
make verify                                    # MASTER gate: health + roundtrip + full-corpus parity
make roundtrip                                 # persistence save/load identity
make parity MOEDEX_CORPUS=/path                # full-corpus exact-match parity vs ripgrep -> PARITY-REPORT.md
make graph-eval                                # hermetic graph-quality + context-budget gate
make graph-eval-private                        # reviewed >=30-query tier (needs MOEDEX_GRAPH_EVAL_SHARDS, MOEDEX_GRAPH_GOLD)

# Build-tagged arms — NOT covered by go test ./...
make test-dense                                # -tags onnx: embedder, semantic graph, indexer
make test-lsp                                  # -tags lsp -race: navigation arm against a real gopls

# Benchmarks
make bench-setops                              # pure-Go set-ops microbenchmarks
make bench-real                                # real-index macro benchmarks + CPU/mem profiles -> .bench/
make bench-latency                             # p50/p95/p99 against the running warm daemon

# Single test / package while iterating
go test ./internal/search/ -run TestRegex -count=1
go test ./internal/parity/ -run 'RoundTrip|MoedexEqualsGold' -count=1
go test ./internal/eval -run '^TestHermeticGraphGoldGate$' -count=1 -v
```

Always pass `-count=1` when iterating — `make test` results cache aggressively (most of `test.log` reads `(cached)`).

## Test File Organization

**Location:**
- **Colocated.** Tests live in the same directory as the code under test. There is no `tests/` tree.

**Naming:**
- `<subject>_test.go`, mirroring the implementation file: `internal/index/codec.go` → `internal/index/codec_test.go`.
- Where one implementation file has many behavioral facets, split tests by facet rather than growing one file. `internal/navigate/` is the reference: `lsp_test.go`, `lsp_close_test.go`, `lsp_doccap_test.go`, `lsp_frame_test.go`, `lsp_position_test.go`, `lsp_symbol_test.go`, `lsp_write_timeout_test.go`, `pool_test.go`, `pool_cooldown_test.go`, `pool_eviction_count_test.go`, `pool_lifecycle_test.go`, `pool_notify_ctx_test.go`, `pool_privacy_test.go`, `pool_routing_test.go`, `pool_stats_test.go`, `pool_symbol_test.go`.
- Test-only helper files that back several test files are `<topic>_testhelpers_test.go` (`internal/parity/attribution_testhelpers_test.go`) or a named `_test.go` fixture file (`internal/eval/fake_embedder_test.go`, `internal/eval/fixture_reranker_test.go`).
- Build-tagged tests carry the same tag as their subject and the tag is repeated in the test file: `internal/embed/onnx_test.go` (`//go:build onnx`) with `internal/embed/onnx_disabled_test.go` (`//go:build !onnx`).

**Package declaration:**
- **Internal test package by default** — 197 of 211 files declare `package <pkg>` and reach unexported identifiers directly (e.g. `internal/index/index_test.go` sets `b.lineStarts` and calls `lineStarts()`).
- **External `package <pkg>_test` only when the test must consume the package as a client** — 14 files: `package search_test` (9, the ripgrep parity battery), `package eval_test` (2), `package httproute_test` (2), `package classify_test` (1).

**Test function naming:**
- `TestSubjectBehavior` in MixedCaps: `TestBlobLineOf`, `TestAddFileDedupBySHA`, `TestParitySynthetic`.
- Use `TestSubject_Behavior` with an underscore when the subject is a specific method or symbol and the behavior clause is long — 271 of 1052 do: `TestDocFor_EvictsLRUAndSendsDidClose`, `TestCall_WedgedStdin_BoundedByContext`, `TestWriteFrame_TokenReleasedAfterAbandonedWriteUnblocks`, `TestSweepIdle_DisabledIsNoop`.
- Suffix `Gate` for a hard-asserting regression gate and `Measurement` for an observational test that logs numbers without asserting: `TestCorpusGoldGate`, `TestHermeticGraphGoldGate`, `TestPathArmCoverageGate`, `TestSymbolArmCoverageGate` vs `TestCorpusMeasurement`, `TestSelectivityMeasurement`, `TestDenseHybridMeasurement`. This distinction is load-bearing — see [Gates vs Measurements](#gates-vs-measurements).

**Not used:** no `TestMain`, no `Example` functions, no `Fuzz` targets, no `//go:generate`.

## Test Structure

**Suite Organization — table-driven with `t.Run` subtests** (61 files, 88 `t.Run` sites). The canonical shape, from `internal/index/index_test.go:12`:

```go
func TestBlobLineOf(t *testing.T) {
	// content: "ab\ncd\ne" -> line starts at byte offsets 0, 3, 6.
	//   bytes: a(0) b(1) \n(2) c(3) d(4) \n(5) e(6)
	b := &Blob{Content: []byte("ab\ncd\ne")}
	b.lineStarts = lineStarts(b.Content)

	cases := []struct {
		name string
		off  int
		want int
	}{
		{"first byte", 0, 1},
		{"newline byte belongs to its line", 2, 1},
		{"start of second line", 3, 2},
		{"offset past end clamps to last line", 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := b.LineOf(tc.off); got != tc.want {
				t.Errorf("LineOf(%d) = %d, want %d", tc.off, got, tc.want)
			}
		})
	}
}
```

**Conventions inside the table:**
- The slice is named `cases`, the loop variable `tc`, the struct fields `name` / inputs / `want`.
- Case names are lowercase prose describing the *behavior*, not the input: `"newline byte belongs to its line"`, `"offset past end clamps to last line"`.
- Annotate non-obvious rows with a comment above them explaining the expected derivation — `internal/tokenindex/tokenindex_test.go` does this for every camelCase-splitting case (`// Three subtokens [ssl,api,client]: start0 -> ssl,sslapi,sslapiclient; ...`).

**Assertion message format:** `"<call> = <got>, want <want>"`, with the inputs interpolated:

```go
t.Errorf("LineOf(%d) = %d, want %d", tc.off, got, tc.want)
t.Fatalf("NumBlobs = %d, want 1 (same SHA must dedup)", ix.NumBlobs())
```

Put the *reason* in parentheses when the expectation encodes an invariant.

**`t.Fatalf` vs `t.Errorf`:** `Fatalf` when continuing would panic or produce meaningless cascading failures (setup failed, a length assertion that later indexing depends on). `Errorf` when the test can usefully report several independent mismatches.

**Setup / teardown:**
- `t.TempDir()` for all filesystem work — 385 sites. Never `os.MkdirTemp` + manual cleanup, never a shared fixture directory.
- `t.Cleanup(...)` for anything else that must unwind (67 sites) — process shutdown, restoring a package-level default.
- No `TestMain`; no global setup.
- **No `t.Parallel()` anywhere** (0 sites). Tests run serially within a package. Do not introduce `t.Parallel()` — several suites build multi-GB-adjacent indexes and drive external processes, and the fixture helpers assume exclusive use.

**Helpers:**
- Any helper taking `*testing.T` calls `t.Helper()` first (231 sites) so failures point at the caller's line.
- Signature convention: `func <verb><Noun>(t *testing.T, ...) <result>` — `mustWrite(t, path, content)`, `writeFixture(t)`, `buildIndex(...)`, `assertSet(t, op, a, b, got, want)`, `findPos(t, file, needle, occurrence)`, `newNav(t, root)`.
- `must*` prefix means "fail the test on error rather than returning one": `mustWrite`, `mustIncremental`, `mustEmbedOne`.
- Domain-specific assertion helpers live next to their subject and check invariants beyond equality — `assertSet` in `internal/setops/setops_test.go:216` verifies sorted-distinct output *and* equality *and* (via `aliases`) that the result does not alias an input buffer.

## Mocking

**Framework:** none. Hand-written fakes implementing the production interface.

**The pattern — define an interface at the boundary, inject a fake in tests.** Every external dependency is behind an interface declared in the package that consumes it:

| Seam | Interface | Production impl | Test double |
|---|---|---|---|
| `glab` / `git` / `moedex-index` subprocesses | `internal/corpus/runner.go:44` `Runner` | `ExecRunner` | `fakeRunner` (`internal/corpus/corpus_test.go:20`) |
| Embedding model | `internal/embed/embed.go:57` `Embedder` | `ONNXEmbedder`, `HTTPEmbedder` | `fakeEmbedder` (`internal/embed/embed_test.go:25`), `conceptEmbedder` (`internal/eval/fake_embedder_test.go`) |
| Ranked context search | `internal/mcp/mcp.go:54` `ContextSearcher` | `server.RankCorpus` | `fakeSearcher` (`internal/mcp/mcp_test.go:17`) |
| Graph annotation | `internal/mcp/neighbors.go:54` `GraphAnnotator` | disk graph | `stubAnnotator` (`internal/mcp/neighbors_test.go:15`) |
| On-disk postings | `internal/index/snapshot.go:49` `PostingProvider` | mmap loader | `fakePP` (`internal/index/snapshot_test.go:143`) |
| Symbol extraction | `internal/symbol/extract.go:41` `Extractor` | per-language extractors | `combinedFake`, `legacyFake` (`internal/symbol/extract_defsrefs_test.go`) |
| LSP navigation | `internal/navigate/navigate.go:146` `Navigator` | `navigate.LSP` | `fakeGraphNavigator` (`internal/server/graphcalls_lsp_test.go:41`) |
| MCP tool | `internal/mcp/mcp.go:111` `ToolHandler` | real tools | `fakeTool` (`internal/mcp/tools_test.go:14`) |

Naming: `fake<Thing>` when it behaves like the real thing; `stub<Thing>` when it returns canned data.

**Recording fake — the reference shape** (`internal/corpus/corpus_test.go:20`):

```go
type fakeRunner struct {
	paths   map[string]string
	runs    map[string]Result
	calls   *[]call
	callsMu *sync.Mutex // guards calls; required when the runner is used under concurrency
	fail    func(cmd string) bool
	respond func(cmd string) (Result, bool) // full control; checked before runs
}

func (f fakeRunner) RunEnv(_ context.Context, env []string, name string, args ...string) (Result, error) {
	cmd := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if f.calls != nil { /* record under callsMu */ }
	if f.fail != nil && f.fail(cmd) {
		return Result{Code: 1, Stderr: []byte("simulated clone failure")}, nil
	}
	...
}
```

Key properties to copy: prefix-matched canned responses so a test registers `"glab auth status"` and ignores trailing flags; an optional call recorder for asserting args and env; an injectable `fail` predicate for the failure path; a `respond` escape hatch for full control. Ignore `context.Context` with `_` when the fake does not honor it.

**Simple fake — deterministic and network-free** (`internal/embed/embed_test.go:25`):

```go
// fakeEmbedder maps each text to a fixed-dim vector via a hashed bag-of-words:
// every whitespace token is hashed (FNV-1a) into one of dim buckets. Texts that
// share tokens land in overlapping buckets and score higher cosine similarity,
// which is exactly what Search relies on.
type fakeEmbedder struct{ dim int }

func (f *fakeEmbedder) Dim() int { return f.dim }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([]Vector, error) { ... }
```

**Document what the fake is modeling and why.** `internal/eval/fake_embedder_test.go` spends 25 comment lines explaining that `conceptEmbedder` is deliberately *not* a hashed bag-of-words — a hashed BoW would make the dense arm "a slower copy of the lexical arm and prove nothing", so it projects onto hand-authored concept axes instead. That reasoning is the difference between a test that measures something and one that measures itself.

**HTTP:** `net/http/httptest` for anything HTTP-shaped — 54 sites across `internal/embed/embed_test.go`, `internal/mcp/http_test.go`, `internal/mcp/contract_test.go`, `internal/server/graphtools_test.go`, `internal/server/graphneighbors_test.go`, `cmd/moedex-serve/{serve,mcphttp,concurrency}_test.go`.

**In-process protocol drivers:** for stdio protocols, drive the real server over `bytes.Buffer` rather than spawning a process. `internal/mcp/mcp_test.go` defines `drive(t, s, msgs...)` which encodes newline-delimited JSON-RPC into an `in` buffer, runs `s.Serve(ctx, &in, &out)`, and decodes the ordered responses — auto-injecting the `initialize` handshake when the messages need it.

**What to Mock:**
- Subprocesses (`glab`, `git`, `rg`, language servers) when the test is about orchestration logic, not the tool.
- Network calls — nothing in the default suite reaches the network.
- The embedding model, so the dense/hybrid arms can be measured hermetically in CI.

**What NOT to Mock:**
- **The index, the trigram core, the codecs, the CAS.** Build a real `index.Index` from real content. Test packages have local `buildIndex(contents ...string) *index.Index` helpers for exactly this (`internal/query/cox_test.go:12`, `internal/rank/ranker_test.go:16`, `internal/search/positional_test.go:58`).
- **The filesystem.** Write real files into `t.TempDir()`.
- **ripgrep, gopls, the ONNX runtime** when the test *is* the parity/behavior claim — drive the real binary and skip if it is absent.

## Fixtures and Factories

**In-code synthetic corpora** are the default. Tests write a handful of files into `t.TempDir()` and index them, with a local closure for terseness (`internal/search/parity_test.go:70`):

```go
func TestParitySynthetic(t *testing.T) {
	requireRipgrep(t)
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("a.cs", "namespace TC.SslApi;\npublic class SslService {\n ... }\n")
	write("unicode.txt", "let prix = café_au_lait;\nΣumма = Δ + ß\nplain ascii line\n")
	dup := "marker line\nZZUNIQUEDUPTOKEN appears here\n"
	write("dup1.cs", dup)
	write("dup2.cs", dup) // byte-identical: exercises the dedup-expand path

	ix, files := indexDir(t, "synthetic", dir)
	runParity(t, ix, files)
}
```

Fixture content is chosen to hit specific edges and is commented as such: multi-byte rune offsets, non-ASCII begin/end grams, byte-identical dedup twins, sub-trigram literals.

**Query batteries** are package-level `var` blocks with per-entry justification comments (`internal/search/parity_test.go:28`):

```go
// Regex queries are kept to the ASCII subset where Go's RE2 and ripgrep's Rust
// regex agree. In particular \w is ASCII-only in Go but Unicode-aware in rg, so
// we never apply \w-style classes to the non-ASCII synthetic content.
var regexQueries = []string{
	`(get|set);`,   // alternation -> OR of trigram queries
	`[Tt]oken`,     // leading char class -> degrades to All, must stay correct
	`.`,            // matches every non-empty line: All-query degradation
}
```

**Location:**
- Inline in the `_test.go` file, or in a named `_test.go` fixture file (`internal/eval/gold_fixture.go`, `internal/eval/fixture_reranker_test.go`, `internal/eval/fake_embedder_test.go`).
- The only `testdata/` directory in the tree is `internal/eval/testdata/graph-corpus`. There are no `.golden` files — round-trip identity is asserted programmatically instead.
- Gold query sets are Go source, not data files: `internal/eval/gold_corpus.go`, `internal/eval/gold_tcsslapi.go`.

## Coverage

**Requirements:** None enforced. There is no coverage target, no `-coverprofile` in the `Makefile`, and no coverage job in `.gitlab-ci.yml`.

**Quality is gated by behavior, not by line coverage** — the ripgrep parity battery, the round-trip identity test, and the NDCG/MRR/Recall gold gates are the real bar.

**View Coverage:**
```bash
go test ./internal/... -coverprofile=/tmp/cover.out && go tool cover -html=/tmp/cover.out
```

**Packages with no tests:** `internal/trigram`, `internal/corpus/catalog`, `cmd/moedex`, `cmd/moedex-nav` (untagged) — see `test.log`.

## Test Types

**Unit Tests:**
- The bulk. Deterministic, hermetic, no network, no corpus. Everything reachable by `go test ./...` on a bare checkout must pass or skip cleanly.

**Integration Tests:**
- Real external binaries driven end to end, gated on availability: ripgrep (`internal/search/parity_test.go`), `git` (`internal/ingest`, `internal/corpus`), gopls and the other language servers (`internal/navigate`, `-tags lsp`), the ONNX runtime (`internal/embed`, `-tags onnx`).
- `cmd/moedex-serve/*_test.go` stands up the real daemon over `httptest` and exercises HTTP, MCP-over-HTTP, middleware, reload, and concurrency.

**Parity / Oracle Tests:**
- `internal/parity` is a differential-testing harness with pluggable oracles: `oracle_ripgrep.go`, `oracle_gold.go`, `oracle_zoekt.go`, `oracle_moedex.go`, driven by `battery.go` / `run.go` and reported through `report.go` into `PARITY-REPORT.md`.
- **The invariant these protect: moedex never under-approximates ripgrep.** Any change to `internal/query`, `internal/search`, or `internal/parity` must keep `make verify` green, and CI runs the full-corpus parity job on any MR touching those paths.

<a id="gates-vs-measurements"></a>
**Gates vs Measurements (`internal/eval`):**
- A **Gate** asserts against a floor and reds the build on regression. A **Measurement** logs numbers for humans and asserts nothing. Keep the two separate and name them accordingly.
- Floors sit a deliberate margin below the measured baseline — "a floor that catches real regressions, not a tight pin". The threshold comment records the measured baseline, the arm-by-arm decomposition, the date, and the history of why it moved (`internal/eval/gold_gate_test.go:7-58`). When you move a floor, extend that comment; do not silently retune.
- `TestCorpusGoldGate` runs with **no embedder and no build tag** so it is runnable under plain `go test ./internal/eval/`. The dense arm is measured separately under `-tags onnx`.

**E2E Tests:** No browser/UI layer exists. `cmd/moedex-serve` HTTP + MCP tests are the end-to-end tier.

**Benchmarks:** 30 `Benchmark*` functions. Micro (`internal/setops`: `BenchmarkIntersectDense/Skewed/Sparse/Medium`, `BenchmarkUnionDense`), query-path (`internal/search`: `BenchmarkLiteralCommon`, `BenchmarkRegexAlternation`, `BenchmarkRegexClassOnly`, `BenchmarkRegexSelective`), and macro (`internal/server`: `BenchmarkSearchContext_Real`, `BenchmarkRankArms`, `BenchmarkContextwin_Assemble`). Macro benchmarks are opt-in against a prebuilt shard dir (`MOEDEX_BENCH_SHARDS`, default `~/.moedex-index/shards`) and skip when absent, so CI never needs the multi-GB index.

## Common Patterns

**Skipping on a missing dependency — a `require*` helper, not an inline check:**

```go
// internal/search/parity_test.go:253
func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
}
```

Skip messages are lowercase and say **how to satisfy the requirement**, not just what is missing:

```go
t.Skip("gold corpus absent (set MOEDEX_CORPUS_ROOT/MOEDEX_CORPUS or populate ~/.moedex-managed)")
t.Skipf("onnx runtime unavailable (set ONNXRUNTIME_LIB_PATH): %v", err)
t.Skip("gopls not on PATH")
t.Skip("typescript-language-server not on PATH; skipping TS routing test")
```

**Env-gated skips are acceptable and expected** (127 `t.Skip` sites), but `make test` greps `test.log` for `--- SKIP` and prints a warning — investigate any skip you did not expect. The CI `health` job installs ripgrep specifically so the rg-backed parity unit tests *run* rather than skip on every push.

Environment variables read by tests: `MOEDEX_CORPUS` / `MOEDEX_CORPUS_ROOT`, `MOEDEX_EVAL_CORPUS`, `MOEDEX_GRAPH_EVAL_SHARDS`, `MOEDEX_GRAPH_GOLD`, `MOEDEX_BENCH_SHARDS`, `MOEDEX_PRIVACY_CORPUS`, `MOEDEX_CAS_PARITY_*`, `MOEDEX_COMPACT_PARITY_*`, `ONNXRUNTIME_LIB_PATH`, `MOEDEX_EMBED_URL` / `MOEDEX_EMBED_MODEL`, `MOEDEX_CODE_MODEL` / `MOEDEX_CODE_TOKENIZER` / `MOEDEX_CODE_DIM`.

**Async / flaky external servers — bounded retry helpers, never `time.Sleep` in the test body:**

```go
// internal/navigate/live_test.go
func queryWithRetry(t *testing.T, fn func(context.Context) ([]Location, error)) []Location
func warm(t *testing.T, pool *Pool, root, file string)
func waitForRefCount(t *testing.T, pool *Pool, declFile string, want int) []Location
```

Language servers need warm-up before they answer; the helper owns the polling and the deadline so individual tests read declaratively.

**Cancellation testing — measure a baseline first, and skip if it is unmeasurable:**

```go
t.Skipf("baseline scan too fast (%s) to test cancellation meaningfully", base)
t.Skipf("baseline scan too fast (%s) to test concurrency overlap reliably", base)
```

Prefer this over a fixed sleep-and-hope threshold that flakes on a fast machine.

**Race detection:** `-race` is used deliberately, not blanket. `make test-lsp` runs `go test -tags lsp -race ./internal/navigate/ ./internal/server/ ./cmd/moedex-serve/` because the `Pool` concurrency tests prove parallel-lane safety (ADR 0017 Condition 2). Run `-race` on any test covering a mutex, an atomic, or a worker fan-out.

**Error Testing:**
```go
got, err := search.Literal(context.Background(), ix, "hello")
if err != nil {
	t.Fatal(err)
}
```
For expected failures, branch on the sentinel with `errors.Is` / `errors.As` — never on the message string.

**Encoding round-trips:** assert identity rather than comparing to a golden blob — encode, decode, `reflect.DeepEqual` against the original. `make roundtrip` runs `go test ./internal/parity/ ./internal/graph/diskgraph/ -run 'RoundTrip|MoedexEqualsGold|CaseInsensitive' -count=1`.

## Where to Add New Tests

- New behavior in `internal/<pkg>` → `internal/<pkg>/<subject>_test.go`, `package <pkg>`, table-driven with `t.Run`.
- New external dependency → declare an interface in the consuming package, write an `ExecRunner`-style production impl and a `fake<Thing>` in the test file. Do not reach for a mocking library.
- Anything touching `internal/query`, `internal/search`, or `internal/parity` → also run `make verify` (full-corpus, needs `MOEDEX_CORPUS` and `rg`), because CI will.
- Anything touching ranking or context assembly → run `make graph-eval` and check `internal/eval` gold gates; if a floor moves, update the threshold comment with the new measured baseline and the reason.
- New optional arm behind a build tag → add the test file with the same tag, add a `!tag` disabled-path test if the stub has behavior, and wire a `make test-<arm>` target — `go test ./...` will not see it otherwise.

---

*Testing analysis: 2026-08-24*
