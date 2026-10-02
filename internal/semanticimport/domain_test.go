package semanticimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These synthetic streams test admission, not framework recognition. The
// separate real-worker domain gate verifies recognition from source and metadata.
func domainStream(t *testing.T, change func(*record)) (string, string) {
	t.Helper()
	root := t.TempDir()
	raw := []byte("class A {}")
	if err := os.WriteFile(filepath.Join(root, "A.cs"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	src := sourceRecord{Path: "A.cs", SHA: digest(raw), ByteSize: uint64(len(raw))}
	cap, _ := json.Marshal(map[string]any{"project": "A.csproj", "repo": "repo", "extractor": "msbuild-roslyn", "extractor_version": "2", "compiler_version": "5.0", "sources": []sourceRecord{src}})
	ctx := digest(cap)
	project := record{Schema: WorkerSchema, Type: "project", Repo: "repo", Project: "A.csproj", Context: ctx, CaptureJSON: string(cap), Status: "complete", Extractor: "msbuild-roslyn", ExtractorVersion: "2", CompilerVersion: "5.0", Sources: []sourceRecord{src}}
	api := symbolRecord{Language: "csharp", NamespaceKind: "assembly", Namespace: "MassTransit.Abstractions, Version=8.3.6.0", Descriptor: "T:MassTransit.IConsumer`1", DescriptorKind: "documentation_comment_id"}
	target := symbolRecord{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A.csproj", Descriptor: "T:Contract", DescriptorKind: "documentation_comment_id"}
	fact := record{Schema: WorkerSchema, Type: "reference", Project: project.Project, Context: ctx, SourcePath: src.Path, SourceSHA: src.SHA, Span: spanRecord{Offset: 6, Length: 1}, SourceText: "A", BindingStatus: "resolved", ReferenceKind: "name", BindingMethod: "roslyn-semantic-model", Symbol: &api, DomainFacts: []domainFactRecord{{Kind: "message_consumer", Rule: "csharp-framework-v1", EvidenceScope: "compile_time", Targets: []domainTargetRecord{{Role: "message", Symbol: &target}}}}}
	if change != nil {
		change(&fact)
	}
	refs, decls := 1, 0
	if fact.Type == "declaration" {
		refs, decls = 0, 1
	}
	rows := []record{project, fact, {Schema: WorkerSchema, Type: "summary", Project: project.Project, Context: ctx, Status: "complete", References: refs, Declarations: decls}, {Schema: WorkerSchema, Type: "stream_summary", Projects: 1, Status: "complete", References: refs, Declarations: decls}}
	var out strings.Builder
	for _, r := range rows {
		b, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	return root, out.String()
}
func TestImportDomainFactsAndRejectContradictions(t *testing.T) {
	root, stream := domainStream(t, nil)
	a, err := Import(context.Background(), strings.NewReader(stream), Options{Repo: "repo", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Bindings) != 1 || len(a.Bindings[0].DomainFacts) != 1 || len(a.Symbols) != 2 {
		t.Fatal("domain target not imported")
	}
	mutations := map[string]func(*record){
		"nil target":           func(r *record) { r.DomainFacts[0].Targets[0].Symbol = nil },
		"wrong project":        func(r *record) { r.DomainFacts[0].Targets[0].Symbol.Namespace = "repo/Other.csproj" },
		"source API lookalike": func(r *record) { r.Symbol.NamespaceKind = "project"; r.Symbol.Namespace = "repo/A.csproj" },
		"unresolved":           func(r *record) { r.BindingStatus = "unresolved"; r.Symbol = nil },
		"wrong API":            func(r *record) { r.Symbol.Descriptor = "T:Other.IConsumer`1" },
		"generic target":       func(r *record) { r.DomainFacts[0].Targets[0].Symbol.Descriptor = "T:Contract`1" },
		"declaration":          func(r *record) { r.Type = "declaration"; r.ReferenceKind = "declaration" },
		"unknown rule":         func(r *record) { r.DomainFacts[0].Rule = "future" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root, stream := domainStream(t, mutate)
			if _, e := Import(context.Background(), strings.NewReader(stream), Options{Repo: "repo", Root: root}); e == nil {
				t.Fatal("accepted contradictory domain evidence")
			}
		})
	}
}
