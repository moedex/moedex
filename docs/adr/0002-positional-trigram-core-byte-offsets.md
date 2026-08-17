# ADR 0002: Keep the positional-trigram retrieval core; byte offsets, not rune offsets

- **Status:** Accepted
- **Date:** 2026-06-25
- **Context owner:** moedex (TurnCommerce)

## Context
The initial deep-research pass on a fresh-from-scratch Zoekt concluded: **keep the engine, replace the chassis.** The positional-trigram core is the part of Zoekt that aged well — n=3 is the proven sweet spot ("too few distinct 2-grams, too many distinct 4-grams"), and storing each trigram's offset lets a substring query intersect a small number of posting lists and verify positional distance rather than scan every file. That research also surfaced a correction: **Zoekt stores rune offsets, not byte offsets** — a clean-room build gets to choose. The durable design lineage and primary sources are summarized in [`ARCHITECTURE.md`](../../ARCHITECTURE.md#design-lineage).

## Decision
Adopt **positional trigrams (n=3)** as the retrieval primitive, and store **byte offsets, not rune offsets**.

- `internal/trigram` defines `const N = 3` and `type Trigram [N]byte`.
- `internal/index` emits one `Posting{Blob, Offset}` per positional byte-trigram. Blobs are added in increasing ID order and a blob's trigrams in increasing offset order, so each posting list stays sorted by `(Blob, Offset)` with no explicit sort step.

## Consequences
**Positive**
- Content stays exactly 1× in memory/on disk (no rune-index expansion); the positional-distance delta for a literal is a fixed byte length.
- Candidate verification is **byte-exact like ripgrep** — correct over UTF-8 regardless of where a trigram straddles a rune boundary. This is what makes the ripgrep-parity invariant ([0003](./0003-cox-reduction-ripgrep-parity.md)) achievable.
- Posting lists arrive pre-sorted by construction, which the compact codec ([0005](./0005-mmap-compact-postings.md)) relies on for delta coding.

**Negative / costs**
- Positional postings are the dominant memory cost (~one posting per content byte) — the memory wall that [0005](./0005-mmap-compact-postings.md) exists to push off-heap. This is *not* Cox's ~20%-of-file-size presence-only index; conflating the two mis-sizes the hardware.
- A trigram floor: literals shorter than 3 bytes have no trigram to intersect and degrade to a scan (a known latency tail — [0012](./0012-search-latency-positional-verify.md)).

## Evidence
Cox's Linux-kernel example narrowed 36,972 files → 25 (~100×, 1.96 s → 0.01 s) using exactly this reduction. The byte-offset choice is what lets the `internal/search` verify stage and the full-corpus parity harness ([0003](./0003-cox-reduction-ripgrep-parity.md)) compare matches against ripgrep at `(file, line)` granularity byte-for-byte. The research run adversarially **refuted** "bigrams can beat trigrams for regex" (1-2) and the "FM-index is only 44% of corpus" claim (0-3), leaving n=3 trigrams the evidence-backed choice.

## Related
[0003](./0003-cox-reduction-ripgrep-parity.md), [0005](./0005-mmap-compact-postings.md), [0012](./0012-search-latency-positional-verify.md).
