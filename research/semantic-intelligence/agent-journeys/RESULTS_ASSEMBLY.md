Private neutral results assembly helper

This tool projects closed independent declarations into broader-claim-results-v1.
It copies raw outcomes/failure classes and source criteria. It does not determine
source correctness, label claim findings, retry solvers, contact a product or
provider, or publish anything. Inputs remain unchanged. Configs, answer blobs,
source and capture references are hash-checked as opaque bytes; no prompt-bearing
configuration or answer body is interpreted. Recorded event metadata joins each
declared final to that assignment's last retained answer event.

Run from the private B directory with Python's bytecode cache disabled:

python3 -B tools/assemble_pilot_results.py \
  --packet packet/launch-packet.json --packet-sha256 <exact-file-sha256> \
  --equal-access reviews/postrun-equal-access.json --equal-access-sha256 <exact-file-sha256> \
  --shared-harness reviews/postrun-shared-harness.json --shared-harness-sha256 <exact-file-sha256> \
  --schema-sha256 <exact-sha256-of-evidence/research/semantic-intelligence/agent-journeys/broader_claim.py> \
  --output results/results.closed.json --manifest-output results/assembly.closed.json

Both output parent directories must exist. Output paths are root relative,
normalized, confined, and free of symlinks. Both destinations must be new files.
All input validation finishes before exclusive-create output writes. Each output
has mode 0600. Plan/sample result hashes use the neutral schema's canonical JSON;
file references use SHA256 of exact file bytes, including actual trailing newlines.

Optional independently approved mechanical raw-review corrections:
  --raw-review-selection reviews/raw-review-selection.closed.json \
  --raw-review-selection-sha256 <exact immutable manifest SHA256>
These arguments must appear together. No manifest means canonical raw review
paths for all slots. The tool never scans for, or selects, the newest correction.
Unlisted assignments still use canonical files. Existing originals are not edited.

Selection manifest has exactly:
  schema:"broader-pilot-raw-review-selection-v1", packet:{path,sha256},
  plan:{path,sha256}, sample:{path,sha256}, selections:[
    {assignment:<own assignment>, original:{path,sha256}, selected:{path,sha256},
     reason:<nonempty>, independent_review:{path,sha256}}, ...
  ]
Original must be reviews/attempt-raw/<assignment>.json. Selected must be a separate
explicit reviews/attempt-raw-corrections/<assignment>.* artifact. All refs must
match closed bytes exactly. Selections must be nonempty, unique, and in the packet.

Each approval receipt has exactly these fields:
  schema:"broader-pilot-raw-review-mechanical-amendment-review-v1",
  packet:{path,sha256}, plan:{path,sha256}, sample:{path,sha256},
  assignment, original:{path,sha256}, selected:{path,sha256}, reason,
  reviewer_id, independent:true, complete:true, approved:true, reasoning,
  original_decision, observed_utc:<UTC>
Packet/plan/sample and the assignment/original/selected/reason must match the
manifest row exactly. Reviewer must differ from all solvers and the raw author.
Receipts approve row content and do not reference the final manifest, avoiding a
circular file hash between receipt and manifest.

Only these mechanical corrections are supported:
  classification_basis dictionary -> its exact nonempty original ['basis'] string,
  with the entire original dictionary retained as new classification_details;
  manual_review.notes nonempty string list -> exactly ' '.join(original list);
  evidence_refs keep the entire original list prefix, then append only missing
  exact packet.plan, packet.sample, and original refs, all of which must be present;
  a new formatting_correction object with exactly:
    schema:"raw-review-formatting-correction-v1", original:{path,sha256},
    observed_utc:<UTC>, reason:<nonempty>, preserved_prior_corrections:[exact refs]
Prior correction refs must be separate own-assignment correction artifacts.
Every unrelated JSON value and type must remain unchanged, including identities,
task/arm/repetition/solver, outcome, failure_class, final, raw_review_fields, manual
booleans, original observed_utc, original_decision, and any original disagreements.
False->0, true->1, or changed original-decision text is rejected. This tool does
not infer semantically equivalent wording or approve a changed classification.
Existing classification_details/formatting_correction fields cannot be replaced.
The assembly manifest retains original+selected+approval refs, the selection
reason, original evidence refs, and all selected prior-correction refs. Results
point only to the explicitly selected effective raw-review artifact. Access and
harness cause receipts continue to bind that effective exact raw-review ref.

Each actual packet row requires these immutable reviews:
  reviews/attempt-raw/<assignment>.json
  reviews/attempt-source/<assignment>.json
