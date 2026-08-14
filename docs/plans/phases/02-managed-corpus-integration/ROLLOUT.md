---
status: awaiting-production-refresh
phase: 02-managed-corpus-integration
owner: "operator (user)"
started: "2026-08-14T09:29:00-06:00"
updated: "2026-08-14T15:08:29-06:00"
soak_until: null
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
| Dense sidecar | Retained v2, fingerprint-fresh | Live daemon retains the prior 942,718-chunk mapping; disk holds a v2 incremental reuse seed pending fixed refresh completion |

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
| Dense chunks | 930,725 | 942,724 current shard target; live daemon 942,718 until reload | temporary benchmark reused 852,481; 90,243 new |
| CAS unique blobs | n/a | 53,205 | 1.23x raw/stored dedup ratio |
| Privacy-eligible file references | unavailable in legacy manifest | 63,225 | restricted references = 0 |

## Automated gates

- [x] `moedex-corpus doctor -corpus <new-corpus>` passes marker, lock, submodule, auth,
      VPN/API reachability, and Git transport checks.
- [x] `moedex-index doctor -shard-dir <new-shards>` passes with zero critical findings.
- [x] `moedex-index check -shard-dir <new-shards> -corpus <new-corpus>` reports no changes.
- [ ] Fixed production refresh writes a fingerprint-fresh 942,724-chunk dense sidecar and reloads
      it; the live daemon remains healthy on its prior 942,718-chunk mapping meanwhile.
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
post-cutover production:
  doctor=21 ok / 2 optional-LSP warnings / 0 critical
  served_blobs=55080 symbol_blobs=27833 dense_chunks=942718
  health=PASS authenticated_MCP=200 representative_search=PASS
  refresh_paths=managed corpus/CAS/shards; schedule=14:10 local; calendar_trigger=PASS
  legacy_corpus_and_shards_retained=PASS
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

## Configuration switch

- Scope expansion approved by: **operator, 2026-08-14**
- Production configuration switch approved by: **operator, 2026-08-14**
- Configuration changed: **serve and refresh launchd agents now use the managed sibling corpus,
  CAS, and shard paths; refresh remains scheduled at 14:10 local**
- Previous configured paths retained at: retained live paths; local values redacted
- Token rotation: completed; the legacy-path daemon was restarted and passed `/healthz` plus an
  authenticated MCP request with the rotated token
- Health immediately after switch: pass; production serves 55,080 blobs, 27,833 symbol blobs, and
  942,718 dense chunks; authenticated MCP and a redacted representative search pass

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
- Scheduled refreshes observed: one isolated scheduled-equivalent refresh before cutover; one real
  14:10 calendar-triggered production run after cutover
- Real production run outcome: sync/CAS/export passed; dense stage terminated by operator after
  the missing-seed wall-clock defect was identified; launchd recorded terminating signal 15
- VPN/glab/Git transport failures: 0
- Availability incidents during refresh: 0; health remained `ok` and the serving PID stayed stable
- Fixed-path benchmark: 852,481 chunks reused, 90,243 new, 11m22s cross-corpus migration; same
  current corpus completed in 4.47s with zero embeddings
- Final outcome: production switch healthy; fixed production refresh completion pending explicit
  kickstart approval
- Approval: scope expansion and production rebootstrap approved by operator

## Open checkpoint

The managed snapshot is live and healthy. ADR 0020 Phase 3 remains blocked until the operator
explicitly approves one fixed production kickstart, launchd records exit zero after warm reload,
and the post-refresh privacy, freshness, and health checks pass. The legacy corpus and shards
remain the rollback set.
