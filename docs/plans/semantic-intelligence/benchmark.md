# Semantic intelligence benchmark, version 1

The first gate measures source extraction through production MCP, rather than
the correctness of traversing an adjacency list constructed by the test.
`internal/eval/testdata/semantic-intelligence/v1/tasks.json` contains questions,
positive relationship facts, negative same-name distractors, and exact source
evidence. Its labels were authored from the fixtures, independently of either
engine's output. They still need independent human review before publishing a
competitive comparison.

Run:

```sh
go test ./internal/eval -run 'TestSemantic' -count=1
go test ./internal/eval -run '^$' -bench '^BenchmarkSemanticExtractionMCP$' -benchtime=3x
```

The test ingests real source bytes with Git blob identities, runs `BuildGraph`,
opens the persisted graph, and invokes HTTP MCP tools. It checks inheritance and
dependency injection with cross-repository homonyms; source-authored byte spans;
Pattern versus Verified filtering; and graph snapshot identity. A mutation
removes inheritance, DI registration, a declaration, and an entire distractor
file. `RefreshGraph` must remove stale facts and produce the same returned edges
and evidence as a clean rebuild. No edges are inserted by the test.
Both updated graphs must also retain a separately labeled interface-implementation
fact, preventing vacuous success from two empty results. Explicit Candidate
queries must retain the named diagnostic distractors at that tier.

The microbenchmark times full graph extraction, opening the result, and two MCP
queries over an already ingested tiny shard, including validation and allocations.
It excludes ingestion and does not represent fleet throughput. Compiler/LSP
resolution, protocol links, real project restoration, and competitor execution
are not measured. The included .NET project files make the C# fixture suitable
for a future compiler adapter; the Go gate does not build those projects.

## Paired execution contract

Both adapters must receive identical repository bytes and the same versioned
questions and labels. The Moedex adapter currently maps `hierarchy` to
`trace_hierarchy` and `dependencies` to `graph_neighbors`, using one-hop queries.
A future CodeGraph adapter must translate qualified repository/path/symbol
anchors to discovered node IDs and preserve ambiguity. It must normalize results
to source/relationship/target/evidence facts without learning answers from gold
labels. Capture each engine's version, configuration, extraction coverage,
corpus digest, index duration, query duration, and complete native responses.

Score task fact recall, distractor leakage, unsupported assertions, and exact
evidence separately. An empty response must not pass through perfect precision:
required facts remain missing. Do not conflate these with endpoint latency or
declare a winner from this two-task smoke suite. Confidence names differ across
engines: compare provenance and observed precision, not ordinal labels alone.
The current Pattern/Verified assertions are specifically Moedex's syntax-only
contract. Keep Candidate diagnostic results separate from accepted answers.
This gate checks explicit positive facts and named distractors; it does not
label every possible returned edge or measure complete graph precision.

For agent-task comparisons, run the same model, prompt, machine/corpus, available
tools, and budgets against each adapter, recording tool calls, elapsed time,
input/output tokens, and answer evidence. Prespecify these budgets before a run;
report tasks that time out or lack extractor coverage as such. Add reviewed real
tasks for calls, overloaded symbols, interface dispatch, HTTP/message/database
paths, change impact, and repeated delete/rename/move updates before making
claims about broader code intelligence. The operational hub, memory, generated
documentation, and dashboard require separate product evaluations.

## Label maintenance

The strict Go loader rejects unsupported versions, unknown fields, duplicate
task IDs, contradictory facts, invalid endpoints, missing positive evidence,
and stale/ambiguous evidence spans. Change the suite version when task semantics
or judging rules change. Never generate gold answers from actual graph edges;
review new labels from source and record their provenance first.
Endpoint validation checks that the named file and symbol text exist; it is a
syntactic anchor check, not compiler validation of a declaration or binding.
