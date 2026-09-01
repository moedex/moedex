# Coding Conventions

**Analysis Date:** 2026-08-24

## Naming Patterns

**Files:**
- All-lowercase, no underscores, no hyphens: `blobstore.go`, `contextwin.go`, `graphrefresh.go`, `rankcorpus.go`. Multi-word names are run together rather than snake_cased.
- Underscores appear only where Go's build system gives them meaning, or for a deliberate `<family>_<variant>` split:
  - Build-tag variants: `internal/embed/onnx.go` (`//go:build onnx`) paired with `internal/embed/onnx_disabled.go` (`//go:build !onnx`); `internal/navigate/lsp.go` / `internal/navigate/lsp_disabled.go`; `internal/server/graphcalls_lsp.go` / `internal/server/graphcalls_disabled.go`; `internal/server/graphsimilar_onnx.go` / `internal/server/graphsimilar_disabled.go`; `cmd/moedex-nav/main.go` / `cmd/moedex-nav/main_disabled.go`; `cmd/moedex-serve/nav_lsp.go` / `cmd/moedex-serve/nav_stub.go`.
  - GOOS variants: `internal/snapshot/lock_unix.go` / `internal/snapshot/lock_other.go`.
  - Arch/experiment variants: `internal/setops/setops_simd_amd64.go` / `internal/setops/setops_fallback.go`.
  - Extractor families: `internal/symbol/extract_cs.go`, `extract_cf.go`, `extract_sql.go`, `extract_ts.go` alongside the shared `extract.go`.
- Tests are colocated as `<subject>_test.go` in the same directory as the code they exercise (`internal/index/codec.go` → `internal/index/codec_test.go`).
- **Rule for new files:** name the file after the concern, lowercase and run-together. Reserve `_` for build-tag/GOOS/arch pairs and for `_test.go`.

**Packages:**
- Single lowercase word, no underscores, no plurals where a singular reads better: `index`, `query`, `search`, `rank`, `server`, `mcp`, `symbol`, `trigram`, `contextwin`, `tokenindex`, `blobstore`, `diskstore`, `setops`, `fmindex`.
- Nested packages carve a sub-domain out of a parent: `internal/graph/candidates`, `internal/graph/cluster`, `internal/graph/diskgraph`, `internal/graph/httproute`, `internal/graph/manifest`, `internal/graph/verify`, `internal/corpus/catalog`, `internal/version/sourcehash`.

**Functions:**
- `MixedCaps` for exported, `mixedCaps` for unexported. Standard Go — no stutter with the package name (`index.New`, not `index.NewIndex`; `search.Literal`, not `search.SearchLiteral`).
- Constructors are `New` for the package's single primary type (`index.New`, `graph.NewGraph`, `candidates.NewSet`) and `New<Type>` when a package builds several (`embed.NewONNXEmbedder`, `embed.NewHTTPEmbedder`, `navigate.NewLSP`, `navigate.NewPool`, `catalog.NewCatalog`, `catalog.NewLock`).
- Option-carrying variants suffix `WithOptions` rather than adding overload-style names: `embed.NewONNXEmbedderWithOptions`, `embed.NewONNXEmbedderFromFilesWithOptions` (`internal/embed/onnx.go`).
- CLI subcommand handlers are `run<Subcommand>` in the `cmd/` package: `runBuild`, `runCheck`, `runRefresh`, `runGraph`, `runCASExport`, `runSnapshotRollback` (`cmd/moedex-index/main.go:195` onward).

**Receivers:**
- One-to-three lowercase letters derived from the type: `(s *Server)`, `(c *LSP)`, `(r Resolved)`, `(p *Pool)`, `(e Embedder)`, `(b *Blob)`, `(ix *Index)`, `(tx *Transaction)`. `s` (112 uses), `c` (74), `r` (68), `p` (50) dominate.
- Never `this`, never `self`, never the full type name.

**Variables:**
- Short names for short scopes (`ix`, `b`, `tc`, `off`, `n`), descriptive names for package-level and long-lived state.
- Sentinel errors are `Err<Condition>` exported, `err<Condition>` unexported: `snapshot.ErrNoCurrent`, `snapshot.ErrBuildLocked`, `navigate.ErrServerDead`, `navigate.errNoLSP`, `embed.errNoONNX`.

