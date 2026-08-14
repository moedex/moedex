---
phase: 01-managed-corpus-foundation
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - internal/corpus/enumerate.go
  - internal/corpus/config.go
  - internal/corpus/runner.go
  - internal/corpus/catalog.go
  - internal/corpus/lock.go
  - internal/corpus/managed.go
  - internal/corpus/managed_sync.go
  - internal/corpus/sync.go
  - internal/corpus/corpus_test.go
  - internal/corpus/managed_test.go
autonomous: true
requirements:
  - ADR-0019
user_setup: []
must_haves:
  truths:
    - A fresh root can become a marked, locally committed submodule superproject without using global Git identity.
    - Project membership and exact default commits are represented by one deterministic lock keyed by GitLab project ID.
    - A partial or ambiguous sync carries the prior safe project state and never authorizes prune.
    - Repeating a no-change sync produces no commit and byte-identical catalog and lock files.
  artifacts:
    - path: internal/corpus/catalog.go
      provides: Versioned managed-corpus ownership and policy schema.
    - path: internal/corpus/lock.go
      provides: Atomic stable-ID project snapshot and validation.
    - path: internal/corpus/managed.go
      provides: Safe managed-root initialization.
    - path: internal/corpus/managed_sync.go
      provides: Pure reconciliation plus serialized superproject mutation.
  key_links:
    - from: internal/corpus/enumerate.go
      to: internal/corpus/lock.go
      via: GitLab numeric project ID is retained from enumeration into the lock.
    - from: internal/corpus/managed_sync.go
      to: .gitmodules and .moedex/corpus.lock.json
      via: One successful local commit stages gitlinks, module configuration, and lock together.
  prohibitions:
    - Never adopt, rewrite, move, or remove an unmarked or non-empty user corpus.
    - Never prune after failed, incomplete, or empty enumeration.
    - Never prune or move a submodule containing local modifications or untracked files.
    - Never persist credentials or credential-bearing clone URLs.
---

<objective>
Build the ownership, snapshot, initialization, and reconciliation core for a Moedex-managed local
Git superproject.

Purpose: turn corpus membership and default commits into a deterministic, recoverable contract that
later indexing and branch acquisition can consume.

Output: versioned marker/lock types, stable project identity, hermetic Git fixtures, `InitManaged`,
and a failure-safe `SyncManaged` engine.
</objective>

<execution_context>
@$HOME/.codex/moe/workflows/execute-plan.md
@$HOME/.codex/moe/templates/summary.md
</execution_context>

<context>
@docs/adr/0019-moedex-managed-submodule-corpus.md
@docs/plans/0019-managed-submodule-corpus.md
@internal/corpus/enumerate.go
@internal/corpus/clone.go
@internal/corpus/sync.go
@internal/corpus/runner.go
</context>

## Scope and reversibility

- The managed format starts at schema version 1; unknown versions fail closed.
- `corpus.json` records version, pinned host, group policy, and `ref_policy: default`.
- `corpus.lock.json` records enumeration completeness and projects sorted by numeric ID. Each entry
  contains ID, namespace path, validated clone URL, default branch, exact default commit, and
  `current` or `carried_forward` status.
- Submodule sections use `project-{decimal-project-id}`; checkout paths use validated
  `path_with_namespace`.
- Initialization is reversible by abandoning the newly marked root. In-place adoption is excluded.
- Explicit prune is a destructive/costly operation and remains opt-in with a complete-enumeration
  precondition.

## Artifacts this phase produces

- `internal/corpus.Catalog`, `LoadCatalog`, `WriteCatalog`, and schema validation.
- `internal/corpus.Lock`, `LockedProject`, `LoadLock`, `WriteLock`, and lock-integrity validation.
- `internal/corpus.InitManaged` and `SyncManaged` entry points.
- `internal/corpus.ManagedPlan` with `Add`, `Update`, `Move`, `Missing`, `Prune`, `CarryForward`, and
  `Conflict` actions.
- Hermetic local-bare-remote fixtures and lifecycle/fault-injection tests.

<tasks>

<task type="auto" id="1.1">
  <name>Task 1: Define stable managed-corpus contracts and fixtures</name>
  <files>internal/corpus/enumerate.go, internal/corpus/catalog.go, internal/corpus/lock.go, internal/corpus/managed_test.go</files>
  <read_first>docs/adr/0019-moedex-managed-submodule-corpus.md, internal/corpus/enumerate.go, internal/corpus/config.go, internal/corpus/corpus_test.go</read_first>
  <action>
Retain GitLab's numeric `id` in `Project`. Add version-1 catalog and lock structs with strict
validation, deterministic project ordering, canonical indented JSON, and durable temp-file, fsync,
and rename writes. Reject unknown versions, duplicate IDs/paths, path escapes, malformed commits,
untrusted URL hosts, and a default commit absent from an entry. Build tests using local bare Git
remotes and per-test Git configuration only; the fixture must not require GitLab, VPN, SSH, `glab`,
or global `user.name`/`user.email`.
  </action>
  <verify>go test ./internal/corpus -run 'Test(Catalog|Lock|ProjectID|ManagedFixture)' -count=1</verify>
  <acceptance_criteria>
    - `Project` unmarshals a non-zero numeric GitLab ID and rejects duplicate IDs during lock construction.
    - Catalog and lock JSON are byte-stable for semantically identical sorted input.
    - Interrupted/failed writes leave the prior valid file readable.
    - Tests explicitly reject `../` paths, absolute paths, duplicate namespace paths, and URLs outside the pinned host.
  </acceptance_criteria>
  <done>The versioned ownership/snapshot contract is deterministic, validated, durably written, and exercised without network access.</done>
