# Semantic intelligence delivery log

## Offline dependency capture and bounded graph selectors

Graph call tracing accepts exact repository and path selectors. Journey clients
share a portable monotonic clock. Compiler capture uses the verified staged
package bundle as a local NuGet feed, allowing floating versions to resolve
without ambient package sources. Synthetic regression tests cover unavailable
package versions and preserve bundle/source verification.

Earlier compiler work adds capture isolation, context-aware semantic indexes,
source-linked observations and bounded MCP retrieval. Public source fixtures and
colocated tests document those implementation contracts. Corpus-specific
measurements and operational evidence are maintained separately from this code.
