package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
)

// This authored protocol fixture tests overlapping occurrence transport. Actual
// Roslyn class/record/struct extraction is covered by test_worker.py.
func TestSemanticPrimaryConstructorImportAndDefinitions(t *testing.T) {
	root := t.TempDir()
	raw := []byte("class Primary(int n) { static Primary Make() => new Primary(1); }")
	if err := os.WriteFile(filepath.Join(root, "Primary.cs"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sources := []map[string]any{{"path": "Primary.cs", "sha256": semanticTestSHA(raw), "byte_size": len(raw)}}
	capture, _ := json.Marshal(map[string]any{"project": "Primary.csproj", "sources": sources})
	buildContext := semanticTestSHA(capture)
	base := func(kind string) map[string]any {
		return map[string]any{"schema": semanticimport.WorkerSchema, "record_type": kind, "project": "Primary.csproj", "build_context": buildContext}
	}
	header := base("project")
	header["repo"], header["capture_json"], header["sources"] = "fixture", string(capture), sources
	header["extractor"], header["extractor_version"], header["compiler_version"] = "authored-wire-test", "1", "test"
	header["compilation_status"] = "complete"
	rows := []map[string]any{header}
	for _, entry := range []struct {
		role, kind, descriptor string
		offset                 int
	}{
		{"declaration", "declaration", "T:Primary", bytes.Index(raw, []byte("Primary"))},
		{"declaration", "constructor_declaration", "M:Primary.#ctor(System.Int32)", bytes.Index(raw, []byte("Primary"))},
		{"reference", "constructor", "M:Primary.#ctor(System.Int32)", bytes.Index(raw, []byte("new Primary")) + 4},
	} {
		r := base(entry.role)
		r["source_path"], r["source_sha256"], r["source_text"] = "Primary.cs", semanticTestSHA(raw), "Primary"
		r["span"] = map[string]int{"byte_offset": entry.offset, "byte_length": 7}
		r["reference_kind"], r["binding_status"], r["binding_method"] = entry.kind, "resolved", "compiler"
		r["symbol"] = map[string]any{"language": "csharp", "namespace_kind": "project", "namespace": "fixture/Primary.csproj", "descriptor": entry.descriptor, "descriptor_kind": "documentation_comment_id"}
		rows = append(rows, r)
	}
	summary := base("summary")
	summary["compilation_status"], summary["declarations"], summary["references"] = "complete", 2, 1
	rows = append(rows, summary, map[string]any{"schema": semanticimport.WorkerSchema, "record_type": "stream_summary", "projects": 1, "compilation_status": "complete", "declarations": 2, "references": 1})
	a, err := semanticimport.Import(context.Background(), bytes.NewReader(encodeSemanticWire(t, rows)), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	x := semanticQueryIndex(t, a)
	refs, err := x.Bindings(context.Background(), semanticindex.Position{Repo: "fixture", Path: "Primary.cs", Offset: uint64(bytes.Index(raw, []byte("new Primary")) + 4)}, 100)
	if err != nil || len(refs.Results) != 1 {
		t.Fatalf("constructor call: %#v %v", refs, err)
	}
	defs, err := x.Definitions(context.Background(), refs.Results[0].Binding.SymbolID, semanticindex.Filter{}, 100)
	if err != nil || len(defs.Results) != 1 || defs.Results[0].Occurrence.Kind != "constructor_declaration" || defs.Results[0].Occurrence.Offset != 6 {
		t.Fatalf("constructor declaration: %#v %v", defs, err)
	}
}
