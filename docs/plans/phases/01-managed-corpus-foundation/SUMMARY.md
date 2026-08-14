---
phase: 01-managed-corpus-foundation
plan: 01
subsystem: corpus
tags: [git, submodules, gitlab, durable-json, reconciliation]

requires: []
provides:
  - Versioned managed-corpus ownership catalog and exact stable-ID lock.
  - Safe initialization of a local Git submodule superproject.
  - Concurrent-fetch, serialized-mutation managed corpus synchronization.
  - Failure, dirty-worktree, argv, and credential-redaction safety boundaries.
affects: [02-managed-corpus-integration, 03-branch-acquisition, indexing]

actuals:
  tokens: 24027
  tasks: 4
  commits: 4

tech-stack:
  added: []
  patterns:
    - Canonical strict JSON with temp-file, fsync, rename, and directory fsync.
    - Stable GitLab project IDs as submodule and reconciliation identity.
    - Concurrent remote reads followed by serialized superproject mutation.

key-files:
  created:
    - internal/corpus/catalog.go
    - internal/corpus/lock.go
    - internal/corpus/managed.go
    - internal/corpus/managed_sync.go
    - internal/corpus/managed_test.go
  modified:
    - internal/corpus/enumerate.go
    - internal/corpus/runner.go

key-decisions:
  - "The lock is a project-ID-sorted array; identity is numeric ID, not namespace path."
  - "Incomplete enumeration and project failures carry prior entries and cannot authorize prune."
  - "Flag-shaped refs, URLs, and managed paths fail preflight; valid positional operands use argv and --."
  - "Local Git identity is supplied only to each commit command and is never persisted."

patterns-established:
  - "Committed snapshot authority: after an interrupted mutation, HEAD remains the recovery source of truth."
  - "Owned-root gate: only a nonexistent or empty destination may be initialized."
  - "Partial progress: independent fetch successes can commit while failed projects retain prior gitlinks."

requirements-completed: [ADR-0019]

coverage:
  - id: D1
    description: Versioned, canonical, durable catalog and lock keyed by stable GitLab project ID.
    requirement: ADR-0019
    verification:
      - kind: unit
        ref: "internal/corpus/managed_test.go#TestProjectIDUnmarshal, TestCatalog*, TestLock*"
        status: pass
    human_judgment: false
  - id: D2
    description: Fresh roots become one-commit local superprojects with stable-ID submodules and no persisted Git identity.
    requirement: ADR-0019
    verification:
      - kind: integration
        ref: "internal/corpus/managed_test.go#TestInitManaged*"
        status: pass
    human_judgment: false
  - id: D3
    description: Managed sync handles add, update, force-push, move, carry-forward, conflict, and explicit prune safely.
    requirement: ADR-0019
    verification:
      - kind: integration
        ref: "go test -race ./internal/corpus -run 'TestManaged' -count=1"
        status: pass
    human_judgment: false
  - id: D4
    description: Failure boundaries preserve an internally consistent committed snapshot and redact credentials.
    requirement: ADR-0019
    verification:
      - kind: integration
        ref: "internal/corpus/managed_test.go#TestManagedFailure*, TestManagedUnsafe*, TestManagedRedacts*"
        status: pass
    human_judgment: false

duration: 27min
completed: 2026-08-14
status: complete
---

# Phase 1: Managed Corpus Foundation Summary

**A deterministic, stable-ID Git superproject lifecycle with safe initialization, partial-failure reconciliation, and committed snapshot recovery boundaries**

## Performance

- **Duration:** 27 min
- **Started:** 2026-08-14T09:01:46-06:00
- **Completed:** 2026-08-14T09:28:46-06:00
- **Tasks:** 4
- **Files modified:** 7

## Accomplishments

- Added strict version-1 ownership and acquisition schemas whose canonical JSON is durably and atomically written.
- Created fresh local superprojects with `project-{id}` submodule sections, exact gitlinks, one lock, and one command-local-identity commit.
- Added pure stable-ID reconciliation plus concurrent fetches and serialized add, update, force-push, move, missing, carry-forward, conflict, and prune behavior.
- Proved local-only lifecycle behavior across dirty content, partial fetch failure, injected mutation failures, unusual argv values, and credential-bearing diagnostics.

## Task Commits

Each task was committed atomically:

1. **Task 1: Define stable managed-corpus contracts and fixtures** - `cb4f05f`
2. **Task 2: Implement the initialization tracer** - `461e6ea`
3. **Task 3: Implement pure reconciliation and failure-safe sync** - `aef767d`
4. **Task 4: Prove crash, corruption, and command-safety boundaries** - `c1c3b1e`

## Files Created/Modified

