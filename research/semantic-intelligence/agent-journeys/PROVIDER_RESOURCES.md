# Opt-in provider resource stops

New freezes may include an exact `provider_resources` object. Historical freezes
without it retain their existing transport and request behavior. The policy is
host enforced, independently hash bound with `provider_resources.py`, and applies
to one isolated assignment. It adds no provider calls and performs no retries.

```json
{
  "schema": "provider-resource-policy-v1",
  "max_requests": 128,
  "max_request_bytes": 8388608,
  "max_response_bytes": 16777216,
  "max_total_request_bytes": 33554432,
  "max_total_response_bytes": 33554432,
  "max_observed_input_tokens": 200000,
  "max_observed_output_tokens": 16000,
  "max_output_tokens": 16000
}
```

Freeze the same `max_output_tokens` in the model settings' exact `provider_fields`
contract. The host retains the original CLI request and injects this fixed field
when absent, retaining the exact upstream request separately. An explicitly
different value stops before dispatch. All other frozen provider settings remain
validated against the upstream body. Count and individual/cumulative request-body
ceilings are checked before dispatch. The count and byte ledger describes
**reserved outbound attempts**, including attempts stopped during later settings
validation; it does not claim each reservation reached the provider or was billed.
The individual request limit applies to both original and upstream bodies, while
the cumulative request limit counts upstream bytes. Local incoming-message
transport retains its historical ceiling before this outgoing check.

New controller journals record exact `controller_reply` bytes before each host
pipe write, and `controller_reply_commit` only after the complete write and
flush. Journal failure before a write prevents relay; missing completion after an
attempt makes delivery unknown. `audit_relay_journal` reconstructs and verifies
the attempted/committed reply roster, including start and metadata messages.
This can establish that a crossing response had no relay attempt, when combined
with independent journal coverage and inventory checks. A completed pipe write
does not prove the model consumed its contents.

Response transport uses the smaller of the individual ceiling and remaining
cumulative allowance. Retained decoded response bodies may include one overflow
sentinel byte; headers and transport framing are excluded. Full or partial bytes
and receipts are retained before accounting or identity checks. Crossing responses
are never relayed to the solver. An exactly reached threshold permits the current
response and refuses the next request.

Token thresholds are **observed post-response stops**, so a single in-flight
request may cross a cumulative threshold. Cached input remains within reported
`input_tokens`; reasoning output remains within reported `output_tokens` and is
not counted twice. Complete 2xx JSON or SSE must establish one unambiguous terminal
response with a response ID, status, and consistent nonnegative integer usage.
Identical duplicated terminal SSE events count once. Unknown, partial, malformed,
conflicting, or HTTP error usage stops immediately and is never filled with zero.
Known failed/incomplete terminal responses also stop. A provider output count over
the fixed output limit, or an incomplete response caused by that limit, stops.
Known totals remain lower bounds when any reserved attempt has unknown usage.

The Responses API documents `max_output_tokens` as bounding visible and reasoning
output together, and documents cached/reasoning token details within usage:
[official Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create).
Reported usage is not authenticated billing. This policy establishes no monetary
ceiling and no pre-dispatch cumulative token reservation.

Each capture retains `provider-resource-policy.json`, the original
`provider-NNNN.request.json`, reserved `provider-NNNN.upstream-request.json`,
observed response/receipt pairs, and `provider-resource-ledger.json`. Ledger events
are also written to the durable controller journal. A reservation aborted before
a retained response is explicitly unknown. Rejected requests remain in the roster.

Replay against an **independently frozen policy**, preserving failures as failures:

```sh
python3 research/semantic-intelligence/agent-journeys/provider_resources.py \
  --capture CAPTURE --policy FROZEN_POLICY_JSON --output NEW_AUDIT_JSON
```

Replay verifies both policy copies, exact original-to-upstream transformation,
response and receipt hashes/accounting, event order, totals, stop reason, and file
rosters. It rejects missing/extra exchanges and symlinked exchange files. A valid
replay of a stopped attempt demonstrates honest accounting; it does not turn the
attempt into a success or resolve unknown usage. Independent capture-inventory,
isolation, native scope, timing, identity, and semantic reviews remain required.

Synthetic tests:

```sh
python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys \
  -p 'test_provider_resources.py'
```
