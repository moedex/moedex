# ADR 0023: Unified Moe shell

- Status: Accepted
- Date: 2026-08-26

## Context

Moe exposed eight independently installed binaries with flat, overlapping flag
surfaces. Operators could resolve stale binaries from different PATH locations,
automation had no common progress protocol, configuration was distributed across
environment reads, and the warm serving package mixed offline graph construction
with online retrieval.

## Decision

Ship one `moe` executable with semantic Cobra commands and Fang presentation.
Bare interactive invocation opens a Bubble Tea search UI; non-TTY invocation
prints help. One-shot search remains deterministic and automation-safe.

Keep existing `MOEDEX_*` names as the public configuration contract. Load config
files only when named with `--config`, validate them through one typed registry,
and use flag > file > environment > default precedence. Versioned NDJSON events
are the machine progress surface.

Install the former binary names as symlinks to `moe` for two releases. Dispatch
them in-process by `argv[0]`, preserve legacy arguments, and print a deprecation
warning. Installation must not overwrite unfamiliar binaries without an explicit
adoption flag.

Separate warm retrieval (`internal/serve`), online graph tools
(`internal/graph/serve`), and offline graph construction
(`internal/graph/build`). Stable artifact names live in
`internal/graph/artifact`, while `internal/shardset` is the neutral shared storage
seam. Import-fence tests enforce the direction.

Reject tokenless non-loopback HTTP and MCP listeners unless the operator passes
`--allow-insecure`. Keep existing public network environment variables and wire
deployment artifacts to semantic commands.

## Consequences

- Documentation, completion, man pages, version output, TUI behavior, and global
  flags have one front door.
- Existing scripts have a two-release migration window without subprocess
  shims or duplicate binaries.
- Engine packages cannot acquire terminal dependencies; Charm and Cobra are
  confined to shell/render/TUI packages.
- Offline graph builds and online serving can evolve independently without a
  shared `internal/server` package.
- External unauthenticated exposure requires a visible, auditable opt-out.
