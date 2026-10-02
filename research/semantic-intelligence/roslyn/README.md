# Direct Roslyn extraction experiment

This is a disposable compiler/SCIP comparison adapter for [S2](../../../docs/plans/semantic-intelligence/S2-PLAN.md) and [ADR 0026](../../../docs/adr/0026-semantic-identities-and-scoped-retrieval.md). Its JSONL is **not a production artifact format**, and it does not change Moedex graph IDs or persistence.

The adapter constructs `CSharpCompilation` directly from a bounded project-file subset. It does not load or execute the fixture's MSBuild targets. Analysis must run against an authorized, privacy-filtered source projection; this CLI is not itself a privacy or filesystem sandbox. Files included or referenced by the project are inputs, including linked files.

## Build and run

Requires .NET SDK 10, with its bundled `Roslyn/bincore` assemblies. The checked-in NuGet configuration clears package sources; this adapter has no NuGet packages. Build a copied adapter directory under scratch, preserving a clean repository:

```sh
cp -R research/semantic-intelligence/roslyn /tmp/moedex-roslyn
DOTNET_ROOT=/path/to/dotnet
export DOTNET_ROOT
"$DOTNET_ROOT/dotnet" build /tmp/moedex-roslyn/Moedex.RoslynSpike.csproj \
  --configfile /tmp/moedex-roslyn/NuGet.Config
"$DOTNET_ROOT/dotnet" /tmp/moedex-roslyn/bin/Debug/net10.0/Moedex.RoslynSpike.dll \
  /projection/compiler-v1/App/App.csproj \
  --root /projection/compiler-v1 \
  --framework-dir "$DOTNET_ROOT/packs/Microsoft.NETCore.App.Ref/10.0.0/ref/net10.0" \
  > /tmp/app.roslyn.jsonl
```

`--framework-dir` is mandatory: framework reference selection must be explicit. The project TFM must equal that directory's final segment. Inputs may be `.csproj`, `.sln`, or `.slnx`; project dependencies are recursively analyzed. A project invocation emits its dependency projects too, so comparison runners should select the requested project's records instead of counting its dependencies repeatedly.

Exit status is `0` for complete compilations, `2` for useful but incomplete/unsupported output, and `1` for fatal invocation/extraction failure. A consumer must inspect the exit status and project/summary status; a failed analysis is never a complete empty index. Stdout is JSONL, stderr is invocation failure detail. The surrounding runner records elapsed time, peak memory, SDK identity and artifact size separately.

## Supported build subset

- SDK-style C# projects with a single `TargetFramework`, `AssemblyName`, `LangVersion`, `Nullable`, `AllowUnsafeBlocks`, and literal `DefineConstants`.
- Default recursive `.cs` inputs excluding `bin`, `obj`, and `.git`; `EnableDefaultCompileItems=false`; literal `Compile Include` and `Remove`, with filename wildcards such as `../App/*.cs`. Linked inputs retain their physical source paths.
- Explicit `ProjectReference` dependencies compiled in-process and metadata `Reference` entries with `HintPath`.
- User-supplied `.g.cs` or `.generated.cs` inputs. The `generated` flag is a filename heuristic, not evidence that a source generator ran.

The adapter refuses compiler facts for recognized unsupported build constructs: conditional XML, property expansion, imports/targets, packages, analyzers, additional files, multi-targeting, implicit usings, framework references, reference aliases/metadata and inherited `Directory.Build.*`/`Directory.Packages.props`. Unsupported project records are emitted with `compilation_status=unsupported` and no declarations/references. Compilation diagnostics can still make an otherwise supported project `incomplete`; useful resolved facts and unresolved occurrences are retained with that context status.

This is **not an MSBuild evaluator**. SDK-generated AssemblyInfo, target-framework attributes and implicit SDK/DEBUG/TRACE preprocessor symbols are not synthesized. A source project with no explicit assembly version attributes consequently has direct-compiler assembly version `0.0.0.0`, not the SDK build's usual generated version. Only literal project `DefineConstants` participate. Package restore, actual source-generator execution, solution configuration mapping, arbitrary SDK extensions and incremental caches are outside this experiment. Do not treat passing fixture labels as proof of real-project build fidelity.

## Output contract

Every record carries `schema=moedex.roslyn-spike.v1`. Output is deterministic for identical projected paths, bytes, reference assemblies and toolchain; it deliberately excludes timing/process IDs.

- `project`: compiler/extractor versions, explicit-source build mode, completeness, source identities, reference assembly content hashes, dependency contexts, settings, issues and limitations.
- `declaration` / `reference`: project/context, physical relative source path, raw SHA-256 and Git blob SHA, exact source text and span, binding status/method, symbol, enclosing symbol and diagnostic candidates.
- `diagnostic`: compiler code, severity, invariant-culture message and source span when available.
- `summary`: per-project declarations/references, unresolved/ambiguous counts and compilation diagnostics.

`span.byte_offset` and `byte_length` address **raw UTF-8 bytes**, including a UTF-8 BOM when present. UTF-16 offsets are included separately because Roslyn uses UTF-16. Lines and UTF-16 columns are one-based. Newlines and source bytes are not normalized. Invalid UTF-8 is a fatal input error, rather than silently rewriting evidence. Runners can verify `raw_bytes[offset:offset+length].decode('utf-8') == source_text`.

Symbols expose:

- `descriptor`: the compiler documentation-comment ID when available, preserving overload signature, nesting and generic arity;
- `assembly_identity`: the compiler's actual identity for this explicit-source compilation or metadata assembly;
- source project/build namespace and `declarations[]` source locations;
- `descriptor_kind`, origin and error-type status, so local-symbol fallback descriptors are distinguishable from documentation IDs.

Constructed/reduced method and generic references normalize to their original definitions. Invocation-name binding uses the invocation expression's resolved target, avoiding a method-group guess. Namespace/type/name references are included; constructor calls have a separate reference record. Interface calls resolve to the declared interface method, not an inferred runtime implementation. Unresolved and ambiguous names are retained with Roslyn candidate reason and candidates. More than one diagnostic candidate is labeled ambiguous; a single rejected candidate remains unresolved.

**Full `symbol.id` values are intentionally snapshot-dependent**: source IDs contain their owning project's dependency/source/configuration context digest. They are experimental occurrence-bound identifiers, not stable semantic IDs across edits. Compare `descriptor` plus owner namespace/assembly identity separately when evaluating overload reordering or incremental stability. A local fallback descriptor includes a compiler display name and source position and is also not edit-stable. The experiment must inform a later explicit separation of semantic identity, occurrence identity and resolved-fact cache identity.

Build-context hashes include tool/schema version, project source/configuration bytes, literal compiler settings, linked source bytes, explicit metadata/framework reference digests and transitive project contexts. The adapter recomputes all projects; changed context hashes demonstrate invalidation inputs, **not incremental indexing performance or equivalence**. Context hashes do not attest to unmodeled MSBuild or package behavior.
