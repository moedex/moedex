package semanticimport_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moedex/internal/app/semanticcmd"
	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
)

// Independent real-project oracle: exact qualified identities join observations
// across selected compiler contexts. No test infers runtime delivery or database
// existence, and variant contexts remain explicit alternatives.
func TestPublicContractImpact(t *testing.T) {
	base := os.Getenv("MOEDEX_CONTRACT_CAPTURE_DIR")
	if base == "" {
		t.Skip("set MOEDEX_CONTRACT_CAPTURE_DIR from check-contract-impact.py")
	}
	root := filepath.Join(base, "fixture")
	captures := map[string]*semantic.Artifact{}
	for _, configuration := range []string{"Debug", "Release"} {
		raw, err := os.ReadFile(filepath.Join(base, configuration+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		a, err := semanticimport.Import(context.Background(), bytes.NewReader(raw), semanticimport.Options{Repo: "contract-gold", Root: root})
		if err != nil {
			t.Fatal(err)
		}
		roots := map[string]string{}
		for _, snapshot := range a.Snapshots {
			roots[snapshot.ID] = root
		}
		if err := semanticimport.Revalidate(context.Background(), a, roots); err != nil {
			t.Fatalf("production recorded-input revalidation %s: %v", configuration, err)
		}
		if !a.Complete() {
			t.Fatalf("%s incomplete", configuration)
		}
		captures[configuration] = a
	}
	dir := t.TempDir()
	var inputs []string
	for _, configuration := range []string{"Debug", "Release"} {
		input := filepath.Join(dir, configuration+".semantic")
		if err := semantic.Write(input, captures[configuration]); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
	}
	artifact := filepath.Join(dir, "contract.semantic")
	if _, err := semanticcmd.ComposeFiles(context.Background(), semanticcmd.ComposeOptions{Inputs: inputs, Output: artifact}); err != nil {
		t.Fatal(err)
	}
	merged, err := semantic.Read(artifact, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	provenance := semanticindex.Provenance{ArtifactSHA256: hash, CorpusFingerprint: strings.Repeat("b", 64)}
	path := filepath.Join(dir, "contract.index")
	if err := semanticindex.Build(path, merged, provenance); err != nil {
		t.Fatal(err)
	}
	x, err := semanticindex.Open(path, provenance, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	provider := &domainGoldProvider{index: x, artifact: hash}
	var tool mcp.ToolHandler
	for _, candidate := range mcp.CompilerTools(provider) {
		if candidate.Name() == "compiler_contract_impact" {
			tool = candidate
		}
	}
	if tool == nil {
		t.Fatal("compiler_contract_impact unavailable")
	}
	ids := map[string]string{}
	for _, s := range merged.Symbols {
		if s.Key.Descriptor == "T:Shared.Notice" {
			ids[s.Key.Namespace] = s.ID
		}
	}
	shared, distractor := ids["contract-gold/Contracts/Contracts.csproj"], ids["contract-gold/Distractor/Distractor.csproj"]
	if shared == "" || distractor == "" || shared == distractor {
		t.Fatalf("qualified same-name fixture invalid: %+v", ids)
	}
	contextFor := func(configuration, project string) string {
		for _, c := range captures[configuration].Contexts {
			if c.Project == project+"/"+project+".csproj" {
				return c.ID
			}
		}
		t.Fatalf("missing context %s/%s", configuration, project)
		return ""
	}
	debugPublisher, releasePublisher := contextFor("Debug", "Publisher"), contextFor("Release", "Publisher")
	if debugPublisher == releasePublisher {
		t.Fatal("variants lost")
	}

	call := func(name, symbol string, contexts []string, limit int) mcp.CompilerContractResult {
		t.Helper()
		args := map[string]any{"symbol_id": symbol, "limit": limit}
		if contexts != nil {
			args["context_ids"] = contexts
		}
		b, _ := json.Marshal(args)
		response, err := tool.Call(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(response["structuredContent"])
		if err != nil {
			t.Fatal(err)
		}
		var result mcp.CompilerContractResult
		if err := json.Unmarshal(wire, &result); err != nil {
			t.Fatal(err)
		}
		if result.EvidenceScope != "compile_time" {
			t.Fatalf("runtime claim: %s", wire)
		}
		t.Logf("contract_query=%s status=%s paths=%d contexts=%d truncated=%v structured_sha256=%x", name, result.Status, len(result.Paths), len(result.Contexts), result.Truncated, sha256.Sum256(wire))
		return result
	}
	discovery := call("discover", shared, nil, 100)
	if discovery.Status != "context_required" || len(discovery.Paths) != 0 || discovery.TotalIsExact || discovery.Truncated || len(discovery.Contexts) != 6 {
		t.Fatalf("discovery merged or lost contexts: %+v", discovery)
	}
	wantContexts := []string{}
	for _, config := range []string{"Debug", "Release"} {
		for _, project := range []string{"Publisher", "Consumer", "Storage"} {
			wantContexts = append(wantContexts, contextFor(config, project))
		}
	}
	gotContexts := []string{}
	for _, c := range discovery.Contexts {
		gotContexts = append(gotContexts, c.ContextID)
	}
	sort.Strings(wantContexts)
	sort.Strings(gotContexts)
	if !reflect.DeepEqual(gotContexts, wantContexts) {
		t.Fatalf("context discovery=%v want=%v", gotContexts, wantContexts)
	}
	limitedDiscovery := call("discovery-limit", shared, nil, 1)
	if len(limitedDiscovery.Contexts) != 1 || !limitedDiscovery.Truncated || len(limitedDiscovery.Paths) != 0 {
		t.Fatal("context discovery cap lost")
	}
	scopes := []string{debugPublisher, contextFor("Debug", "Consumer"), contextFor("Debug", "Storage")}
	all := call("selected-projects", shared, scopes, 100)
	if all.Status != "ok" || len(all.Paths) != 4 || all.Count != 4 || !all.TotalIsExact || all.Truncated {
		t.Fatalf("scoped facts=%+v", all)
	}
	expectedOwners := map[string]string{"message_publish": "M:Publishers.Producer.Send(MassTransit.IPublishEndpoint)", "message_consumer": "T:Consumers.Consumer", "storage_entity": "T:Storage.Store", "storage_table": "M:Storage.Store.Configure(Microsoft.EntityFrameworkCore.ModelBuilder)"}
	seenKinds := map[string]bool{}
	for _, path := range all.Paths {
		if path.Contract.ID != shared || path.Contract.Namespace != "contract-gold/Contracts/Contracts.csproj" || path.Contract.Descriptor != "T:Shared.Notice" {
			t.Fatal("same-name target identity joined incorrectly")
		}
		if seenKinds[path.Fact.Kind] {
			t.Fatal("duplicate fact")
		}
		seenKinds[path.Fact.Kind] = true
		if path.Owner == nil || path.Owner.Descriptor != expectedOwners[path.Fact.Kind] {
			t.Fatalf("wrong compiler owner %+v", path)
		}
		if path.Source.ContextID != contextFor("Debug", strings.Split(path.Source.Project, "/")[0]) {
			t.Fatal("implicit variant merge")
		}
		source, err := os.ReadFile(filepath.Join(root, path.Source.Path))
		if err != nil {
			t.Fatal(err)
		}
		if path.Source.RawSHA256 != fmt.Sprintf("%x", sha256.Sum256(source)) {
			t.Fatal("wrong raw source identity")
		}
		marker := map[string]string{"message_publish": "publish-debug", "message_consumer": "consumer", "storage_entity": "entity", "storage_table": "table"}[path.Fact.Kind]
		token := []byte("/*gold:" + marker + "*/")
		if path.Source.ByteOffset != uint64(bytes.Index(source, token)+len(token)) {
			t.Fatal("wrong evidence span")
		}
		if path.Fact.Kind == "storage_table" && (path.Fact.Table != "notices" || path.Fact.Schema != "app") {
			t.Fatal("table scalar lost")
		}
	}
	for _, variant := range []string{"Debug", "Release"} {
		only := call("publisher-"+variant, shared, []string{contextFor(variant, "Publisher")}, 100)
		if len(only.Paths) != 1 || only.Paths[0].Source.ContextID != contextFor(variant, "Publisher") || only.Paths[0].Fact.Kind != "message_publish" {
			t.Fatal("publisher variant filtering failed")
		}
	}
	other := call("same-name-distractor", distractor, []string{contextFor("Debug", "Distractor")}, 100)
	if len(other.Paths) != 1 || other.Paths[0].Contract.ID != distractor || other.Paths[0].Source.Project != "Distractor/Distractor.csproj" {
		t.Fatal("distractor exact identity failed")
	}
	empty := call("no-same-name-join", shared, []string{contextFor("Debug", "Distractor")}, 100)
	if len(empty.Paths) != 0 || empty.Status != "no_recorded_evidence" || !empty.TotalIsExact {
		t.Fatal("same-name-only join appeared")
	}
	capped := call("path-limit", shared, scopes, 1)
	if len(capped.Paths) != 1 || !capped.Truncated || capped.TotalIsExact {
		t.Fatal("path truncation not truthful")
	}
	invalid := call("conflicting-variants", shared, []string{debugPublisher, releasePublisher}, 100)
	if invalid.Status != "invalid_context_selection" || len(invalid.Paths) != 0 {
		t.Fatal("incompatible project variants silently merged")
	}
	duplicate := call("duplicate-scope", shared, []string{debugPublisher, debugPublisher}, 100)
	if duplicate.Status != "invalid_arguments" {
		t.Fatal("duplicate scope accepted")
	}
	unknown := call("unknown-context", shared, []string{"context:" + strings.Repeat("f", 64)}, 100)
	if unknown.Status != "invalid_context_selection" {
		t.Fatal("unknown context accepted")
	}
	if provider.acquired != provider.released {
		t.Fatal("lease leak")
	}
}
