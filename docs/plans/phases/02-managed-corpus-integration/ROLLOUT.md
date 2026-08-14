---
status: ready-for-cutover
phase: 02-managed-corpus-integration
owner: "<soak owner pending>"
started: "2026-08-14T09:29:00-06:00"
updated: "2026-08-14T13:48:46-06:00"
soak_until: "<pending operator-selected soak window>"
---

# Managed Corpus Sibling Rollout

Record redacted aggregate evidence only. Do not include project names, clone URLs, tokens,
usernames, or other corpus membership details.

## Paths and snapshot identities

| Artifact | Previous live | Managed sibling |
|---|---|---|
| Corpus root | Retained conventional root; local path redacted | Isolated managed sibling; local path redacted |
| Corpus snapshot / lock digest | No managed lock | `5030891ff8b5e530785683fecd80503962c3b61e` / `sha256:a9a9be4a753e2757bf43783cbc057c267e5ea457f5501bf5cf397b1a7d45053a` |
| CAS directory / manifest digest | No legacy CAS manifest | Isolated sibling / `sha256:0b8c7b0c737be38f8a5e08b7f226968d99d0f5ffb316de326dbd06a6d813dc9d` |
| Served shard directory / fingerprint | Retained live / `sha256:7f1cfa10e4f95463835056a57ef62aabdc6f023a7639244e828b9149d2edd54c` | Isolated sibling / `sha256:9d2d165f652d40c1ee98d08292f7e8bd9246f8d8bc5ef89aa3c5942817076c11` |
| Dense sidecar | Retained v2, fingerprint-fresh | v2, fingerprint-fresh / meta `sha256:9608916becd6e31038eb5abc5302a5c68b46e8d39815a0aa0a98821218a114f8` |

## Redacted counts

| Count | Previous live | Managed sibling | Gate |
|---|---:|---:|---|
| Served projects / manifest heads | 359 | 491 | **Operator adjudication:** candidate intentionally expands stale live scope by 132 projects |
| Locked default commits | n/a | 491 | equals managed projects |
| Canonical `.ai-privacy.yml` policies | 114 | 116 | all parse; difference follows scope expansion |
| Globally level-1 repositories | 1 | 1 | candidate contributes zero indexed file references |
| Level-1 path overrides | 1 | 1 | candidate contributes zero indexed file references |
| Privacy-eligible indexed files | unavailable in legacy manifest | 63,225 | candidate equals its ripgrep oracle scope |
| Served blobs | 48,300 | 55,080 | explained by scope expansion and four-repo refresh |
| Dense chunks | 930,725 | 942,718 | 852,492 reused; 90,226 new |
| CAS unique blobs | n/a | 53,205 | 1.23x raw/stored dedup ratio |
| Privacy-eligible file references | unavailable in legacy manifest | 63,225 | restricted references = 0 |

## Automated gates

- [x] `moedex-corpus doctor -corpus <new-corpus>` passes marker, lock, submodule, auth,
      VPN/API reachability, and Git transport checks.
- [x] `moedex-index doctor -shard-dir <new-shards>` passes with zero critical findings.
- [x] `moedex-index check -shard-dir <new-shards> -corpus <new-corpus>` reports no changes.
- [x] Dense sidecar is v2/incremental-ready, contains 942,718 chunks, and its fingerprint matches
      the managed shard snapshot.
- [x] Policy-only audit validates every canonical `.ai-privacy.yml` without reading repository
      content; the current conventional-corpus baseline is 114 policies and zero `.yaml` aliases.
- [x] CAS and served manifests contain privacy fingerprints for every repository; every global or
      path-level effective level-1 scope contributes zero searchable references.
- [x] `make parity MOEDEX_CORPUS=<new-corpus>` passes on the post-refresh lock.
- [x] Hermetic conventional-clone versus managed-submodule comparison passes for
      `(path_with_namespace, relative_path, line, matched_text)` across direct, CAS, deduped,
      refresh, and ripgrep paths.
- [x] Previous-live versus candidate equality is not satisfiable because the retained live index
      has 359 heads and the managed candidate has 491. The operator approved this intentional
      scope expansion on 2026-08-14; archived GitLab projects contribute zero candidate-only paths.
- [x] One scheduled-equivalent refresh completes in the order managed sync → CAS refresh →
      deduped export → sidecars → warm reload.
- [x] A forced pre-export failure leaves the previous served snapshot searchable.

Evidence (redacted command summaries, digests, counts):

