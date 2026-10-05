# Source static POST endpoint mapping acceptance

Date: 2026-10-01. Status: accepted for the bounded endpoint rule.
Single-agent, CPU-only execution.

## Result

The frozen ForkJoint POST mapping gap is closed. Public MCP records the declared
`/order` pattern with the exact source method
`OrderRoutes.SubmitOrder(Order, IRequestClient<SubmitOrder>, CancellationToken)`.
The method target has the same symbol identity as the enclosing method of the
previously recorded request/response call. Public definition navigation and
contract context verify this source-backed chain without claiming execution.

ForkJoint improves from 7/11 to 8/11 requested higher-level facts. eShopOnWeb stays
3/8. All 21 frozen source anchors resolve. Across the two applications, 11/19
requested facts are recorded and eight gaps remain; two binding-only expectations
are excluded from the denominator. Sources, commits and gold are unchanged.
Independent solver coverage remains 3/12. These development checks do not close
the paired CodeGraph comparison.

## Rule boundary

Worker12 introduces `endpoint_post_configuration` under `csharp-endpoint-v1`.
Only the exact metadata API
`EndpointRouteBuilderExtensions.MapPost(IEndpointRouteBuilder,string,Delegate)`
in Microsoft.AspNetCore.Routing is accepted. `route_pattern` contains the bounded,
nonempty compiler-constant argument; the sole `handler` target is a source-defined
static ordinary method with no generic method or containing type. Compiler
conversions and direct delegate creation are unwrapped, with a fixed depth bound.

The pattern is declared at this call site. Group prefixes, conventions and hosting
are not composed into an effective URL. Surrounding conditions, route grammar,
registration success, endpoint activation and execution are not inferred.

Delegate variables, properties/factories, lambdas, local functions, instance or
metadata handlers, generic methods/owners, dynamic/empty/null/oversized patterns,
other HTTP verbs, and the RequestDelegate overload remain excluded. Exact
metadata identity rejects source lookalikes; assembly identity is captured
provenance rather than publisher authentication. The
[ASP.NET8 source](https://raw.githubusercontent.com/dotnet/aspnetcore/v8.0.0/src/Http/Routing/src/Builder/EndpointRouteBuilderExtensions.cs)
provides the API and group-prefix semantics used to bound the observation.

The additive `route_pattern` field is carried through import, artifact/index
persistence and MCP schemas. It is omitted from older facts, preserving their
serialized identities. New readers accept older captures; the new rule requires
worker12, and unsupported future workers are rejected. Handler navigation uses
exact method definitions and contract evidence. Class evidence lookup retains its
existing class-target scope.

## Validation

- Seven positive configurations: literal/constant patterns, static invocation,
  named arguments, selected overload, grouped route and conditional source call.
- Fifteen excluded cases and suppression of all interpreted facts when compilation
  is incomplete.
- Artifact/index write-reopen, exact handler definition navigation, contract-impact
  and contract-context navigation, including validation against public JSON schemas.
- Five earlier native suites pass with worker12. Retained worker11 fixtures pass
  public persistence checks; index compatibility covers workers6 through12.
- Full Go tests, vet and race checks pass, with all six native fixtures enabled.
  Pinned Roslyn4.11 and default package-free builds pass with zero warnings/errors.
- Both frozen applications recapture, publish and pass public checks. Only the
  `route-post` expectation changes. Its exact pattern, method descriptor and link
  to the request call's enclosing method were checked.

Earlier failures are retained: a misplaced serialization edit caught by the build,
an attempted native run before that build succeeded, and an overbroad test
expectation that class evidence lookup would return method-target facts. The final
checks use supported method navigation and pass.

Final artifact SHA256:

- ForkJoint: `16fb2cc29b91ef88bfdc3e49729073cc6a043afa52437c270a37f99deb077a98`.
- eShopOnWeb: `e806bfdd740b44ae014a2d9fb590ebdc2bb89c7f6d94cecd9e58421b646e6633`.

See result.json (archival evidence maintained separately) for hashed source
snapshots, commands, logs, native captures and public requests/responses. Large
artifacts, corpora, dependencies and binaries remain local. Test servers stopped.

## Remaining work

ForkJoint: consumer scanning, activity scanning, routing-slip configuration.
eShopOnWeb: two MediatR sends, nested-generic handler correspondence, assembly
scanning, open-generic repository registration. Next: bounded MediatR dispatch
observations at the frozen source anchors. Missing facts do not imply missing
application behavior.
