---
status: complete
phase: 02-managed-corpus-integration
owner: "operator (user)"
started: "2026-08-14T09:29:00-06:00"
updated: "2026-08-17T08:37:52-06:00"
completed: "2026-08-17T08:37:52-06:00"
soak_until: null
---

# Managed Corpus Sibling Rollout

Record redacted aggregate evidence only. Do not include project names, clone URLs, tokens,
usernames, or other corpus membership details.

## Paths and snapshot identities

| Artifact | Previous live | Managed sibling |
|---|---|---|
| Corpus root | Retained conventional root; local path redacted | Isolated managed sibling; local path redacted |
| Corpus snapshot / lock digest | No managed lock | `764ed889d630ea40198dc35f6a78cbac344d52a2` / `sha256:79ba75229959760ae155f439420f2ee75f5eb277c410422329a9ae85e4c12cd8` |
| CAS directory / manifest digest | No legacy CAS manifest | Isolated sibling / `sha256:29aaeefb065fdbc5b67033ce8b0918429c3985d08a3b5dd6287ecb03c5f56927` |
| Served shard directory / fingerprint | Retained live / `sha256:7f1cfa10e4f95463835056a57ef62aabdc6f023a7639244e828b9149d2edd54c` | Isolated sibling / `sha256:7f05d57f400087745379d137e09314e8dd3c045069cfb05880d496dcad5f7d14` |
| Dense sidecar | Retained v2, fingerprint-fresh | v2, fingerprint-fresh, 942,867 chunks; production daemon loaded from cache |

## Redacted counts

| Count | Previous live | Managed sibling | Gate |
|---|---:|---:|---|
| Served projects / manifest heads | 359 | 491 | **Operator adjudication:** candidate intentionally expands stale live scope by 132 projects |
| Locked default commits | n/a | 491 | equals managed projects |
| Canonical `.ai-privacy.yml` policies | 114 | 116 | all parse; difference follows scope expansion |
| Globally level-1 repositories | 1 | 1 | candidate contributes zero indexed file references |
| Level-1 path overrides | 1 | 1 | candidate contributes zero indexed file references |
| Privacy-eligible indexed files | unavailable in legacy manifest | 63,268 | candidate equals its ripgrep oracle scope |
| Served blobs | 48,300 | 55,118 | explained by scope expansion and subsequent managed refreshes |
| Dense chunks | 930,725 | 942,867 | production refresh reused 852,265; 90,602 new |
| CAS unique blobs | n/a | 53,406 | 962.8 MB raw / 795.8 MB stored |
| Privacy-eligible file references | unavailable in legacy manifest | 63,268 | restricted references = 0 |

## Automated gates

- [x] `moedex-corpus doctor -corpus <new-corpus>` passes marker, lock, submodule, auth,
      VPN/API reachability, and Git transport checks.
- [x] `moedex-index doctor -shard-dir <new-shards>` passes with zero critical findings.
- [x] `moedex-index check -shard-dir <new-shards> -corpus <new-corpus>` reports no changes.
- [x] Fixed production refresh writes and reloads a fingerprint-fresh 942,867-chunk dense sidecar;
      launchd exits zero and the production daemon serves the new mapping.
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
  repos=491 eligible_file_refs=63268 restricted_file_refs=0
  missing CAS/served identities=0 fingerprint mismatches=0
CAS build: repos=491 unique_blobs=53192 file_refs=63224 stored=782.5 MB
scheduled-equivalent refresh:
  changed=4 added=0 removed=0 failed=0; +13 blobs / +0.3 MB
  export rewrote 4 shards and carried 2; served repos=491
  SIGHUP reload PASS; health PASS; representative exact query PASS
fixed production refresh:
  changed=4 added=0 removed=0 failed=0; export rewrote 3 shards and carried 3
  dense_chunks=942867 reused=852265 new=90602 distinct_texts=86820
  dense_wall=18m29s total_wall=23m57s; launchd_exit=0; SIGHUP_reload=PASS
post-refresh CAS: unique_blobs=53406 file_refs=63268 stored_bytes=795839386
post-refresh parity:
  repos=491 files=63268 content=962.8 MB shards=6 queries=1053
  under_approximations=0 over_approximations=0 rg_errors=0 moedex_errors=0
post-cutover production:
  doctor=21 ok / 2 optional-LSP warnings / 0 critical
  installed_commit=4317b61 served_blobs=55118 symbol_blobs=27854 dense_chunks=942867
  health=PASS authenticated_MCP=200 representative_search=PASS
  refresh_paths=managed corpus/CAS/shards; schedule=14:10 local; calendar_trigger=PASS
  legacy_corpus_and_shards_retained=PASS
