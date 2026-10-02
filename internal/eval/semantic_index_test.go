package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
)

func semanticQueryIndex(t *testing.T, artifact *semantic.Artifact) *semanticindex.Index {
	t.Helper()
	dir := t.TempDir()
	audit := filepath.Join(dir, "artifact.json")
	if err := semantic.Write(audit, artifact); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	provenance := semanticindex.Provenance{ArtifactSHA256: semanticTestSHA(data), CorpusFingerprint: semanticTestSHA([]byte("independent-fixture-corpus"))}
	path := filepath.Join(dir, "semantic.index")
	if err := semanticindex.Build(path, artifact, provenance); err != nil {
		t.Fatal(err)
	}
	index, err := semanticindex.Open(path, provenance, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index
}

func importSemanticQueryWire(t *testing.T, root string, wire []byte) *semantic.Artifact {
	t.Helper()
	artifact, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func assertCompactSharedBindings(t *testing.T, index *semanticindex.Index, root string) semanticindex.QueryResult {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "Shared", "LinkedCall.cs"))
	if err != nil {
		t.Fatal(err)
	}
	position := semanticindex.Position{Repo: "fixture", Path: "Shared/LinkedCall.cs", Offset: uint64(bytes.Index(raw, []byte("Resolve")))}
	result, err := index.Bindings(context.Background(), position, 100)
	if err != nil {
		t.Fatal(err)
	}
	contexts, targets := map[string]bool{}, map[string]bool{}
	for _, row := range result.Results {
		if row.Binding.Status != "resolved" || row.Symbol == nil {
			t.Fatalf("missing resolved shared target: %+v", row)
		}
		contexts[row.Context.ID], targets[row.Symbol.ID] = true, true
		if row.Source.RawSHA256 != semanticTestSHA(raw) || row.Occurrence.Offset != position.Offset || row.Source.Path != position.Path {
			t.Fatal("shared query lost exact raw source location")
		}
		selected := position
		selected.BuildContextID = row.Context.ID
		one, err := index.Bindings(context.Background(), selected, 100)
		if err != nil || len(one.Results) == 0 {
			t.Fatalf("context lookup: %+v %v", one, err)
		}
		for _, candidate := range one.Results {
			if candidate.Context.ID != row.Context.ID || candidate.Symbol == nil || candidate.Symbol.ID != row.Symbol.ID {
				t.Fatal("selected context leaked another binding")
			}
		}
		definitions, err := index.Definitions(context.Background(), row.Symbol.ID, semanticindex.Filter{Repo: "fixture", BuildContextID: row.Context.ID}, 100)
		if err != nil || len(definitions.Results) == 0 {
			t.Fatalf("definitions missing: %+v %v", definitions, err)
		}
		for _, definition := range definitions.Results {
			wantPath := strings.Split(row.Context.Project, "/")[0] + "/Target.cs"
			if definition.Source.Path != wantPath || definition.Context.ID != row.Context.ID || definition.Occurrence.Role != "declaration" || definition.Binding.Status != "resolved" {
				t.Fatalf("wrong project definition: %+v", definition)
			}
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(wantPath)))
			if err != nil {
				t.Fatal(err)
			}
			o := definition.Occurrence
			if o.Offset+o.Length > uint64(len(content)) || string(content[o.Offset:o.Offset+o.Length]) != "Resolve" {
				t.Fatal("definition does not match independent source anchor")
			}
		}
	}
	if len(contexts) != 2 || len(targets) != 2 || result.Truncated {
		t.Fatalf("context alternatives=%d targets=%d truncated=%v", len(contexts), len(targets), result.Truncated)
	}
	return result
}

func TestSemanticIndexImportedBindingsRawBOMAndOwnership(t *testing.T) {
	root, wire := semanticArtifactWire(t, true)
	index := semanticQueryIndex(t, importSemanticQueryWire(t, root, wire))
	result := assertCompactSharedBindings(t, index, root)
	first := result.Results[0]
	wrongOffset := first.Occurrence.Offset - 3
	normalized, err := index.Bindings(context.Background(), semanticindex.Position{Repo: "fixture", Path: first.Source.Path, Offset: wrongOffset}, 100)
	if err != nil || len(normalized.Results) != 0 {
		t.Fatalf("raw API silently accepted BOM-normalized offset: %+v %v", normalized, err)
	}
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(result)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("returned results borrowed closed mapping: %v", err)
	}
}