**Types:**
- `MixedCaps` nouns. Option/config bundles are named `Config`, `Options`, or `<Verb>Options` — never a long positional parameter list:
  - `internal/rank/ranker.go:18` `Config`, `internal/contextwin/contextwin.go:80` `Options`, `internal/navigate/lsp.go:41` `Config`, `internal/corpus/config.go:36` `Config`, `internal/parity/run.go:15` `RunConfig`, `internal/server/rankcorpus.go:24` `RankConfig`, `internal/server/graphbuild.go:54` `GraphBuildOptions`, `internal/corpus/reindex.go:20` `ReindexOptions`, `internal/embed/onnx_options.go:8` `ONNXOptions`.
- Interfaces are agent nouns (`Runner`, `Extractor`, `Embedder`, `Navigator`, `Reranker`, `GramSelector`, `PostingProvider`, `ContextSearcher`, `GraphAnnotator`) or `-er` forms. Capability-extension interfaces prefix the capability: `DetailedNavigator`, `RefExtractor`, `DefsRefsExtractor`, `SnapshotContextSearcher`, `ConfidenceGraphAnnotator`, `SnapshotGraphAnnotator` (`internal/mcp/mcp.go:68`, `internal/mcp/neighbors.go:62`).

## Code Style

**Formatting:**
- `gofmt` (Go 1.26 toolchain, `GOTOOLCHAIN: local` pinned in `.gitlab-ci.yml`). Tabs for indentation, gofmt-standard everything else. There is no `.editorconfig`, no `.golangci.yml`, and no third-party formatter.
- **Run `gofmt -w` on every file you touch.** `gofmt -l internal cmd` currently reports 27 files with drift (mostly trailing-comment alignment in table-driven test literals, e.g. `internal/index/codec_test.go:47`). Neither `make health` nor CI enforces formatting, so drift is invisible until someone runs `gofmt -l`.

**Linting:**
- `go vet ./...` via `make vet`. It is a hard gate in `make health`, which is the fast pre-push check and the CI `health` job.
- Build-tagged arms need their own vet pass because the default `go vet ./...` does not compile them: `make vet-lsp` (`-tags lsp`), `make vet-simd` (cross-compiles `-tags moedex_simd` for linux/amd64).
- There is no `//nolint`-style suppression vocabulary in the tree.

**Numeric and byte literals:**
- Octal file modes use the `0o` prefix: `0o644` for files, `0o755` for directories, `0o600` for anything that should not be world-readable.

## Import Organization

**Order (gofmt-grouped, blank line between groups):**
1. Standard library, alphabetized.
2. `moedex/internal/...`, alphabetized.
3. Third-party modules, alphabetized (rare — only `internal/mcp`, `internal/embed` under `-tags onnx`, and the SDK-facing code have any).

Example from `internal/rank/ranker.go`:

```go
import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"

	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)
```

**Path Aliases:**
- No module-level aliasing. Import aliases appear only to disambiguate a name collision, e.g. `indexsnapshot "moedex/internal/snapshot"` in `cmd/moedex-serve/main.go` (the local `snapshot` identifier is already taken).

**Dependency discipline:**
- The default build is pure Go standard library. `go.mod` direct requires are `github.com/google/jsonschema-go`, `github.com/modelcontextprotocol/go-sdk` (MCP surface), and `github.com/sugarme/tokenizer` + `github.com/yalue/onnxruntime_go` — the latter two reachable **only** under `-tags onnx`. Do not add dependencies without maintainer agreement.

## Error Handling

**Patterns:**
- Return errors; do not panic. There are 5 `panic(` sites in all of `internal/` + `cmd/` (excluding tests), all for genuinely unreachable programmer errors.
- **Prefix error strings with the package name and a colon**, so a bubbled-up error names its origin without a stack trace:

```go
return fmt.Errorf("snapshot: legacy source contains symlink %s", path)
return fmt.Errorf("blobstore: ...")   // 76 sites
return fmt.Errorf("diskstore: ...")   // 74 sites
return fmt.Errorf("server: ...")      // 69 sites
```

- **Wrap with `%w` when the caller may want to branch on the cause**, and keep the prefix:

