---
phase: 03-branch-acquisition
plan: 01
type: execute
wave: 3
depends_on:
  - 02-01
files_modified:
  - internal/corpus/catalog.go
  - internal/corpus/lock.go
  - internal/corpus/managed_sync.go
  - internal/corpus/inventory.go
  - internal/corpus/refs.go
  - internal/corpus/managed_test.go
  - cmd/moedex-corpus/main.go
  - cmd/moedex-corpus/main_test.go
  - internal/ingest/source.go
  - internal/ingest/tree.go
  - internal/ingest/catfile.go
  - internal/ingest/ingest.go
  - internal/ingest/privacy.go
  - internal/ingest/tree_test.go
  - internal/ingest/catfile_test.go
  - internal/ingest/snapshot_test.go
  - docs/plans/phases/03-branch-acquisition/CAPACITY.json
  - docs/plans/phases/03-branch-acquisition/CAPACITY.md
autonomous: false
requirements:
  - ADR-0020
  - ADR-0021
user_setup:
  - Active TC VPN and authenticated `glab`/Git transport for the real-corpus capacity run.
  - A reviewed disk, memory, refresh-duration, and daemon-RSS budget for all-head indexing.
must_haves:
  truths:
    - The chosen branch policy is backed by measured real-corpus capacity rather than branch-count estimates alone.
    - A successful sync locks a complete deterministic branch-to-commit snapshot without checking out non-default branches.
    - A failed ref fetch/list carries the prior complete project ref snapshot and cannot imply branch deletion.
    - Managed ingestion reads exact locked commit trees and content from Git objects, not mutable working-tree bytes.
    - Every commit snapshot parses its own `.ai-privacy.yml` bootstrap blob before requesting any other blob content and never requests effective level-1 objects.
  artifacts:
    - path: internal/corpus/inventory.go
      provides: Machine-readable real-corpus branch/content/capacity inventory.
    - path: internal/corpus/refs.go
      provides: Transactional remote-head acquisition and canonical ref snapshot.
    - path: internal/ingest/tree.go
      provides: NUL-safe exact commit tree enumeration.
    - path: internal/ingest/catfile.go
      provides: Long-lived batch Git-object reader with cancellation and cleanup.
  key_links:
    - from: internal/corpus/refs.go
      to: internal/corpus/lock.go
      via: Only a complete validated staging ref set advances a project's lock-v2 branch map.
    - from: internal/corpus/lock.go
      to: internal/ingest/tree.go
      via: Each distinct locked commit is walked once and branch aliases retain provenance.
    - from: internal/ingest/catfile.go
      to: internal/ingest/ingest.go
      via: Git object bytes pass through the same text, BOM, path, and content-hash normalization contract.
    - from: commit tree .ai-privacy.yml
      to: internal/ingest/catfile.go
      via: Policy blob is requested first; its effective rules filter all remaining object requests.
  prohibitions:
    - Never fetch full history, tags, merge-request refs, or local-only branches in the initial policy.
    - Never interpolate a branch, path, ref, or object ID into a shell command.
    - Never enable `ref_policy: all` before the capacity checkpoint is approved.
    - Never infer one branch's privacy policy from the default checkout or request a level-1 blob from `git cat-file`.
---

<objective>
Measure the all-head policy, acquire remote branch tips into an atomic lock snapshot, and ingest exact
locked Git trees through a reusable object-reader path.

Purpose: establish the race-free acquisition-to-ingestion boundary on which branch-aware CAS and
serving can safely build.

Output: capacity inventory/report, catalog and lock v2 ref policy, transactional branch fetch, exact
tree/object ingestion, and deterministic local-remote fixtures.
</objective>

<execution_context>
@$HOME/.codex/moe/workflows/execute-plan.md
@$HOME/.codex/moe/templates/summary.md
@$HOME/.codex/moe/references/checkpoints.md
</execution_context>

<context>
@docs/adr/0020-branch-aware-indexing.md
@docs/adr/0021-ai-privacy-aware-indexing.md
@docs/plans/phases/02-managed-corpus-integration/PLAN.md
@docs/plans/phases/02-managed-corpus-integration/SUMMARY.md
@internal/corpus/lock.go
@internal/corpus/managed_sync.go
@internal/ingest/source.go
@internal/ingest/ingest.go
</context>

## Scope and one-way gates

- Initial selectable policies are `default` and `all`; `all` means remote `refs/heads/*` tips only.
- The capacity report must count source references and unique normalized content separately.
- Enabling `all` is a costly deployment decision. The checkpoint records explicit thresholds and
  owners before ref acquisition changes the managed lock.
- Lock schema v1 stays readable as default-only input; branch-enabled writes use v2.
- The working-tree ingestion path remains available for unmarked/single-repository commands.
- AI privacy is snapshot-scoped: two branch tips at different commits may apply different policies.