func TestSemanticIndexDiagnosticCandidatesAndIncompleteAdmission(t *testing.T) {
	root, wire := semanticArtifactWire(t, false)
	rows := semanticWireRecords(t, wire)
	var symbols []any
	for _, row := range rows {
		if row["record_type"] == "declaration" {
			symbols = append(symbols, row["symbol"])
		}
	}
	for _, row := range rows {
		if row["record_type"] == "reference" && row["project"] == "ContextA/ContextA.csproj" {
			delete(row, "symbol")
			row["binding_status"], row["candidates"] = "ambiguous", symbols
		}
	}
	// Authored protocol diagnostic: this tests candidate storage/query semantics,
	// not a claim that an ambiguous C# compilation is publishable.
	artifact := importSemanticQueryWire(t, root, encodeSemanticWire(t, rows))
	index := semanticQueryIndex(t, artifact)
	var selected string
	for _, c := range artifact.Contexts {
		if c.Project == "ContextA/ContextA.csproj" {
			selected = c.ID
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "Shared", "LinkedCall.cs"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := index.Bindings(context.Background(), semanticindex.Position{Repo: "fixture", Path: "Shared/LinkedCall.cs", Offset: uint64(bytes.Index(raw, []byte("Resolve"))), BuildContextID: selected}, 100)
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("candidate result: %+v %v", result, err)
	}
	row := result.Results[0]
	if row.Binding.Status != "ambiguous" || row.Symbol != nil || len(row.Candidates) != 2 {
		t.Fatalf("candidate promoted to resolved: %+v", row)
	}
	for _, candidate := range row.Candidates {
		definitions, err := index.Definitions(context.Background(), candidate.ID, semanticindex.Filter{}, 100)
		if err != nil || len(definitions.Results) != 1 || definitions.Results[0].Occurrence.Role != "declaration" {
			t.Fatalf("candidate-use leaked into definitions: %+v %v", definitions, err)
		}
	}
	for _, row := range rows {
		row["compilation_status"] = "incomplete"
		if row["record_type"] == "project" {
			row["issues"] = []string{"fixture incomplete"}
		}
	}
	incomplete := importSemanticQueryWire(t, root, encodeSemanticWire(t, rows))
	path := filepath.Join(t.TempDir(), "rejected.index")
	if err := semanticindex.Build(path, incomplete, semanticindex.Provenance{ArtifactSHA256: semanticTestSHA(wire), CorpusFingerprint: semanticTestSHA([]byte("fixture"))}); err == nil {
		t.Fatal("incomplete evidence admitted to compact query index")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("incomplete build created artifact: %v", err)
	}
}

func TestSemanticIndexActualMSBuildBindings(t *testing.T) {
	input, root := os.Getenv("MOEDEX_SEMANTIC_WORKER_OUTPUT"), os.Getenv("MOEDEX_SEMANTIC_WORKER_ROOT")
	if input == "" {
		t.Skip("set MOEDEX_SEMANTIC_WORKER_OUTPUT for real compiler query gate")
	}
	if root == "" {
		t.Fatal("MOEDEX_SEMANTIC_WORKER_ROOT required")
	}
	wire, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	index := semanticQueryIndex(t, importSemanticQueryWire(t, root, wire))
	assertCompactSharedBindings(t, index, root)
}

func TestSemanticIndexActualGeneratedDefinitions(t *testing.T) {
	input, root := os.Getenv("MOEDEX_SEMANTIC_GENERATED_OUTPUT"), os.Getenv("MOEDEX_SEMANTIC_WORKER_ROOT")
	if input == "" {
		t.Skip("set MOEDEX_SEMANTIC_GENERATED_OUTPUT for real generated query gate")
	}
	if root == "" {
		t.Fatal("MOEDEX_SEMANTIC_WORKER_ROOT required")
	}
	wire, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	artifact := importSemanticQueryWire(t, root, wire)
	index := semanticQueryIndex(t, artifact)
	sources := map[string]semantic.Source{}
	for _, source := range artifact.Sources {
		sources[source.ID] = source
	}
	checked := 0
	for _, occurrence := range artifact.Occurrences {
		source := sources[occurrence.SourceID]
		if occurrence.Role != "declaration" || !strings.HasPrefix(source.Path, ".generated/") {
			continue
		}
		result, err := index.Bindings(context.Background(), semanticindex.Position{Repo: "fixture", Path: source.Path, Offset: occurrence.Offset, BuildContextID: occurrence.ContextID}, 100)
		if err != nil || len(result.Results) == 0 {
			t.Fatalf("generated binding missing: %+v %v", result, err)
		}
		for _, row := range result.Results {
			if !row.Source.Generated || row.Source.RawSHA256 != source.RawSHA256 || len(row.Source.Content) != 0 || row.Symbol == nil {
				t.Fatal("generated location provenance lost or content eagerly copied")
			}
			definitions, err := index.Definitions(context.Background(), row.Symbol.ID, semanticindex.Filter{BuildContextID: occurrence.ContextID}, 100)
			if err != nil || len(definitions.Results) == 0 {
				t.Fatalf("generated definitions missing: %+v %v", definitions, err)
			}
			found := false
			for _, definition := range definitions.Results {
				if definition.Occurrence.ID == occurrence.ID {
					found = true
				}
			}
			if row.Occurrence.ID == occurrence.ID && !found {
				t.Fatal("generated declaration absent from own symbol definitions")
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no actual virtual generated declarations tested")
	}
}

type semanticQueryProvider struct {
	index    *semanticindex.Index
	releases atomic.Int64
}

func (p *semanticQueryProvider) AcquireCompilerSession(context.Context) (mcp.CompilerSession, error) {
	return mcp.CompilerSession{Reader: p.index, SnapshotID: "fixture-snapshot", ArtifactSHA256: semanticTestSHA([]byte("fixture-audit")), CorpusFingerprint: semanticTestSHA([]byte("fixture-corpus")), Release: func() { p.releases.Add(1) }}, nil
}

type semanticToolResult struct {
	Status     string `json:"status"`
	SnapshotID string `json:"snapshot_id"`
	Evidence   string `json:"evidence"`
	Contexts   []struct {
		ID      string `json:"context_id"`
		Project string `json:"project"`
		SHA     string `json:"raw_sha256"`
	} `json:"contexts"`
	Results []struct {
		Status  string `json:"binding_status"`
		Context string `json:"context_id"`
		Path    string `json:"path"`
		Role    string `json:"role"`
		Offset  uint64 `json:"byte_offset"`
		SHA     string `json:"raw_sha256"`
		Symbol  *struct {
			ID string `json:"id"`
		} `json:"symbol"`
	} `json:"results"`
}

func semanticCompilerToolCall(t *testing.T, handler http.Handler, tool string, args map[string]any) semanticToolResult {
	t.Helper()
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(request))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool                       `json:"isError"`
			Content semanticToolResult         `json:"structuredContent"`
			Meta    map[string]json.RawMessage `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("tool JSON: %v: %s", err, response.Body.String())
	}
	if response.Code != http.StatusOK || len(envelope.Error) != 0 || (envelope.Result.IsError && envelope.Result.Content.Status != "source_mismatch") {
		t.Fatalf("tool failed: %s", response.Body.String())
	}
	if raw, ok := envelope.Result.Meta["dev.moedex/snapshot"]; ok {
		var identity struct {
			Cacheable bool `json:"cacheable"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			t.Fatal(err)
		}
		if identity.Cacheable {
			t.Fatal("historical compiler evidence advertised as live cacheable result")
		}
	}
	return envelope.Result.Content
}

