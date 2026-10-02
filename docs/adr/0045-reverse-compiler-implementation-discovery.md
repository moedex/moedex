# ADR 0045: Reverse compiler implementation discovery

**Status:** Accepted

**Date:** 2026-10-01

## Context

Binding and definition queries expose a known implementation's interface
correspondence. An agent starting from an interface method still needs a way to
find recorded implementations. Index v4's implementation posting is keyed by
implementing method; scanning every declaration would defeat bounded retrieval.

## Decision

Semantic index v5 adds an interface-member posting with symbol row, declaration
fact row, and implementation-fact ordinal. Entries sort by exact interface
identity, build context, declaration and ordinal. Opening proves complete,
unique coverage of the validated implementation facts, including exact target
identity. A new 24-byte row per relationship and one 16-byte section descriptor
grow the header from 384 to 400 bytes.

`compiler_implementations` accepts an exact interface method symbol ID. Omitting
`context_ids` discovers up to `min(limit, 32)` contexts with recorded relationships.
Supplying contexts returns implementation declarations with their original
source/context/snapshot proof, bounded fact payload and deduplicated symbols.
Each match identifies the fact ordinal for the queried interface member.

Selected queries require 1–32 unique contexts and reject alternatives of the
same repository/project. Each call permits 1–100 results, 10,000 charged posting
rows and 2 MiB of conservatively estimated materialization. The estimate is not a
serialized JSON or RSS ceiling. Discovery seeks past all declarations in each
context rather than scanning duplicates; reported rows exclude binary-search
probes. Limits report truncation, byte overflow fails, and cancellation releases
the serving lease. Invalid reader evidence fails closed at the public boundary.

Versions 1–4 remain readable for their existing queries. The new tool reports
`index_upgrade_required` on older indexes. Rebuilding the derived index from the
same complete artifact enables the lookup; neither compiler recapture nor a new
extractor is required. The compiler tool catalog remains available even when the
current serving generation has no semantic index.

## Consequences

An agent can move from a qualified interface method to recorded implementation
declarations without already knowing their source paths or publisher behavior.
Metadata interface identity includes the full assembly namespace; same names in
other project or assembly identities do not join.

Results are compiler correspondences, not runtime dispatch or DI selection.
Discovery enumerates captured evidence, not deployment closure. The existing
generic/inherited/default/static implementation exclusions remain. Empty records
for a generic default-interface bridge are a coverage gap, not proof that concrete
handlers are absent. Truncated discovery can leave contexts undiscovered; the
caller receives that limitation explicitly.

## Evidence

The [acceptance record](../../research/semantic-intelligence/IMPLEMENTATION-DISCOVERY-ACCEPTANCE.md)
describes six source-frozen interface identities across eShop and Outbox, eight
selected context queries, metadata identity, context-alternative rejection, and
actual legacy-snapshot behavior. This is a development API regression, not a
fresh held-out or natural-language agent benchmark.

## Related

- [ADR 0044](0044-public-compiler-implementation-evidence.md): public implementation evidence.
- [ADR 0029](0029-compiler-lookup-index-and-tools.md): compiler lookup index and serving tools.
