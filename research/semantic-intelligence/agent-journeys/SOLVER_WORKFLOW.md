# Opt-in solver evidence workflow

`solver_workflow.py` prepares a user-request requirement inventory and checks a
solver's final claim ledger against recorded presentation or client-delivered source excerpts. It runs offline,
without a corpus, provider, hidden expected answers, or model retries. The MCP
server instructions also ask agents to inventory user requirements, review
terminal paths, and verify claim-specific citations.

Run the commands below from `research/semantic-intelligence/agent-journeys`.
For a new task, save the original user request verbatim as `request.txt`. Create
`inventory.json` as an array of `{id,start,end,quote,kind}` objects. `start` and
`end` are zero-based, end-exclusive Python character offsets into that request;
`quote` must equal that exact span. Use `kind=requirement` for requested facts
and `kind=terminal_path` for outcomes explicitly requested. Distinct requested
obligations should have distinct IDs even when they share an anchor. An anchor
only establishes that an item came from the request: review the whole request
for missing obligations. The tool cannot infer that the inventory is complete.

```sh
python3 -B solver_workflow.py prepare --request request.txt --inventory inventory.json > plan.json
```

Supply the plan's request, inventory, and generated instructions to the solver
as part of a **new** prompt. For a comparison, freeze that prompt and disclose the
workflow before execution. The historical prompt and frozen pilot are unchanged;
this CLI does not rewrite, replay, or re-evaluate them. It does not automatically
change existing container prompts or final-answer formats.

The generated instructions specify a JSON answer with `claims` and
`requirements`. Each requirement gets `status=addressed` and `claim_ids`, or
`status=unresolved` and a reason. An addressed item must include `terminal_paths`
with named outcomes and the same disposition fields. For a literal-only ordinary
requirement, use empty `terminal_paths` with
`terminal_path_review={"status":"not_applicable","reason":"Literal-only fact"}`.
Explicit `kind=terminal_path` items cannot use this exemption. The solver reviews relevant
success, rejection, cancellation, and failure outcomes, including outcomes it
discovers during exploration. Unavailable paths remain explicitly unresolved.
This checks that a review was **declared**; it cannot establish that all runtime
paths were found or that the explanation is correct.

Each claim has `id`, `text`, `source_role` (`interface`, `implementation`, or
`other`) and `citations`, or an `unresolved_reason`. Each citation binds
either `presentation_id` from an opt-in broker notice or an independently known
`display_sha256` (SHA-256 of the exact presentation envelope bytes), `repo`, `path`,
`blob_sha`, `start_line`, `end_line`, and `source_role`. Line ranges are inclusive.
Keep interface declarations and implementation bodies in separate claims and
perform narrow declaration reads before labeling their roles. The checker
compares role labels for consistency; it does **not** classify source semantics
or detect two matching but mistaken role labels.

## Explicit evidence gaps

For a new prompt, opt into the more explicit ledger instructions and dispositions:

```sh
python3 -B solver_workflow.py prepare --request request.txt --inventory inventory.json \
  --citation-reference-mode broker-ordinal-v1 --ledger-mode explicit-gaps-v2 > plan.json
```

The generated plan binds the mode and instructions; freeze its exact bytes before
execution. Plans without this option retain their original instructions and checks.
Do not use a new plan to replace or rescore an existing frozen run.

An item with cited findings and unavailable evidence may use `status=partial`,
`claim_ids`, and a nonempty `reason`. It must link at least one cited finding.
Partial requirements and terminal paths remain in `unresolved`, so they cannot
produce `declared_complete=true`. An entirely unavailable item uses `unresolved`.
`addressed` continues to require only resolved cited claims. Explaining why runtime
evidence is unavailable does not establish a runtime result.

A non-source evidence limitation or process statement uses `source_role=other`,
empty `citations`, and `unresolved_reason`; it receives no source credit. A cited
source claim must use the same role in each citation. Matching labels establish
consistency only. Citation identity and complete visible line bounds remain strict
for every claim, including those linked by partial items.

With nonempty `terminal_paths`, a requirement may omit `terminal_path_review` or
use `{"status":"reviewed","reason":"Relevant outcomes inventoried below"}`.
A reviewed declaration needs both paths and a reason. `not_applicable` still
requires empty paths and an ordinary requirement; explicitly requested terminal
paths cannot use it. Unavailable outcomes remain unresolved, even when other
outcomes have source evidence.

