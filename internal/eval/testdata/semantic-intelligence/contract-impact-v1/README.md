# Independent contract-impact fixture

Six projects compile against real MassTransit.Abstractions 8.3.6 and EF Core
Relational 10.0.0 metadata using the .NET 10 SDK. `Aggregate` references the other
five projects. The fixture executes no messaging or database operations.

`Contracts` defines `Shared.Notice`. Publisher, Consumer, and Storage produce four
recorded facts about that exact project-qualified type: publish, consumer, entity,
and table mapping. Distractor defines another `Shared.Notice` in a different
project and produces its own publish fact. Equal names must never join them.

Debug and Release capture the same project graph separately. Publisher's
conditional source has distinct publish anchors in each configuration. The
source-to-MCP gate uses production semantic composition by exact ID; both
contexts remain independently discoverable. It rejects selecting both variants
of one project, while explicit distinct-project contexts return separate facts.

```sh
python3 research/semantic-intelligence/check-contract-impact.py \
  --dotnet /path/dotnet --sdk-path /path/sdk/10.0.100 \
  --worker /path/Moedex.SemanticWorker.dll --packages /scratch/public-packages \
  --output-dir /scratch/contract-gold
MOEDEX_CONTRACT_CAPTURE_DIR=/scratch/contract-gold \
  go test ./internal/semanticimport -run TestPublicContractImpact -count=1 -v
```

The Go gate persists/reopens the artifact and mapped index, then queries production
MCP handlers. Eleven queries check exact identities, source hashes and spans,
owners, table/schema values, context alternatives, scoped results, negative
same-name joins, truncation, and invalid scopes. Cross-project results are compiler
observations, not evidence that a deployment delivers messages or shares tables.

For full snapshot publication, set `MOEDEX_CONTRACT_CAPTURE_DIR` and a fresh
`MOEDEX_COMPOSITION_OUTPUT`, then run `TestPublicComposedContractPublication` in
`internal/app/indexcmd`. It composes the captures through the production app API,
builds a lexical snapshot, and attaches the artifact with explicit retained-root
mappings. `TestPublicComposedContractLeasedMCP` in `internal/app/servecmd` opens that
published `CURRENT` through the real serving holder and checks context choices,
scoped paths, conflicting variants, and manifest-bound snapshot/artifact identity.
