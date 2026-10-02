# Compiler domain gold fixture

This independently authored fixture binds real framework metadata:

- .NET 10.0.0 `Microsoft.AspNetCore.App` reference pack supplies DI.
- `MassTransit.Abstractions` 8.3.6 supplies messaging interfaces.
- `Microsoft.EntityFrameworkCore.Relational` 10.0.0 supplies EF/table APIs.

`gold.json` specifies 26 source anchors: 24 in a complete project, including
10 positive facts, and two in a deliberately incomplete project. Negative cases
cover lookalike APIs, dynamic/unresolved calls, excluded generic targets,
factory/instance/open-generic DI, nonconstant table names, and invalid scalar
values. The consumer's enclosing type must remain `DomainGold.Consumer`.

The source uses marker comments only to locate exact reference spans. Expected
facts are authored independently of extraction. The harness checks every emitted
fact, including the absence of unmarked extras. It does not execute registrations,
publish messages, activate consumers, or connect to a database.

Build a disposable worker copy, then run from the repository:

```sh
python3 research/semantic-intelligence/check-domain.py \
  --dotnet /path/to/dotnet --sdk-path /path/to/sdk/10.0.100 \
  --worker /path/to/Moedex.SemanticWorker.dll --output-dir /scratch/domain-gold
MOEDEX_DOMAIN_STREAM=/scratch/domain-gold/complete.jsonl \
MOEDEX_DOMAIN_ROOT=/scratch/domain-gold/fixture \
  go test ./internal/semanticimport -run TestPublicDomain -count=1 -v
```

The harness restores exact package pins into isolated storage and records package,
source, gold, and worker checksums. The Go gate imports the capture, writes and
reopens the semantic artifact and mapped query index, checks public MCP bindings
and target definitions, rejects forged domain claims, and rejects serving an
incomplete context. MSBuild workspace loading requires local IPC permissions.
