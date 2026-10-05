# Capture portability acceptance

Date: 2026-10-01. Status: accepted for the two pinned .NET8 application closures.
Execution remained single-agent and CPU-only. No extractor C# logic or worker
version changed; this cycle changes capture orchestration, dependency budgeting,
and an explicit compiler-toolchain build option.

## Result

The previously blocked eShopOnWeb Web closure now captures and publishes from the
original combined SDK8/SDK10 installation, without editing its committed sources,
project closure or `global.json`. All five contexts are complete: 247 sources,
73 generated sources, 4,481 symbols and 45,896 bindings. The capture records
Roslyn4.11.0.0. All ten previously frozen exact source anchors resolve through
public MCP. Artifact SHA256:
`6c1f2cc22de3b61e8ba4ccff9f178a2ab68d226019658c2bf76676a7bf42ecb8`.

ForkJoint also recaptures successfully with the same compiler and original combined
SDK installation. Its eleven frozen source anchors all resolve and retain the
previous observation coverage. Artifact SHA256:
`85f21ffc79468b372904fb4f645060d562bce6610c503a9c863200b7f1cc8fb1`.

Both gold files and upstream commits are unchanged from the
[fresh baseline](COMPACT-EVIDENCE-ACCEPTANCE.md). These source-authored development
checks do not increase the independent solver score or establish superiority over
CodeGraph.

## Changes

- Restore invokes the requested SDK's `MSBuild.dll`, rather than asking `dotnet
  restore` to select an SDK independently.
- A private toolchain directory exposes exactly one installed SDK. The runner
  privately copies the small host executable and links trusted installed runtime,
  SDK, targeting-pack and manifest directories. This also controls Roslyn's child
  build-host discovery. The committed application SDK file is untouched.
- Pack and capture accept explicit `--max-dependency-bytes` budgets up to 4 GiB.
  The default remains 1 GiB and must be overridden independently at both steps.
  Every dependency file is still hashed, privately staged and reverified; manifest
  size, file-count, exclusive-output and cleanup protections remain intact.
- Failed worker captures expose bounded error diagnostics from JSONL, excluding
  declarations and capture/source payload fields, rather than only an exit code.
- `UsePinnedRoslyn=true` selects coherent compiler/workspace4.11.0 packages,
  Locator1.7.8 and MSBuild compile references17.11.4 with SDK-owned runtime loading.
  The default package-free SDK-bundled worker build remains available and passed.

The complete eShopOnWeb cache is 1,166,409,179 bytes across 8,672 files; it uses an
explicit 2 GiB budget. Its manifest SHA256 is
`b9a0dfa13621a88afdd95d667db3480f0d7b6493867006dc9af00fcd5937b953`.
ForkJoint's 992,067,197-byte bundle succeeds with the unchanged 1 GiB default.
Capture summaries record the effective budget.

The toolchain directory references installed files, so those installations must
remain available. This is SDK-selection control, not immutable tooling or an
execution sandbox. Captured compiler/input hashes retain the existing provenance
boundary. A selected SDK must have the standard installed `sdk/version` layout.

## Diagnosis and retained failures

The prior SDK8 `dotnet-format` deployment reports compiler version **4.8.0.0**,
not 4.11 as earlier prose described. Its manifest and assembly hashes were always
retained; historical result archives were not rewritten. Live worker guidance now
corrects the description. The coherent4.11 build resolves the Razor component
failures and produces the generated sources accepted by the existing independent
generator-driver verification.

The first package-based worker incorrectly loaded transitive MSBuild runtime
assemblies; excluding those assets restored SDK-owned loading. A later combined
host run still selected the wrong child-build-host SDK. The SDK-only control
passed, and the automatic private toolchain directory then passed against the
original combined installation. Earlier failures are archived, including a test
assertion corrected for macOS `/var` versus `/private/var` canonical paths.

## Measured coverage and next work

| Application | Resolved source anchors | Recorded requested higher-level facts | Missing requested higher-level facts |
| --- | ---: | ---: | ---: |
| eShopOnWeb | 10/10 | 3/8 | 5/8 |
| ForkJoint | 11/11 | 4/11 | 7/11 |

Two eShopOnWeb expectations are binding-only and are excluded from the fact
columns. Its remaining gaps are two MediatR sends, the handler with a nested
generic result type, assembly scanning and open-generic repository registration.
ForkJoint still lacks typed response, consumer/activity namespace scanning,
conditional singleton registration, local-method POST mapping, two-response
request and routing-slip configuration observations. Absence of a recorded fact
is not absence of behavior, and no runtime relationship is inferred.

Next: bounded typed-response and conditional-DI rules with adversarial fixtures,
then more complex scanning and generic shapes. Independent agent coverage remains
3/12 and the paired CodeGraph gate remains open.

## Validation

Full Go tests passed with newly captured forwarding, default-selection and
framework-bridge fixtures enabled. Native fixture scripts passed; the default
SDK10 worker build passed with zero warnings/errors. Go vet and race checks for
capture/app/CLI packages passed. Tests cover budget rejection and cleanup, one-SDK
discovery, private host copying, canonical paths, pinned restore arguments,
property-separator rejection and bounded diagnostic filtering. Both public source
baseline checks passed. Test servers were stopped after measurements.

See result.json (archival evidence maintained separately) for retained
commands, logs, manifests, source snapshots and hashes. Large corpora, caches,
indexes and executables remain under `.local/capture-portability/`.
