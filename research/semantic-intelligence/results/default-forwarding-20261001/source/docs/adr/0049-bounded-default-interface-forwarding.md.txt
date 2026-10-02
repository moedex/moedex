# ADR 0049: Bounded default-interface forwarding

Status: accepted, implemented in worker9.

## Context

Worker8 identifies the closed default selected for a concrete class. Selection
alone does not describe the body. The eShop bridge casts its argument and invokes
the generic interface member, which the concrete handler implements. Agents
previously had to inspect and connect these observations manually.

## Decision

Attach an optional `forwarding` observation to `interface_default_selection`.
Its independent rule is `csharp-default-forward-v1`, requiring worker9. Preserve
selection-only evidence when the body falls outside the supported pattern.

Use Roslyn operations and symbols to admit only:

- One expression-bodied invocation, one returned invocation, or one void
  expression statement. No async body or additional statements.
- One ordinary abstract instance member on the same generic interface container,
  invoked on the containing instance (`this`), with the same return type.
- One by-value argument: an explicit cast of the default method's sole parameter
  to one of that interface's type parameters. No nested casts, `as`, user-defined
  conversions, other argument computation, extra arguments, or generic calls.
- A supported closed substitution and a concrete source implementation declared
  on the selected class, resolved by `FindImplementationForInterfaceMember`.

Roslyn's [conversion operation](https://learn.microsoft.com/en-us/dotnet/api/microsoft.codeanalysis.operations.iconversionoperation)
distinguishes the operand, operator and try-cast semantics. Require actual cast
syntax and reject `IsTryCast`; `as T` must not be labelled as `(T)value`.

The observation retains receiver `this`, conversion
`explicit_type_parameter_cast`, type-parameter ordinal, qualified cast type,
closed target interface member, and source implementation identity. Its exact
call witness includes occurrence/context/source IDs, raw source hash, path, and
UTF8 token span. Existing symbol materialization now accounts for up to seven
deduplicated identities per selection fact; existing work/result/byte caps remain.

The importer resolves the worker's call project/fingerprint/path/span into actual
artifact identities. Artifact validation requires a matching resolved invocation,
the selected template as its enclosing method, the original interface target,
complete worker9 provenance, the same snapshot, and a context reachable through
the selecting context's captured dependency closure. Symbol validation checks
closed argument equality, cast ordinal/type and target/class ownership.

Index open independently joins the occurrence ID against sorted fact rows and
checks the source/context/enclosing-method/target witness. It reads fixed columns
only, avoiding materialization of another row's not-yet-validated JSON payload.
The JSON payload, source witness and additional symbols count toward normal
query byte bounds. There is no new index section or layout change (v5).

`compiler_symbols`, `compiler_binding_at`, `compiler_definitions`, and
`compiler_implementations` expose the nested observation. The returned locator
can be passed directly to `compiler_binding_at`; the returned implementation ID
can be passed to `compiler_definitions`. Use the recorded call context when
resolving the template, rather than combining context variants.

## Consequences and limits

This automatically connects a selected default, its source forwarding operation,
closed interface target, and concrete implementation. It remains a source
computation with a cast obligation. It does not establish cast success, actual
runtime receiver type, service activation, broker routing, or execution.

The rule does not follow inherited interface-container calls or inherited class
implementations, even where worker8 can select their default body. It does not
expand arbitrary control flow or recursively traverse further calls. Existing
`compiler_contract_paths` still excludes default selections/templates from its
ordinary implementation-hop model; this observation does not silently add a
multi-step path or change that API's semantics.

Worker6–8 artifacts remain readable. New forwarding observations require fresh
worker9 capture and a reader supporting the new optional field/rule. Older readers
reject the field rather than treating the cast as an unconditional call edge.

## Validation

Native controls cover five forwarding forms (including `this`, block, Task return
and ordinal-one substitution), ten selection-only cases, BOM offsets, incomplete
compilation, and prior selection exclusions. Public round trips verify the call
and implementation definitions. Semantic tests reject malformed substitutions;
artifact admission rejects invented calls; mapped-index open rejects corrupted
source witnesses even with a recomputed file checksum.

See [acceptance](../../research/semantic-intelligence/DEFAULT-FORWARDING-ACCEPTANCE.md)
for the three pinned eShop public workflows, compatibility, and retained evidence.
