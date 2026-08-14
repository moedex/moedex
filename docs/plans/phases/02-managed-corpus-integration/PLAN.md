---
phase: 02-managed-corpus-integration
plan: 01
type: execute
wave: 2
depends_on:
  - 01-01
files_modified:
  - cmd/moedex-corpus/main.go
  - cmd/moedex-corpus/main_test.go
  - internal/corpus/doctor.go
  - internal/corpus/corpus_test.go
  - internal/ingest/discover.go
  - internal/ingest/source.go
  - internal/ingest/discover_test.go
  - internal/parity/corpus.go
  - internal/parity/manifest.go
  - internal/parity/corpus_test.go
  - internal/blobstore/build.go
  - internal/blobstore/manifest.go
  - internal/blobstore/cas_export_parity_test.go
  - internal/blobstore/refresh_deduped_parity_test.go
  - cmd/moedex-index/main.go
  - scripts/install-macos.sh
  - scripts/refresh-corpus.sh
  - scripts/managed-refresh-test.sh
  - deploy/moedex-sync.service
  - deploy/moedex-serve.env.example
  - deploy/README.md
  - docs/plans/phases/02-managed-corpus-integration/ROLLOUT.md
autonomous: false
requirements:
  - ADR-0019
user_setup:
  - An authenticated `glab` session for `gitlab.tcdevops.com`.
  - Active TC VPN access during the real-corpus checkpoint.
  - Free disk for a sibling corpus, CAS, shard directory, and rollback copy.
must_haves:
  truths:
    - CLI users can initialize, sync, dry-run, and diagnose a managed corpus with distinct auth, VPN, and Git-transport failures.
    - Every default-branch indexing path consumes the managed lock and excludes the superproject itself.
    - Unmanaged and single-repository workflows keep their existing discovery and output behavior.
    - A managed default-only corpus is cut over beside the live corpus only after parity and health checks pass.
  artifacts:
    - path: internal/ingest/source.go
      provides: One source-enumeration seam for managed and unmanaged corpora.
    - path: cmd/moedex-corpus/main.go
      provides: Managed lifecycle CLI and compatibility routing.
    - path: internal/corpus/doctor.go
      provides: Ownership, snapshot, auth, reachability, and Git transport diagnostics.
    - path: scripts/refresh-corpus.sh
      provides: Managed sync to CAS refresh/export/sidecar/reload orchestration.
  key_links:
    - from: .moedex/corpus.lock.json
      to: internal/ingest/source.go
      via: Locked project ID, namespace, directory, and default commit become the indexing source list.
    - from: internal/ingest/source.go
      to: internal/parity/corpus.go and internal/blobstore/build.go
      via: Direct, parity, CAS, refresh, and export paths share one deterministic source identity.
    - from: scripts/refresh-corpus.sh
      to: moedex-corpus sync and moedex-index cas-refresh
      via: Corpus snapshot advances before index snapshot and warm reload.
  prohibitions:
    - Never silently fall back from an invalid managed lock to filesystem discovery.
    - Never index the superproject root as a source repository.
    - Never switch or delete the existing live corpus/index during automated setup.
---

<objective>
Expose the managed lifecycle to operators, make its lock the default-only indexing source, and prove
a safe sibling migration through the deployed refresh path.

Purpose: finish ADR 0019 as a usable vertical slice before branch-specific storage and query changes
begin.

Output: CLI and diagnostics, unified source enumeration, default-only parity coverage, operational
scripts/configuration, and a human-approved managed-corpus cutover report.
</objective>

<execution_context>
@$HOME/.codex/moe/workflows/execute-plan.md
@$HOME/.codex/moe/templates/summary.md
@$HOME/.codex/moe/references/checkpoints.md
</execution_context>

<context>
@docs/adr/0019-moedex-managed-submodule-corpus.md
@docs/plans/phases/01-managed-corpus-foundation/PLAN.md
@docs/plans/phases/01-managed-corpus-foundation/SUMMARY.md
@cmd/moedex-corpus/main.go
@internal/corpus/doctor.go
@internal/ingest/discover.go
@internal/parity/corpus.go
@internal/blobstore/build.go
@cmd/moedex-index/main.go
@scripts/install-macos.sh
@scripts/refresh-corpus.sh
</context>

## Scope and migration boundary

- The current `clone` path remains available and documented as compatibility behavior.
- A marked root is lock-driven. Missing/corrupt lock data is a hard error, never a discovery
  fallback that could silently alter corpus membership.
- An unmarked root retains current recursive discovery, including nested ordinary repositories.
- `.git` files are recognized as repository markers without descending into Git metadata.
- The live migration creates a sibling root and sibling CAS/shards; the old paths remain the
  immediate rollback until the soak period is accepted.

## Artifacts this phase produces