The mode provides structural examples and a final consistency checklist, not
hidden answers or automatic solver feedback. Synthetic tests establish schema
behavior only; they do not establish better model compliance or retrieval quality.

## Field-projection and negative-selection review

When a request asks which fields a conversion selects or leaves out, compare
the requested source declarations, destination declaration, and complete bounded
conversion body. Keep destination assignments, transformations, fallback choices,
and filtering predicates distinct. A field can affect whether a row is emitted
without becoming a destination field. A similarly named field is not evidence
that the conversion selects it.

For example, in an invented conversion from `Input` to `Output`, `Output.Host`
may select `Input.PrimaryHost`, leave `Input.BackupHost` unused, and depend on
`Input.Enabled` in a guard. Record those three dispositions separately, citing
the relevant declaration and implementation spans. To say a field is unused
in this conversion, inspect its complete bounded body, including guards and
helpers that might receive the field or entire input. If helper behavior, a
clipped tail, or a source file is unavailable, mark that conclusion unresolved.
Do not turn a missing search result into a repository-wide absence claim.

Before submitting, reconcile each requested field or field category with the
actual answer, not merely with retrieved source or an `addressed` ledger flag.
An internal review table can list source field, destination or predicate use,
selection/omission/unknown disposition, and citations. This is guidance for a
source audit, not a new mandatory answer schema or automated completeness proof.
Keep the scope anchored to the request; never add hidden expected field names
to an inventory or comparative prompt. Independently check that every scored
obligation is actually requested before freezing a new task.

## Solver-visible citation references

For new runs, set `citation_reference_mode="broker-ordinal-v1"` in both the freeze
and execution config. This option requires the existing validated frozen
`source_scope_policy`. Freeze the exact SHA-256 of `solver_workflow.py` in the
`runners` map even when `presentation_mode` is absent. Reference and compact
presentation modes are independent; absent reference mode preserves prior
presentation bytes. Existing exact-hash citations remain accepted.

Prepare the opt-in instructions with:

```sh
python3 -B solver_workflow.py prepare --request request.txt --inventory inventory.json \
  --citation-reference-mode broker-ordinal-v1 > plan.json
```

Each scope-accepted tool reply gets an outer
`_meta["dev.moedex/presentation-reference"]` object such as
`{"mode":"broker-ordinal-v1","id":"display-4"}` and a short text notice exposing
that ID. The ID uses the native attempt ordinal within this assignment, including
prior onboarding or denied attempts. Copy the visible ID into the claim's
`presentation_id`. The solver does not need to reconstruct or hash invisible
JSON-RPC/MCP wrappers. The compact display's `validated_envelope_sha256` identifies
an intermediate envelope and is **not** the final recorded presentation hash.

The offline checker resolves IDs only from the supplied exact presentation
envelopes. `presentation_hashes` reports ID-to-exact-byte-hash mappings, and
`resolved_citations` reports the hash used for each mechanically valid citation.
The original answer is left intact. Identical byte duplicates are permitted;
the same ID attached to distinct byte hashes is ambiguous and fails the check.
When a citation includes both locators, they must resolve to the same hash.
Unknown IDs, malformed reference metadata, wrong source identity, and ranges
outside complete visible lines fail closed. Scope-denied/error replies and
unparsed prefix fallbacks provide no reference-based source credit.

The reference notice is stamped after scope acceptance and before display
capping. It appears in both legacy and compact text channels when the parsed
reply fits. The complete envelope still obeys its original display cap; adding
the notice may reduce available source space. An oversized legacy envelope can
become an unparsed prefix with no citeable ID or source; request a narrower range.
The scope decision records the reference mode and accepted ordinal ID, and
recording preflight and audit check them against the freeze and presentation.
Public replay uses `present_response(response, cap, presentation_mode,
citation_reference_mode, ordinal)` only after scope acceptance. Denied/error
replay passes no reference mode, while its receipt retains the frozen mode with
no ID. No reference mode retains the original replay signature and behavior.

