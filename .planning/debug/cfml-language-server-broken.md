---
status: resolved
trigger: "I can't believe I'm saying this but I need CFML working too. Please take that on next."
created: 2026-08-27T14:47:32Z
updated: 2026-08-27T16:00:00Z
---

## Current Focus

hypothesis: Confirmed root cause; bounded follow-up is that the installer needs a CFML-only execution mode so repair can be verified without invoking unrelated language installers.
test: Tighten the test oracle so it detects .NET installer activity rather than the shared final PATH reminder, then rerun focused tests and `bash -n`.
expecting: `--cfml-only` executes only the CFML installation section, while existing default and `--with-cfml` behavior remain unchanged.
next_action: Replace the overbroad `dotnet` substring with installation-specific .NET output markers, format the helper, and rerun the same checks.
bug_class: bohrbug
reasoning_checkpoint:
  hypothesis: "A stale cflsp wrapper causes CFML navigation to die because its managed server.js target is absent, and setup/doctor perpetuate the defect because they use PATH presence as their entire health contract."
  confirming_evidence:
    - "The PATH wrapper names /Users/ZKeown/.moedex-tools/cfc/cflsp-vscode/out/server.js, which does not exist."
    - "Direct cflsp --stdio exits 1 with MODULE_NOT_FOUND before any LSP input is processed."
    - "The real TestCFML_Definition reaches initialize and fails with server-died EOF in 0.04s."
    - "Installer and doctor both accept exec.LookPath/command -v without checking the managed artifact or process startup."
  falsification_test: "If direct cflsp startup survived with the managed artifact absent, or setup rebuilt despite command presence, the hypothesis would be false; both observations produced the opposite result."
  fix_rationale: "Make the installer require the exact official server.js output before declaring the managed CFML install present, and make doctor require bounded startup survival before reporting cflsp healthy. These repair the producer and checker contracts rather than masking navigation EOF."
  blind_spots: "The official source has not yet been built on this machine after the patch; machine-wide deployment and the final live navigation rerun remain for the parent after patch review."
  candidate_causes:
    - "code: setup and doctor classify an executable pathname as a healthy CFML server"
    - "environment: the managed cfc checkout/bundle was removed while /opt/homebrew/bin/cflsp survived"
    - "config: an alternate MOEDEX_TOOLS_DIR could make a managed wrapper and bundle disagree, but the observed wrapper uses the documented default"
  and_gate: "yes — the missing bundle makes the wrapper fail, while the presence-only code prevents setup from repairing it and makes doctor falsely report health; both contribute to the reported defect"
tdd_checkpoint: null

## Symptoms

expected: CFML definition navigation starts a working language server, and setup/doctor accurately report its health.
actual: The cflsp command is present but exits immediately because its JavaScript server bundle is absent; doctor reports it as healthy.
errors: "Cannot find module '/Users/ZKeown/.moedex-tools/cfc/cflsp-vscode/out/server.js'"
reproduction: Run the CFML definition navigation test or invoke navigation for a .cfm/.cfc file.
started: Existing machine state; no prior known working CFML installation was established.

## Eliminated

- hypothesis: The upstream cfc build command or output path changed.
  evidence: Official main at 8f1a207 still defines build-cflsp-prod and emits cflsp-vscode/out/server.js exactly.
  timestamp: 2026-08-27T14:54:11Z
- hypothesis: CFML source input or LSP initialization options cause the immediate process death.
  evidence: Direct cflsp invocation with no LSP message exits in the Node loader because server.js is absent.
  timestamp: 2026-08-27T14:54:11Z
- hypothesis: Node itself is absent or cannot execute.
  evidence: Node v24.19.0 runs and emits MODULE_NOT_FOUND for the wrapper's target.
  timestamp: 2026-08-27T14:54:11Z

## Evidence

- timestamp: 2026-08-27T14:47:32Z
  checked: /opt/homebrew/bin/cflsp and /Users/ZKeown/.moedex-tools/cfc
  found: The wrapper execs a missing server.js file, and its containing checkout does not exist.
  implication: PATH presence is insufficient to prove CFML server health.
- timestamp: 2026-08-27T14:51:04Z
  checked: scripts/install-lsp-servers.sh, internal/app/indexcmd/doctor.go, internal/navigate/multilang_test.go
  found: Installer idempotency, doctor readiness, and live-test availability all use command presence only; none validates that cflsp survives startup or can complete LSP initialize.
  implication: A stale executable wrapper is consistently misclassified as installed, healthy, and testable until the real navigation client launches it.
- timestamp: 2026-08-27T14:51:04Z
  checked: Official softwareCobbler/cfc main at 8f1a207fafa43531c646ebd30e1ba6116bb525bf
  found: The official root scripts remain `npm run install-all` and `npm run build-cflsp-prod`; src/build/build.ts emits exactly cflsp-vscode/out/server.js, and the VS Code package publishes that file.
  implication: The repository's intended upstream and build command are still current, but discovery should use the documented exact output rather than grepping bundle contents.
- timestamp: 2026-08-27T14:52:45Z
  checked: Existing TestCFML_Definition through the real Pool/LSP initialize path
  found: The test deterministically fails in 0.04s with `navigate: initialize: navigate: language server died: EOF` instead of skipping.
  implication: Command presence passes the availability gate, but the real server process dies during initialization exactly as predicted.
