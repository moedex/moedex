# ADR 0021: Enforce repository AI-privacy policy at ingestion

- **Status:** Accepted
- **Date:** 2026-08-14
- **Context owner:** moedex

## Context

configured repositories can declare AI access constraints in a root
`.ai-privacy.yml` file. Governance requires an AI system to read that policy before repository
content, default a missing policy to level 3 (Internal), apply the most restrictive matching rule,
and never read, summarize, embed, or transmit level-1 (Restricted) content.

Moedex previously enumerated tracked files with `git ls-files` and read them without consulting the
policy. That made direct indexes, CAS manifests, served shards, parity corpora, scale runs, and eval
indexes capable of including level-1 content. Managed-corpus migration increases the impact because
Moedex itself acquires and refreshes the corpus.

The Git commit alone is also not a sufficient privacy freshness key. A policy can change in the
working tree without changing `HEAD`; an index refresh keyed only by `HEAD` could then carry old
searchable references forward.

## Decision

### Canonical policy and resolution

Moedex SHALL treat the repository-root `.ai-privacy.yml` file named by configured Governance as
the canonical policy. A missing or empty policy defaults to global level 3. The parser accepts only
the documented `global_privacy_level` and `privacy_levels` subset, validates levels 1–4 and rooted
repository-relative paths, and rejects unknown or ambiguous syntax. A path's effective level is the
lowest number among the global level and every matching exact or ancestor override. Overrides may
only become more restrictive.

The policy file is a bootstrap input and is never searchable content. A globally level-1 repository
returns no files without running `git ls-files`. A level-1 path is excluded before its file is
opened. Tracked symlinks and gitlinks are not followed, preventing an allowed path from aliasing a
restricted path or content outside the repository.

### Fail-closed publication

Every production build path SHALL treat a policy read or parse error as fatal rather than as an
ordinary skipped repository. Multi-repository paths validate all policies before opening or writing
their output store. Direct, parity, CAS, served-export, scale, and eval paths share the same ingest
enforcement.

If a policy changes while a repository is being ingested, the operation aborts. A failed build or
refresh does not publish a replacement manifest or served shard set; the operator must resolve the
policy and retry. The running daemon therefore remains on its last atomically published snapshot
rather than receiving a partial index.

### Privacy-aware freshness

CAS and served manifests SHALL record a stable fingerprint of the effective policy alongside each
repository's Git `HEAD`. A change to either value triggers re-ingestion and re-export, including an
uncommitted policy edit. Manifests created before this field existed have an empty fingerprint and
therefore receive a one-time rebuild that scrubs newly restricted references.

Repositories that contribute no shard—globally level-1 or simply empty—remain represented in
freshness manifests. This prevents perpetual “added” classifications and allows a later policy
relaxation to make eligible content searchable again.

### Enforcement boundary

Moedex enforces the local content boundary it can prove: level-1 content never enters an index,
mirror, CAS reference set, embedding input, or served shard. Levels 2–4 also depend on facts about
the consuming AI account and platform (enterprise agreement, training controls, approval) that a
local indexer cannot establish; operators and clients remain responsible for those eligibility
checks.

Corpus acquisition may materialize Git objects and working trees locally before the repository
policy can be read. This ADR does not transmit those bytes to an AI system: ingestion reads only the
policy first and never opens level-1 content. Excluding projects before clone would require a trusted
GitLab policy-metadata service and is outside this decision.

The optional LSP-tagged live-navigation arm reads working trees through an external language server
and is not made privacy-safe by index filtering alone. Default builds do not include that arm. Before
an LSP-enabled deployment is used with governed corpora, it must conservatively reject workspaces
containing level-1 paths or operate on a proven policy-sanitized workspace; this gate is carried into
the branch serving plan rather than being implied by this ingestion change.

## Consequences

**Positive**

- Level-1 repositories and paths cannot become searchable or embedded through any Moedex build
  path.
- Malformed policies cannot degrade into permissive defaults or ordinary skipped-repository
  warnings.
- Policy-only working-tree changes invalidate CAS and served freshness even when `HEAD` is stable.
- The managed and conventional corpus paths apply identical privacy semantics.

**Negative / costs**

- Every build and refresh reads and validates each root policy before content work; changed repos
  validate it again around ingestion to detect races.
- A malformed policy stops publication and requires human correction, leaving the daemon on its
  prior atomic snapshot.
- The parser intentionally supports only the governance schema, not arbitrary YAML features.
- Level-2/3 platform eligibility remains an operational control outside Moedex.
- Live LSP navigation needs a separate workspace-level privacy gate because language servers may
  scan beyond the explicitly queried file.

## Alternatives considered

- **Rely on the AI client to filter search results.** Rejected because prohibited content would
  already have been read, indexed, and possibly embedded before the client could filter it.
- **Use `.gitignore` or sparse checkout.** Rejected because those files do not express the
  governance policy and cannot safely implement inherited privacy levels.
- **Key freshness only on `HEAD`.** Rejected because an uncommitted policy restriction would leave
  stale searchable references intact.
- **Use a general YAML dependency.** Deferred in favor of a dependency-free strict parser matching
  the documented schema and the existing Moe privacy guard.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.

## Related

[0019](./0019-moedex-managed-submodule-corpus.md),
[0020](./0020-branch-aware-indexing.md),
configured AI Governance §10.