```text
make health: PASS (build, vet, full go test ./...)
make roundtrip: PASS
managed-refresh-test.sh: PASS, including forced cas-export failure and no reload
managed doctor: 491 locked projects; marker, superproject, lock, cleanliness,
  agreement, glab auth, VPN/API, Git, and clone/fetch transport all PASS
index doctor: 6 MOEDEX05 shards; 0 critical findings
freshness check: no changed repos
privacy policy audit:
  conventional policies=114 global_level1=1 level1_overrides=1
  managed policies=116 global_level1=1 level1_overrides=1
publication privacy audit:
  repos=491 eligible_file_refs=63225 restricted_file_refs=0
  missing CAS/served identities=0 fingerprint mismatches=0
CAS build: repos=491 unique_blobs=53192 file_refs=63224 stored=782.5 MB
scheduled-equivalent refresh:
  changed=4 added=0 removed=0 failed=0; +13 blobs / +0.3 MB
  export rewrote 4 shards and carried 2; served repos=491
  SIGHUP reload PASS; health PASS; representative exact query PASS
post-refresh CAS: unique_blobs=53205 file_refs=63225 stored_bytes=782771828
post-refresh parity:
  repos=491 files=63225 content=962.5 MB shards=6 queries=1046
  under_approximations=0 over_approximations=0 rg_errors=0 moedex_errors=0
```

## Preflight deviations

- The first sibling initialization was stopped before CAS/index creation when the missing
  `.ai-privacy.yml` enforcement was discovered.
- Its incomplete sibling root is retained untouched pending explicit cleanup authorization; it is
  not a rollout candidate and was never connected to the live daemon.
- The first full managed parity run exposed invalid legacy single-byte samples in the Unicode
  battery. The sampler now accepts valid UTF-8 rune runs only; regression tests and two subsequent
  full-corpus parity runs pass with zero oracle errors.
- Finder created an untracked root `.DS_Store`. It remains untouched. Doctor now ignores only
  untracked superproject-root noise while tracked metadata, gitlinks, `.moedex`, and every
  submodule worktree continue to fail closed on local changes.
- The scheduled-equivalent refresh advanced four repositories by 13 net-new blobs. All privacy,
  freshness, health, and full-corpus parity gates were rerun against the resulting lock.
- The macOS serve and refresh templates initially could have refreshed the managed corpus while
  continuing to serve legacy shards. The three corpus/CAS/shard paths now render together, and a
  hermetic installed-plist test prevents that split-brain configuration.
- The first dense candidate build was stopped before publication after confirming it had no reuse
  seed. Copying the retained v2 store into the sibling enabled content-key reuse; the incremental
  run completed in 11 minutes with 852,492 reused and 90,226 new chunks.

## Configuration switch

- Scope expansion approved by: **operator, 2026-08-14**
- Production configuration switch approved by: **pending explicit approval with soak owner/window**
- Configuration changed: **none; live launchd configuration remains untouched**
- Previous configured paths retained at: retained live paths; local values redacted
- Token rotation: completed; the legacy-path daemon was restarted and passed `/healthz` plus an
  authenticated MCP request with the rotated token
- Health immediately after SIGHUP: isolated test daemon passed; production switch not attempted

## Rollback

Trigger conditions: failed health, parity regression, missing source, stale lock, or refresh failure.

1. Restore the previous corpus/CAS/shard path configuration.
2. Warm-reload (or restart only if warm reload is unavailable) the daemon.
3. Confirm `/healthz`, snapshot fingerprint, and representative exact queries.
4. Preserve the failed sibling paths for diagnosis; do not delete or adopt them in place.

Rollback exercised by: hermetic forced-export-failure test at
`2026-08-14T12:28:00-06:00` — outcome: pass; live rollback remains pending because no switch occurred

## Soak outcome

- Soak owner: pending
- Soak window: pending
- Scheduled refreshes observed: 1 isolated scheduled-equivalent refresh
- VPN/glab/Git transport failures: 0
- Freshness/health/parity incidents: 0 after the documented auto-fixed defects above
- Final outcome: ready for cutover; legacy service remains healthy
- Approval: scope expansion approved; production rebootstrap pending named soak owner/window

## Open checkpoint

The candidate itself is green, and the operator approved the 359 → 491 served-project scope
expansion. Production cutover and ADR 0020 Phase 3 remain blocked until the operator names the soak
owner and window and explicitly approves rebootstrap of both launchd agents onto the three managed
paths. The incomplete approval was not applied; the legacy configuration remains live and healthy.
