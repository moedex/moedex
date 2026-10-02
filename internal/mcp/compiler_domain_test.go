package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
)

func TestCompilerBindingDomainFactsCarryDescriptorAndProvenance(t *testing.T) {
	raw := strings.Repeat("a", 64)
	source := semantic.Source{ID: "source", Path: "file.cs", RawSHA256: raw}
	context := semantic.BuildContext{ID: "selected", Project: "Project.csproj"}
	snapshot := semantic.SourceSnapshot{ID: "source-snapshot", Repo: "repo"}
	target := semantic.Symbol{ID: "symbol:target", Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/Project", DescriptorKind: "documentation_comment_id", Descriptor: "T:Orders.OrderPlaced"}}
	fact := semantic.DomainFact{Kind: "message_publish", Rule: "csharp-framework-v1", EvidenceScope: "compile_time", Targets: []semantic.DomainTarget{{Role: "message", SymbolID: target.ID}}}
	reader := &compilerReaderFixture{contexts: []semanticindex.SourceContext{{Source: source, Context: context, Snapshot: snapshot}}, query: semanticindex.QueryResult{Results: []semanticindex.Result{{Source: source, Context: context, Snapshot: snapshot, Occurrence: semantic.Occurrence{ID: "occurrence", Offset: 42, Length: 7, Role: "reference", Kind: "invocation"}, Binding: semantic.Binding{Status: "resolved", Method: "roslyn-semantic-model", Extractor: "msbuild-roslyn", ExtractorVersion: "2", DomainFacts: []semantic.DomainFact{fact}}, DomainSymbols: []semantic.Symbol{target}}}}}
	provider := &compilerProviderFixture{reader: reader}
	tool := CompilerTools(provider)[0]
	out := compilerPayload(t, tool, `{"repo":"repo","path":"file.cs","byte_offset":42,"context_id":"selected","raw_sha256":"`+raw+`"}`)
	if len(out.Results) != 1 || len(out.Results[0].DomainFacts) != 1 {
		t.Fatalf("missing facts: %+v", out)
	}
	result := out.Results[0]
	got := result.DomainFacts[0]
	if got.Kind != fact.Kind || got.Rule != fact.Rule || got.EvidenceScope != "compile_time" || len(got.Targets) != 1 || got.Targets[0].Symbol.Descriptor != target.Key.Descriptor {
		t.Fatalf("domain fact changed: %+v", got)
	}
	if result.ContextID != context.ID || result.RawSHA256 != raw || result.SourceSnapshotID != snapshot.ID || result.ByteOffset != 42 || out.ArtifactSHA256 != "audit" {
		t.Fatalf("missing provenance: %+v", out)
	}
	if !strings.Contains(tool.Specification().Description, "not runtime") {
		t.Fatal("missing evidence qualification")
	}
	// Legacy records do not gain empty domain arrays or altered wire fields.
	legacy, err := compilerBinding(semanticindex.Result{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "domain_facts") {
		t.Fatal("empty optional facts serialized")
	}
}
