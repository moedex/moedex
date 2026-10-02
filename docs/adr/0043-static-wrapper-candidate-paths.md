# ADR 0043: Static wrapper candidate paths

Date: 2026-10-01
Status: Accepted

## Context

The two remaining reviewed Outbox wrapper sites call interface members. Existing
bindings retain their source callers and exact interface targets, but do not
connect those targets to implementation declarations. A direct-publisher fact at
a wrapper call would claim more than the compiler evidence supports.

## Decision

Worker 6 records optional `implementation_facts` on complete, resolved source
method declarations. An `interface_method_implementation` fact under
`csharp-interface-v1` identifies the interface method and implementing source
type; the declaration binding identifies the implementing method. The compiler's
member-implementation lookup must confirm the relationship.

The initial subset covers directly declared, nonabstract instance methods on
nongeneric source types, including implicit and explicit implementations.
Inherited, static/default-interface, generic, and metadata implementations remain
outside the subset. At most 32 relationships may be attached to a declaration;
overflow omits the entire relationship list. Absence is not evidence that no
implementation exists. Canonical ordering, identity binding, symbol/type checks,
complete-context provenance, and worker-version validation apply at ingestion and
index opening. Earlier bindings retain their serialization when the field is absent.

Semantic index v4 adds bounded implementation payloads, invocation-target
postings, and implementing-method postings. Readers retain v1/v2/v3 compatibility;
older indexes cannot answer the new path query. The audit artifact format remains
unchanged, with an optional identity-bound field.

`compiler_contract_paths` starts with direct `message_publish` observations for an
exact message contract, and returns at most one wrapper step. A path connects the
publisher's enclosing method to an exact caller, optionally through an explicit
interface implementation relationship. Every hop retains source span/hash,
context, and snapshot identity. Paths are static candidates, not proof of dispatch,
execution, runtime DI selection, or message delivery. No publisher facts are
created at wrapper sites.

The initial API requires explicit context selection. It does not reuse existing
contract discovery, which omits wrapper-only projects. Multiple alternatives of
one repository/project cannot be selected together. Joins across selected contexts
do not certify dependency-closure compatibility. Queries share result, work, and
materialization budgets and report truncation; they do not perform unbounded
callgraph traversal.

## Evaluation

Keep all existing domain gold unchanged, including its two unsupported direct
wrapper cases. Freeze separate source-authored path labels before capture, with
exact callers, interface members, implementation declarations, direct publishers,
and message identities. Check both component-context alternatives, cross-message
negative cases, and the continued absence of direct wrapper publication facts.
Use real compiler fixtures for implicit/explicit relations and excluded cases,
plus mapped-index corruption, legacy-read, budget, cancellation, and lease tests.
