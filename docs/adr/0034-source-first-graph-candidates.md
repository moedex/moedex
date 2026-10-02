# ADR 0034: Verify graph sources before target expansion

Status: accepted, implemented locally

## Context

The pinned Roslyn trial scheduled 237,535 candidate batches. `Main` alone had
22,903 definitions and a 993,188,595 source/definition pair upper bound. Batching
limited task size but did not reduce total work. Inspection also found that the
C# method extractor accepted declarations inside quoted test programs.

## Decision

C# declaration and reference masking recognizes ordinary, verbatim, and raw
strings and comments. The method pass checks the same mask as the type pass.
Extractor content version 3 invalidates persisted symbol sidecars. Graph corpus
identity now includes this version, so unchanged source bytes cannot preserve
edges from an obsolete extractor through incremental refresh.

The graph verifier independently recognizes the full C# raw-string opening
quote run. Shorter quote runs inside a literal do not end it or promote embedded
test-program calls to Pattern confidence. Interpolated raw strings remain
masked, including their interpolation holes, at this inexpensive tier. An
unterminated single-line raw literal recovers at CR/LF; an opening delimiter
followed by horizontal whitespace and a newline starts a multiline literal.
The binding policy is `scoped-bindings-v2`, included in corpus identity, so a
refresh recomputes graphs from the prior verifier even when source bytes and
extractor version are unchanged. This removes stale false Pattern calls rather
than carrying them forward as trusted evidence.

Graph construction verifies each source occurrence and resolves its enclosing
symbol before pairing it with definitions. Verification is target-independent;
repository filtering remains target-dependent. Unanchored Candidate sources
are suppressed using the exact non-self target count without allocating their
Cartesian product. Anchored ambiguous candidates and sibling definitions remain
explicit, and no arbitrary target cap is introduced. Each prepared name verifies
one source blob at a time and reuses a session with at most 128 compiled pattern
sets; a new uncached pattern clears the session cache when that limit is reached.

Names share immutable lexical masks through a cache owned by the graph sweep.
The cache allocates on demand and retains at most 512 MiB of mask payload and
32,768 entries. This budget can cover the measured approximately 442 MB source
corpus without reserving that memory up front. LRU eviction bounds larger
working sets; an oversized mask is classified without retention. Concurrent
fills for the same blob and language are coalesced, and in-flight bookkeeping
is also limited to the entry cap. Masks remain keyed by immutable blob identity
and language, not just a filename. The sweep releases the cache when it closes;
there is no process-global state.

The byte budget and reported retained bytes count mask payload only. Bounded
entry bookkeeping, masks currently being classified or held by workers, prepared
sources, and output edges consume additional memory. Preparation diagnostics
report cache hits, misses, waits, evictions, entries, and retained bytes alongside
source and candidate-pair counts. Reuse reduces repeated classification work;
it does not cap the memory required for accepted graph output.

Before parallel name preparation, a graph build or refresh scans identifier
runs once for its requested eligible names and builds a complete text-occurrence
table. This immutable corpus view replaces repeated trigram decoding for those
names; the symbol arm still supplies its original classifications and wins at
overlapping positions. The table has a separate 512 MiB accounting budget and a
262,144-name roster limit. Its accounting includes roster and occurrence-slice
capacity allocations, but excludes allocator rounding and transient growth
copies. If the complete table cannot fit, the entire attempted
table is discarded and the original occurrence path is used. Unsupported names
also retain that original path; no partial table can silently remove candidates.

The table belongs to the graph sweep, is replaced from the original corpus on
repeated preparation, and is released when the sweep closes. Its budget is
independent of the 512 MiB lexical-mask cache and does not bound total graph
memory. Scan start/end diagnostics record names, blobs, content bytes,
occurrences, accounted retained bytes, elapsed time, and any fallback reason.
Completed-name callbacks also emit throttled preparation progress without a
background timer or goroutine.

The fallback occurrence lookup selects one required trigram from at most three count probes,
decodes that posting list, and verifies the full name and identifier boundaries.
An unavailable selective-index gram is unknown, not proof of absence; scanning
remains the fallback. Count misses walk encoded postings; immutable mmap readers
cache at most 4,096 integer counts per shard, admitting only lists of at least
256 encoded bytes. Counting occurs outside the lock, so concurrent misses may
repeat work. No decoded posting lists are cached, and the disk format is unchanged.

The C# `using` verifier checks only the occurrence's LF-delimited line. Its
existing anchored regex permits no newline inside a directive; scanning the
whole file for every occurrence produced identical answers at much greater cost.
Bare-CR handling, masking, and byte offsets retain their prior semantics.

## Validation and limits

Differential tests compare persisted records and both suppression counters with
the prior expanded-candidate pipeline. Tests cover repository boundaries,
comments, literals, sibling definitions, shared content, ordering, and batch
boundaries. A migration regression removes an obsolete literal definition from
an old graph and verifies that the following refresh settles. Occurrence tests
compare eager, lazy, selective, and scan paths with decoding instrumentation. The
graph differential test also requires the complete occurrence table to be used;
a lifecycle test verifies stable repeated preparation and releases both caches
on close.

Raw-string verifier regressions cover four- and five-quote delimiters containing
shorter runs, interpolated raw strings, ordinary and verbatim controls, and
malformed single-line recovery with LF and CRLF. Each literal fixture also
checks that a real call after the literal still earns Pattern confidence. A
policy migration regression seeds a false quoted Pattern call under
`scoped-bindings-v1`, requires its removal while preserving a real call, and
checks that the next refresh is a no-op.

Verifier tests compare cached sessions with the uncached `Verify` API through
mask evictions, mixed-language contexts, and malformed source. Separate tests
exercise byte and entry limits, oversized-mask bypass, pattern-cache limits,
and actual hit/miss accounting. Sixteen concurrent sessions verify identical
answers while sharing one mask fill; focused race tests pass. An exhaustive
C# `using` differential test compares every byte range, including zero-width
and cross-line ranges, with the former whole-file implementation across CRLF,
bare-CR, comment, string, alias, global, and malformed directive fixtures.

Two frozen anchors in the original Roslyn source independently distinguish a
real generator `Main` from a quoted test-program `Main`; the provisioned corpus
test verifies exact file hashes before extraction. Default tests skip that
external check unless `MOEDEX_ROSLYN_SOURCE` is set.

These changes do not prove semantic binding or bound the size of genuinely
ambiguous output. Candidate, Pattern, and compiler evidence retain their
existing meanings. Real-corpus timing, output size, and retained candidate counts
must be reported separately from the synthetic suppression benchmark.

The [Roslyn follow-up](../../research/semantic-intelligence/ROSLYN-GRAPH-ACCEPTANCE.md)
measured 19.009-second preparation but failed full graph acceptance during
aggregation of 182.6 million retained candidate pairs. Bounded preparation
caches do not solve expanded-output storage; that remains separate work.
