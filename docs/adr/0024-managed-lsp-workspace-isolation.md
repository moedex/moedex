# ADR 0024: Isolate managed-corpus language-server workspaces

- Status: Accepted
- Date: 2026-08-27

## Context

The managed corpus is an acquisition snapshot, not a user working tree. External
language servers do not share that contract: loading C# projects through Roslyn
and MSBuild can create design-time intermediates under `obj/`, and other servers
may create their own caches. Running them directly against the managed corpus
made refresh fail its recoverable-cleanliness gate even though no source or
locked commit had changed.

Ignoring known output paths would hide mutation without preventing it. Broadly
cleaning submodules before refresh would race live language servers and weaken the
guard protecting unknown local data.

## Decision

`navigate.Pool` recognizes a workspace inside a managed corpus from the ownership
marker and exact acquisition lock before it starts an external server. It lazily
materializes the locked project commit with `git archive` into a writable cache
keyed by schema, stable project ID, and commit. Publication is atomic, and an
existing cache target is reused only when its manifest matches exactly.

The external server receives only the projected workspace root. Navigation input
paths are translated from the canonical corpus to the projection, and returned
locations and symbols are translated back. Unmanaged repositories retain their
live-working-tree and overlay behavior.

The cache defaults to `MOEDEX_INDEX_DIR/lsp-workspaces` or
`~/.moedex-state/lsp-workspaces` and can be overridden with
`MOEDEX_LSP_WORKSPACE_DIR`.

Corpus cleanliness diagnostics name affected projects and paths and separately
identify known untracked MSBuild intermediates. Unknown content remains a
fail-closed condition.

## Consequences

- Language-server writes cannot mutate the canonical managed corpus.
- The LSP sees the exact locked commit rather than untracked or modified checkout
  content.
- Navigation continues to return canonical paths understood by search, graph,
  MCP, and callers.
- The first LSP query for a project pays a one-time local archive extraction cost;
  subsequent languages and processes reuse the verified projection.
- Writable projections consume additional disk and may be discarded safely as
  cache data; the managed corpus remains the source of truth.

## Evidence

Colocated tests cover the implementation contracts. Corpus-specific evaluation
records and calibrated gates are maintained outside the public repository.
