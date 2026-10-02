# CleanArchitecture holdout preparation

Subsequent status: the [completed source-only holdout run](HOLDOUT-CLEANARCHITECTURE-ACCEPTANCE.md)
retains a compiler setup failure and one transport-blocked assignment. Historical
preparation details below and their immutable archives remain unchanged.

The subsequent [environment preparation](HOLDOUT-CLEANARCHITECTURE-ENVIRONMENT.md)
installs the exact SDK, verifies build/offline restore and stages the source-review
assignment. Independent review and product execution remain pending. The original
source packet below and its archive retain their creation-time status.

2026-10-02. Prepared source packet; no Moedex queries, capture, solver execution
or competitive result on this repository yet.

Repository: <https://github.com/jasontaylordev/CleanArchitecture>, pinned at
`5353a9edae000d576eade1a4f2c0d72d3b1c1785`. The initial repository selection was
based on its public application structure and MediatR/validation stack; a search
of existing evaluation docs/protocols found no prior use. This does not establish
absence from model training or every historical session. The local checkout
remains under `.local/holdout-cleanarchitecture/source`.

The [source packet](results/holdout-cleanarchitecture-20261002/result.json)
contains hashes for all 258 tracked files, six prompts, and 30 source-derived
scoring atoms. Two tasks locate definitions/configuration, two trace behavior,
and two assess change impact. Each atom includes exact line ranges, excerpts,
and raw source hashes. The coordinator authored this oracle without product
output; independent review is **pending**. It is not an independently validated
gold set yet. Negative claims are scoped to cited files and distinguish configured
behavior from executed behavior.

The task mix covers title validation, database/interceptor registration,
completion events, validation error handling, Done semantics and title uniqueness.
Keep each task intact if product support is incomplete. Freeze any independent
oracle corrections before product queries. The six prompts and 30 atoms are a
bounded holdout, not evidence of whole-repository coverage or a large-corpus
resource test; the separate Roslyn resource gate remains applicable.

Next execution steps:

1. Independently review the source oracle without querying either product. Retain
   amendments and freeze the final rubric before capture/querying.
2. Resolve capture prerequisites. The pinned `global.json` requires SDK10.0.401;
   the existing SDK10.0.100 does not satisfy it. Record template build variants,
   dependencies, complete project contexts and binary hashes. Do not edit the
   pinned sources or substitute a friendlier commit after seeing failures.
3. Freeze native catalogs and both browser/accounting scripts. For each task use
   a fresh solver, 24 attempted calls, 131,072 complete response bytes and a
   600-second full assignment deadline. Retain displayed-page transcripts as well
   as complete wire records; allow local page reads within the same deadline.
4. Run matching CodeGraph tasks against the same source and rubric if its setup
   succeeds. Report arm setup failures explicitly. Independently review answers
   and preserve disagreements; do not pool with the development corpus.

Current work remains single-threaded. No independent agents were launched.
The next independent review/solver phase needs an explicit delegation instruction
under the user's current no-subagents constraint. Meanwhile, capture environment
preparation and paired-arm setup can proceed without exposing product answers.