- `internal/corpus/catalog.go` - Versioned ownership marker, strict decoding, and durable canonical JSON.
- `internal/corpus/lock.go` - Stable-ID snapshot schema and path, URL, commit, status, and collision validation.
- `internal/corpus/managed.go` - Empty-root initialization and one-commit superproject construction.
- `internal/corpus/managed_sync.go` - Pure reconciliation, concurrent acquisition, serialized mutation, carry-forward, and explicit prune.
- `internal/corpus/managed_test.go` - Hermetic local-bare-remote lifecycle and fault-injection coverage.
- `internal/corpus/enumerate.go` - Retains GitLab's numeric project ID.
- `internal/corpus/runner.go` - Redacts credential material from managed Git diagnostics.

## Shipped Schema

`corpus.json`:

```json
{
  "version": 1,
  "host": "gitlab.tcdevops.com",
  "group_policy": {
    "top_level_groups": ["Libraries.Common", "Services.Payment"]
  },
  "ref_policy": "default"
}
```

`corpus.lock.json`:

```json
{
  "version": 1,
  "enumeration_complete": true,
  "projects": [
    {
      "id": 101,
      "path_with_namespace": "group/project",
      "clone_url": "git@gitlab.tcdevops.com:group/project.git",
      "default_branch": "main",
      "default_commit": "0123456789abcdef0123456789abcdef01234567",
      "status": "current"
    }
  ]
}
```

Projects are sorted by ascending `id`; status is `current` or `carried_forward`. Unknown versions or fields, duplicate/overlapping identities or paths, unsafe paths, foreign or credential-bearing URLs, invalid refs, and missing/malformed commits fail closed.

## Decisions Made

- Kept schema v1 deliberately narrow: the lock records the exact default commit, while later phases extend acquisition for multiple branch refs.
- Used `git ls-remote` for concurrent add probes and per-submodule `git fetch` for existing projects; all `.gitmodules`, worktree, index, lock, and commit mutations remain serialized.
- Made `EnumerationComplete` explicit and safe by default. Prune requires both a complete enumeration and an explicit prune request.
- A complete enumeration that omits a project records it as `carried_forward` unless explicit prune is requested.
- Existing local modifications or untracked content turn update, move, or prune into a reported conflict rather than a reset or deletion.

## Deviations from Plan

### Auto-fixed Issues

**1. Missing critical path-overlap validation**

- **Found during:** Task 3
- **Issue:** Exact duplicate-path checks did not prevent one project path from nesting beneath another.
- **Fix:** Lock construction, initialization, and reconciliation reject overlapping managed paths.
- **Verification:** `TestLockRejectsDuplicateIDsAndPaths` and managed reconciliation tests pass.
- **Committed in:** `aef767d`

**2. Git porcelain cannot safely initialize a dash-leading submodule path**

- **Found during:** Task 4
- **Issue:** The installed Git's `submodule add` rejected a dash-leading path even when passed after `--` and prefixed with `./`.
- **Fix:** Managed paths beginning with `-` now fail preflight; whitespace-bearing and unusual valid values remain supported as individual argv elements.
- **Verification:** `TestManagedUnsafeArgumentsAreRejectedOrSeparated` passes against real local remotes.
- **Committed in:** `c1c3b1e`

---

**Total deviations:** 2 auto-fixed safety issues
**Impact on plan:** Both changes tighten the ownership and command-safety contract without expanding lifecycle scope.

## Issues Encountered

- The filesystem sandbox could not use the default Go build cache, so validation used the task-owned `/tmp/moedex-go-cache` cache.
- This repository has repo-native `docs/plans/` artifacts rather than Moe's formal `.planning/STATE.md` and `.planning/ROADMAP.md`; execution and summary metadata therefore remain under `docs/plans/`.

## Validation

- `go test ./internal/corpus -count=1` - pass
- `go test -race ./internal/corpus -run 'TestManaged' -count=1` - pass
- `go vet ./internal/corpus` - pass
- `git diff --check` - pass
- All managed lifecycle tests use isolated Git configuration and local bare remotes; none require GitLab, VPN, SSH, `glab`, or global Git identity.

## User Setup Required

None - Phase 1 introduces library behavior only. CLI integration is Phase 2.

## Next Phase Readiness

- Phase 2 can wire `InitManaged`, `ReconcileManaged`, and `SyncManaged` into `moedex-corpus init`, `sync`, `doctor`, and scheduled refresh.
- The lock is now the deterministic acquisition-to-indexing boundary required by later branch acquisition.
- No blocker remains in the managed-corpus foundation.

---
*Phase: 01-managed-corpus-foundation*
*Completed: 2026-08-14*
