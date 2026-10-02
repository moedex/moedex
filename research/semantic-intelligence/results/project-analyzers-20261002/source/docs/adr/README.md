# Architecture Decision Records — moedex

These ADRs capture the architectural decisions behind moedex, a clean-room, single-node trigram code-search engine and agent-context API (a Zoekt successor). They were consolidated from a sprint's worth of spike/latency/scale/parity working notes (2026-06); those throwaway reports have since been **removed and their evidence baked into the relevant ADRs** below.

| # | Decision | Status |
|---|---|---|
| [0001](./0001-single-node-scope-pure-go-default.md) | Single-node scope, ~8 GB corpus, pure-Go zero-dependency default | Accepted |
| [0002](./0002-positional-trigram-core-byte-offsets.md) | Keep the positional-trigram core; byte offsets, not rune offsets | Accepted |
| [0003](./0003-cox-reduction-ripgrep-parity.md) | Regex → boolean-trigram (Cox) reduction, verified to ripgrep parity — never under-approximate | Accepted |
| [0004](./0004-content-addressable-blob-store.md) | Content-addressable storage by git blob SHA — global dedup, per-blob delta, deduped served format | Accepted |
| [0005](./0005-mmap-compact-postings.md) | mmap'd compact (varint-delta) postings as the memory strategy | Accepted |
| [0006](./0006-rrf-hybrid-ranking.md) | Hybrid multi-arm ranking fused via RRF — not a learned reranker | Accepted |
| [0007](./0007-optional-dense-arm.md) | Optional dense arm — zero-dep default, ONNX behind a build tag or local HTTP | Accepted |
| [0008](./0008-polyglot-symbol-sidecar.md) | Polyglot syntactic symbol layer as a precomputed sidecar — not tree-sitter in the binary | Accepted |
| [0009](./0009-agent-context-api.md) | Agent-first context API — token-budgeted, deduplicated, symbol-scoped windows over MCP | Accepted |
| [0010](./0010-warm-serving-spine.md) | Warm multi-shard serving spine — mmap retrieval daemon + ranked context, sidecar persistence, hardening | Accepted |
| [0011](./0011-shard-level-freshness.md) | Shard-level freshness via a git-HEAD manifest — rebuild only affected shards | Accepted |
| [0012](./0012-search-latency-positional-verify.md) | Search latency — fold-aware candidate prefilter + positional-postings verification | Accepted |
| [0013](./0013-pure-go-defer-simd.md) | Pure-Go execution; native SIMD kernel deferred (tried, no consistent win at scale) | Accepted |
| [0014](./0014-eval-harness-gold-gate.md) | Evaluation harness + hard gold gate — NDCG floor and a distraction-aware (UDCG) metric | Accepted |
| [0015](./0015-structured-context-result.md) | Structured `search_context` result — lossless blocks + per-arm provenance for machine consumers (Moe) | Accepted |
| [0016](./0016-incremental-embedding-refresh.md) | Incremental embedding refresh — reuse unchanged chunk vectors across a rebuild | Accepted |
| [0017](./0017-lsp-navigation-and-the-serena-boundary.md) | LSP-precise navigation — the three conditions to subsume Serena (real LSP semantics · concurrency-safe under parallel lanes · live working-tree freshness) | Proposed (all 3 conditions met & productionized in-process: `internal/navigate`, `-tags lsp` — multi-language, pooled, incremental sync) |
| [0018](./0018-name-based-navigation-workspace-symbol.md) | Name-based navigation — `find_symbol` (workspace/symbol) + `symbols_overview` (documentSymbol); the name→position unblock for a Serena drop-in | Proposed (implemented: `internal/navigate` `WorkspaceSymbol`/`DocumentSymbol` + MCP tools, `-tags lsp`) |
| [0019](./0019-moedex-managed-submodule-corpus.md) | Moedex-managed corpus — local Git superproject, curated submodules, and an exact committed corpus lock | Proposed ([program and phases](../plans/0019-managed-submodule-corpus.md)) |
| [0020](./0020-branch-aware-indexing.md) | Branch-aware indexing — locked Git trees, content-deduped snapshots, honest provenance, and explicit branch scope | Proposed ([program and phases](../plans/0020-branch-aware-indexing.md)) |
| [0021](./0021-ai-privacy-aware-indexing.md) | AI-privacy-aware indexing — fail-closed level-1 exclusion and policy-aware freshness | Accepted |
| [0022](./0022-mcp-sdk-contract-and-snapshot-identity.md) | Official MCP SDK contract, typed tool outputs, and snapshot-bound result identity | Accepted |
| [0023](./0023-unified-moe-shell.md) | Unified `moe` shell, typed configuration, compatibility window, serving split, and fail-closed listeners | Accepted |
| [0024](./0024-managed-lsp-workspace-isolation.md) | Immutable managed corpus with disposable locked-commit workspaces for external language servers | Accepted |
| [0025](./0025-mmap-bm25-and-dense-sidecars.md) | mmap the BM25 and dense sidecars, not just postings — live heap 8,748 MB → ~1,255 MB | Accepted |
| [0026](./0026-semantic-identities-and-scoped-retrieval.md) | Semantic identities and scoped retrieval | Accepted design; staged implementation |
| [0027](./0027-compiler-worker-and-semantic-artifact.md) | Compiler worker and validated semantic artifact | Accepted |
| [0028](./0028-semantic-snapshot-attachments.md) | Explicit semantic snapshot attachments | Accepted |
| [0029](./0029-compiler-lookup-index-and-tools.md) | Compiler lookup index and MCP tools | Accepted |
| [0030](./0030-managed-compiler-capture.md) | Managed compiler capture and retained input revalidation | Accepted for explicit local capture |
| [0031](./0031-file-bounded-shards-and-refresh-closure.md) | File-bounded shards and connected refresh dependencies | Accepted |
| [0032](./0032-public-git-compiler-capture.md) | Public Git compiler capture with explicit provenance | Accepted for explicit local capture |
| [0033](./0033-offline-compiler-dependency-bundles.md) | Offline compiler dependency bundles | Accepted for explicit local capture |
| [0034](./0034-source-first-graph-candidates.md) | Source-first graph verification and C# literal exclusion | Accepted |
| [0035](./0035-factored-graph-adjacency.md) | Shared target sets and source evidence in graph adjacency | Accepted |
| [0036](./0036-persisted-graph-serving-caches.md) | Persisted graph serving caches | Accepted |
| [0037](./0037-compiler-framework-domain-evidence.md) | Compiler-backed framework domain evidence | Accepted |

