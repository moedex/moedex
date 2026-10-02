package semanticimport

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func implementationStream(t *testing.T, change func(*record)) (string, string) {
	root, original := domainStream(t, nil)
	var rows []record
	for _, line := range strings.Split(strings.TrimSpace(original), "\n") {
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	var capture map[string]any
	if err := json.Unmarshal([]byte(rows[0].CaptureJSON), &capture); err != nil {
		t.Fatal(err)
	}
	capture["extractor_version"] = "6"
	raw, _ := json.Marshal(capture)
	rows[0].CaptureJSON = string(raw)
	rows[0].ExtractorVersion = "6"
	for i := range rows {
		rows[i].Context = digest(raw)
		if rows[i].Type == "summary" || rows[i].Type == "stream_summary" {
			rows[i].References = 0
			rows[i].Declarations = 1
		}
	}
	r := &rows[1]
	r.Type = "declaration"
	r.ReferenceKind = "declaration"
	r.DomainFacts = nil
	symbol := func(d string) *symbolRecord {
		return &symbolRecord{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A.csproj", Descriptor: d, DescriptorKind: "documentation_comment_id"}
	}
	r.Symbol = symbol("M:A.Run")
	r.ImplementationFacts = []implementationFactRecord{{Kind: "interface_method_implementation", Rule: "csharp-interface-v1", EvidenceScope: "compile_time", InterfaceSymbol: symbol("M:I.Run"), ImplementingType: symbol("T:A")}}
	if change != nil {
		change(r)
	}
	var out strings.Builder
	for _, row := range rows {
		b, _ := json.Marshal(row)
		out.Write(b)
		out.WriteByte('\n')
	}
	return root, out.String()
}
func TestImplementationStreamAdmission(t *testing.T) {
	root, stream := implementationStream(t, nil)
	a, err := Import(context.Background(), strings.NewReader(stream), Options{Repo: "repo", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Bindings) != 1 || len(a.Bindings[0].ImplementationFacts) != 1 || len(a.Symbols) != 3 {
		t.Fatal("implementation relationship not imported")
	}
	for name, change := range map[string]func(*record){
		"reference":     func(r *record) { r.Type = "reference" },
		"nil interface": func(r *record) { r.ImplementationFacts[0].InterfaceSymbol = nil },
		"nil type":      func(r *record) { r.ImplementationFacts[0].ImplementingType = nil },
		"wrong project": func(r *record) { r.ImplementationFacts[0].InterfaceSymbol.Namespace = "repo/Other.csproj" },
		"duplicate":     func(r *record) { r.ImplementationFacts = append(r.ImplementationFacts, r.ImplementationFacts[0]) },
		"overflow": func(r *record) {
			f := r.ImplementationFacts[0]
			r.ImplementationFacts = make([]implementationFactRecord, 33)
			for i := range r.ImplementationFacts {
				r.ImplementationFacts[i] = f
			}
		},
		"wrong owner": func(r *record) { r.ImplementationFacts[0].ImplementingType.Descriptor = "T:B" },
		"unresolved":  func(r *record) { r.BindingStatus = "unresolved"; r.Symbol = nil },
		"future rule": func(r *record) { r.ImplementationFacts[0].Rule = "future" },
	} {
		t.Run(name, func(t *testing.T) {
			root, stream := implementationStream(t, change)
			if _, err := Import(context.Background(), strings.NewReader(stream), Options{Repo: "repo", Root: root}); err == nil {
				t.Fatal("admitted invalid implementation facts")
			}
		})
	}
}
func TestRealImplementationStream(t *testing.T) {
	path, root := os.Getenv("MOEDEX_IMPLEMENTATION_STREAM"), os.Getenv("MOEDEX_IMPLEMENTATION_ROOT")
	if path == "" || root == "" {
		t.Skip("set MOEDEX_IMPLEMENTATION_STREAM and MOEDEX_IMPLEMENTATION_ROOT")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a, err := Import(context.Background(), f, Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, b := range a.Bindings {
		total += len(b.ImplementationFacts)
	}
	if total != 6 {
		t.Fatalf("implementation relations=%d want6", total)
	}
}