func TestSemanticIndexCompilerToolsRequireExactContextAndRawSource(t *testing.T) {
	root, wire := semanticArtifactWire(t, true)
	index := semanticQueryIndex(t, importSemanticQueryWire(t, root, wire))
	provider := &semanticQueryProvider{index: index}
	handler := mcp.NewServer(nil, mcp.WithTools(mcp.CompilerTools(provider)...)).HTTPHandler()
	raw, err := os.ReadFile(filepath.Join(root, "Shared", "LinkedCall.cs"))
	if err != nil {
		t.Fatal(err)
	}
	offset := uint64(bytes.Index(raw, []byte("Resolve")))
	args := map[string]any{"repo": "fixture", "path": "Shared/LinkedCall.cs", "byte_offset": offset}
	choices := semanticCompilerToolCall(t, handler, "compiler_binding_at", args)
	if choices.Status != "context_required" || len(choices.Contexts) != 2 || len(choices.Results) != 0 {
		t.Fatalf("ambiguous source position silently selected: %+v", choices)
	}
	choice := choices.Contexts[0]
	args["context_id"], args["raw_sha256"] = choice.ID, semanticTestSHA(raw)
	selected := semanticCompilerToolCall(t, handler, "compiler_binding_at", args)
	if selected.Status != "ok" || selected.SnapshotID != "fixture-snapshot" || selected.Evidence != "recorded-compiler-context" || len(selected.Results) != 1 {
		t.Fatalf("selected query: %+v", selected)
	}
	row := selected.Results[0]
	if row.Context != choice.ID || row.Offset != offset || row.SHA != semanticTestSHA(raw) || row.Symbol == nil {
		t.Fatalf("selected query provenance: %+v", row)
	}
	definitions := semanticCompilerToolCall(t, handler, "compiler_definitions", map[string]any{"symbol_id": row.Symbol.ID, "repo": "fixture", "context_id": choice.ID})
	if definitions.Status != "ok" || len(definitions.Results) != 1 || definitions.Results[0].Role != "declaration" || definitions.Results[0].Path != strings.Split(choice.Project, "/")[0]+"/Target.cs" {
		t.Fatalf("tool definition bound wrong source: %+v", definitions)
	}
	args["raw_sha256"] = semanticTestSHA(raw[3:])
	mismatch := semanticCompilerToolCall(t, handler, "compiler_binding_at", args)
	if mismatch.Status != "source_mismatch" || len(mismatch.Results) != 0 {
		t.Fatalf("normalized SHA accepted as raw compiler identity: %+v", mismatch)
	}
	if provider.releases.Load() != 4 {
		t.Fatalf("tool lease releases=%d want4", provider.releases.Load())
	}
}