</task>

<task type="auto" id="1.2">
  <name>Task 2: Implement the initialization tracer</name>
  <files>internal/corpus/managed.go, internal/corpus/runner.go, internal/corpus/managed_test.go</files>
  <read_first>internal/corpus/catalog.go, internal/corpus/lock.go, internal/corpus/clone.go, internal/corpus/runner.go</read_first>
  <action>
Implement `InitManaged(ctx, runner, cfg, projects)` for a nonexistent or empty destination. Refuse a
non-empty directory and any pre-existing unmarked Git repository. Initialize Git, write the marker,
add validated submodules under namespace paths using `project-{id}` sections, pin each checkout to
the enumerated default commit, write the lock, and create one local commit. Supply the commit
identity with command-local `git -c user.name=Moedex -c user.email=moedex@localhost`; never edit
global or system Git config. Leave an interrupted marked root recoverable and report its exact path;
do not recursively clean it.
  </action>
  <verify>go test ./internal/corpus -run 'TestInitManaged' -count=1</verify>
  <acceptance_criteria>
    - A fresh fixture contains `.git`, `.gitmodules`, `.moedex/corpus.json`, `.moedex/corpus.lock.json`, and one gitlink per non-empty project.
    - The first local commit contains marker, lock, module configuration, and gitlinks as one snapshot.
    - Initialization succeeds with empty global Git configuration.
    - Non-empty, unmarked, path-escaping, and host-mismatched destinations fail before modifying user-owned content.
  </acceptance_criteria>
  <done>A fresh local fixture is reproducibly created as an owned superproject from stable project records.</done>
</task>

<task type="auto" id="1.3">
  <name>Task 3: Implement pure reconciliation and failure-safe sync</name>
  <files>internal/corpus/managed_sync.go, internal/corpus/sync.go, internal/corpus/managed_test.go</files>
  <read_first>internal/corpus/lock.go, internal/corpus/managed.go, internal/corpus/sync.go, internal/corpus/clone.go</read_first>
  <action>
Implement a pure reconciliation function keyed by project ID that emits ordered actions for add,
tip update/force-push, namespace move, missing, explicit prune, carry-forward, and conflict. Execute
fetches concurrently at configured concurrency, but serialize mutations to `.gitmodules`, paths,
the superproject index, the lock, and the final commit. A project fetch/update failure must preserve
its prior gitlink and lock entry while allowing independent successes to advance; return a non-zero
aggregate outcome. Permit prune only when enumeration is explicitly marked complete and `--prune`
was requested. Use coordinated `git submodule deinit` and `git rm` behavior, not filesystem removal.
Refuse prune/move when the affected submodule or superproject path has local modifications or
untracked files. Skip lock writes and commits when the semantic snapshot is unchanged.
  </action>
  <verify>go test -race ./internal/corpus -run 'TestManaged(Sync|Reconcile|Concurrent)' -count=1</verify>
  <acceptance_criteria>
    - Tests cover add, update, force-push, ID-stable rename, missing carry-forward, explicit prune, and ID/path collision.
    - An incomplete or failed enumeration cannot produce a prune action.
    - One failed fetch carries its previous entry while successful entries advance in the same committed snapshot.
    - Dirty or untracked submodule content turns prune/move into a reported conflict without deleting it.
    - A no-change rerun creates no commit and does not change catalog or lock bytes.
  </acceptance_criteria>
  <done>Managed synchronization is idempotent, partial-failure-safe, and serialized at the superproject boundary.</done>
</task>

<task type="auto" id="1.4">
  <name>Task 4: Prove crash, corruption, and command-safety boundaries</name>
  <files>internal/corpus/managed_test.go, internal/corpus/runner.go</files>
  <read_first>internal/corpus/managed.go, internal/corpus/managed_sync.go, internal/corpus/runner.go</read_first>
  <action>
Add fault injection at clone/fetch, path move, lock write, stage, and commit boundaries. Assert the
last committed snapshot remains internally consistent or the marked root reports a recoverable
state. Exercise branch/path/URL values that begin with `-`, contain whitespace, or use unusual valid
Git characters and ensure every Git argument boundary uses argv plus `--` where supported rather
than shell interpolation. Assert diagnostics redact credential material.
  </action>
  <verify>go test ./internal/corpus -run 'TestManaged.*(Failure|Recovery|Unsafe|Redact)' -count=1</verify>
  <acceptance_criteria>
    - No injected failure makes an unmarked directory managed or removes pre-existing user content.
    - The committed `.gitmodules`, gitlinks, and lock agree after recovery from every injected boundary.
    - Flag-shaped refs/paths cannot be interpreted as Git options.
    - Test output contains no embedded token or credential-bearing URL.
  </acceptance_criteria>
  <done>The lifecycle has explicit recovery behavior and regression coverage at every destructive or external-process boundary.</done>
</task>

</tasks>

<verification>

- [ ] `go test ./internal/corpus -count=1`
- [ ] `go test -race ./internal/corpus -run 'TestManaged' -count=1`
- [ ] `go vet ./internal/corpus`
- [ ] `git diff --check`
- [ ] All tests use local remotes and isolated Git config; no test contacts GitLab.

</verification>

<success_criteria>

- The four tasks and their acceptance criteria pass.
- One committed, versioned snapshot is the only successful handoff state.
- Partial failure and incomplete enumeration preserve the prior searchable state.
- No command can implicitly adopt or prune a user-owned corpus.

</success_criteria>

<output>
After execution, create `docs/plans/phases/01-managed-corpus-foundation/SUMMARY.md` with task commits,
test evidence, deviations, and the exact catalog/lock schema that shipped.
</output>
