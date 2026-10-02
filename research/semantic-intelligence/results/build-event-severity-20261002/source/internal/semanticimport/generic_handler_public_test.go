package semanticimport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
)

func TestPublicGenericHandler(t *testing.T) {
	stream, root := os.Getenv("MOEDEX_GENERIC_HANDLER_STREAM"), os.Getenv("MOEDEX_GENERIC_HANDLER_ROOT")
	if stream == "" || root == "" {
		t.Skip("set MOEDEX_GENERIC_HANDLER_STREAM/ROOT for native fixture")
	}
	raw, err := os.ReadFile(stream)
	if err != nil {
		t.Fatal(err)
	}
	a, err := semanticimport.Import(context.Background(), bytes.NewReader(raw), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "artifact")
	if err := semantic.Write(path, a); err != nil {
		t.Fatal(err)
	}
	a, err = semantic.Read(path, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, _ := os.ReadFile(path)
	sha := fmt.Sprintf("%x", sha256.Sum256(artifact))
	provenance := semanticindex.Provenance{ArtifactSHA256: sha, CorpusFingerprint: strings.Repeat("b", 64)}
	ip := filepath.Join(t.TempDir(), "index")
	if err := semanticindex.Build(ip, a, provenance); err != nil {
		t.Fatal(err)
	}
	x, err := semanticindex.Open(ip, provenance, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	p := &domainGoldProvider{index: x, artifact: sha}
	tools := map[string]mcp.ToolHandler{}
	for _, tool := range mcp.CompilerTools(p) {
		tools[tool.Name()] = tool
	}
	call := func(name string, args any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		out, err := tools[name].Call(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		if out["isError"] == true {
			t.Fatal(out)
		}
		schemaJSON, _ := json.Marshal(tools[name].Specification().OutputSchema)
		var schema jsonschema.Schema
		if err := json.Unmarshal(schemaJSON, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(out["structuredContent"])
		var schemaValue any
		json.Unmarshal(wire, &schemaValue)
		if err := resolved.Validate(schemaValue); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		var result map[string]any
		if json.Unmarshal(wire, &result) != nil {
			t.Fatal("bad response")
		}
		return result
	}
	declarations := call("compiler_symbols", map[string]any{"repo": "fixture", "path": "Fixture.cs"})
	if declarations["status"] != "ok" || declarations["truncated"] != false {
		t.Fatal(declarations)
	}
	// Every implementation survives artifact and index persistence and is
	// navigable through the public reverse implementation tool.
	symbols := map[string]semantic.Symbol{}
	for _, symbol := range a.Symbols {
		symbols[symbol.ID] = symbol
	}
	expected := map[string]int{}
	for _, binding := range a.Bindings {
		for _, fact := range binding.ImplementationFacts {
			expected[fact.InterfaceSymbolID]++
		}
	}
	closed := 0
	for _, binding := range a.Bindings {
		for _, fact := range binding.ImplementationFacts {
			if fact.Rule == "csharp-interface-closed-v2" {
				closed++
				if (binding.ExtractorVersion != "14" && (binding.ExtractorVersion != "15" && (binding.ExtractorVersion != "16" && (binding.ExtractorVersion != "17" && (binding.ExtractorVersion != "18" && (binding.ExtractorVersion != "19" && binding.ExtractorVersion != "20")))))) || symbols[fact.InterfaceSymbolID].Key.DescriptorKind != "constructed_interface_method_v2" {
					t.Fatal(binding, fact)
				}
			}
			result := call("compiler_implementations", map[string]any{"symbol_id": fact.InterfaceSymbolID})
			if result["status"] != "context_required" {
				t.Fatal(result)
			}
			contexts := result["contexts"].([]any)
			if len(contexts) != 1 {
				t.Fatal(result)
			}
			id := contexts[0].(map[string]any)["context_id"]
			selected := call("compiler_implementations", map[string]any{"symbol_id": fact.InterfaceSymbolID, "context_ids": []any{id}})
			if selected["status"] != "ok" || len(selected["matches"].([]any)) != expected[fact.InterfaceSymbolID] {
				t.Fatal(selected)
			}
		}
	}
	if closed != 3 || len(expected) != 3 {
		t.Fatalf("closed=%d distinct slots=%d", closed, len(expected))
	}
	// The compiled interface invocation must use the exact same closed key.
	calls := 0
	occurrences := map[string]semantic.Occurrence{}
	for _, o := range a.Occurrences {
		occurrences[o.ID] = o
	}
	for _, binding := range a.Bindings {
		if occurrences[binding.OccurrenceID].Role == "reference" && expected[binding.SymbolID] == 2 {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("closed interface calls=%d", calls)
	}
	if p.acquired != p.released {
		t.Fatal("lease leak")
	}
}
