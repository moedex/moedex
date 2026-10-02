# External application-sample impact gate

Recorded October 1, 2026. Baseline verified locally; not deployed. This run uses
CPU/disk and the compiler pipeline, with no dense embedding arm or GPU workload.

## Results

| Measure | Result |
| --- | ---: |
| Supported source observations | 8 / 8 |
| Context-specific supported observations | 14 / 14 |
| Captured domain facts preserved through MCP | 14 / 14 |
| False positive observations in reviewed scope | 0 |
| Negative-control violations | 0 / 2 |
| Selected unsupported application patterns | 9, all still unrepresented |
| Leased MCP queries with identical normal/race reports | 72 / 72 |

The eight supported sites comprise two DI registrations, two direct publishers,
and four consumer declarations. The nine gaps comprise two state-machine fluent
publishes, one saga event configuration, two private service-wrapper calls, two EF
entity operations, and two AddDbContext registrations. Their ordinary compiler
bindings resolve in every captured context: capture succeeded, but domain rules
and wrapper analysis do not yet represent these relationships.

Precision/recall of 1.0 in the machine report applies only to the frozen supported
roster. It is not overall application impact recall. Unsupported sites remain
coverage gaps, and the nine selected gaps are not an exhaustive inventory of all
unsupported patterns. No domain detector or gold label was changed after baseline
selection.

## Scope and label independence

The selected target is the public [MassTransit transactional-outbox sample](https://github.com/MassTransit/Sample-Outbox).
It is an external application sample, not newly authored Moedex fixture code or a
production deployment. Its API and worker reference shared components. The pinned revision is
[`1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5`](https://github.com/MassTransit/Sample-Outbox/tree/1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5). Original
source and project bytes must remain unchanged at the recorded Git revision.

A source-review agent authors and freezes labels before compiler extraction.
The [frozen gold](results/application-outbox-20261001/source-gold.json) has SHA-256
`b91cdf38bbeb036274ff689618056e43c712c62f73a7f659726869a1437c3605`.
It reviews all 26 tracked C# files and labels eight supported observations, nine
selected unsupported patterns, and two negative controls. Labels include source
hashes, raw UTF-8 token spans, and exact expected target identities. Six supported
observations have source-authored owner identities; the two top-level/lambda DI
sites leave synthesized owner names unspecified and retain structural provenance
checks instead. This separates expected behavior
from extractor output; it is not independent human annotation. Selection favors
a manageable project closure with messaging and DI observations, so scores must
not be generalized to arbitrary applications.

Supported observations, unsupported application patterns, and genuine negative
controls have separate denominators. In particular, an unsupported relevant
pattern does not count as a successful negative. Exact extra facts within reviewed
scope count as false positives. An audit-derived inventory may expand the set of
queried symbols to find extras; it must never supply expected answers.

## Workflow

Acquire a pinned public Git checkout outside the repository. Provision public
packages separately, freeze an offline dependency bundle, and use the production
capture command for each application entry project. Compose complete artifacts,
publish with retained workspace mappings, and query the published snapshot via
the actual leased compiler provider. Verify source identities, contexts, owners,
API evidence, exact spans, and the manifest's artifact hash.

Do not modify detectors after seeing this baseline without retaining the initial
results and reusing unchanged labels. Capture or publication failures are recorded
as workflow failures, not silently replaced by rewritten projects or incomplete
compiler results. No application, broker, or database needs to execute to evaluate
recorded compiler observations.

## Expected limitations

The source review includes registration/storage wrappers and state-machine APIs
outside the first framework rule. These can be meaningful impact relationships
without being supported compiler domain facts. Absence of a recorded fact cannot
establish absence of impact. Shared event types do not establish runtime delivery,
service activation, database persistence, or deployment compatibility.


A consumer declaration in the shared Components library is not an application
activation result. Both entry projects can capture that library; capture-root
membership must not be converted into a claim that both applications register or
execute its consumers. Application registration/state-machine configuration is a
separate coverage dimension from exact contract-to-source observations.


## Publication defect found before scoring

The first real API/Worker capture and composition completed, but publication
rejected two hashes for the shared Components project's generated MSBuild
editorconfig file. Each retained projection correctly recorded its own absolute
build paths. The file was absent from the clean lexical corpus, yet attachment
rejected the differing optional inputs before checking whether they were indexed.

Attachment now retains optional input alternatives until it examines indexed
bytes. Every indexed variant must still match; conflicting required source or
project inputs still reject. Unindexed optional inputs remain subject to the
existing per-snapshot retained-root revalidation. Regression tests cover absent
optional variants, matching indexed variants, conflicting indexed variants, and
conflicts involving required sources/projects. No domain detector changed.

A machine restart erased the temporary SDK, captures, packages, and initial run
logs. The source/gold fingerprints survived. The initial publication failure is
therefore recorded from the pre-restart observation rather than a surviving raw
log. Recovery uses an ignored local workspace and reruns the pinned workflow;
the frozen gold remains unchanged.


## Validation and replay

Both pinned entry projects capture through the production public-Git adapter with
a verified offline package bundle. Production composition and publication create
one immutable snapshot containing two source snapshots, four build contexts,
58 sources, and 2,031 bindings; the lexical corpus contains 40 files. All original
tracked bytes remain unchanged. The package bundle exactly reproduces the
pre-restart manifest hash, and recovered worker source matches the earlier accepted
worker source. Its rebuilt binary hash is recorded separately.

Full Go tests, vet, and build pass. Changed-package race checks cover publication,
serving, artifacts/import, and snapshots. The live acceptance passes normally and
under race detection with identical reports. Adversarial scoring tests exercise
false joins, missing facts, partial context loss, unexpected facts, unsupported
emissions, and duplicate captures. Audit-to-MCP inventory parity prevents a dropped
unexpected fact from disappearing from the evaluation. Independent review found
no blocking issue in the scorer or publication fix.

The [evidence record](results/application-outbox-20261001/result.json) binds frozen
gold, original source hashes, recovered SDK/worker/package identities, publication
manifest, command logs, source fingerprints, and final normal/race reports.
SDKs, package caches, application source, and generated artifacts stay in the
ignored `.local/application-impact/` workspace.

With provisioned SDK, worker, pinned checkout, and offline bundle, use the
[replay runner](check-application-capture.py) with a fresh output directory. Then:

```sh
MOEDEX_APPLICATION_INDEX=/path/to/run/index \
MOEDEX_APPLICATION_SOURCE_ROOT=/path/to/Sample-Outbox \
MOEDEX_APPLICATION_GOLD=research/semantic-intelligence/results/application-outbox-20261001/source-gold.json \
MOEDEX_APPLICATION_REPORT=/path/to/report.json \
  go test ./internal/app/servecmd -run '^TestPublicApplicationImpact$' -count=1 -v
```

Next work should add verified EF and MassTransit configuration rules against these
unchanged labels, retaining this baseline. Private-wrapper propagation requires a
separate source-linked analysis rather than treating arbitrary Publish-like calls
as framework facts. Application activation and runtime delivery remain distinct
from recorded type observations.
