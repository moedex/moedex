# Public corpus oracle: Roslyn

This is a reproducible large-source **lexical correctness and resource test**.
It is not yet a compiler-binding evaluation, a CodeGraph comparison, or proof of
production semantic coverage. Repository size supplies realistic stress; the
independent query oracles supply the correctness checks.

## Pinned acquisition

Inventory verified directly from the detached checkout on 2026-09-30:

| Property | Value |
|---|---|
| Origin | `https://github.com/dotnet/roslyn.git` |
| Commit | `36d26c5466e4d25940657ccb8d5b9557ccaf7be1` |
| Git tree | `5b672971231f561490c9280a2086fbf422a92df3` |
| Tracked blob entries | 35,115 |
| Tracked blob bytes | 456,238,809 (about 435 MiB) |
| `.cs` paths | 18,176 |
| `.csproj` paths | 408 |
| Checkout status at inventory | Clean, detached HEAD |
| License | MIT, .NET Foundation and Contributors |
| `License.txt` SHA-256 | `ae48df11a335dc1a615f4f938b69cba73bcf4485c4f97af49b38efb0f216353b` |
| `global.json` SHA-256 | `0a14db34cdfccad73ff1e38eeb7a5300ec5d0a70b9de159ec79873454c0a76b6` |

Counts come from `git ls-tree -rlz --full-tree HEAD`, not GitHub repository-size
metadata. They include all tracked blobs; Moedex's ingested file count and bytes
can differ because ingestion applies its normal file-selection rules. Keep both
denominators visible.

