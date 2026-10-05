# C# compiler / SCIP spike research

Date: 2026-09-30. Supports [S2](../../docs/plans/semantic-intelligence/S2-PLAN.md)
and [ADR 0026](../../docs/adr/0026-semantic-identities-and-scoped-retrieval.md).
This note separates source inspection and tool installation from behavioral
measurements. It does not establish a competitor win or a production importer.

## Recommendation

Proceed with a compiler-backed extraction worker and Moedex-owned semantic
identity, occurrence and completeness contracts. The experiment below favors
direct Roslyn extraction for the C# lane; its handwritten project subset is not
the production project loader. Retain SCIP as an interchange candidate, with
index/project provenance and explicit validation; do not adopt this indexer's
emitted strings unchanged as durable qualified identities. Sourcegraph documents C# definitions,
references and implementations as supported, but cross-repository navigation as
unsupported. Moedex must supply project/package/context mapping and domain
relationships independently. [Official support table](https://sourcegraph.com/docs/code-navigation/writing-an-indexer)

## Reproducible external tools

Pin `scip-dotnet` **0.2.14**, published on NuGet, rather than floating `latest` or
the repository's development branch. Its tagged project targets .NET 6 through
10, while retaining Roslyn **4.4.0**. A .NET 10 runtime target therefore does not
prove support for every modern C# construct. Test the language version actually
used by each corpus. [NuGet package](https://www.nuget.org/packages/scip-dotnet/0.2.14),
[tagged project](https://github.com/sourcegraph/scip-dotnet/blob/v0.2.14/ScipDotnet/ScipDotnet.csproj)

The current local spike uses an isolated SDK 10.0.100 and tool directory outside
the repository. The installation shape is:

```sh
dotnet tool install --tool-path /private/tmp/moedex-compiler-spike/tools scip-dotnet --version 0.2.14
scip-dotnet index path/to/Project.csproj --working-directory path/to/projection --output index.scip --skip-dotnet-restore
```

Run explicit restore/build first and record their outcomes. Set `DOTNET_ROOT`
and `PATH` to the isolated SDK when invoking the generated tool launcher. Compare
the default command with `--allow-global-symbol-definitions`; that flag changes
the namespace of emitted symbols and is part of the artifact configuration.

The official **SCIP CLI v0.10.0** Darwin arm64 archive was downloaded and verified
against its release checksum. SHA256:
`7ea200390e0790b3da8999b7b1cd4e3597700dcb3d354911872523bfe0090779`.
The binary is locally at
`/private/tmp/moedex-compiler-spike/scip-tools/scip`. Decode with a positional file
whose extension is `.scip`:

```sh
scip print --json path/to/index.scip > path/to/index.json
```

No Go module dependency or production SDK requirement was added by this setup.
[Official CLI release and checksums](https://github.com/scip-code/scip/releases/tag/v0.10.0)

The repository runner [run-scip.py](./run-scip.py) copies the fixture into a new
output directory, executes one index per project with global definitions enabled,
and adds default-namespace controls for ContextA and ContextB. It records commands,
source hashes, process status, separate logs, `/usr/bin/time -l` measurements and
decoded JSON. It requires macOS's `time -l`; this is a local research runner, not a
portable production adapter. Each command has a five-minute limit and its process
group is terminated on timeout.

```sh
python3 research/semantic-intelligence/run-scip.py \
  --dotnet /private/tmp/moedex-compiler-spike/dotnet/dotnet \
  --scip-dotnet /private/tmp/moedex-compiler-spike/tools/scip-dotnet \
  --scip /private/tmp/moedex-compiler-spike/scip-tools/scip \
  --fixture internal/eval/testdata/semantic-intelligence/compiler-v1 \
  --output /private/tmp/moedex-compiler-spike/scip-fresh-run
```

Raw output uses `<project>.scip` and `<project>.json`, with
`ContextA-default` / `ContextB-default` controls. `run.json` records provenance.
JSON documents use `relative_path`; occurrences have `range`, `symbol` and,
for definitions, `symbol_roles: 1`. Preserve character-coordinate encoding and
convert using the original source bytes; do not reinterpret columns as bytes.

The first successful raw run is locally retained under
`/private/tmp/moedex-compiler-spike/scip-run-verified`. It predates the runner's
explicit NuGet-cache override and source-roster additions; its manifest records
its actual environment and must not be represented as a run of the later script.
All six project indexes and two controls decoded successfully. The intentionally
broken project failed `dotnet build` but its index command exited zero. The
aborted earlier `scip-run` directory records a sandbox restriction on `time -l`,
not a compiler failure. Behavioral results belong in the comparison results
section once checked against the source-authored labels.

## Source-audited limitations to test

The following observations refer to the **v0.2.14 tag**, not an observed quality
score. Fixture runs must distinguish confirmed behavior from these predictions.

- Source definitions default to the index-local package `. .`; opting into global
  definitions uses assembly name and assembly version, not resolved NuGet package
  identity. Separate projects with equal names need independent context.
- Method overload disambiguators count preceding same-name members instead of
  encoding parameter signatures. Declaration reordering may change IDs.
- Type descriptors use `sym.Name`; there is no generic-arity component in that
  construction. Test `Box`, `Box<T>` and nested constructed types.
- Ranges use `GetMappedLineSpan`, so `#line` mappings require careful original-byte
  validation. Null symbols produce no occurrence; absence is not an explicit
  unresolved record. Type/interface and method override/implementation
  relationships are emitted; caller/callee containment must be supplied separately.

[Descriptor, occurrence and relationship implementation](https://github.com/sourcegraph/scip-dotnet/blob/v0.2.14/ScipDotnet/ScipDocumentIndexer.cs)

The C# walker visits `IdentifierName` references but has no `GenericName`
override. Explicit generic calls and types therefore need positive reference
fixtures, not just declaration checks.
[C# walker](https://github.com/sourcegraph/scip-dotnet/blob/v0.2.14/ScipDotnet/ScipCSharpSyntaxWalker.cs)

For a multi-target solution, the indexer selects the project matching the running
runtime's framework, falling back to the first. It enumerates `project.Documents`,
without explicitly requesting source-generated documents. Restore timeout logs a
warning and continues. Treat target-framework and generated-source coverage as
explicit limits until tested.
[Project orchestration](https://github.com/sourcegraph/scip-dotnet/blob/v0.2.14/ScipDotnet/ScipProjectIndexer.cs)

The command logs MSBuild workspace failures after writing the index, then returns
zero. It also embeds a hardcoded `0.1.0-SNAPSHOT` tool version in metadata. Record
the installed executable version externally and independently check diagnostics,
expected documents and bindings before marking any snapshot complete. Index
construction accumulates documents in memory, so bounded importer streaming does
not imply bounded upstream compiler memory.
[Command implementation](https://github.com/sourcegraph/scip-dotnet/blob/v0.2.14/ScipDotnet/IndexCommandHandler.cs)

## Measured descriptor controls

The local control run at
`/private/tmp/moedex-compiler-spike/scip-overload-reorder` copied the frozen fixture
and swapped only the `Pick(int)` / `Pick(string)` declaration lines. Reproduce by
adding `--projects Contracts --overload-reorder` to the runner above with a fresh
output directory. Its manifest records the perturbation's before/after SHA256,
full copied input roster, isolated-cache environment and actual commands. Restore,
build, index and decode all succeeded. This run used the hardened runner.

Comparing emitted symbol documentation by signature, rather than by line number,
confirmed:

| Signature | Baseline suffix | Reordered suffix |
| --- | --- | --- |
| `Overloads.Pick(int value)` | `Overloads#Pick().` | `Overloads#Pick(+1).` |
| `Overloads.Pick(string value)` | `Overloads#Pick(+1).` | `Overloads#Pick().` |

Both prefixes remained
`scip-dotnet nuget CompilerFixture.Contracts 1.0.0.0 Contracts/`.
Thus an unchanged method signature received a different descriptor solely from
declaration order. This rules out adopting these strings unchanged as durable
cross-refresh method identities. It does not imply that navigation within a
single consistently generated index is incorrect.

The baseline default-flag controls also confirmed ContextA and ContextB both emit
`scip-dotnet nuget . . Bindings/Target#Resolve().` and
`scip-dotnet nuget . . Bindings/LinkedCaller#Run().`. Those index-local identifiers
must retain index/project provenance when importing multiple artifacts; merging
their raw strings globally would collapse distinct bindings. The global-symbol
flag distinguishes these fixtures through their different assembly names, but
does not itself establish an immutable Moedex build-context or NuGet identity.
The raw comparison is retained locally as
`scip-overload-reorder/controls.json`; baseline indexes remain in
`scip-run-verified`.

## Final paired fixture results

The final runs used byte-identical source rosters, verified by comparing the
runners' SHA256 manifests. Both used SDK **10.0.100**, runtime **10.0.0**; the direct
adapter loaded SDK Roslyn **5.0.0.0**, while the packaged SCIP indexer uses its
bundled compiler. The SDK archive was checked against Microsoft's official
release-metadata SHA512. The executable SCIP version was recorded externally as
`0.2.14+3e1f671e65692f517b0d35521fd1951c63939e3c`, since its embedded metadata is stale.

[RESULTS.json](./RESULTS.json) preserves source hashes, individual label outcomes,
process measurements, artifact hashes and provenance checks. Raw complete runs
remain at `/private/tmp/moedex-compiler-spike/roslyn-final` and
`/private/tmp/moedex-compiler-spike/scip-final`; both use the final isolated runners.

| Probe | Direct Roslyn subset | SCIP 0.2.14 with global definitions |
| --- | --- | --- |
| 19 source-authored reference/status labels | 19 matched | 13 matched; 5 unmet; 1 descriptor mapping not compared |
| 5 identity groups | 5 matched | 4 matched; generic type arities collide |
| Explicit generic method / type references | Present and target-matched | Three labeled references absent |
| Unresolved / ambiguous references | Explicit statuses; ambiguous candidates retained | Labeled occurrences absent |
| Intentionally broken project | Incomplete, two expected compiler errors, exit 2 | Build fails; index still exits 0 |
| Identical linked source in distinct projects | Distinct contexts and targets | Distinct with global flag; default strings collide |
| Overload declaration reordering | Qualified descriptors remain stable | Qualified descriptor strings swap |

The external framework reference is present in SCIP. The harness does not map
its ordinal SCIP descriptor to the labeled compiler documentation ID, so that
case is **not a demonstrated SCIP resolution failure**. Likewise, an omitted
unresolved occurrence is a missing diagnostic contract, not proof of an incorrect
resolved target. These are 19 synthetic smoke labels authored before extraction,
not precision/recall estimates, human-reviewed benchmark gold or a CodeGraph win.

The direct adapter's separate provenance gate checks all **301 emitted occurrence
spans** (including dependency records) against exact UTF-8 source text, raw SHA256
and Git blob identity. It also checks project completeness, the expected error
codes/spans, source rosters, supplied generated-file marking, distinct contexts
for identical source bytes, and byte-identical repeat output. All passed.

Eight fresh-recompilation perturbation checks also passed. Setting `FAST` in the
same App project left source bytes unchanged, changed its context digest, retained
the unchanged dependency context, and switched the active target from
`Pick(string)` to `Pick(int)`. Reordering the two overload declarations preserved
the compiler documentation descriptors while invalidating the source context.
Full experimental symbol IDs deliberately include that context and therefore
change across edits; they are **not yet Moedex's stable semantic IDs**. Results:
`/private/tmp/moedex-compiler-spike/roslyn-perturb/results.json`.

Independent review found and fixed incomplete dependencies not propagating to
parents, ignored project/reference settings, and missing explicit source inputs.
Six adapter regression groups passed, including unsupported-input handling and
BOM/Unicode/CRLF byte-span round trips. The Go gold validator and six Python
harness tests passed. The default graph evaluation gate now validates compiler
fixture labels without requiring .NET to run ordinary Go tests.

### Resource observations

Single measured App invocation, after tool build/setup:

| Adapter | Wall time | Peak RSS | Native artifact |
| --- | ---: | ---: | ---: |
| Direct explicit-source compilation | 0.458 s | 158,302,208 bytes | 253,859-byte JSONL |
| SCIP index command | 1.964 s | 235,339,776 bytes | 5,552-byte protobuf |

These are reproducibility observations, **not a speed or storage-efficiency
comparison**. SCIP timing excludes prior restore/build but performs actual MSBuild
workspace loading and includes SDK-generated files. The direct path omits those
steps and emits verbose dependency records, hashes and diagnostics. There was no
cold OS-cache reset, statistical sampling or real-corpus scale test. All six
project measurements are recorded in RESULTS.json. Repeat extraction verified
identical output; neither lane implements a validated incremental cache here.

### Reproduce the direct lane and checks

Run from the repository root, using fresh output directories:

```sh
python3 research/semantic-intelligence/run-roslyn.py \
  --dotnet /private/tmp/moedex-compiler-spike/dotnet/dotnet \
  --fixture internal/eval/testdata/semantic-intelligence/compiler-v1 \
  --framework-dir /private/tmp/moedex-compiler-spike/dotnet/packs/Microsoft.NETCore.App.Ref/10.0.0/ref/net10.0 \
  --output /private/tmp/moedex-compiler-spike/roslyn-fresh
python3 research/semantic-intelligence/evaluate.py \
  --fixture internal/eval/testdata/semantic-intelligence/compiler-v1 \
  --roslyn /private/tmp/moedex-compiler-spike/roslyn-fresh \
  --output /private/tmp/moedex-compiler-spike/roslyn-fresh/evaluation.json
python3 research/semantic-intelligence/verify-roslyn.py \
  --fixture internal/eval/testdata/semantic-intelligence/compiler-v1 \
  --roslyn /private/tmp/moedex-compiler-spike/roslyn-fresh \
  --output /private/tmp/moedex-compiler-spike/roslyn-fresh/provenance.json
```

Use `evaluate.py --scip <run-directory>` for the SCIP lane. The evaluator checks
only labeled anchors; it deliberately refuses to infer success from missing
observations and does not equate different adapters' raw identifier strings.
`verify-roslyn.py` provides the separate raw provenance/completeness checks.
`perturb-roslyn.py --help` describes the configuration and reorder controls.
The extractor's [README](./roslyn/README.md) documents its precise project subset
and the independent `test_adapter.py` invocation.

## Consequence for Moedex implementation

The next implementation should separate a stable qualified descriptor from a
source occurrence and a resolved-fact build-context digest. Keep existing blob
storage for bytes; neither a blob-position graph key nor this experiment's
context-containing ID becomes the durable symbol key. Include language, project
or resolved package namespace, overload signature and generic arity explicitly.

Use a subprocess compiler worker outside the pure-Go serving process. The
production loader must use actual MSBuild evaluations/compilations or a precisely
captured build manifest, with explicit target-framework/configuration selection,
restored package identities and source-generator inputs. The experimental subset
must not silently graduate into a general project loader. Preserve unresolved,
ambiguous, unsupported and failed/incomplete outcomes, and publish only validated
artifacts while retaining useful lexical retrieval.

Before production acceptance: exercise real package-version changes, source
generators, multi-targeting, generated/line-mapped source, actual restore failure,
add/delete/rename refresh and incremental-versus-clean equivalence. Add reviewed
real-project and held-out labels, bound worker resources, and implement versioned
storage round-trips/corruption rejection. No compiler facts are served by Moedex
as a result of this research delivery; the serving engine and persistence format
remain unchanged.