The helper validates reviewer IDs against the full 360-solver roster, requires
distinct raw/source reviewer IDs, and appends artifact:{path,sha256} only to the
copied schema-ready review fields. Original review artifacts must not contain
their own artifact field/hash or a self-reference.

The source review's explicit review_history object is:
  {"no_task_authorship":true,
   "no_prospective_source_or_gold_review":true,
   "no_feedback_to_solver":true}
Required_claims and material_claims_and_citations are lists of original findings;
the helper does not translate labels into criteria. No-final applicability requires
all five declared source success criteria false. False completeness/integrity
declarations from an actual completed review are retained as readiness diagnostics.
Raw complete:true requires every mandatory manual-read and verification flag true;
a contrary flag fails binding consistency. A closed review with incomplete raw
evidence and false manual checks instead retains complete:false. The helper never
changes either declaration or derives criteria from review findings.
Optional status/review_status and string manual_review/semantic_source_review
closure fields must explicitly equal one of closed, complete, completed, reviewed,
final, finalized, or sealed (case-insensitive after trimming). Unknown, draft,
active, in_progress, pending and prepared values are rejected. Nested manual or
semantic review status fields obey the same rule. This closure state is separate
from false evidence-completeness declarations. A nonterminal status, absent review,
absent intent, or absent terminal driver result aborts assembly, producing no
invented classifications or partial denominator.

Raw evidence_refs must include the exact packet/plan/sample/config, driver intent
and result, plus assignment/freeze, capture inventory, final answer event and own
final wherever those artifacts exist. Source evidence_refs must include the exact
packet/plan/sample/config and own final, plus the reviewer's original own-evidence
and source references. A file reference is exactly {"path":...,"sha256":...};
repository/pin/span citation metadata can carry those refs inside separate fields.
Missing evidence is a diagnostic declaration, never a reference to a nonexistent
file presented as retained evidence. Raw machine_review has exactly this shape:
  {"artifact":{"path":<immutable machine-report path>,"sha256":<exact file SHA256>},
   "assignment":<this raw review's exact assignment name>}
Missing, different, or extra selectors are rejected; the tool does not infer a
selector or change machine findings. The final all360 raw audit remains separate.

No-assignment launch/process failures require actual intent/result records, a
closed independently confirmed non-success classification, false capture
integrity, and matching source applicability. An absent inventory always requires
false capture integrity. Evidence completeness may be false in these closed,
independently classified failures. Existing finals remain retained after stops and capture
defects. This tool never creates substitute captures or changes reviewed outcomes.

Both postrun declaration receipts are mandatory, independently reviewed, and
hash-bound even when their booleans are false. They contain exactly these common
fields (all refs are root relative and exact):
  schema, packet:{path,sha256}, plan:{path,sha256}, sample:{path,sha256},
  reviewer_id, independent:true, complete:true, evidence_refs:[refs...],
  reasoning:<nonempty text>, original_decision:<nonempty immutable finding>,
  observed_utc:<YYYY-MM-DDTHH:MM:SS[.fraction]Z>

Equal-access receipt:
  schema = broader-pilot-postrun-equal-access-review-v1
  established = explicit boolean
Every confirmed per-assignment access loss must appear as its exact raw-review
reference in this receipt, and established must then be false.

Shared-harness receipt:
  schema = broader-pilot-postrun-shared-harness-review-v1
  confirmed = explicit boolean
  reviewed_causes = [
    {assignment:<exact assignment>, cause:"shared_harness",
     raw_review:{path,sha256}, reasoning:<independent causal review>}, ...
  ]
The cause roster must exactly equal assignments classified shared_harness, with
no absent/extra/duplicate causes. Each cause needs the exact closed raw-review hash.
False confirmed requires reviewed_causes:[] and still needs the full receipt.
Suspected shared defects must have their exact raw-review ref in evidence_refs;
suspected/unclassified causes are never promoted by this tool.

The results schema requires shared_harness_defect.review:null when confirmed is
false; its explicit independent false receipt remains in the assembly manifest.
Equal_access.review always references the explicit receipt. The manifest retains
all 360 slots, terminal/config/event/inventory refs, original decisions, original
review artifact hashes and nested evidence/disagreement refs. Original artifact
files preserve all other diagnostic fields. Disagreements or alternative decisions
are retained and never selected as replacement scores by assembly. A changed
declared score still requires the independent adjudication workflow outside this
tool; hashes and declarations cannot authenticate review semantics or authorship.

Synthetic tests:
python3 -B -m unittest discover -s tools -p test_assemble_pilot_results.py -v
All synthetic fixtures live temporarily under tools and are removed afterward.
No actual packet assembly or all-attempt/raw/semantic review is substituted by
these synthetic tests. Root alone controls eventual private GitLab publication.