| [0038](./0038-scoped-compiler-contract-impact.md) | Scoped recorded compiler contract impact | Accepted |

| [0039](./0039-compiler-capture-composition.md) | Explicit compiler capture composition | Accepted |

| [0040](./0040-compact-contract-context-and-ef-observations.md) | Compact contract context and versioned EF observations | Accepted |

| [0041](./0041-compiler-context-registration-evidence.md) | Compiler context-registration evidence | Accepted |

| [0042](./0042-masstransit-configuration-evidence.md) | MassTransit configuration evidence | Accepted |

| [0043](./0043-static-wrapper-candidate-paths.md) | Static wrapper candidate paths | Accepted |

| [0044](./0044-public-compiler-implementation-evidence.md) | Public compiler implementation evidence | Accepted |

| [0045](./0045-reverse-compiler-implementation-discovery.md) | Reverse compiler implementation discovery | Accepted |

| [0063](./0063-native-build-diagnostic-severity.md) | Native build diagnostic severity | Accepted |
| [0064](./0064-project-built-analyzer-preparation.md) | Project-built analyzer preparation | Accepted |

**Companion docs**: [`../../ARCHITECTURE.md`](../../ARCHITECTURE.md) (what the code is today),
[`../plans/`](../plans/) (implementation plans for proposed decisions),
[`../../research/`](../../research) (the deep-research notes these decisions rest on), and
[`../../PARITY-REPORT.md`](../../PARITY-REPORT.md) (the generated correctness-gate artifact, see
[0003](./0003-cox-reduction-ripgrep-parity.md)).

> Format: lightweight ADR — Status · Context · Decision · Consequences · Evidence · Related. One decision per record.
