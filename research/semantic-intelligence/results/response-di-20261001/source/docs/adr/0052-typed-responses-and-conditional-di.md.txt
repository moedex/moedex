# ADR 0052: Typed response and conditional DI observations

Status: accepted. Date: 2026-10-01.

## Decision

Worker10 introduces `message_response` and `di_registration_if_absent` under
`csharp-framework-v5`. The former admits only the two one-argument generic
MassTransit ConsumeContext response signatures. The latter admits only the
one/two-type generic TryAdd singleton/scoped/transient extensions with no factory,
instance or runtime Type argument. Metadata owner, assembly and signatures are
exact; targets must retain concrete named identities.

The conditional DI kind preserves the if-absent behavior instead of presenting
TryAdd as unconditional registration. The response fact records its contract
without inventing a requester, endpoint, delivery or request/response edge.
Both remain compile-time observations attached to the existing exact binding.
No new wire fields or runtime execution claims are introduced.

## Compatibility and validation

Existing rule generations remain valid with worker10. All persistence and public
query gates recognize the new worker, and older captures remain readable.
Unknown future workers and downgraded new-rule facts are rejected. Real-framework
fixtures cover overloads, generic exclusions, lookalikes and incomplete capture;
public tests verify artifact/index roundtrips and typed targets.

See [acceptance](../../research/semantic-intelligence/RESPONSE-DI-ACCEPTANCE.md)
for frozen application measurements and retained evidence. Scanning, generic
identity expansion and multi-response requests remain separate work.
