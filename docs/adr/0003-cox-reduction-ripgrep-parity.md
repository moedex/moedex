# ADR 0003: Regex → boolean-trigram (Cox) reduction, verified to ripgrep parity — never under-approximate

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
A trigram index turns a regex into cheap candidate retrieval only if the reduction is *sound*: the candidate set must contain every blob that could match, or results silently go missing. A code-search tool that quietly drops matches is worse than useless — an agent or engineer cannot tell a real "no results" from a bug. The bar was set explicitly: **moedex must return every line ripgrep returns** (no under-approximation) and no spurious lines (no over-approximation), across the whole corpus, as a gated invariant — not a hope.

## Decision
Reduce a regex to a **necessary-condition boolean trigram query** (Cox-style), select candidate blobs with it, then **verify every candidate line with Go's real `regexp` engine**. The reduction may over-approximate but **never under-approximate**: when analysis is unsure, a node degrades to `All` (scan everything) rather than risk dropping a match.

- `internal/query` (`FromRegexp`, `cox.go`) does the full bottom-up subexpression analysis with per-node exact/prefix/suffix sets (capped at 8) and boundary-trigram synthesis across concatenation.
- `internal/search` (`Literal`, `Regex`) selects candidates and verifies with `regexp`.
- The invariant is **gated, not documented-and-hoped**: `make verify` / `make parity` run `internal/parity` + `cmd/moedex-parity` over the whole corpus and exit non-zero on any under-approximation.

The correctness contract is the project's load-bearing rule: **ripgrep is immovable ground truth; any feature that violates AC-D3 (no under-approximation) is rejected, not shipped.** Oracles, the seeded battery, and the corpus are never weakened to make a run pass.

## Consequences
**Positive**
- Correctness is a CI gate with a hard oracle, not a claim. New ranking/latency work is free to change *what order* and *how fast*, never *whether a match is returned*.
- Over-approximation is harmless to correctness (the RE2 verify step removes false candidates) — only a latency cost, addressed in [0012](./0012-search-latency-positional-verify.md).

**Negative / costs**
- Degenerate patterns (sub-trigram literals, broad classes, fold-dirty literals) degrade to `All` and scan — the inherent latency tail ([0012](./0012-search-latency-positional-verify.md)).
- The parity harness needs the `rg` binary on `PATH` (and optionally `zoekt` for the differential), and a sharded whole-corpus build to run.

## Evidence
Full-corpus parity run (`PARITY-REPORT.md`, the generated gate artifact): **484/484 repos**, |F| = 60,883 indexed files, 953.6 MB content, 6 shards, a seed-reproducible **1000-query** battery across nine buckets (156 common / 152 rare / 113 phrase / 117 metachar literal, 51 sub-trigram, 45 high-frequency, 236 regex, 114 case-insensitive, 16 unicode). Verdict: **AC-D3 1000/1000 (zero under-approximations)**, **AC-D4 zero over-approximations**, with exactly one justified RE2-vs-Rust-regex semantics quirk documented (query #819 `i.es`). Three oracles adjudicate: ripgrep is ground truth, an independent in-process Go-regexp/`bytes.Contains` "gold" scan (no trigram filtering) classifies any divergence — `moedex ⊆ gold` always, so a non-empty `gold \ moedex` is a real under-approximation — and Zoekt is a soft competitive differential at file granularity. The harness surfaced and fixed a real bug: the verify-stage literal prefilter treated a case-folded `OpLiteral` (from `(?i)`) as a case-*sensitive* required byte run, dropping other-case matches.

## Related
[0002](./0002-positional-trigram-core-byte-offsets.md), [0012](./0012-search-latency-positional-verify.md), [0014](./0014-eval-harness-gold-gate.md).
