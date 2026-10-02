# .NET semantic worker foundation

This offline CLI loads a restored C# project with the real `MSBuildWorkspace`,
extracts compiler bindings, and writes `moedex.semantic-worker.v1` JSONL to stdout.
It does not restore packages, publish an index, or alter a serving snapshot.
The Go importer must validate the complete stream before admitting an artifact.

The first supported toolchain is the verified .NET SDK **10.0.100**. Build the
project using that SDK. Roslyn 5.0 and workspace dependencies come from its
`DotnetTools/dotnet-format` bundle; no NuGet dependencies are required. The entire
adjacent `BuildHost-netcore` directory is copied because workspace loading uses a
separate build-host process. This SDK layout is an explicit dependency, not a
portable assumption about every SDK release.

`--sdk-path` selects project evaluation; it does not replace the compiler and
workspace libraries bundled with the worker. Those libraries must support the
project's generators and language features. Inspection during the fresh-corpus
gate found that the retained SDK8 `dotnet-format` deployment actually reports
Roslyn **4.8.0.0** in its capture manifest. Earlier descriptions calling that
bundle 4.11 were incorrect; the hashed captures retain the actual version.

For SDK8 Razor projects, the verified opt-in build uses coherent Roslyn4.11
packages. Provision the exact dependencies explicitly, then build from the
retained cache with cleared feeds for offline replay:

```sh
dotnet build tools/semantic-dotnet/Moedex.SemanticWorker.csproj \
  -p:UsePinnedRoslyn=true --source https://api.nuget.org/v3/index.json \
  --packages /isolated/worker-packages
```

This pins compiler/workspaces4.11.0, Build.Locator1.7.8 and MSBuild compile
references17.11.4. MSBuild runtime assets are excluded so the selected installed
SDK supplies the engine. Package provisioning is opt-in; the default SDK-bundled
build remains package-free. Use separate output/build directories when switching
toolchains. Compiler, workspace and extractor assembly hashes remain part of capture identity.
The current extractor version is 21; readers retain support for earlier captures.

```sh
dotnet build tools/semantic-dotnet/Moedex.SemanticWorker.csproj -p:UseSharedCompilation=false
dotnet /path/to/Moedex.SemanticWorker.dll \
  --repo group/project --root /isolated/projection \
  --project App/App.csproj --configuration Debug --framework net10.0 \
  --sdk-path /path/to/dotnet/sdk/10.0.100 > capture.jsonl
```

`--repo`, `--root`, `--project`, `--framework`, and `--sdk-path` are required.
Configuration defaults to `Debug`. One invocation selects one explicit target
entry framework and configuration; worker21 emits that entry project and its
transitive compiler-reference closure. Referenced projects retain their own
declared frameworks. It does not emit every loaded framework variant or treat
analyzer-only projects as application compiler contexts.
`LoadMetadataForReferencedProjects=false` prevents fallback to stale built
project outputs. Non-C# dependencies and unverified workspace diagnostics yield
incomplete status. Version 19 normalizes named tuple element aliases to their underlying
`System.ValueTuple` storage fields, including long-tuple `Rest` chains, instead
of treating their source locations as missing source-project ownership.

Worker21 requires project-built analyzers/generators to exist before workspace
extraction. Managed capture restores the declared framework graph, then runs the
pinned SDK's `ResolveReferences` target in the isolated projection. The worker
matches each loaded output to exactly one evaluated declared framework; missing
or ambiguous identities fail closed. It reports missing analyzer inputs directly.
Analyzer DLL hashes remain capture inputs and are revalidated before publication.
Direct worker callers must prepare project references themselves.

Worker21's `native-build-events-v2` also preserves explicit Roslyn `Warning`
diagnostics and records them separately from native-correlated failures. A
workspace `Failure` still cannot be downgraded without matching native warning
evidence. Legacy no-log workspaces still require no diagnostics. See
[ADR 0064](../../docs/adr/0064-project-built-analyzer-preparation.md).

