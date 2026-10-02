# Static wrapper-path acceptance

Recorded October 1, 2026. Locally verified; not deployed.

Both reviewed Outbox wrapper cases now have compiler-backed static candidate
paths. Worker 6 captures interface-member implementation evidence at source method
declarations. Semantic index v4 and `compiler_contract_paths` join that evidence
with exact invocations and direct publisher observations. Each returned hop retains
source span/hash, context, and snapshot identity. See
[ADR 0043](../../docs/adr/0043-static-wrapper-candidate-paths.md).

## Independent path gate

The pinned application remains at
`1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5`, with unchanged source. Separate
[path gold](results/application-outbox-20261001/wrapper-path-gold-v1.json) was frozen
before capture, SHA-256
`386cd0f02408434a08c65f54bc38d8e17d6c082395761441471efabcc70c6506`.
It identifies two paths:

- Controller `Submit` → `IRegistrationService.SubmitRegistration` →
  `RegistrationService.SubmitRegistration` → direct `RegistrationSubmitted` publisher.
- Consumer `Consume` → `IRegistrationValidationService.ValidateRegistration` →
  `RegistrationValidationService.ValidateRegistration` → direct `RegistrationValidated` publisher.

These arrows connect recorded source relationships; they do not establish runtime
dispatch, execution, DI selection, or delivery. Contexts are explicitly selected,
with one variant per project. Implementation evidence must share the publisher's
compilation; a selected caller context is not certified deployment closure.

| Measure | Result |
| --- | ---: |
| Source-authored paths | 2 / 2 |
| Positive path instances across both component variants | 4 / 4 |
| Missing / extra positive paths | 0 / 0 |
| Wrong-message checks | 4 passed |
| Synthetic direct-publisher facts at wrapper calls | 0 |
| Truncated responses | 0 |
| Existing supported domain observations | 15 / 15 |
| Existing context-specific domain facts | 26 / 26 |

The four positive calls return one seed and one candidate path each, using four
evidence records per response. They total 43,972 serialized complete-result bytes
(42,844 structured-content bytes). The four negative calls total 30,754 complete
bytes (29,626 structured bytes); two legitimately return a different caller's
path. Negative checks reject the wrong caller/message join, not all possible
paths for that message. Maximum inspected work in these cases is five rows.
This is neither a latency measurement nor a competitive comparison.

Existing domain gold remains unchanged: its two wrapper sites are still excluded
as direct publisher observations. The new path gate establishes a separate
capability rather than turning those labels into framework facts.

## Bounds and validation

The initial API requires explicit context IDs and searches one caller step. It
does not discover wrapper-only projects or recursively traverse calls. Limits are
100 total returned evidence records (including repeated evidence), 20 seeds,
10,000 inspected rows, and 2 MiB materialization, with separate seed/path
truncation. Up to 32 selected contexts are allowed; same-project alternatives
cannot be combined. Index v1–v3 remain readable but require rebuilding for paths.

Real compiler fixtures pass five implementation declarations/six relationships,
including implicit, explicit, overload, and metadata-interface cases; nine
exclusions cover unsupported shapes and overflow. Incomplete compilation emits
no relationships. Real stream import, malformed-stream rejection, and worker
version checks pass. Existing worker and EF/MassTransit regression gates pass.

Full Go tests, build, vet, affected-package race checks, mapped-index corruption,
legacy-header reads, cancellation/lease, and budget tests pass. Normal and race
application, task, discovery, and path reports agree. Tests specifically cover
checksum-recomputed worker downgrades, constructor/declaration mismatch, exact
declaring types, and charging symbol bytes before materialization.

The [evidence manifest](results/application-wrapper-v1-20261001/result.json)
records reports, hashes, and replay commands. The ignored persistent workspace
`.local/application-impact/run-wrapper-v1/` retains the compiler captures and
published index. No GPU work is involved.

Next acceptance work is broader independent application/path coverage,
discovery-inclusive journeys, scale/latency measurement, and a matched CodeGraph
comparison. Generic, inherited, default/static-interface implementations and
multi-step traversal remain outside this initial relationship/path subset.