- `moedex-corpus init`, managed `sync`, project-ID-aware `--dry-run`, and expanded `doctor` output.
- `internal/ingest.RepoSource` and `DiscoverSources` (or equivalently named typed seam).
- Lock-driven source wiring for direct build, parity, CAS build/refresh, and served export.
- Default-only managed/unmanaged equivalence tests.
- Updated install/refresh/deployment examples and a rollout report template.
- `docs/plans/phases/02-managed-corpus-integration/ROLLOUT.md` for redacted counts, gates,
  configuration switch, rollback, owner, and soak result.

<tasks>

<task type="auto" id="2.1">
  <name>Task 1: Wire lifecycle commands and actionable diagnostics</name>
  <files>cmd/moedex-corpus/main.go, cmd/moedex-corpus/main_test.go, internal/corpus/doctor.go, internal/corpus/corpus_test.go</files>
  <read_first>internal/corpus/managed.go, internal/corpus/managed_sync.go, cmd/moedex-corpus/main.go, internal/corpus/doctor.go</read_first>
  <action>
Add `moedex-corpus init -corpus ROOT` and route managed roots through `SyncManaged`; preserve
`clone` as the independent-clone compatibility command. Make `sync --dry-run` print stable-ID add,
update, move, missing, carry-forward, prune, and conflict actions without mutation. Expand `doctor`
to validate marker/schema, Git work tree, recoverable cleanliness, `.gitmodules`/lock/gitlink
agreement, pinned hosts, and submodule presence. Report three separate preflights: `glab`
authentication, GitLab host/VPN reachability, and a real Git clone/fetch transport probe. Redact
tokens and userinfo from all output.
  </action>
  <verify>go test ./cmd/moedex-corpus ./internal/corpus -run 'Test.*(Init|DryRun|Doctor|Diagnostic)' -count=1</verify>
  <acceptance_criteria>
    - `init`, `sync`, `sync --dry-run`, and `doctor` have usage and parsing tests.
    - Doctor distinguishes authenticated-but-unreachable from reachable-but-Git-transport-failed.
    - Doctor reports lock/gitlink/module disagreement as an error with a non-destructive recovery instruction.
    - No command output includes a token or credential-bearing URL.
  </acceptance_criteria>
  <done>The managed lifecycle is operable and its failure modes identify the layer an operator must fix.</done>
</task>

<task type="auto" id="2.2">
  <name>Task 2: Make the managed lock the default-branch indexing source</name>
  <files>internal/ingest/discover.go, internal/ingest/source.go, internal/ingest/discover_test.go, internal/parity/corpus.go, internal/parity/manifest.go, internal/blobstore/build.go, internal/blobstore/manifest.go, cmd/moedex-index/main.go</files>
  <read_first>internal/corpus/lock.go, internal/ingest/discover.go, internal/ingest/ingest.go, internal/parity/corpus.go, internal/blobstore/build.go, cmd/moedex-index/main.go</read_first>
  <action>
Introduce a typed `RepoSource` carrying project ID, namespace label, directory, locked default
commit, and managed status. `DiscoverSources(root)` must read and validate a marked root's lock,
exclude the superproject, require every locked submodule, and return stable project-ID order. For an
unmarked root, wrap current discovery with legacy labels and HEAD freshness. Update generic
`DiscoverRepos` to recognize `.git` files safely. Route direct build, parity manifest/build, CAS
build/refresh, and export preparation through the typed seam instead of independent
`filepath.Base`/`DiscoverRepos` calls. Before ingesting a managed source, verify its current HEAD is
the locked default commit; a mismatch is surfaced rather than indexing mutable bytes.
  </action>
  <verify>go test ./internal/ingest ./internal/parity ./internal/blobstore ./cmd/moedex-index -run 'Test.*(Managed|Discover|Default|Source)' -count=1</verify>
  <acceptance_criteria>
    - A managed fixture includes every locked submodule exactly once and excludes the superproject.
    - `.git` directory and `.git` file repositories are discovered without entering Git metadata.
    - Same-leaf-name projects retain distinct namespace/project identities.
    - Missing submodules, corrupt locks, and HEAD/lock mismatch fail loudly and retain the prior served snapshot.
    - Existing unmanaged and single-repository tests remain unchanged or receive only compatibility assertions.
  </acceptance_criteria>
  <done>Every default-only build path uses one deterministic source contract and cannot silently under-index a managed corpus.</done>
</task>

<task type="auto" id="2.3">
  <name>Task 3: Integrate safe refresh and sibling migration operations</name>
  <files>scripts/install-macos.sh, scripts/refresh-corpus.sh, scripts/managed-refresh-test.sh, deploy/moedex-sync.service, deploy/moedex-serve.env.example, deploy/README.md, docs/plans/phases/02-managed-corpus-integration/ROLLOUT.md</files>
  <read_first>scripts/install-macos.sh, scripts/refresh-corpus.sh, deploy/com.moedex.refresh.plist, deploy/moedex-sync.service, deploy/moedex-serve.env.example, deploy/README.md</read_first>
  <action>
