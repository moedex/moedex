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

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
)

func TestPublicDefaultForwarding(t *testing.T) {
	stream, root := os.Getenv("MOEDEX_FORWARD_STREAM"), os.Getenv("MOEDEX_FORWARD_ROOT")
	if stream == "" || root == "" {
		t.Skip("set MOEDEX_FORWARD_STREAM/ROOT for native fixture")
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
		wire, _ := json.Marshal(out["structuredContent"])
		var result map[string]any
		if json.Unmarshal(wire, &result) != nil {
			t.Fatal("bad response")
		}
		return result
	}

	declarations := call("compiler_symbols", map[string]any{"repo": "fixture", "path": "Fixture.cs", "query": "T:"})
	if declarations["status"] != "ok" || declarations["truncated"] != false {
		t.Fatalf("symbol discovery status=%v truncated=%v", declarations["status"], declarations["truncated"])
	}
	forwarded, selectionOnly := 0, 0
	for _, row := range declarations["results"].([]any) {
		raw, _ := json.Marshal(row)
		var b mcp.CompilerBinding
		json.Unmarshal(raw, &b)
		for _, f := range b.ImplementationFacts {
			if f.Kind != "interface_default_selection" {
				continue
			}
			if f.Forwarding == nil {
				selectionOnly++
				continue
			}
			forwarded++
			v := f.Forwarding

			contexts := []string{b.ContextID}
			if v.CallContextID != b.ContextID {
				contexts = append(contexts, v.CallContextID)
			}
			compact := call("compiler_evidence_path", map[string]any{"symbol_id": b.Symbol.ID, "context_ids": contexts})
			if compact["status"] != "ok" || compact["truncated"] != false || len(compact["required_context_ids"].([]any)) != 0 {
				t.Fatal(compact)
			}
			found := map[string]bool{}
			for _, row := range compact["records"].([]any) {
				r := row.(map[string]any)
				source := r["source"].(map[string]any)
				found[source["occurrence_id"].(string)] = true
			}
			if !found[b.OccurrenceID] || !found[v.CallOccurrenceID] {
				t.Fatal("compact bundle lost class or witness")
			}
			site := call("compiler_binding_at", map[string]any{"repo": "fixture", "path": v.CallPath, "byte_offset": v.CallOffset, "context_id": v.CallContextID, "raw_sha256": v.CallSHA256})
			if site["status"] != "ok" || len(site["results"].([]any)) != 1 {
				t.Fatal(site)
			}
			witness := site["results"].([]any)[0].(map[string]any)
			if witness["occurrence_id"] != v.CallOccurrenceID || witness["source_id"] != v.CallSourceID || witness["enclosing_symbol_id"] != f.DefaultTemplateSymbolID {
				t.Fatal(witness)
			}
			target := call("compiler_definitions", map[string]any{"symbol_id": v.ImplementationSymbolID, "context_id": b.ContextID})
			if target["status"] != "ok" || len(target["results"].([]any)) != 1 {
				t.Fatal(target)
			}
			raw, _ = json.Marshal(target["results"].([]any)[0])
			var method mcp.CompilerBinding
			json.Unmarshal(raw, &method)
			if len(method.ImplementationFacts) != 1 || method.ImplementationFacts[0].InterfaceSymbolID != v.InterfaceSymbolID || method.ImplementationFacts[0].ImplementingTypeSymbolID != f.ImplementingTypeSymbolID {
				t.Fatal(method)
			}
		}
	}
	if forwarded != 5 || selectionOnly != 10 {
		t.Fatalf("forwarded=%d selectionOnly=%d", forwarded, selectionOnly)
	}
	// Keep v5's checksum valid while corrupting only a call-source witness.
	// Open must cross-check the captured call, not merely trust well-shaped JSON.
	indexBytes, err := os.ReadFile(ip)
	if err != nil {
		t.Fatal(err)
	}
	var callSHA string
	for _, binding := range a.Bindings {
		for _, fact := range binding.ImplementationFacts {
			if fact.Forwarding != nil {
				callSHA = fact.Forwarding.CallSHA256
			}
		}
	}
	needle := []byte(`"call_sha256":"` + callSHA + `"`)
	if !bytes.Contains(indexBytes, needle) {
		t.Fatal("missing stored forwarding payload")
	}
	damaged := bytes.Replace(indexBytes, needle, []byte(`"call_sha256":"`+strings.Repeat("f", 64)+`"`), 1)
	checksum := sha256.Sum256(damaged[400:])
	copy(damaged[24:56], checksum[:])
	badPath := filepath.Join(t.TempDir(), "bad-index")
	if err := os.WriteFile(badPath, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if bad, err := semanticindex.Open(badPath, provenance, semanticindex.Limits{}); err == nil {
		bad.Close()
		t.Fatal("corrupt forwarding source witness admitted")
	}
	// A self-consistent locator for an absent call must fail artifact admission.
	for i := range a.Bindings {
		b := &a.Bindings[i]
		for j := range b.ImplementationFacts {
			f := &b.ImplementationFacts[j]
			if f.Forwarding == nil {
				continue
			}
			v := f.Forwarding

			oldOffset, oldID := v.CallOffset, v.CallOccurrenceID
			v.CallOffset++
			o := semantic.Occurrence{SourceID: v.CallSourceID, ContextID: v.CallContextID, Role: "reference", Kind: "invocation", Offset: v.CallOffset, Length: v.CallLength}
			v.CallOccurrenceID = o.ComputeID()
			b.ID = b.ComputeID()
			if a.Validate() == nil {
				t.Fatal("invented call witness admitted")
			}
			v.CallOffset, v.CallOccurrenceID = oldOffset, oldID
			b.ID = b.ComputeID()
		}
	}
	if p.acquired != p.released {
		t.Fatal("lease leak")
	}
}
