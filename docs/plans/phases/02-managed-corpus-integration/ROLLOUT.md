---
status: pending
phase: 02-managed-corpus-integration
owner: "<operator>"
started: "<ISO-8601>"
updated: "<ISO-8601>"
soak_until: "<ISO-8601>"
---

# Managed Corpus Sibling Rollout

Record redacted aggregate evidence only. Do not include project names, clone URLs, tokens,
usernames, or other corpus membership details.

## Paths and snapshot identities

| Artifact | Previous live | Managed sibling |
|---|---|---|
| Corpus root | `<old-corpus>` | `<new-corpus>` |
| Corpus snapshot / lock digest | `<old-snapshot-or-n/a>` | `<new-superproject-commit-and-lock-digest>` |
| CAS directory / manifest digest | `<old-cas>` / `<digest>` | `<new-cas>` / `<digest>` |
| Served shard directory / fingerprint | `<old-shards>` / `<fingerprint>` | `<new-shards>` / `<fingerprint>` |

## Redacted counts

| Count | Previous live | Managed sibling | Gate |
|---|---:|---:|---|
| Projects | `<n>` | `<n>` | equal |
| Locked default commits | `<n>` | `<n>` | equals managed projects |
| Indexed files | `<n>` | `<n>` | equal |
| Unique blobs | `<n>` | `<n>` | explain any dedup-only difference |
| File references | `<n>` | `<n>` | equal |

## Automated gates

- [ ] `moedex-corpus doctor -corpus <new-corpus>` passes marker, lock, submodule, auth,
      VPN/API reachability, and Git transport checks.
- [ ] `moedex-index doctor -shard-dir <new-shards>` passes.
- [ ] `moedex-index check -shard-dir <new-shards> -corpus <new-corpus>` reports no changes.
- [ ] `make parity MOEDEX_CORPUS=<new-corpus>` passes.
- [ ] Default-only exact result comparison passes for
      `(path_with_namespace, relative_path, line, matched_text)`.
- [ ] One scheduled-equivalent refresh completes in the order managed sync → CAS refresh →
      deduped export → sidecars → warm reload.
- [ ] A forced pre-export failure leaves the previous served snapshot searchable.

Evidence (redacted command summaries, digests, counts):

```text
<paste redacted evidence>
```

## Configuration switch

- Switch approved by: `<owner>` at `<ISO-8601>`
- Configuration changed: `<service/env reference; no secrets>`
- Previous configured paths retained at: `<rollback paths>`
- Health immediately after SIGHUP: `<status, fingerprint, timestamp>`

## Rollback

Trigger conditions: failed health, parity regression, missing source, stale lock, or refresh failure.

1. Restore the previous corpus/CAS/shard path configuration.
2. Warm-reload (or restart only if warm reload is unavailable) the daemon.
3. Confirm `/healthz`, snapshot fingerprint, and representative exact queries.
4. Preserve the failed sibling paths for diagnosis; do not delete or adopt them in place.

Rollback exercised by: `<owner>` at `<ISO-8601>` — outcome: `<pass/fail>`

## Soak outcome

- Soak owner: `<owner>`
- Soak window: `<start>` through `<end>`
- Scheduled refreshes observed: `<n>`
- VPN/glab/Git transport failures: `<redacted counts and outcomes>`
- Freshness/health/parity incidents: `<none or redacted summary>`
- Final outcome: `<accepted | rolled back | extended>`
- Approval: `<owner and ISO-8601 timestamp>`