Teach install to initialize only a missing/new managed root and refuse implicit adoption. Make the
refresh sequence `managed sync -> CAS refresh -> deduped export/refresh -> sidecar refresh -> warm
SIGHUP`, stopping before later stages when the snapshot is unsafe. Preserve prior index data on any
failure. Fix the no-backup prune pipeline so an empty backup set is a successful no-op under
`pipefail`. Document the existing 13:00 local schedule, VPN/auth prerequisite, 12-hour VPN timeout,
sibling paths, validation commands, hot-swap, soak, and rollback. Do not change live paths during
installation or documentation tests. Add a shell harness with stub `glab`, Git, index, signal, and
filesystem commands to exercise stage ordering, fail-stop behavior, empty-backup pruning, and
refusal to adopt a populated root. Create the rollout report template without secrets or project
names.
  </action>
  <verify>
bash -n scripts/install-macos.sh scripts/refresh-corpus.sh scripts/managed-refresh-test.sh
bash scripts/managed-refresh-test.sh
  </verify>
  <acceptance_criteria>
    - Install refuses a populated unmarked root and prints the sibling-root procedure.
    - A sync failure prevents CAS/index advancement while leaving the currently served directory intact.
    - An empty backup directory does not cause a successful refresh to exit non-zero.
    - Deployment docs specify 13:00 local refresh and separate VPN, `glab`, and Git transport checks.
    - The report template contains old/new snapshot IDs, redacted counts, parity/health gates, switch, rollback, owner, and soak outcome.
  </acceptance_criteria>
  <done>The operational path can create and refresh a sibling managed corpus without implicitly touching the live one.</done>
</task>

<task type="auto" id="2.4">
  <name>Task 4: Prove default-only managed parity in hermetic fixtures</name>
  <files>internal/parity/corpus_test.go, internal/blobstore/cas_export_parity_test.go, internal/blobstore/refresh_deduped_parity_test.go</files>
  <read_first>internal/parity/corpus.go, internal/parity/oracle_ripgrep.go, internal/blobstore/cas_export_parity_test.go, internal/blobstore/serving_spine_test.go</read_first>
  <action>
Build equivalent conventional-clone and managed-submodule fixtures at the same commits. Compare the
sorted exact result identity `(path_with_namespace, relative_path, line, matched_text)` through
direct build, CAS export, deduped export, and served reload. Include duplicate leaf names, a `.git`
file, missing locked submodule, binary/BOM content, and a default-tip advance. The missing source
case must fail the refresh and prove that the previously served snapshot remains searchable.
  </action>
  <verify>go test ./internal/parity ./internal/blobstore -run 'Test.*Managed.*Parity' -count=1</verify>
  <acceptance_criteria>
    - Conventional and managed fixtures yield identical default-only result sets.
    - CAS and deduped served paths preserve namespace attribution after save/load.
    - Failure to materialize one locked source cannot publish a manifest that omits it.
  </acceptance_criteria>
  <done>Hermetic tests isolate and prove the default-only compatibility gate required before branch work.</done>
</task>

<task type="checkpoint:human-verify" gate="blocking" id="2.5">
  <what-built>A complete sibling managed corpus, CAS, served shard directory, sidecars, and test daemon using the authenticated TC GitLab corpus.</what-built>
  <how-to-verify>With VPN active, initialize and sync the sibling root; run the verification block below; compare project/lock/file/blob counts and sample exact results; exercise one refresh; then approve the configuration switch only if health, parity, freshness, and rollback paths are recorded. Retain the old corpus/index for the agreed soak period.</how-to-verify>
  <resume-signal>Type `approved` with the rollout-report path, or describe the failed gate.</resume-signal>
</task>

</tasks>

<verification>

- [ ] `go test ./cmd/moedex-corpus ./internal/corpus ./internal/ingest ./internal/parity ./internal/blobstore ./cmd/moedex-index -count=1`
- [ ] `make health`
- [ ] `make roundtrip`
- [ ] `make parity MOEDEX_CORPUS=/path/to/managed-default-only-corpus`
- [ ] `moedex-corpus doctor -corpus /path/to/managed-default-only-corpus`
- [ ] `moedex-index doctor -shard-dir /path/to/new-shards`
- [ ] `moedex-index check -shard-dir /path/to/new-shards`
- [ ] Sibling rollout report records old/new lock, project/file/blob counts, health, rollback, and soak owner.

</verification>

<success_criteria>

- Automated tests and the human checkpoint pass.
- The managed default-only result universe is equivalent to the conventional corpus.
- The live service can roll back by restoring the prior configured paths without rebuilding.
- ADR 0020 Phase 3 is unblocked only after the rollout report is approved.

</success_criteria>

<output>
After execution, create `docs/plans/phases/02-managed-corpus-integration/SUMMARY.md` and link the
operator-approved sibling rollout report.
</output>