```go
return fmt.Errorf("snapshot: publish immutable directory: %w", err)
return fmt.Errorf("%w (%s)", ErrBuildLocked, string(owner))
```

  Roughly 314 of 715 `fmt.Errorf` sites wrap. Wrap when the underlying error is actionable (`os.ErrNotExist`, a sentinel, an RPC error); use a plain formatted string when the condition is fully described by your own message.

- **Declare a package-level sentinel for any condition callers branch on**, and document the `errors.Is` contract on it:

```go
// internal/snapshot/publish.go:17
var ErrBuildLocked = errors.New("snapshot: another build holds the writer lock")

// internal/navigate/lsp.go:103
var ErrServerDead = errors.New("navigate: language server died")
```

  Branch with `errors.Is` / `errors.As`, never string comparison. `internal/navigate/lsp_disabled.go:81` re-declares `ErrServerDead` in the `!lsp` build so callers can keep the same `errors.Is` branch in the default build.

- **Distinguish "the operation failed" from "the operation succeeded and reported failure."** `internal/corpus/runner.go` is the reference: a non-zero process exit lands in `Result.Code` with a **nil** error (a normal outcome callers inspect), while a non-nil error means the process could not be run to completion. Document that distinction on the method.

- CLI error handling is centralized in `main`: subcommand handlers return `error`, `main` prints `<binary>: <err>` to stderr and exits 1; unknown-subcommand / usage failures exit 2 (`cmd/moedex-index/main.go:83-134`).

## Logging

**Framework:** two, split by layer.
- **`log/slog`** — structured, for library code with an injectable logger and for the daemon. `internal/navigate/lsp.go` carries `Logger *slog.Logger` in its `Config` (`internal/navigate/lsp.go:72`) and stores it as `c.log`; `resolveLogger` (`internal/navigate/lsp.go:376`) defaults to a discard handler so tests are silent and a debug flag flips it to `os.Stderr` at `LevelDebug`. `cmd/moedex-serve/main.go` and `middleware.go` configure the process-wide handler (`slog.NewTextHandler` / `slog.NewJSONHandler`, `slog.SetDefault`).
- **`log.Printf`** — 10 sites total, all long-running batch progress reporting in `internal/server/graphrefresh.go`, `graphcalls_lsp.go`, and `graphsimilar_onnx.go`.

**Patterns:**
- Prefix the message with the package: `log.Printf("server: graph schedule progress %d/%d batches complete; active=%q", ...)`.
- slog messages are lowercase phrases with key/value pairs, not interpolated sentences: `c.log.Debug("lsp notify didOpen", "uri", pathToURI(abs), "version", ds.version, "bytes", len(content))`.
- Errors go in as a value under an `"err"` key: `c.log.Debug("lsp notify didClose failed", "path", v.path, "err", err.Error())`.
- **Libraries never log unconditionally.** Take a `*slog.Logger` (or a `logf func(string, ...any)`, as `buildShards` in `cmd/moedex-index/main.go:445` does) and default it to a discard/no-op.

## Comments