Primary source links: [pinned commit](https://github.com/dotnet/roslyn/commit/36d26c5466e4d25940657ccb8d5b9557ccaf7be1),
[license](https://github.com/dotnet/roslyn/blob/36d26c5466e4d25940657ccb8d5b9557ccaf7be1/License.txt),
[SDK configuration](https://github.com/dotnet/roslyn/blob/36d26c5466e4d25940657ccb8d5b9557ccaf7be1/global.json).

## Local layout and reproduction

The repeatable periodic gate is separate from the fast default tests:

```sh
make public-corpus-parity \
  PUBLIC_CORPUS=/private/tmp/moedex-public-oracle/corpus/roslyn \
  PUBLIC_RUN=/private/tmp/moedex-public-oracle/new-run
```

The checkout must already exist at the pinned commit and be clean, including
ignored/untracked files. Before and after the run, the wrapper rejects hidden
index flags and verifies raw tracked bytes against the pinned Git blobs, bypassing
clean/smudge filters. It records a deterministic source roster digest.
The run directory must be new and outside both checkouts.
The wrapper builds the current CLI, or accepts `PUBLIC_BINARY=/path/to/candidate`
and copies that executable into the run directory. It records the binary hash,
tool versions, lossless arguments, source recheck, latency CSV, reports, and exit
statuses. The latency CSV must have the expected schema and exactly 1,052 unique
query IDs with valid timings. It rejects tracked-file or eligible-mirror count drift instead of
letting a reduced corpus silently pass. Changes to those expectations require
reviewing the source pin or ingestion policy.

This command performs no acquisition or automatic scheduling. It needs Go,
Python 3.9+ (standard library only), ripgrep, Git, standard shell tools, and either
`sha256sum` or `shasum`.
Wrapper tests cover invalid source state, existing or contained destinations,
argument forwarding, changed scope, and preservation of tool failures. Its real
Roslyn preflight was also checked against an existing destination and safely
refused it before launching another oracle run.

Independent wrapper review exposed and closed hidden-index/filter source drift
and missing latency evidence. Real-Git regressions cover both, and the full
Roslyn raw-byte verification passed with source roster SHA-256
`fc874eb88c8372a37d27bc42d2197da8532f6c40ecb795c6c59385674d654b9b`.
Both completed CSVs pass the same evidence validator.

The checkout and generated data are external to the Moedex source tree:

```text
/private/tmp/moedex-public-oracle/
  corpus/roslyn/       # immutable detached source checkout
  parity-work/        # generated shards and independent-oracle mirror
  PARITY-REPORT.md
  latency.csv
  parity.stdout
  parity.stderr       # harness progress; optional time-wrapper diagnostics
```

For a new acquisition, fetch only the pinned revision into a fresh directory;
do not refresh an existing benchmark checkout in place:

```sh
git init /private/tmp/moedex-public-oracle/corpus/roslyn
git -C /private/tmp/moedex-public-oracle/corpus/roslyn remote add origin https://github.com/dotnet/roslyn.git
git -C /private/tmp/moedex-public-oracle/corpus/roslyn fetch --depth=1 origin 36d26c5466e4d25940657ccb8d5b9557ccaf7be1
git -C /private/tmp/moedex-public-oracle/corpus/roslyn checkout --detach FETCH_HEAD
git -C /private/tmp/moedex-public-oracle/corpus/roslyn rev-parse HEAD 'HEAD^{tree}'
git -C /private/tmp/moedex-public-oracle/corpus/roslyn status --porcelain
```

Run from the Moedex repository with the normal Go and ripgrep prerequisites.
The following is the original baseline recipe; preserve that binary and use a
new binary/work/report name for every implementation comparison:

```sh
go build -o /private/tmp/moedex-public-oracle/moedex ./cmd/moedex
/usr/bin/time -l /private/tmp/moedex-public-oracle/moedex parity \
  -corpus /private/tmp/moedex-public-oracle/corpus \
  -work /private/tmp/moedex-public-oracle/parity-work \
  -report /private/tmp/moedex-public-oracle/PARITY-REPORT.md \
  -latency-csv /private/tmp/moedex-public-oracle/latency.csv \
  -seed 20260930 -floor 1000 -scan-parallel 4 -rg-parallel 2 \
  -rg-threads 1 -no-zoekt -keep \
  > /private/tmp/moedex-public-oracle/parity.stdout \
  2> /private/tmp/moedex-public-oracle/parity.stderr
```

The run requests the default 150 MiB shard target. The baseline implementation
checks the threshold only between repositories, so this is **not** a hard bound:
Roslyn produced one 441.9 MB content shard. The corrective implementation splits
at file boundaries; an individual oversized file is still allowed alone.
Ripgrep remains enabled; disabling
it would turn this into latency profiling rather than the intended parity gate.
Zoekt is deliberately absent from this first run. Preserve the per-query report
and latency CSV, plus tool versions and machine identity, when comparing runs.
Do not infer competitor performance from these measurements.

## First-run results

Baseline executable: `/private/tmp/moedex-public-oracle/moedex`.
Its SHA-256 is
`56535d148abd253c689e810b38e3186ac9fefd1851a89373a30329c671bf6c44`;
version output is `moedex dev commit=unknown dense=false go=go1.27.1 built=unknown`.
The workspace lacks a Git commit identity, so the executable hash is the precise
baseline identity; do not replace it with a rebuilt binary.

Machine: Mac17,6, arm64, 18 CPUs, 68,719,476,736 bytes (64 GiB) RAM.
Tools: Go 1.27.1; ripgrep 15.2.0, revision `e89fff89ac`.

Both builds indexed the same 34,791 eligible files and 441.9 MB of content:

| Build measurement | Baseline | File-boundary fix |
|---|---:|---:|
| Shards | 1 | 3 |
| Shard content, decimal MB | 441.9 | 157.3 / 157.3 / 127.3 |
| Unique blobs, summed across shards | 31,987 | 31,998 |
| Reported build peak RSS, decimal MB | 8,459.5 | 6,516.3 |
| Build wall time, seconds | 79.511 | 140.077 |

The corrected executable is `moedex-bounded`, SHA-256
`421e23a19e0081406c9d3b98775798dd98f2dcf60576e6a4e84a21a57686fd32`.
Its outputs use `parity-bounded-work`, `PARITY-BOUNDED-REPORT.md`,
`latency-bounded.csv`, and `parity-bounded.stdout`/`.stderr` under the same external
directory. The corrected run overlaps the baseline's exhaustive query scan;
these timings are not a controlled throughput comparison. Memory observations
are single-run measurements, not a hard RSS guarantee. Splitting can repeat a
blob across shards while retaining each file reference exactly once.

The [sharding correction](../../docs/adr/0031-file-bounded-shards-and-refresh-closure.md)
also closes refresh dependencies across overlapping repository/shard membership.
Default Go tests, focused race tests, and independent review passed. Both real
query batteries contain 1,052 queries in nine buckets. The baseline completed
with **1,052/1,052 queries passing**, no under-approximations, over-approximations,
engine quirks, or oracle errors. Its reported run peak RSS was 10,170.5 MB;
scan wall time was 25m57.030s, ripgrep wall time 29m22.376s, and total wall time
56m41.659s (CLI completion at 56m41.717s). Query latency p50/p95/max was
25.839/623.161/8,672.173 ms. The [raw baseline report](results/roslyn-20260930/baseline.md)
is retained with the research evidence.
The corrected run also passed **1,052/1,052 queries**, with no mismatches, quirks,
or oracle errors. Its reported run peak RSS was 6,816.5 MB; scan wall time was
26m35.259s, ripgrep wall time 28m0.459s, and total wall time 56m59.850s
(CLI completion at 56m59.883s). Query latency p50/p95/max was
40.792/729.380/9,640.275 ms. See the [raw corrected report](results/roslyn-20260930/bounded.md).
Both CSVs have exactly the same query IDs, patterns, flags, buckets, and per-query
match counts. The [comparison manifest](results/roslyn-20260930/comparison.json)
records the battery digest and hashes of binaries, reports, and CSVs.
A one-second native stack sample of the baseline
showed active exhaustive Go-regexp gold scanning; scan wall time includes that
independent reference work and is not retrieval latency.

An exact recursive comparison of the two materialized oracle mirrors exited
successfully with no differences (`mirror-comparison.txt` is empty). The retained
Roslyn checkout remained clean after both builds. These checks establish equal
input scope; the completed query-result comparisons independently establish parity.

Both baseline and corrected hard parity gates passed.
Roslyn alone has
fewer than the harness's 40,000-file scale target. AC-B3 reports that target
separately; the command's hard parity exit status depends on query correctness
and oracle availability/errors, not this file-count floor.

The optional `/usr/bin/time -l` wrapper failed after each CLI completed
successfully because the sandbox denied `sysctl kern.clockrate`. Each wrapper exit status
is therefore 1 despite the CLI's explicit `RESULT: PASS`; harness timings and RSS
above remain available, but supplementary wrapper resource statistics are not.
The preserved raw reports contain historical FoldCase regression boilerplate
headed “Engine fix surfaced by this run”; that is not a new Roslyn finding. The
report generator now labels it as a previously fixed regression.

## Semantic follow-up boundaries

The pinned source requires .NET SDK `11.0.100-rc.1.26425.128`, allows prerelease
SDKs with patch roll-forward, and declares Arcade/Helix SDK
`12.0.0-beta.26479.3`, Traversal `3.4.0`, and NoTargets `3.7.0`. The existing
synthetic compiler fixture's .NET `10.0.100` and empty-feed restore are not a
supported Roslyn build environment. Prepare the pinned toolchain and dependencies
separately before claiming compiler coverage; record every acquisition and build
selection. Start with named projects rather than treating 408 projects as one
undifferentiated successful compilation.

The full tracked checkout also exceeds the managed projection's default 256 MiB
input budget. Compiler artifacts remain bounded at 64 MiB and compact indexes at
128 MiB. These are explicit gaps for whole-repository semantic capture, not limits
to silently disable. Measure per-project source closure and artifact expansion
before selecting batches or designing streaming storage.

This GitHub checkout uses unmanaged lexical ingestion. The public Git compiler
adapter now accepts explicit `--checkout`, `--commit`, and `--origin` without a
GitLab catalog or project ID; see [ADR 0032](../../docs/adr/0032-public-git-compiler-capture.md).
An explicit 512 MiB projection budget accommodates the pinned tracked inputs.
The real projection gate passed with all 35,115 tracked files and 456,238,809 bytes
verified against the commit and revalidated after materialization.
Dependency staging now passes on the named CSharpErrorFactsGenerator project:
a verified ten-package bundle supplies a private offline restore, and all ten
frozen cases pass the resulting production compiler artifact and executable MCP
on the full lexical/ranking snapshot. This snapshot explicitly omits the syntax
graph: the separate full-graph trial exposed excessive name-collision candidate
fan-out and remains a failed scale gate. The exact SDK and
source pins remain unchanged. Whole-repository compiler coverage remains open;
see the [compiler acceptance record](ROSLYN-COMPILER-ACCEPTANCE.md).

For subsequent semantic quality measurement, author and review source anchors
before inspecting Moedex outputs; stratify by overloads, generics, partial types,
interfaces, generated files, conditional builds, and project-reference bindings.
Keep a held-out label set. Compiler queries can provide a broad differential
oracle, but a Roslyn-backed producer agreeing with Roslyn is not independent
semantic ground truth. Score source-reviewed labels separately, including
ambiguous/unresolved cases and plausible wrong alternatives. Exercise the full
capture → import → snapshot → mmap → MCP path, with context and raw-source-hash
checks, and retain failed/incomplete projects in the coverage denominator.