Worker20 uses the public workspace `BinaryLogger` overload and replays the actual
design-time build events. SDK10.0.401/Roslyn5.9 supplies those logs. A workspace
failure can be recorded as a warning only when typed warning events account for
every diagnostic by exact project/message identity and multiplicity, every build
and project completes successfully, and no build errors or log-read problems
occur. The warning text itself never determines severity. Unknown diagnostic
formatting/localization fails closed. Captures retain normalized event evidence
and the reader assembly identity; temporary raw binlogs are removed on disposal.
Limits are 64 logs, 128 MiB per log and 256 MiB aggregate. Repeated diagnostic facts
are emitted once; event multiplicity remains in the capture evidence.

Older workspaces (including the verified SDK10.0.100 and pinned Roslyn4.11 builds)
ignore the logger overload. They keep the prior strict policy: only a workspace
with **no diagnostics** can complete. This is explicitly recorded as
`workspace-no-diagnostics-v1`; it never justifies downgrading a warning. Native
verification uses `native-build-events-v1`. Import and publication revalidation
check these policy records. Compilation and generator errors remain independent
failure conditions. No warning suppression, package/source rewrite, or
`TreatWarningsAsErrors` override is applied. See
[ADR0063](../../docs/adr/0063-native-build-diagnostic-severity.md).

Run the isolated regression (local build-host IPC required):

```sh
python3 tools/semantic-dotnet/test_workspace_diagnostics.py \
  --dotnet /path/to/dotnet --sdk-path /path/to/sdk/10.0.401 \
  --output-dir /new/disposable/regression
```

Compilation errors remain visible and propagate to dependent contexts.
Because workspace compilation diagnostics omit generator-driver failures, a
public `CSharpGeneratorDriver` verification pass runs the same generators using
the workspace's actual parse options, additional texts, and analyzer-options
provider. It collects generator diagnostics and compares generated tree paths
and text digests with workspace output. Generator exceptions or disagreement
mark the context incomplete. Generators therefore execute twice; generators
with nondeterministic output cannot receive complete status from this worker.

Run against an already restored **disposable, writable projection**. MSBuild
evaluation executes project targets, and compiler generators/analyzers execute
code. This executable is **not a build sandbox**; the caller must isolate hostile
projects, environment, network, and credentials. It may write `obj/` through
design-time targets. Local build-host IPC must be permitted. Do not point it at a
canonical managed checkout. Production projection/process lifecycle integration
and cancellation/time/memory limits are not implemented by this standalone
worker. The managed capture command below owns projection, deadline, output and
process cleanup; OS-level memory and execution containment remain external.

Each project record includes evaluated project/import/package assets, compiler
and workspace identities, actual compiler options, metadata references, project
references (including aliases and embedded-interop settings), analyzers,
additional files, analyzer configurations, and raw source digests. Sensitive
arbitrary environment properties are not dumped. `capture_json` holds the exact
serialized capture; `build_context` is SHA256 of its UTF8 bytes. Referenced
project contexts are included transitively. Input records distinguish `source`,
`sdk`, and `external` scope for consumer verification. Source/config bytes are
rehashed before the final terminal row; a detected mutation fails the stream.
This is observed-input capture, **not an immutable-build attestation**: files can
change before first observation or after the final check, and arbitrary custom
target/generator reads, environment values, and transitive analyzer dependencies
are not comprehensively intercepted. The importer rechecks confined source and
configuration inputs at admission. SDK/external identities are retained for audit.

Physical source paths must remain inside the projection; escaping links and
external linked sources fail explicitly. Real source-generated documents receive
reserved `.generated/` paths and embedded raw UTF8 bytes. Disk-generated source
files also carry embedded bytes. Original UTF8/BOM/CRLF bytes are preserved and
verified against compiler text. Other source encodings fail explicitly; emitted
byte spans never refer to a transcoded stand-in. Local roots and physical paths
may appear inside capture evidence or diagnostic text; do not treat the output as
a redacted public report.

Symbols use Roslyn original-definition documentation descriptors where available.
The namespace is `repo/relative.csproj` for source symbols and assembly identity
for external symbols. `descriptor_kind: location_fallback` explicitly marks
locals and other symbols without documentation IDs; these are not edit-stable.
Merged namespaces use the global assembly namespace because no unique owning
project exists. References currently cover simple names, invocations, and object
creation. Compiler-generated implicit operations, every operator/conversion
binding, dynamic dispatch targets, and runtime call graphs are not claimed.

