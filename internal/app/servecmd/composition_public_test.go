package servecmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/snapshot"
)

// Consume a CURRENT manifest published by the production index command, open
// the actual immutable serving holder, and dispatch through its leased provider.
func TestPublicComposedContractLeasedMCP(t *testing.T) {
	base := os.Getenv("MOEDEX_COMPOSITION_OUTPUT")
	if base == "" {
		t.Skip("set published real composition output")
	}
	raw, err := os.ReadFile(filepath.Join(base, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Symbol      string            `json:"symbol_id"`
		Contexts    map[string]string `json:"contexts"`
		Snapshot    string            `json:"snapshot_id"`
		Fingerprint string            `json:"corpus_fingerprint"`
	}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	resolved, err := snapshot.Resolve(filepath.Join(base, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Manifest.CorpusFingerprint != expected.Fingerprint {
		t.Fatal("published corpus fingerprint changed")
	}
	audit, ok := resolved.Manifest.Components[snapshot.SemanticComponent]
	if !ok || len(audit.Artifacts) != 1 {
		t.Fatal("missing manifest audit identity")
	}
	artifactSHA := audit.Artifacts[0].SHA256
	holder := compilerLeaseHolder(t, resolved)
	session, err := holder.AcquireCompilerSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.Release != nil {
		defer session.Release()
	}
	if session.CorpusFingerprint != expected.Fingerprint || session.ArtifactSHA256 != artifactSHA || session.SnapshotID != expected.Snapshot {
		t.Fatal("leased manifest identity differs")
	}

	var tool mcp.ToolHandler
	for _, candidate := range mcp.CompilerTools(holder) {
		if candidate.Name() == "compiler_contract_impact" {
			tool = candidate
		}
	}
	if tool == nil {
		t.Fatal("missing contract tool")
	}
	invoke := func(name string, contexts []string) mcp.CompilerContractResult {
		args := map[string]any{"symbol_id": expected.Symbol, "limit": 100}
		if contexts != nil {
			args["context_ids"] = contexts
		}
		raw, _ := json.Marshal(args)
		result, err := tool.Call(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(result["structuredContent"])
		if err != nil {
			t.Fatal(err)
		}
		var out mcp.CompilerContractResult
		if err := json.Unmarshal(wire, &out); err != nil {
			t.Fatal(err)
		}
		if out.SnapshotID != expected.Snapshot || out.ArtifactSHA256 != artifactSHA || out.EvidenceScope != "compile_time" {
			t.Fatalf("lease provenance missing: %s", wire)
		}
		t.Logf("leased_mcp=%s snapshot=%s paths=%d status=%s structured_sha256=%x", name, out.SnapshotID, len(out.Paths), out.Status, sha256.Sum256(wire))
		return out
	}
	discover := invoke("discovery", nil)
	if discover.Status != "context_required" || len(discover.Contexts) != 6 || len(discover.Paths) != 0 {
		t.Fatalf("discovery %+v", discover)
	}
	selected := []string{expected.Contexts["Debug:Publisher/Publisher.csproj"], expected.Contexts["Debug:Consumer/Consumer.csproj"], expected.Contexts["Debug:Storage/Storage.csproj"]}
	got := invoke("selected", selected)
	if got.Status != "ok" || len(got.Paths) != 4 || got.Truncated || !got.TotalIsExact {
		t.Fatalf("selected %+v", got)
	}
	selectedIDs := map[string]bool{}
	for _, id := range selected {
		selectedIDs[id] = true
	}
	for _, p := range got.Paths {
		if p.Contract.ID != expected.Symbol || !selectedIDs[p.Source.ContextID] || p.Source.RawSHA256 == "" || p.Owner == nil {
			t.Fatal("path identity missing")
		}
	}
	conflict := invoke("conflicting-variants", []string{expected.Contexts["Debug:Publisher/Publisher.csproj"], expected.Contexts["Release:Publisher/Publisher.csproj"]})
	if conflict.Status != "invalid_context_selection" || len(conflict.Paths) != 0 {
		t.Fatal("production lease merged alternatives")
	}
}
