# Logged native MCP journeys

Coordinator setup (fresh directory per task):

```sh
python3 research/semantic-intelligence/agent-journeys/client.py init --run-dir RUN --endpoint http://127.0.0.1:19381/mcp --prompt PROMPT --catalog CATALOG
```

Solver commands:

```sh
python3 research/semantic-intelligence/agent-journeys/client.py list --run-dir RUN
python3 research/semantic-intelligence/agent-journeys/client.py call --run-dir RUN --name TOOL --arguments '{"query":"example"}'
```

Only `tools/list` and `tools/call` reach the endpoint. Setup copies the supplied prompt and initial public catalog; it does not access gold labels. The initial catalog is uncharged. Subsequent list/call attempts, including native errors, count against 24 calls and 131,072 complete serialized response bytes. Full crossing responses are retained and returned verbatim, followed by a separate stderr exhaustion notice; subsequent requests are blocked. An 8 MiB transport safety cap retains a prefix explicitly marked incomplete. Transport failures or interrupted accounting stop the run conservatively.

A file lock serializes reservation, network request, response retention, and atomic accounting. Raw requests/responses and a JSONL ledger remain in the run directory. The 600-second client clock starts at first interaction and includes time between calls. The coordinator must separately record solver spawn and final-answer times and enforce the full solver deadline, including time before the first call. Local JSON/argument validation errors are separate ledger events, not MCP calls; argparse-level invocation errors must be inventoried by the coordinator.

Token usage is **unknown**. Host filesystem isolation is **not enforced**: this wrapper does not prevent a solver from reading the workspace, editing accounting files, or bypassing the endpoint wrapper. The coordinator must disclose that limitation and inspect the solver transcript for violations; these are supervised trials, not sandboxed evaluations.

Tests: `python3 -B -m unittest discover -s research/semantic-intelligence/agent-journeys -p 'test_*.py'`.

## Bounded browser for future runs

`browse.py` wraps the unchanged `client.py` accounting engine. Initial catalog
browsing and rereading retained responses are local operations. Each new call
still charges the complete wire response and persists the original bytes before
displaying anything. Freeze both scripts for future evaluations; old runs and
scores retain their original presentation protocol.

```sh
python3 research/semantic-intelligence/agent-journeys/browse.py --run-dir RUN catalog
python3 research/semantic-intelligence/agent-journeys/browse.py --run-dir RUN catalog --tool compiler_symbols
python3 research/semantic-intelligence/agent-journeys/browse.py --run-dir RUN call --name list_repos --arguments '{}'
python3 research/semantic-intelligence/agent-journeys/browse.py --run-dir RUN --pointer /result/structuredContent response --ordinal 1
python3 research/semantic-intelligence/agent-journeys/browse.py --run-dir RUN --pointer /result/structuredContent --offset 7000 response --ordinal 1
```

Output is at most 8,192 bytes including its JSON envelope and newline by default;
`--max-bytes` permits 2,048–16,384. Place presentation options before the subcommand.
The catalog index contains names only; `--tool` opens a complete descriptor,
including input and output schemas. `--pointer` uses JSON Pointer syntax (`~1`
for slash, `~0` for tilde). Every page identifies the raw artifact SHA-256 and size.
Use the returned `next_offset` with the same tool/pointer to continue; never guess
an offset. Concatenate decoded `data` strings and JSON-decode once to recover the
selected value exactly. `partial` describes the selected value; a completed
selection does not imply the rest of the response has been viewed.

A successful local view says nothing about MCP semantic success: inspect the
response's `isError`, error/status fields and provenance. The `call` view includes
transport completeness, budget use and stop reason. Full raw artifacts and the
ledger remain authoritative. Views do not reset the wall clock or authorize
viewing after the coordinator's solver deadline. The coordinator must retain the
solver's tool transcript to audit which views were actually displayed; the browser
is read-only for local views and does not add a separate view ledger. Full response
retention is distinct from full solver inspection. This is a presentation helper,
not a change to the MCP server or an enforced filesystem boundary.
