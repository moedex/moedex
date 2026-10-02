# Original-project compiler acceptance

The selected local target is CodeGraph's original `src/TC.CodeGraphApi.McpHub.Abstractions/TC.CodeGraphApi.McpHub.Abstractions.csproj`. Its 20 C# files and project file are copied byte-for-byte into a disposable Git repository. The original project targets net10.0, enables implicit usings and nullable, and has no package/project references or custom targets. Reviewed project ancestors through the checkout have no Directory.Build or Directory.Packages files. The original repository NuGet configuration includes private feeds; execution supplies a separate empty-feed configuration. No source or project rewrite is permitted by this acceptance gate.

`codegraph-abstractions-gold.json` freezes the original file digests and ten independently authored token spans, binding kinds, and target descriptors. Initial gold SHA256: `b485594d05178467751bde864763c6f8a49f22437dc6eaba9518e29114871715`. Labels were written before running the worker. They cover a private helper, local and cross-type fields, a cross-file type, nested type and primary constructor, a record type, SDK method overload and constructor, and a reduced generic LINQ extension.

Run the checker against the exact retained projection and raw capture:

```sh
python3 research/semantic-intelligence/check-codegraph-abstractions.py \
  --source-root /path/to/retained-projection \
  --wire /path/to/worker.jsonl \
  --output /path/to/evaluation.json
```

The checker requires unchanged authored inputs, a complete single-project capture, the exact source byte span/hash, one resolved reference of the expected kind and descriptor, and a same-symbol declaration for selected source-local targets. It deliberately reports binding resolution separately from declaration coverage. It is a selected-case acceptance test, not whole-project precision/recall, a CodeGraph service execution, or a competitive quality score.

## Initial raw-worker observation

SDK10.0.100, original project, isolated writable CLI/NuGet caches, explicit empty feeds. Restore and worker both exited0. Capture:175 declarations,281 references,663133 bytes; worker wall time approximately2.49s on this machine. All10 selected bindings resolved to their expected descriptor;9/10 full cases passed. The remaining case was the nested primary constructor `McpProviderDenialScope.ScopeLease(Holder? previous)`: its call resolved correctly, but no matching constructor declaration was emitted. Keep this initial failure in the record when rerunning after a producer fix.

Raw observations are outside the repository under `/private/tmp/moedex-compiler-spike/codegraph-abstractions-acceptance`, with original-source Git projection, command/hash manifest, stdout/stderr, raw capture, and evaluator output.

## Managed capture and query acceptance

The subsequent producer fix emits primary constructors at the original type identifier, using the distinct `constructor_declaration` occurrence kind. Artifact and compact-index validators distinguish that declaration from a constructor reference and from the overlapping type declaration. Regressions cover classes, records, structs, partial types, explicit parameterless constructors, import, artifact roundtrip, and compact-index definitions.

The same frozen ten labels passed against the **managed artifact**, with no source/project edits and no changes to gold. The artifact has one complete context,23 sources,256 symbols,189 declarations,281 references, and8 diagnostics. The disposable acquisition repository is `example/codegraph`, project ID101, locked commit `802e49848c63db555f94435e2d58e67c58327e36`; this is the local acceptance copy's commit, not an upstream CodeGraph revision.

Commands used (tool paths are local setup, not repository dependencies):

```sh
moedex semantic capture \
  --managed-root "$ACCEPTANCE/managed" --repo example/codegraph \
  --project src/TC.CodeGraphApi.McpHub.Abstractions/TC.CodeGraphApi.McpHub.Abstractions.csproj \
  --framework net10.0 --dotnet "$DOTNET" --worker "$WORKER_DLL" \
  --sdk-path "$SDK_PATH" --workspace "$ACCEPTANCE/managed-workspace" \
  --output "$ACCEPTANCE/managed.semantic" --restore-offline

python3 research/semantic-intelligence/check-codegraph-abstractions.py \
  --source-root "$RETAINED_PROJECTION" --artifact "$ACCEPTANCE/managed.semantic" \
  --output "$ACCEPTANCE/managed-evaluation.json"

moedex index snapshot build --corpus "$ACCEPTANCE/managed" \
  --index-dir "$ACCEPTANCE/index" --semantic-artifact "$ACCEPTANCE/managed.semantic" \
  --semantic-workspace "$RETAINED_PROJECTION"
```