Reference IDs do not authenticate arbitrary `--display` files. Those files are
still assumed to be exact client presentation bytes supplied by a trusted caller.
Use `--record-root` for audited recorded presentations. Neither path establishes
relay completion, model consumption, or semantic support. This is a prospective
generic usability option; historical frozen workflows and pilot evidence are
unchanged, and these synthetic validations provide no comparison-score evidence.

For recorded broker presentations, use the audited recording:

```sh
python3 -B solver_workflow.py check --plan plan.json --answer answer.json \
  --record-root /authorized/evidence --assignment new-assignment
```

This path first audits the recording and reads only its hashed `display` event
references, excluding native raw response captures. These events record prepared
presentation bytes before relay completion. They establish what the broker
prepared, not that the client received, rendered, or consumed it; separately
verify relay completion and client behavior before making delivery claims. For another client, pass
`--display delivered-1.json --display delivered-2.json` instead. Those inputs must
be exact client-delivered envelopes; the CLI cannot authenticate arbitrary files
as deliveries. Do not pass full raw responses in place of clipped displays.

The check rejects missing dispositions, addressed items without cited claims,
unknown references, inconsistent labels, wrong repository/path/blob identities,
and ranges extending beyond complete shown lines. Moedex `read_source` and
`search_context` source shapes and CodeGraph `graph_source` numbered lines are
recognized. Declaration metadata alone, unparsed truncation prefixes, and clipped
partial final lines provide no source evidence. Physical source lines use LF;
CRLF keeps its CR as part of the preceding line, and Unicode separators, VT, FF,
and lone CR cannot manufacture line coordinates. Unsupported response shapes
remain unavailable. Exact response hashes bind a citation to a particular
shown excerpt even when snapshots change.

Exit code 1 means a mechanical validation failed. A mechanically valid result
can still have `declared_complete=false` because it retains unresolved items.
`semantic_correctness` is always `not assessed`; source presence and valid
bounds do not prove a claim. `source_visibility` explicitly leaves client
delivery, rendering, and consumption unassessed. Treat the report as a final-answer preflight and
review findings before submission. It supplies no automatic solver feedback or
retry loop.

## Single-source bounded presentation

Future recorded runs may set `presentation_mode=single-source-v1` in **both** the
freeze and execution config. Include the exact SHA-256 of `solver_workflow.py`
in the frozen `runners` map. The mode requires the existing frozen
`source_scope_policy`; mismatches or missing runner hashes fail before dispatch.
Absence of the mode retains historical presentation behavior.

The broker validates native provenance, source pins, selector issuance, cursors,
and finite serialization through the existing scope adapter before presenting
anything. Compact presentation then retains one `structuredContent` object and
a lightweight text notice. It preserves the adapter's admitted identity and
navigation fields; native fields already discarded by scope validation remain
discarded. Raw native bytes, denied/error replies, call charging, policy receipts,
and exact prepared presentation bytes continue to be recorded. The display receipt binds the
validated envelope's hash and byte count; the existing scope receipt separately
binds the actual raw native response.

When the complete envelope exceeds the display cap, source is clipped only at
LF boundaries or numbered-line entries. The longest fitting source prefix is
found with logarithmic envelope sizing, retaining repository/path/blob and
snapshot metadata, selectors, cursors, explicit truncation, and instructions to
request narrower ranges or the returned cursor. Multiple records may be trimmed
starting with the largest. Empty source has no citeable lines. If metadata alone
cannot fit, the historical bounded prefix fallback retains error signaling but
provides no parseable source for the ledger.

Public replay code should call `isolated_solver.present_response` on the
scope-accepted envelope with the frozen display cap and presentation mode. The
record audit requires every scope decision's mode to match the freeze and checks
its raw-response and recorded-presentation hashes. Legacy freezes omit the mode and
retain the legacy transform. Private frozen auditors are unchanged.

This mode requires a client that exposes `structuredContent` to the solver.
The synthetic tests prove adapter sizing, presentation recording, replay bytes,
source-fact survival, and citation bounds. They do not prove relay completion or that the real Codex/model
client displays this structured channel or that reducing duplication improves
answers. Keep the mode opt-in until a separately authorized client compatibility
check establishes that behavior. No SDK or model configuration changes are made.

```sh
python3 -B -m unittest test_citation_references test_solver_workflow test_native_scope test_isolated_solver
```