## Artifacts this phase produces

- `moedex-corpus inventory -corpus ROOT -refs all -output REPORT.json`.
- Version-2 catalog/lock fields for ref policy, default branch, branch-to-commit map, and aliases.
- `internal/corpus.AcquireRemoteHeads` with staged refs and atomic canonical-ref promotion.
- `internal/ingest.TreeEntries`, `CatFileBatch`, `Snapshot`, and shared normalization helpers.
- Commit-exact privacy bootstrap and policy fingerprints on every snapshot.
- `CAPACITY.json` containing redacted aggregate measurements and `CAPACITY.md` recording threshold
  owners, pass/fail decisions, and the approved ref policy.

<tasks>

<task type="auto" id="3.1">
  <name>Task 1: Build the branch capacity inventory tracer</name>
  <files>internal/corpus/inventory.go, internal/corpus/refs.go, cmd/moedex-corpus/main.go, cmd/moedex-corpus/main_test.go, internal/corpus/managed_test.go, docs/plans/phases/03-branch-acquisition/CAPACITY.json, docs/plans/phases/03-branch-acquisition/CAPACITY.md</files>
  <read_first>docs/adr/0001-single-node-scope-pure-go-default.md, docs/adr/0020-branch-aware-indexing.md, internal/corpus/managed_sync.go, internal/blobstore/manifest.go, internal/index/index.go</read_first>
  <action>
Add a non-lock-mutating `inventory` command that reports schema version, project count, remote-head
distribution, distinct tip commits, alias counts, reachable file references/bytes, unique normalized
text blobs/bytes, projected CAS/source-metadata/token/symbol/dense growth, fetch bytes/duration,
tree-walk duration, and peak process memory where measurable. The command may fetch objects into a
command-owned staging ref namespace but must clean only those refs and must not change gitlinks,
checkouts, catalog, lock, or canonical managed refs. JSON output must be deterministic and contain
enough raw counts to recompute ratios. Unit tests use local bare remotes. The real run writes
redacted aggregate evidence to this phase's `CAPACITY.json` and threshold owner/limit/observed/
pass-fail decisions to `CAPACITY.md`; any per-project raw report stays in an operator-selected local
path and is not committed. Report policy counts and privacy-excluded occurrence counts separately;
read/normalize/project sidecar capacity only for privacy-eligible blobs, never level-1 content.
  </action>
  <verify>go test ./internal/corpus ./cmd/moedex-corpus -run 'Test.*Inventory' -count=1</verify>
  <acceptance_criteria>
    - Repeated fixture inventories emit identical counts and ordering.
    - The report separates raw source occurrences from unique normalized blobs and bytes.
    - Capacity measurements never request effective level-1 blob content and identify exclusions only through redacted aggregates.
    - The command leaves marker, lock, gitlinks, checkouts, and canonical managed refs unchanged.
    - Cleanup deletes only the transaction's own staging refs and never broad ref namespaces or working files.
    - A partial project measurement is explicit and cannot be presented as a complete baseline.
  </acceptance_criteria>
  <done>A deterministic inventory can decide whether the proposed ref policy fits the single-node envelope.</done>
</task>

<task type="checkpoint:decision" gate="blocking" id="3.2">
  <decision>Approve the initial deployed ref policy and capacity thresholds.</decision>
  <context>Run the inventory on the authenticated TC corpus. `all` is eligible only if every owned threshold passes with headroom for the sibling migration; otherwise ADR 0020 must adopt a narrower explicit policy before implementation continues.</context>
  <options>
    <option id="approve-all"><name>Approve all heads</name><pros>Meets the requested complete branch corpus.</pros><cons>Accepts the measured storage, build, refresh, and serving cost.</cons></option>
    <option id="revise-policy"><name>Revise ref policy</name><pros>Keeps the system within its single-node envelope.</pros><cons>Requires an ADR amendment defining explicit include/exclude semantics.</cons></option>
  </options>
  <resume-signal>Select `approve-all` with the report path and thresholds, or `revise-policy` with the amended ADR decision.</resume-signal>
</task>

<task type="auto" id="3.3">
  <name>Task 2: Acquire and lock a complete remote-head snapshot</name>
  <files>internal/corpus/catalog.go, internal/corpus/lock.go, internal/corpus/refs.go, internal/corpus/managed_sync.go, internal/corpus/managed_test.go</files>
  <read_first>internal/corpus/catalog.go, internal/corpus/lock.go, internal/corpus/managed_sync.go, internal/corpus/runner.go</read_first>
  <action>