The published generation was `20260930T194832Z-1`. An actual `moedex serve -index-dir ... -mcp -embed none` process returned the nested constructor binding at raw byte offset1479 in `McpProviderDenialScope.cs`, alongside the name/type fact at the same position. `compiler_definitions` returned one matching `constructor_declaration`. This verifies the managed capture→artifact→snapshot→MCP path for the selected project.

Evidence remains in the acceptance directory: `managed-approved.stdout`, `managed-evaluation.json`, `managed.semantic`, `index/`, `mcp-stdio.jsonl`, and `mcp-stdio.stderr`. The retained source projection is `managed-workspace/semantic-projection-1567441527/source`. Original frozen gold SHA is unchanged. Full worker integration passed after the fix, recorded in `/private/tmp/moedex-compiler-spike/primary-approved.stdout`; an earlier sandboxed invocation failed because the MSBuild build-host IPC timed out and was rerun with that permission. These results cover this dependency-free original project; no private-feed restore or entire CodeGraph solution execution was attempted.

## Managed-process acceptance gates

- Timeout and caller cancellation kill the owned process group, including a grandchild holding stdout open; shutdown remains bounded.
- Stdout over the configured cap terminates extraction; stderr diagnostics remain bounded even when a child floods them.
- Exit0 without a terminal record, exit2/incomplete analysis, malformed stream, and nonzero failure cannot publish an artifact.
- Tracked source and configuration mutation synchronized after the first project record reject completion; original source and retained projection are distinguished.
- Only declared managed Git inputs enter the projection; untracked build outputs and unrelated repositories do not silently enter capture.
- Success retains the exact projection for admission revalidation. Failure removes owned staging, preserves any existing output byte-for-byte, and does not advance CURRENT.

These controls bound a managed trusted build. They do not sandbox MSBuild targets or analyzers, and cannot promise containment of a hostile process that escapes its process group.

## Public Git adapter acceptance

The same original source fixture was copied into a separate disposable Git
checkout at `/private/tmp/moedex-public-capture-acceptance/codegraph`, preserving
commit `802e49848c63db555f94435e2d58e67c58327e36`. Its explicitly configured test
origin is `https://example.com/codegraph-fixture.git`. This tests the public Git
adapter; it is not an upstream CodeGraph acquisition claim.

Capture used `--checkout`, `--repo codegraph`, the commit and origin above, the
same original project/framework, SDK 10.0.100, and current worker
`/private/tmp/moedex-compiler-spike/worker-primary-final/worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll`.
The artifact records origin and commit, omits project ID, and contains one complete
context, 23 sources, 256 symbols, 189 declarations and 281 references. All ten
unchanged frozen labels passed. The retained projection is
`workspace-final/semantic-git-projection-1902167716/source` under that evidence root.

Unmanaged snapshot attachment published generation `20260930T215356Z-1`.
Executable MCP returned both overlapping facts at offset 1479 and one matching
primary constructor declaration, in the same generation. Evidence files are
`capture-final.stdout`, `capture-final.semantic`, `evaluation.json`,
`index.stdout`, `mcp.jsonl`, and `mcp.stderr` under the external evidence root.

An initial attempt accidentally selected the older `worker-primary-tests` binary,
which emits the superseded constructor kind. Validation rejected it and cleaned
owned staging without publishing an artifact. The current worker rerun passed;
the earlier failure logs remain as `capture.stdout`/`capture.stderr`.