full parity wall: build=58s scan=3m59s ripgrep=37m6s total=42m22s; PASS
Zoekt: installed after this run for future differential coverage
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
- The first production serve bootstrap returned launchd error 5 while the previous KeepAlive job
  was still unloading. The validated managed plist succeeded on exact retry, producing about 83
  seconds of startup downtime. The installer now retries that bounded transient and its hermetic
  test forces a first-bootstrap failure.
- The first real 14:10 production refresh completed managed sync, CAS refresh, and atomic shard
  export for a two-project/10-blob delta, but the export had discarded the dense reuse seed. The
  full rebuild was still active after 39 minutes and was terminated by the operator. The live
  daemon stayed healthy and retained its in-memory snapshot. Commit `4317b61` adds a no-change
  fast path and O(1) dense seed carry; full tests and a production-scale temporary benchmark pass.
- Two weekend calendar triggers and the first Monday kickstart failed closed at managed sync while
  the VPN was unavailable. No CAS or served snapshot advanced. After VPN reconnection, the exact
  same launchd job completed successfully with exit zero.
- The accepted production dense migration took 18m29s rather than the isolated benchmark's 11m22s,
  and the complete refresh took 23m57s. The operator accepted this as a substantial bounded
  improvement over the interrupted full rebuild.
- Warm reload proved zero-downtime data publication, then the operator requested a real process
  promotion onto commit `4317b61`. The cold restart took 10m47s while full parity saturated the
  machine; the new PID passed health, doctor, and authenticated MCP checks.

## Configuration switch

- Scope expansion approved by: **operator, 2026-08-14**
- Production configuration switch approved by: **operator, 2026-08-14**
- Configuration changed: **serve and refresh launchd agents now use the managed sibling corpus,
  CAS, and shard paths; refresh remains scheduled at 14:10 local**
- Previous configured paths retained at: retained live paths; local values redacted
- Token rotation: completed; the legacy-path daemon was restarted and passed `/healthz` plus an
  authenticated MCP request with the rotated token
- Health after fixed refresh and executable promotion: pass; production serves 55,118 blobs,
  27,854 symbol blobs, and 942,867 dense chunks from commit `4317b61`; authenticated MCP passes

## Rollback

Trigger conditions: failed health, parity regression, missing source, stale lock, or refresh failure.

1. Restore the previous corpus/CAS/shard path configuration.
2. Warm-reload (or restart only if warm reload is unavailable) the daemon.
3. Confirm `/healthz`, snapshot fingerprint, and representative exact queries.
4. Preserve the failed sibling paths for diagnosis; do not delete or adopt them in place.

Rollback exercised by: hermetic forced-export-failure test at
`2026-08-14T12:28:00-06:00` — outcome: pass. A live rollback was not required; the previous corpus
and shard manifest were confirmed retained after the production switch.

## Event-based acceptance outcome

- Acceptance owner: operator (user)
- Scheduled refreshes observed: one isolated scheduled-equivalent refresh before cutover; three
  real 14:10 calendar triggers after cutover; two weekend triggers failed closed without VPN
- First production run outcome: sync/CAS/export passed; dense stage terminated by operator after
  the missing-seed wall-clock defect was identified; launchd recorded terminating signal 15
- Fixed production run outcome: sync/CAS/export/dense/reload all passed; launchd exit code 0
- VPN/glab/Git transport failures: 3 managed-sync attempts failed closed before CAS publication
- Availability incidents during refresh: 0; health remained `ok` and the serving PID stayed stable
- Fixed-path benchmark: 852,481 chunks reused, 90,243 new, 11m22s cross-corpus migration; same
  current corpus completed in 4.47s with zero embeddings
- Accepted production result: 852,265 chunks reused, 90,602 new, 18m29s dense, 23m57s total;
  privacy/freshness/health/authenticated-MCP/full-parity gates pass
- Final outcome: accepted; Phase 2 complete and Phase 3 unblocked
- Approval: scope expansion, production rebootstrap, fixed kickstart, and final wall clock accepted
  by operator

## Closure

The managed snapshot is live and healthy on the installed `4317b61` executable. The fixed refresh
completed with launchd exit zero; privacy, freshness, health, authenticated MCP, and full-corpus
parity all pass. ADR 0020 Phase 3 is unblocked. The legacy corpus and shards remain the rollback
set.
