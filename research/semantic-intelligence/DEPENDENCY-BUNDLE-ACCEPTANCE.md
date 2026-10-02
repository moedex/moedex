# Offline dependency bundle acceptance

Date: 2026-09-30. Result: a real local NuGet package passed provisioned-cache
packing, public Git capture, snapshot attachment and executable MCP lookup.
This is a synthetic integration fixture, not Roslyn corpus acceptance or a
semantic precision/recall measurement.

## Frozen inputs

Evidence lives at `/private/tmp/moedex-package-acceptance`. The fixture declares
`Oracle.Dependency` version `1.2.3`, containing
`public static int Read() => 42`, and an app with a `PackageReference` to that
package. `Calls.cs` invokes `Oracle.Dependency.Value.Read()`.

The source-authored `gold.json` was written before any compilation. It expects
`M:Oracle.Dependency.Value.Read` in assembly
`Oracle.Dependency, Version=1.2.3.0, Culture=neutral, PublicKeyToken=null` at the
`Read` token. Its SHA-256 is
`646bee277fdeb199d606aa5b85372d8181926431043d69e4833a9dcac2c7d5a6`.
The app project, source and cleared NuGet configuration were committed before
compilation at `fd8af37f35eb79bb3902d650c00facfe4710ee52`. The namespace is `app`.
Its configured origin, `https://example.com/package-fixture.git`, is a test
identity; no upstream acquisition or ownership claim is made.

SDK 10.0.100 packed the library into a private local feed, then restored the app
from only that feed into a private extracted package cache. `provision.sh`
records the exact commands and isolated HOME, temporary directory and NuGet
cache settings. No network package source or ambient cache was used. The
resulting `.nupkg` hash, measured after packaging, is
`cdf4d0b300cf5b40ceff0962d506f7458359f5ca92ef111f11dfabe26a0c5bbd`.

## Capture and admission

The reviewed executable was copied into the evidence directory; its SHA-256 is
`4cecd66d4cf62ecf23e84ec753c8d557e91b57a04aaa6b5530f30b6206c133d7`.
The worker was the SDK-10 build at
`/private/tmp/moedex-compiler-spike/worker-primary-final/worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll`.

```sh
moedex semantic dependencies pack --packages packages --output bundle
moedex semantic capture --checkout app --repo app \
  --commit fd8af37f35eb79bb3902d650c00facfe4710ee52 \
  --origin https://example.com/package-fixture.git \
  --project App.csproj --framework net10.0 \
  --dotnet DOTNET --sdk-path SDK --worker WORKER \
  --workspace capture-workspace --output artifact.json \
  --dependency-bundle bundle --restore-offline
```

These abbreviated commands are relative to the evidence root; `capture.sh`
preserves the actual absolute paths. Packaging recorded five files, 8,082 bytes,
and exact manifest SHA-256
`39d24ea4abfe031e6a32cd1116f4120933fa3626a93f18949133d305b187a4d6`.
The captured context records that same digest. Capture returned one complete
context, three sources, 21 symbols, 39 occurrences/bindings and four diagnostics.

The first sandboxed capture failed with an MSBuild worker `TimeoutException`;
`capture.stderr` retains that failure. The approved rerun outside the sandbox
succeeded using the same local bundle and cleared feeds. This permission enabled
worker IPC; it does not establish OS network or process isolation.

The artifact hash is
`75f5685febbc5117417fd55865820cbe07a705278e8320ac404945c6e519fe39`.
`evaluation.json` records the frozen expectation passing **1/1**, with the raw
token beginning at byte 83. Attachment through `attach.sh` used:

```sh
moedex index snapshot build --corpus app --index-dir index \
  --semantic-artifact artifact.json \
  --semantic-workspace capture-workspace/semantic-git-projection-1084192098/source
```

It published local snapshot `20260930T223335Z-1`. `probe.py` started the actual
`moedex serve -index-dir ... -mcp -embed none` executable and sent JSON-RPC
requests over stdio. `compiler_binding_at` returned one resolved package method
with the expected descriptor and assembly. `compiler_definitions` returned
`no_definition`, correctly reflecting that the app capture contains package
metadata but no package source declaration. Both responses identified the same
published snapshot.

## Evidence and limits

Retained files include `gold.json`, `provision.sh`, `provision.stdout`,
`provision.stderr`, `pack.sh`, `pack.json`, `bundle/manifest.json`, `capture.sh`,
`artifact.json`, `evaluation.json`, `verify-gold.py`, `attach.sh`, `attach.stdout`, `attach.stderr`,
`probe.py`, `mcp-argv.json`, `mcp-input.jsonl`, `mcp.jsonl`, `mcp.stderr` and
`mcp-evaluation.json`. The original app and the retained capture projection are
distinct directories.

This proves the real NuGet package-cache path for one dependency and one frozen
binding. It does not execute the method to verify its return value, cover
transitive dependency graphs, establish package authenticity, prove sandboxing,
or complete the large Roslyn compiler gate. Bundle executable-mode preservation
and mode-change rejection are covered separately by deterministic unit tests.
No production deployment occurred.
