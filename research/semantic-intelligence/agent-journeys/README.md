# Logged native MCP journeys

For new protocols, run native setup **before assignment**, in the same network
permission context the solver will use. This checks loopback access, negotiates
Moedex's stateless compatibility protocol, sends the initialized notification,
and retains the native instructions and complete catalog:

```sh
python3 research/semantic-intelligence/agent-journeys/prepare.py --setup-dir SETUP --run-dir RUN --endpoint http://127.0.0.1:19381/mcp --prompt PROMPT
python3 research/semantic-intelligence/agent-journeys/browse.py --run-dir RUN instructions
```

Both directories must be fresh. Inspect `SETUP/result.json` for `ready=true`
before assigning the solver or starting its full-assignment clock. Setup failure
retains the attempted exchange and error without creating a scored task. A
coordinator's successful network check does not grant a differently sandboxed
solver access: use the verified permission context from its first call. Never
reset or bootstrap an already assigned/stopped run. All setup exchanges are
separate, uncharged evidence; the first task call still starts normal accounting.
Require solvers to read every initialization-instruction page before tool use,
using the returned `next_offset` when needed. Record those displayed views along
with the other browser views. Freeze `prepare.py`, `browse.py`, `client.py`, the
setup artifacts and this onboarding policy before future scored execution.

For new scored runs, use `view.py` in place of direct `browse.py` commands.
After setup succeeds, the coordinator writes `RUN/assignment.json` containing
`deadline_monotonic` (assignment start plus 600 seconds, on the same host).
Use `journey_clock.monotonic()` from this directory to mint that value, and freeze
`journey_clock.py` alongside the client and browser scripts. On macOS Python 3.9,
the standard `time.monotonic()` origin is process-relative; persisted deadlines
and call-ledger elapsed times would otherwise reset in each new CLI process.
The shared helper uses the system uptime clock on that runtime, and the standard
system-wide monotonic clock on newer Python. Existing archives remain unchanged;
do not resume an old run under a different clock implementation.
The wrapper records exact stdout/stderr, arguments, timestamps and hashes under
`RUN/views`, including every initialization-instruction page. It rejects display
cap overrides and applies the remaining assignment deadline to each subprocess.
Freeze all five scripts. This logs supervised access; it does not provide OS
isolation or prevent a solver bypassing the wrapper.

Legacy coordinator setup (retained for old frozen protocols only):

```sh
python3 research/semantic-intelligence/agent-journeys/client.py init --run-dir RUN --endpoint http://127.0.0.1:19381/mcp --prompt PROMPT --catalog CATALOG
```

Solver commands:

```sh
python3 research/semantic-intelligence/agent-journeys/client.py list --run-dir RUN
python3 research/semantic-intelligence/agent-journeys/client.py call --run-dir RUN --name TOOL --arguments '{"query":"example"}'
```

During a scored task, only `tools/list` and `tools/call` reach the endpoint. Legacy setup copies the supplied prompt and initial public catalog; it does not access gold labels. The initial catalog is uncharged. Subsequent list/call attempts, including native errors, count against 24 calls and 131,072 complete serialized response bytes. Full crossing responses are retained and returned verbatim, followed by a separate stderr exhaustion notice; subsequent requests are blocked. An 8 MiB transport safety cap retains a prefix explicitly marked incomplete. Transport failures or interrupted accounting stop the run conservatively.

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

## Comparing independently collected native arms

[`compare.py`](compare.py) checks two arm records against one frozen corpus,
source manifest, prompt, rubric, protocol and budget contract. It verifies local
evidence hashes, accounting and review identities, preserves planned/assigned/
eligible denominators, and separates setup failures and unknown transport costs
from scores. Paired deltas use only mutually eligible tasks and never infer a win.
See [input format and interpretation](COMPARISON.md). This is an offline report
validator, not a service launcher, blind solver or replacement for independent
raw-evidence audits. Existing frozen results are not rewritten.

New runs default to immutable model/product provenance. An explicitly authorized
[`observed-service-v1` policy](COMPARISON.md#explicit-observed-service-provenance)
records the available service/model identities and reproducibility limits while
retaining isolation, budgets, raw capture and independent review. Reports keep
provenance separate from paired coverage and answer quality.