**When to Comment:**
- **Every package has a package doc comment** starting `// Package <name> ...` — all 25 `internal/` packages do. It states what the package owns and, where relevant, why the design is what it is (`internal/trigram/trigram.go` explains the byte-vs-rune offset choice in the package doc).
- **Every exported identifier is documented**, starting with the identifier's own name: `// Posting records that a trigram begins at Offset (byte index) within Blob.`
- Comment density is deliberately high and explanatory rather than descriptive. Comments carry the *why*, the invariant, and the failure mode — not a restatement of the code. `internal/index/index.go:65-80` spends 16 lines on the `Index` type explaining the selective-index membership hinge because getting it wrong silently breaks parity.
- **Cite the ADR when a decision is load-bearing.** 57 comment sites reference `ADR NNNN` (e.g. `internal/navigate` references ADR 0017's Condition 2). Link the decision, do not re-argue it.
- Inline comments explain non-obvious byte arithmetic and boundary semantics: `end = b.lineStarts[i+1] - 1 // exclude the '\n' that begins the next line`.
- Uppercase words mark invariants and warnings that must survive a skim: `NOT`, `MUST`, `NEVER`, `KEY FINDING`, `HISTORY`.

**Godoc:**
- Standard `//` line comments. No `/* */` blocks. No JSDoc-style tags.
- Document the *contract*, especially for interfaces: `internal/corpus/runner.go` documents on each method what a nil vs non-nil error means and what a non-zero exit code means.

**TODO/FIXME:**
- Effectively unused — 5 sites across the whole tree. Do not leave TODOs; either fix it or record it as an ADR / CONCERNS item.

## Function Design

**Size:** No hard limit, but functions are single-purpose. Long functions exist in the CLI dispatch and shard-build paths (`cmd/moedex-index/main.go`) where they are essentially linear scripts.

**Parameters:**
- `context.Context` is always the **first** parameter and always named `ctx` (153 non-test sites): `func (f *fakeSearcher) SearchContext(ctx context.Context, q string, budget, topK int)`.
- Three or more related knobs become a `Config`/`Options` struct passed by value. Zero values are meaningful defaults — see `navigate.Config`, `rank.Config`, `contextwin.Options`.
- Small orthogonal helper hooks are passed as function values rather than interfaces: `logf func(string, ...any)`, `score func(T) float64`, `combine func(a, b T) T`.

**Return Values:**
- `(value, error)` is the norm. Multi-value returns beyond that are used where the extra value is genuinely part of the answer: `func (b *Blob) LineAt(off int) (int, []byte)`.
- A `(value, ok bool)` form signals "absent, not broken" — `eval.BuildGoldCorpusIndex()` returns `(ix, n, perRepo, ok)` so callers skip rather than fail.
- Named return values only where a `defer` must set them (`runRecovered[T]` in `internal/server/graphrefresh.go:746`).

**Generics:** Used sparingly and only where a type parameter removes real duplication — 4 sites: `computeParallel[T any]`, `runRecovered[T any]` (`internal/server/graphrefresh.go`), `sortByScoreThenBlob[T any]` (`internal/rank/coverage.go:20`), `MergeLineRanges[T any]` (`internal/rank/merge.go:16`).

## Module Design

**Exports:**
- All library code lives under `internal/`; nothing is importable outside the module. Executables in `cmd/` stay thin — dispatch, flag parsing, and printing — with reusable behavior pushed into `internal/`.
- Export the minimum. Packages expose a small named surface (`index.New`, `index.Index`, `index.Posting`, `index.Blob`, `index.FileRef`) and keep fields unexported behind methods where an invariant depends on them (`Index.blobs`, `Index.bySHA`, `Index.postings`, `Blob.lineStarts`).

**Barrel Files:** None. Go packages are the unit of grouping; there are no re-export shim files.

**Build tags — the hard boundary:**
- Optional arms are **compiled out by default**, not runtime-flagged. Each tagged file has a `!<tag>` twin that provides the same exported symbols with a sentinel error, so callers compile and behave identically either way. Tags in use: `onnx`, `lsp`, `moedex_simd` (+ `goexperiment.simd`, `amd64`).
- New optional functionality follows the same shape: real impl behind `//go:build <tag>`, stub behind `//go:build !<tag>`, sentinel error explaining how to enable it (`navigate.errNoLSP` reads `"navigate: LSP navigation unavailable; rebuild with -tags lsp"`).
- Add the tagged arm to the corresponding `make build-*`, `make vet-*`, and `make test-*` targets in the `Makefile` — the default `./...` walk will not see it.

**No code generation:** There are zero `//go:generate` directives. Everything in the tree is hand-written and committed.

## Concurrency

- Guard shared mutable state with `sync.Mutex`/`sync.RWMutex` (18 non-test sites) or `sync/atomic` (8 sites); document what each mutex guards on the field (`callsMu *sync.Mutex // guards calls; required when the runner is used under concurrency`).
- Fan-out uses `sync.WaitGroup` + bounded worker counts, not unbounded `go func()` per item. `computeParallel[T]` in `internal/server/graphrefresh.go:690` is the shared bounded-parallel helper.
- **Honor cancellation.** 65 sites check `ctx.Err()` or select on `<-ctx.Done()`. Any loop over a corpus, a shard, or an RPC batch must be cancellable.
- No `errgroup` — `golang.org/x/sync` is an indirect dependency only.

---

*Convention analysis: 2026-08-24*
