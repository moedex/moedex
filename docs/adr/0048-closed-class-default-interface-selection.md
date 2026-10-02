# ADR 0048: Closed-class default-interface selection

Status: accepted, implemented in worker8.

## Context

Worker7 records an open default-interface template and closed generic handler
correspondence separately. Neither establishes which default body the compiler
selects for a particular class. The eShop handlers require this additional link:
the nongeneric entry point selects a generic interface's explicit default body.

## Decision

Ask Roslyn's
[FindImplementationForInterfaceMember](https://learn.microsoft.com/en-us/dotnet/api/microsoft.codeanalysis.itypesymbol.findimplementationforinterfacemember)
for each eligible abstract instance method on a nongeneric interface implemented
by a concrete, nongeneric source class. Emit only when the selected implementation
is a source-backed explicit default method in a supported closed generic interface.
Roslyn establishes the selection; names do not infer it.

The class declaration carries `interface_default_selection`, rule
`csharp-interface-selection-v1`, evidence scope `compile_time`, with four identities:

- `interface_symbol_id`: the nongeneric entry-point slot.
- `implementing_type_symbol_id`: the concrete class, also the declaration anchor.
- `selected_default_symbol_id`: the closed default method, retaining qualified
  named type arguments in `constructed_interface_method_v1`.
- `default_template_symbol_id`: the original method's source identity, usable
  with `compiler_definitions` to inspect the body.

The closed descriptor must name exactly the template definition and encode the
explicit entry-point member. Readers validate every referenced identity and its
namespace. New fields are forbidden on older correspondence rules. Worker8 is
required for this rule; worker6/7 evidence remains readable. The v5 index layout
already stores bounded JSON payloads and reverse postings, so its layout does not
change. Older readers reject the new fields/rule rather than misinterpret them.

`compiler_symbols`, `compiler_binding_at`, and `compiler_definitions` expose the
class fact. `compiler_implementations` discovers and returns these class selections
alongside separately labelled template or class-method correspondences. Context
selection remains explicit. Template lookup uses its own recorded project context;
the constructed identity is never silently replaced by the template identity.

At most 32 selections are recorded per class declaration; overflow suppresses
that declaration's entire selection payload. Existing query work/result/byte
bounds include all four deduplicated symbols. Incomplete projects, abstract/open
classes, structs, generic methods, nested generic interface containers, unsupported
arguments (including arrays), metadata-only bodies, and nongeneric default bodies
are excluded. Inherited **default** selection is supported when Roslyn selects it;
inherited class-method correspondence remains outside the earlier rule.

## Consequences

This answers which closed default Roslyn selects for a known class. It does not
prove the runtime receiver's type, successful casts, forwarding, service activation,
or execution. Both default templates and class-selection facts are excluded from
automatic `compiler_contract_paths` implementation hops; MCP also rejects them
when supplied as such a hop by a reader.

The next increment can add a separately versioned, bounded forwarding observation:
inspect the selected template's operation tree, identify its receiver and argument
conversion, substitute the selected closed arguments, and retain the source call
and target correspondence as separate witnesses. Do not infer forwarding merely
because a default was selected: the native fixture includes an arbitrary body.

## Validation

`test_default_selection.py` exercises two source projects, two distinct qualified
arguments, most-specific selection, a shared diamond, inherited default selection,
an arbitrary body, class overrides, reabstraction, excluded type shapes, a 33-fact
overflow, metadata-only bodies, and an ambiguous compilation. The associated Go
public test traverses import, artifact persistence, mapped-index validation,
symbol discovery, reverse lookup, and cross-project template definitions.

See [acceptance](../../research/semantic-intelligence/DEFAULT-SELECTION-ACCEPTANCE.md)
for the pinned eShop development regression and retained evidence. No independent
agent score or CodeGraph comparison changes in this increment.
