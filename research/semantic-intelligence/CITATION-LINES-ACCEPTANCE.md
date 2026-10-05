# Numbered source presentation

The MCP text presentation now numbers source lines and directs agents to cite
the relevant lines. Structured source stays unnumbered and byte-preserving for
hash verification and compiler-offset discovery. See
[ADR0061](../../docs/adr/0061-numbered-source-presentation.md).

This addresses the manual counting exposed by the
[worker18 journey run](AGENT-JOURNEY-WORKER18-ACCEPTANCE.md): correct Worker
registrations were cited one line early or late. It also makes narrow citations
easier than citing an entire configuration block. It does not fill omitted facts
or establish a measured improvement in agent answers.

Validation: focused MCP/source-tool tests, the full Go suite, vet and focused race
tests pass. The public HTTP check reads Worker lines 60–78 and verifies explicit
labels for the scoped service at 62 and consumer pairing at 77. Search responses
carry numbered text with unchanged structured blocks. A complete source read
preserves the BOM-sensitive bytes used for compiler hash matching. Evidence is in
the scripted regression manifest (archival evidence maintained separately).

Numbering affects only readable text. `search_context(format="structured")`
continues to return a compact summary plus unnumbered structured blocks. Text
clients needing source bytes must use structured content rather than hashing or
copying numbered presentation. Source-token estimates exclude presentation and
protocol overhead. The new prefixes add some response bytes; compact display and
capability discovery remain separate work.

The previous 12-task evaluation, its answers, scores and binary are preserved.
This three-call scripted regression is not a new independent evaluation. No GPU
or dense retrieval is involved.