Extractor version 2 optionally attaches `domain_facts` to existing resolved
reference rows in complete contexts. These are compile-time facts under
`csharp-framework-v1`, with typed targets and the original source/build evidence:

- Microsoft DI generic `AddSingleton`, `AddScoped`, and `AddTransient` registrations
  with service/implementation types (or a self registration), without factories
  or instance arguments.
- MassTransit `IPublishEndpoint.Publish<T>` and class base-list `IConsumer<T>`
  references, retaining the message contract.
- EF Core `DbSet<T>` property types and generic `ToTable<T>` calls with constant
  nonempty table names and optional constant schemas.

Recognition requires the exact framework metadata type and assembly name;
project-local lookalikes do not qualify. Assembly bytes remain bound by the
captured metadata references. This is API identity evidence, not an authenticity
attestation for a package. Constructed/open generic target types, generic
containing types, unresolved/dynamic calls, factory bodies, private wrappers,
nonconstant table names, and incomplete contexts produce no positive domain
fact. Their ordinary compiler occurrences and diagnostics remain present.
Facts establish configuration sites and declared contracts, not actual service
activation, message delivery, database reads/writes, or an effective runtime
configuration. They do not change occurrence or stream-summary counts.
The allowlist follows the public
[Microsoft DI API](https://github.com/dotnet/runtime/blob/main/src/libraries/Microsoft.Extensions.DependencyInjection.Abstractions/src/ServiceCollectionServiceExtensions.cs),
[MassTransit publish contract](https://github.com/MassTransit/MassTransit/blob/develop/src/MassTransit.Abstractions/IPublishEndpoint.cs),
[consumer contract](https://github.com/MassTransit/MassTransit/blob/develop/src/MassTransit.Abstractions/IConsumer.cs),
and [EF relational builder API](https://github.com/dotnet/efcore/blob/main/src/EFCore.Relational/Extensions/RelationalEntityTypeBuilderExtensions.ToTable.cs).

Exit 0 means all loaded projects are complete; exit 2 means a structurally
complete stream with incomplete analysis; exit 1 means failure and **no terminal
summary**. Every successful stream terminates in `stream_summary` with exact
project/declaration/reference counts. Consumers must reject truncation regardless
of earlier project statuses. The format is a worker interchange contract; the
Go importer owns persisted semantic identities and artifact format.

Run the offline integration tests (build and all fixture work happen in a temp
directory; no corpus artifacts are written to this repository):

```sh
python3 tools/semantic-dotnet/test_worker.py \
  --dotnet /path/to/dotnet --sdk-path /path/to/dotnet/sdk/10.0.100
```

The suite checks 19 independently authored fixture reference labels, distinct
linked-file project contexts, UTF8/BOM/CRLF spans, exact capture digests, repeated
output determinism, configuration invalidation, missing restore status,
outside-source rejection, project-reference aliases, and repository namespaces.
It also exercises the SDK's real `GeneratedRegex` generator and a custom throwing
generator whose failure would otherwise be absent from compilation diagnostics.
Arbitrary package graphs, third-party incremental generator ecosystems, and
general analyzer execution diagnostics are not yet comprehensive test coverage.
An additional pipe-synchronized mutation control changes a source after its
project row is emitted and verifies that the worker fails without a terminal
summary.

## Managed capture

For a repository already acquired into Moedex's managed catalog and lock, use
the explicit capture command with an installed SDK and built worker:

```sh
moedex semantic capture \
  --managed-root /managed/corpus --repo group/project \
  --project App/App.csproj --framework net10.0 --configuration Debug \
  --dotnet /path/to/dotnet --sdk-path /path/to/dotnet/sdk/10.0.100 \
  --worker /path/to/Moedex.SemanticWorker.dll \
  --workspace /scratch/new-capture --output /scratch/new-artifact.json \
  --restore-offline --timeout 5m
```

Both destinations must be new. The runner projects exact locked Git blobs,
excluding dirty/untracked checkout content, and enforces managed workspace
privacy eligibility. It performs explicitly enabled restore using cleared feeds
and a private empty package cache. The initial route supports SDK framework
references without package downloads; unavailable packages fail. It then runs
the worker with bounded output and a deadline, validates the complete stream,
and stages the artifact. Failure cleans up newly owned workspace state.

Successful JSON output includes `artifact` and `workspace`. Keep the returned
workspace at that exact location: it is the compiler projection root, which is
nested inside the `--workspace` parent. Captured `obj`/assets/configuration inputs
must remain available for later attachment:

```sh
moedex index snapshot build --corpus /managed/corpus --index-dir /scratch/index \
  --semantic-artifact /scratch/new-artifact.json \
  --semantic-workspace /exact/workspace/from/capture/output
```

The attachment validates recorded inputs against the retained workspace and
source correspondence against canonical indexed bytes. There is no separate
receipt file. Do not recreate assets in a different directory and assume the
capture still matches.

This executes trusted MSBuild/analyzer/generator code. Empty package feeds and
process cleanup are not an OS network, memory or disk sandbox. See
[ADR 0030](../../docs/adr/0030-managed-compiler-capture.md) for the exact boundary.

## Public Git capture

For a public Git checkout, replace `--managed-root` with `--checkout /path/to/repo`,
`--commit FULL_COMMIT_ID`, and `--origin https://host/owner/repo.git`.
Use the canonical checkout directory basename as `--repo` so the artifact can
attach through unmanaged indexing. The checkout must be the repository root;
HEAD, configured origin, and raw tracked file bytes must match the selection.
Untracked build outputs are excluded. Origin is retained in the artifact's hashed
audit metadata without claiming network verification or remote ownership, and no
acquisition project ID is assigned.

`--max-projection-bytes 536870912` explicitly allows a 512 MiB source projection;
the default remains 256 MiB and the maximum is 1 GiB. Other artifact and worker
limits remain in force. Restore uses cleared feeds and a private package cache,
empty by default or populated from the explicit verified bundle described below.
In particular, successful Roslyn source projection does not imply its SDK and
Arcade requirements are available. See [ADR 0032](../../docs/adr/0032-public-git-compiler-capture.md).

## Explicit offline dependencies

For a project requiring packages, first acquire its dependency closure separately
into an isolated NuGet global-packages directory. Record the package sources and
archive hashes; packing does not authenticate a package's publisher.

```sh
moedex semantic dependencies pack \
  --packages /scratch/provisioned-packages --output /scratch/new-bundle
```

Add `--dependency-bundle /scratch/new-bundle` to the capture command, retaining
`--restore-offline`. The runner verifies and copies the complete manifest into
its private cache, keeps cleared feeds, and records `dependency_bundle_sha256`
in the result and artifact contexts. Missing packages or cache/manifest mutation
fail the capture. Keep the bundle and the retained capture workspace for audit;
the latter also contains `dependency-manifest.json`. See
[ADR 0033](../../docs/adr/0033-offline-compiler-dependency-bundles.md) for limits and
the distinction between cleared feeds and OS network isolation.

If a pinned SDK fails in NuGet static-graph restore,
`--restore-standard-evaluation` explicitly selects standard MSBuild evaluation
for the restore command only. The default preserves project-selected behavior;
this option does not rewrite upstream project files. It is recorded in capture
JSON as `restore_standard_evaluation`. Roslyn’s pinned SDK11 required this option
after its static-graph task threw a null-reference exception.

Large worker JSONL streams can use explicit `--max-worker-bytes`, up to
268435456 bytes (256 MiB). The default stays 64 MiB. This changes only transport
accounting: source/input and persisted-artifact limits remain 64 MiB. A capture
that exceeds the artifact limit still fails and publishes nothing.

Snapshot building enables the syntax graph by default. Use explicit
`moedex index snapshot build --graph=false` when validating lexical/ranking and
compiler components independently. The resulting snapshot advertises no graph
capabilities; this is not a successful graph-scale run. The Roslyn acceptance
record retains its separately interrupted candidate-graph scale attempt.

## Worker7: closed interface members and keyed DI

Worker7 preserves qualified named arguments in `constructed_interface_method_v1`
identities for supported closed interface methods. Nongeneric source classes can
record `csharp-interface-closed-v1` correspondence; explicit default interface
bodies record `csharp-interface-default-v1` at their own declarations. The latter
are open templates and do not select a closed class or prove dispatch. Generic
classes/methods, nested interface containers, array/open/constructed generic type
arguments and inherited class correspondence remain outside this increment.

`csharp-keyed-di-v1` recognizes exact two-type Microsoft DI keyed registration
calls with a `typeof` key. A bounded source helper summary substitutes closed
arguments into a first-statement keyed registration, regardless of helper name.
It emits configuration evidence at the helper call, with service, implementation,
key type, exact metadata API witness and lifetime. Conditional/nested bodies,
factories, arbitrary keys, recursive helpers and incomplete contexts produce no
positive summary. This does not resolve later container mutations or prove runtime
service selection. See [ADR 0047](../../docs/adr/0047-closed-interface-and-keyed-registration-evidence.md).

Run `test_framework_bridge.py` with explicit `--dotnet`, `--sdk`, `--worker`,
`--packages` and a fresh `--output` directory. It pins SDK selection for offline
restore, then checks positive, negative and incomplete source fixtures. Pass its
`capture.jsonl` and source root as `MOEDEX_BRIDGE_STREAM` / `MOEDEX_BRIDGE_ROOT` to
`go test ./internal/semanticimport -run TestPublicFrameworkBridge` for import,
persistence, mapped-index and public compiler-tool verification.

## Worker8: closed-class default selection

`csharp-interface-selection-v1` records Roslyn's selected closed default at a
concrete class declaration, separately from the open template. Its
`selected_default_symbol_id` preserves qualified arguments;
`default_template_symbol_id` permits source-body lookup. Overrides, most-specific
defaults, and inherited defaults are resolved by the compiler. This rule does not
infer forwarding or runtime execution, and it does not create contract-path hops.

Run `test_default_selection.py` with the same explicit SDK/worker/package arguments
and a fresh output directory. Pass `capture.jsonl` and its root through
`MOEDEX_DEFAULT_STREAM` / `MOEDEX_DEFAULT_ROOT` to
`go test ./internal/semanticimport -run TestPublicDefaultSelection` for the public
import/index/query gate. See [ADR 0048](../../docs/adr/0048-closed-class-default-interface-selection.md)
for exact bounds and exclusions.

## Worker9: bounded default forwarding

A selected default may carry `forwarding` under `csharp-default-forward-v1`:
one call on `this`, casting the sole parameter to an interface type parameter,
with its closed interface target, concrete source implementation, qualified cast
type and exact source-call witness. Unsupported bodies retain selection-only
evidence. Cast success and runtime execution remain unproved.

Run `test_default_forwarding.py` with explicit `--dotnet`, `--sdk`, `--worker`,
`--packages` and a fresh `--output`. Pass `capture.jsonl` and its source root through
`MOEDEX_FORWARD_STREAM` / `MOEDEX_FORWARD_ROOT` to
`go test ./internal/semanticimport -run TestPublicDefaultForwarding` for the public
round-trip and corruption gate. See [ADR 0049](../../docs/adr/0049-bounded-default-interface-forwarding.md).

## Capture portability budgets

Managed capture creates a private toolchain directory exposing only the requested
installed `sdk/version` directory. It copies the small dotnet host executable and
links trusted installed SDK/runtime directories, so both restore and Roslyn's
child build host discover the selected SDK without editing committed `global.json`.
The SDK and runtimes must remain installed while the retained workspace is used.
This does not sandbox MSBuild or make runtime/toolchain files immutable.

Restore invokes the selected SDK's `MSBuild.dll` directly with cleared feeds.
Worker failures report bounded explicit error diagnostics; source declarations and
capture manifests are excluded from failure messages.

`semantic dependencies pack` and `semantic capture` both accept
`--max-dependency-bytes`. Zero retains the 1 GiB default. An explicit value up to
4 GiB permits larger complete caches; eShopOnWeb's frozen cache uses a 2 GiB budget.
Every file is still enumerated, hashed, privately staged and verified before and
after execution. The manifest/file-count limits and exclusive output rules remain.
Budgets must be supplied independently at pack and capture; a larger bundle does
not silently raise a later capture's default.


### Typed responses and conditional registrations (worker10)

`csharp-framework-v5` records two bounded compile-time observations:

- `message_response`: the message contract in the bound one-argument generic
  `MassTransit.ConsumeContext.RespondAsync<T>(T)` or `RespondAsync<T>(object)` API.
  It does not establish message delivery, a requester, or a request/response route.
- `di_registration_if_absent`: the service, implementation and lifetime in the
  parameterless generic `TryAddSingleton`, `TryAddScoped` or `TryAddTransient`
  extension, with one or two type arguments. The kind retains the API's
  if-absent condition; it does not prove registration occurred or select a runtime
  implementation. A surrounding branch remains outside this observation.

Matching requires exact metadata owner, assembly name and method signature.
Factory/instance/runtime-type DI overloads, response pipe/non-generic overloads,
source lookalikes, open or constructed generic targets, and incomplete
compilations do not receive these facts. Assembly names are provenance, not
package publisher authentication. Run `test_response_di.py` against the cached
MassTransit.Abstractions8.2.1 package and SDK8 framework references, then set
`MOEDEX_RESPONSE_DI_STREAM` and `MOEDEX_RESPONSE_DI_ROOT` for
`TestPublicResponseAndConditionalDI` to verify artifact/index persistence and MCP.


### Two-response request configuration (worker11)

`csharp-framework-v6` emits `message_request_configuration` for the exact
MassTransit.Abstractions `IRequestClient<TRequest>.GetResponse<T1,T2>` overloads
whose first argument is `TRequest` or `object`, followed by CancellationToken and
RequestTimeout. Targets are ordered `request`, `response_1`, `response_2`.
All three must have supported concrete named identities; repeated types retain
their separate roles. Callback, one-response and three-response overloads,
open/constructed generic contracts, source lookalikes and incomplete captures
are excluded. The observation does not identify a runtime destination, consumer,
delivery, or selected response. It is attached to an exact source binding.

Run `test_request_response.py` with the pinned MassTransit.Abstractions8.2.1
cache, then set `MOEDEX_REQUEST_RESPONSE_STREAM` and `MOEDEX_REQUEST_RESPONSE_ROOT`
for `TestPublicRequestResponseConfiguration`. The native and public checks cover
response order, same-named request types, inherited clients, repeated responses,
negative overloads and incomplete suppression.


### Source static POST handlers (worker12)

`csharp-endpoint-v1` emits `endpoint_post_configuration` for the exact ASP.NET
Routing `MapPost(IEndpointRouteBuilder,string,Delegate)` metadata API. Its
`route_pattern` is a nonempty, bounded compiler-constant argument; the `handler`
target is an exact source-defined static ordinary method, with no generic method
or containing type. Named arguments and direct delegate conversions preserve
identity. Delegate variables, factories, lambdas, local functions, instance or
metadata handlers, generic owners/methods, dynamic patterns, other verbs and the
RequestDelegate overload are excluded.

The pattern is declared at the call site. It is not a composed URL: group prefixes,
conventions and hosting are not evaluated. A surrounding branch is not evaluated,
and pattern grammar, registration success or execution are not asserted. Navigate
the method target with `compiler_definitions` or retrieve its recorded facts with
`compiler_contract_impact` / `compiler_contract_context`; class evidence lookup
retains its existing scope.

Run `test_endpoint_mapping.py` with SDK8 framework references, then set
`MOEDEX_ENDPOINT_STREAM` and `MOEDEX_ENDPOINT_ROOT` for `TestPublicEndpointMapping`.
The gate checks seven configurations, fifteen exclusions, incomplete suppression,
persistence, method navigation and relevant public response schemas. The additive
`route_pattern` field is omitted from older facts, preserving their serialized
identities. New readers retain old captures; old readers need upgrading for the
new rule.


### Direct MediatR dispatch (worker13)

`csharp-mediatr-v1` emits `mediator_send_configuration` for the exact MediatR
ISender `Send<TResponse>(IRequest<TResponse>, CancellationToken)` API. Only a
direct request construction, beneath bounded implicit compiler conversions, is
accepted. The request's MediatR.Contracts interface must declare that exact
response type. Targets are `request` and `response`; handler selection, pipeline
behavior and execution are not inferred.

Responses support simple named identities and one closed generic level with up
to eight simple named arguments. `constructed_named_type_v1` stores the original
type documentation ID and independently qualified argument keys, bounded to
32 KiB. It preserves, for example, IEnumerable<OrderViewModel> without erasing the
project identity of OrderViewModel. Worker13 emits this representation for the
rule's response target; worker14 also embeds it in bounded interface arguments. Generic
response targets do not fabricate a definition for the constructed type.

Variables, factories, explicit casts, object/no-response overloads, concrete
Mediator methods, covariant response adaptation, generic request types, open or
nested generic responses, arrays and tuples remain excluded. Run
`test_mediatr_dispatch.py` with the pinned MediatR12.0.1 / Contracts2.0.1 cache,
then set `MOEDEX_MEDIATR_STREAM` and `MOEDEX_MEDIATR_ROOT` for
`TestPublicMediatrDispatch`. The gate covers seven positives, sixteen exclusions,
qualified generic identities, incomplete suppression, persistence and MCP schemas.


### Generic handler correspondence (worker14)

`csharp-interface-closed-v2` records ordinary source implementations of abstract
closed interface members whose arguments include a bounded constructed named type,
such as `IRequestHandler<GetMyOrders, IEnumerable<OrderViewModel>>`. Roslyn selects
the corresponding declaration. `constructed_interface_method_v2` preserves the
original method ID and qualified type arguments; the outer descriptor is at most
32 KiB, each generic level has at most eight arguments, and constructed arguments
contain only simple named types. Simple interfaces retain v1 keys and rules.

Source declarations must be ordinary methods on nongeneric classes. Open/deeper
generics, arrays, tuples, wide arguments and explicit generic implementations are
excluded. Existing default selection/forwarding bounds and the 32-fact cap remain.
Supported interface calls share the implementation target key. Dispatch response
keys can be compared exactly with interface arguments, without claiming runtime
handler selection or execution.

Run `test_generic_handler.py` against the retained MediatR cache, then set
`MOEDEX_GENERIC_HANDLER_STREAM` and `MOEDEX_GENERIC_HANDLER_ROOT` for
`TestPublicGenericHandler`. The fixture covers distinct response types, multiple
implementations, exact call and dispatch keys, nine exclusions and incomplete
suppression; the public gate checks persistence, navigation and response schemas.


### Positional open-generic DI registration (worker15)

`csharp-open-di-v1` emits `di_open_generic_registration_configuration` for exact
metadata AddSingleton/AddScoped/AddTransient overloads taking `Type, Type`.
Direct `typeof` operands must name unbound, non-nested definitions of arity 1–8:
an interface service and a nonabstract class implementation. The compiler must
map each service parameter to the implementation parameter at the same ordinal
position, including inherited interface correspondence. Targets are explicitly
`service_template` and `implementation_template`, with their original qualified
definition identities and the declared lifetime.

Swaps, repeated parameters, nested substitutions, unrelated types, class services,
abstract implementations, wide/nested definitions, variables, factories, explicit
casts, conditional type expressions, closed types and TryAdd/keyed APIs are
excluded. Constraints, constructors, registration order and runtime resolution
are not evaluated. The rule records a template, never a fabricated closed service.
Existing simple DI facts and symbol descriptors are unchanged.

Run `test_open_generic_di.py` with SDK8 framework references; set
`MOEDEX_OPEN_DI_STREAM` and `MOEDEX_OPEN_DI_ROOT` for `TestPublicOpenGenericDI`.
The gate covers nine positives, eighteen exclusions, incomplete suppression,
persistence, source definitions, reverse context and public response schemas.


### Declared MediatR assembly scan (worker16)

`csharp-mediatr-scan-v1` emits `mediator_assembly_scan_configuration` for the exact
MediatR metadata RegisterServicesFromAssembly(Assembly) API when its argument is
`typeof(Marker).Assembly`. Both the System.Runtime System.Type.Assembly property
and direct typeof operation are compiler-verified. The simple named marker is
recorded as `assembly_marker`, preserving project or assembly ownership.

This records declared scope, including detached or conditional configuration calls.
It does not enumerate handlers, infer registrations, evaluate callbacks or assert
runtime activation. The marker is not claimed to be a handler or namespace filter.
Generic types/owners, arrays, tuples, reflection lookups, variables, factories,
casts, conditional arguments, TypeInfo chains, alternate APIs and lookalikes are
excluded. The verified metadata boundary uses SDK8 and MediatR12.0.1.

Run `test_mediatr_scan.py` against the retained MediatR cache, then set
`MOEDEX_MEDIATR_SCAN_STREAM` and `MOEDEX_MEDIATR_SCAN_ROOT` for
`TestPublicMediatrScan`. The gate covers eight positives, twenty-one exclusions,
incomplete suppression, persistence, marker navigation, reverse context and schemas.


### Declared MassTransit namespace scans (worker17)

`csharp-masstransit-scan-v1` emits `consumer_namespace_scan_configuration` or
`activity_namespace_scan_configuration` for the exact RegistrationExtensions
AddConsumersFromNamespaceContaining<T> / AddActivitiesFromNamespaceContaining<T>
metadata overloads taking an optional Func<Type,bool>. The predicate must be
compiler-constant null, including its omitted default. A simple marker in a
non-global namespace becomes the qualified `namespace_marker` target.

The marker declares assembly/namespace scan scope; it is not asserted to be a
consumer, activity or discovered registration. No registration inventory, endpoint
construction or execution is inferred. Non-null/unknown predicates, generic
markers/owners, arrays, type parameters, global-namespace markers, Type overloads
and source lookalikes remain excluded. Static/named calls and branches preserve
their declaration without evaluating runtime behavior.

Run `test_masstransit_scan.py` with pinned MassTransit8.2.1 and SDK8, then set
`MOEDEX_MASSTRANSIT_SCAN_STREAM` and `MOEDEX_MASSTRANSIT_SCAN_ROOT` for
`TestPublicMassTransitScan`. The gate covers fourteen positives, twenty-four
exclusions, incomplete suppression, persistence, exact marker navigation, reverse
context and public response schemas.


### Bounded routing-slip activity configuration (worker18)

`csharp-routing-slip-v1` records `routing_slip_activity_configuration` for the exact
IItineraryBuilder.AddActivity(string, Uri, object) API. A nameof activity label must
match the activity argument of an IEndpointNameFormatter.ExecuteActivity<A,T>()
call that initializes the address field. Targets retain activity, arguments,
address_field and formatter_api identities.

The field must be private, readonly, uninitialized and referenced on the current
instance. Its owner has exactly one source constructor: at most 32 statements and
1,024 descendant nodes, one field reference, and one direct top-level assignment.
Only Uri(string) over `exchange:` plus that direct formatter invocation is accepted.
No aliases, conditional/repeated assignments, multiple constructors, mutable fields,
other-instance reads, factories, alternate URI patterns or overloads are inferred.

The result is declared configuration, not a resolved URI, payload validation,
runtime formatter selection, itinerary execution, compensation or delivery proof.
Run `test_routing_slip.py` with MassTransit8.2.1 and SDK8; set
`MOEDEX_ROUTING_SLIP_STREAM` / `MOEDEX_ROUTING_SLIP_ROOT` for `TestPublicRoutingSlip`.
Five positives and nineteen exclusions cover identity agreement, witness bounds,
incomplete suppression, persistence and public navigation/schema validation.

## Project-built analyzer regression

`test_project_analyzers.py` runs CLI capture and publication on a disposable,
committed multi-target application, netstandard library and source generator,
using custom artifacts output paths. Supply `--moedex`, `--dotnet`, `--sdk-path`,
`--worker`, `--dependency-bundle`, and a fresh `--output-dir`. The offline bundle
must contain Microsoft.CodeAnalysis.CSharp 4.12.0 and its dependencies, including
the netstandard targeting package. It tests Debug/Release generated bindings,
source-scoped analyzer hash revalidation, generator-build errors and missing
analyzer rejection. It provisions no packages or services itself.