Add `ref_policy: default|all` and lock schema v2. Fetch `refs/heads/*` at depth one into a unique
`refs/moedex/staging/{transaction}/*` namespace using argv-only Git execution. Enumerate and validate
the complete staged set, group aliases by commit with the default alias first, then atomically
promote set/delete operations to `refs/moedex/heads/*` with `git update-ref --stdin -z`. Only after
promotion succeeds may the project lock entry advance. Sort projects by ID, branches by ref name,
and snapshots by commit/default-first rules. A fetch, parse, validation, or promotion failure carries
the prior project's entire branch snapshot; never merge a partial new list with the old list.
  </action>
  <verify>go test -race ./internal/corpus -run 'Test.*(Ref|Branch|LockV2|Promotion)' -count=1</verify>
  <acceptance_criteria>
    - Tests cover branch add, move/force-push, delete, rename-as-delete+add, alias commits, and default-branch change.
    - Failed staged acquisition leaves canonical refs and lock-v2 project snapshot at the prior complete state.
    - V1 lock input maps to one default snapshot; V2 writes are deterministic and unknown versions fail closed.
    - Ref names are validated and never interpreted by a shell.
  </acceptance_criteria>
  <done>Every successful managed sync exposes one complete branch snapshot with exact commits and deterministic aliases.</done>
</task>

<task type="auto" id="3.4">
  <name>Task 3: Implement exact tree and batch-object ingestion</name>
  <files>internal/ingest/source.go, internal/ingest/tree.go, internal/ingest/catfile.go, internal/ingest/ingest.go, internal/ingest/privacy.go, internal/ingest/tree_test.go, internal/ingest/catfile_test.go, internal/ingest/snapshot_test.go</files>
  <read_first>internal/ingest/source.go, internal/ingest/ingest.go, internal/ingest/privacy.go, internal/blobstore/build.go, docs/adr/0003-cox-reduction-ripgrep-parity.md, docs/adr/0021-ai-privacy-aware-indexing.md</read_first>
  <action>
Extend the source model with project identity, commit snapshot, ordered branch aliases, default flag,
and tree-entry metadata. Parse `git ls-tree -r -z --full-tree COMMIT` records without path
splitting or shell expansion. Keep one `git cat-file --batch` process per repository ingestion,
request each unique object ID once, verify announced type/size, read exactly the payload plus
terminator, and guarantee close/wait on success, error, and context cancellation. Apply the existing
VCS-path filter and binary/BOM rules through shared helpers. Locate and request only the root
`.ai-privacy.yml` bootstrap blob first, parse the strict policy, and filter every effective level-1
tree entry before requesting its object. A missing policy defaults to level 3; an invalid policy
fails that project snapshot. Record the effective policy fingerprint and Git object ID separately from a
content key computed after normalization. Index regular/executable blobs, skip gitlinks, and lock
symlink behavior to the default-checkout ripgrep oracle before enabling it. Walk each distinct commit
once even when several branches alias it. Managed default commits use this path; unmanaged repos keep
working-tree ingestion.
  </action>
  <verify>go test -race ./internal/ingest -run 'Test(Tree|CatFile|Snapshot|Normalize)' -count=1</verify>
  <acceptance_criteria>
    - Paths with tabs/newlines/unusual valid bytes survive NUL parsing without truncation or escape.
    - Short reads, missing objects, wrong object types, cancellation, and child-process exit are errors with no leaked process.
    - Alias branches cause one tree walk and one read per distinct object.
    - Identical normalized content across commits produces one content key and multiple source occurrences.
    - The locked default tree exactly matches the materialized default-checkout search universe.
    - Tests prove a globally Restricted commit requests only its policy blob, path-restricted blobs are never requested, and branches at different commits apply their own policy.
  </acceptance_criteria>
  <done>Managed ingestion is commit-exact, deterministic, content-true, and independent of checkout mutation.</done>
</task>

</tasks>

<verification>

- [ ] `go test ./internal/corpus ./cmd/moedex-corpus -count=1`
- [ ] `go test -race ./internal/corpus -run 'Test.*Ref.*Concurrent' -count=1`
- [ ] `go test -race ./internal/ingest -count=1`
- [ ] Snapshot tests prove zero `cat-file` requests for effective level-1 content.
- [ ] Real-corpus inventory is complete and every capacity threshold has owner/limit/observed/status.
- [ ] The approved policy is recorded in the report and catalog; `all` is not enabled on a failed gate.
- [ ] `git diff --check`

</verification>

<success_criteria>

- The capacity checkpoint has an explicit decision and evidence path.
- Lock v2 advances only from complete remote-head acquisition.
- Default and non-default sources are read from exact locked commits without worktrees.
- Every snapshot applies its own fail-closed privacy policy before non-policy object reads.
- Phase 4 receives a deterministic project/commit/branch/file stream and normalized content keys.

</success_criteria>

<output>
After execution, create `docs/plans/phases/03-branch-acquisition/SUMMARY.md` and link the approved
capacity report and final ref-policy decision.
</output>