- timestamp: 2026-08-27T14:54:11Z
  checked: Direct `cflsp --stdio` process startup
  found: Exit status 1 with Node v24.19.0 MODULE_NOT_FOUND for the exact absent managed server.js path.
  implication: The server dies before protocol parsing; CFML content and initialization options cannot cause this immediate failure.
- timestamp: 2026-08-27T14:54:11Z
  checked: Spectrum-based fault localization eligibility
  found: Skipped because the failure is an external-process integration test with no per-test coverage spectrum contrasting passing and failing tests at the shell/doctor fault sites.
  implication: Deterministic reproduction plus producer/checker tracing provides the applicable Bohrbug localization evidence.
- timestamp: 2026-08-27T14:56:46Z
  checked: Agent-authored focused regressions before the fix
  found: Installer test is RED because a fake stale cflsp is logged present instead of entering the build path; doctor tests are RED because no startup-health seam or bounded probe exists.
  implication: The tests reproduce both halves of the confirmed root cause and will exercise the intended fix sites directly.
- timestamp: 2026-08-27T14:58:20Z
  checked: First focused run after implementing the fix
  found: Both installer tests pass; doctor classification tests pass, while the live-helper branch fails because `select {}` triggers Go's runtime deadlock detector instead of modeling a server blocked on stdin.
  implication: The production fix signals are green; the remaining failure is isolated to the agent-authored test fixture and requires no production-code change.
- timestamp: 2026-08-27T14:59:00Z
  checked: Focused tests after correcting the helper fixture
  found: `go test ./scripts -run '^TestCFMLDryRun'` and the focused indexcmd doctor/probe tests both pass.
  implication: The exact stale-wrapper and healthy-boundary classifications are now protected by specified-contract regressions.
- timestamp: 2026-08-27T15:01:06Z
  checked: Adjacent package and registry tests
  found: Full `go test ./scripts ./internal/app/indexcmd`, default navigate registry tests, and lsp-tagged language/registry tests all pass.
  implication: The strengthened health contract does not regress neighboring installer, doctor, or navigation registry behavior.
- timestamp: 2026-08-27T15:01:06Z
  checked: Behavioral revert-and-reconfirm at both production fix sites
  found: Reverting the installer artifact predicate made the stale-wrapper regression fail; bypassing the doctor CFML probe made its warning regression fail. Reapplying both changes returned every focused test to green.
  implication: The production changes, rather than incidental test/environment state, causally fix both reported misclassifications.
- timestamp: 2026-08-27T15:01:30Z
  checked: Final static checks and scoped worktree status
  found: `bash -n`, `go vet ./scripts ./internal/app/indexcmd`, and `git diff --check` pass; only the four intended implementation/test files plus this debug session are in task scope.
  implication: The patch is syntactically and statically clean and is ready for parent review/deployment without touching unrelated worktree changes.
- timestamp: 2026-08-27T15:05:10Z
  checked: CFML-only installer follow-up implementation
  found: `--cfml-only` now implies CFML opt-in and gates off the prerequisite and non-CFML language sections; the CFML regression helper uses the new flag and checks that unrelated installer output is absent.
  implication: The new mode is isolated in argument/routing logic while existing default and `--with-cfml` branches remain structurally unchanged.
- timestamp: 2026-08-27T15:06:12Z
  checked: First focused test invocation for the CFML-only follow-up
  found: Go setup failed before compiling tests because the sandbox denied access to `/Users/ZKeown/Library/Caches/go-build`; the chained `bash -n` did not run.
  implication: This is an environment/cache-path failure, not product evidence; rerun with a fresh writable temporary `GOCACHE`.
- timestamp: 2026-08-27T15:06:43Z
  checked: Focused CFML-only regression with a writable temporary Go cache
  found: The new skip test reached the installer and failed only because its broad `dotnet` substring matched the unchanged final PATH reminder (`$HOME/.dotnet/tools`); no .NET installer section output appeared.
  implication: The production branch behaved as intended, but the test oracle needs installation-specific markers to avoid a false positive.

## Resolution

root_cause: "The managed cfc checkout/server.js disappeared while its cflsp wrapper remained; scripts/install-lsp-servers.sh and `moe doctor` only checked executable presence, so setup skipped rebuilding the broken install and doctor falsely reported it healthy."
fix: "Installer health now requires the exact official managed server.js artifact and always refreshes its wrapper; doctor performs a CFML-only bounded process-startup probe before reporting cflsp healthy."
verification:
  target_test: {result: pass}
  mutation_check: {result: skipped, reason_if_skipped: "No Stryker or Go mutation runner is configured; behavioral reversions at both fix sites were killed by the focused regressions.", mutant_killed: null}
  no_op_deletion: {result: pass, deletion_justified_by_rca: false}
  adjacent_tests: {result: pass, suites_run: ["go test ./scripts ./internal/app/indexcmd -count=1", "go test ./internal/navigate registry subset", "go test -tags lsp ./internal/navigate language/registry subset", "go vet ./scripts ./internal/app/indexcmd"]}
  revert_and_reconfirm: {result: pass, bug_returned_on_revert: true, fixed_on_reapply: true}
  guardrail_verdict: accepted
oracle_type: specified
files_changed: [scripts/install-lsp-servers.sh, scripts/install_lsp_servers_test.go, internal/app/indexcmd/doctor.go, internal/app/indexcmd/doctor_lsp_test.go]
