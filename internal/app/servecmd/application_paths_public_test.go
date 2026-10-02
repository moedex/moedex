package servecmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/snapshot"
)

func TestPublicApplicationWrapperPaths(t *testing.T) {
	index, sourceRoot := os.Getenv("MOEDEX_APPLICATION_INDEX"), os.Getenv("MOEDEX_APPLICATION_SOURCE_ROOT")
	if index == "" || sourceRoot == "" || os.Getenv("MOEDEX_APPLICATION_PATH_REPORT") == "" {
		t.Skip("set application index/source/path report for real wrapper paths")
	}
	gold, sourceGold := readApplicationPathGold(t)
	for p, want := range sourceGold.ReviewedClosure.SourceHashes {
		b, e := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(p)))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatal("frozen path source changed", p, e)
		}
	}
	resolved, err := snapshot.Resolve(index)
	if err != nil || resolved.Legacy || resolved.Manifest == nil {
		t.Fatal("immutable snapshot required", err)
	}
	artifacts := resolved.Manifest.Components[snapshot.SemanticComponent].Artifacts
	if len(artifacts) != 1 {
		t.Fatal("semantic artifact required")
	}
	audit, err := semantic.Read(filepath.Join(resolved.Root, filepath.FromSlash(artifacts[0].Path)), semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range audit.Snapshots {
		if s.Repo != gold.Repo || s.Commit != gold.Commit {
			t.Fatal("path capture pin mismatch")
		}
	}
	contexts := map[string]semantic.BuildContext{}
	byProject := map[string][]string{}
	for _, c := range audit.Contexts {
		contexts[c.ID] = c
		var capture struct {
			Global map[string]string `json:"global_properties"`
		}
		if json.Unmarshal([]byte(c.Capture), &capture) != nil {
			t.Fatal("invalid capture")
		}
		if c.Status == "complete" && capture.Global["Configuration"] == "Debug" && capture.Global["TargetFramework"] == "net8.0" {
			byProject[c.Project] = append(byProject[c.Project], c.ID)
		}
	}
	for p := range byProject {
		sort.Strings(byProject[p])
	}
	sources := map[string]semantic.Source{}
	for _, s := range audit.Sources {
		sources[s.ID] = s
	}
	occurrences := map[string]semantic.Occurrence{}
	for _, o := range audit.Occurrences {
		occurrences[o.ID] = o
	}
	bindings := map[string]semantic.Binding{}
	for _, b := range audit.Bindings {
		bindings[b.OccurrenceID] = b
	}
	var tool mcp.ToolHandler
	for _, candidate := range mcp.CompilerTools(compilerLeaseHolder(t, resolved)) {
		if candidate.Name() == "compiler_contract_paths" {
			tool = candidate
		}
	}
	if tool == nil {
		t.Fatal("path tool missing")
	}
	type report struct {
		Case            string                          `json:"case"`
		Negative        bool                            `json:"negative"`
		Contexts        []string                        `json:"contexts"`
		CompleteBytes   int                             `json:"complete_response_bytes"`
		StructuredBytes int                             `json:"structured_content_bytes"`
		Result          mcp.CompilerContractPathsResult `json:"result"`
	}
	var reports []report
	defer func() {
		b, _ := json.MarshalIndent(reports, "", "  ")
		if e := os.WriteFile(os.Getenv("MOEDEX_APPLICATION_PATH_REPORT"), append(b, '\n'), 0600); e != nil {
			t.Error(e)
		}
	}()
	location := func(b mcp.CompilerBinding) applicationPathLocation {
		return applicationPathLocation{Project: b.Project, Path: b.Path, Offset: b.ByteOffset, Length: b.ByteLength, SHA: b.RawSHA256}
	}
	validateBinding := func(b mcp.CompilerBinding, selected map[string]bool) {
		t.Helper()
		o, ok := occurrences[b.OccurrenceID]
		s := sources[b.SourceID]
		c := contexts[b.ContextID]
		a := bindings[b.OccurrenceID]
		if !ok || !selected[b.ContextID] || b.Repo != gold.Repo || c.SnapshotID != b.SourceSnapshotID || c.Project != b.Project || s.SnapshotID != b.SourceSnapshotID || s.Path != b.Path || s.RawSHA256 != b.RawSHA256 || o.ContextID != b.ContextID || o.SourceID != b.SourceID || o.Offset != b.ByteOffset || o.Length != b.ByteLength || o.Role != b.Role || o.Kind != b.ReferenceKind || a.Status != "resolved" || b.BindingStatus != a.Status || b.Symbol == nil || b.Symbol.ID != a.SymbolID || b.EnclosingSymbolID != a.EnclosingSymbolID || b.ExtractorVersion != a.ExtractorVersion || b.Method != a.Method || b.Extractor != a.Extractor {
			t.Fatal("path hop provenance mismatch", b.OccurrenceID)
		}
		if applicationSymbol(applicationKey(*b.Symbol)).ID != b.Symbol.ID {
			t.Fatal("path symbol identity mismatch")
		}
	}
	// Domain gold is deliberately unchanged: wrapper invocations must not become direct facts.
	for _, want := range gold.Paths {
		for _, o := range audit.Occurrences {
			s := sources[o.SourceID]
			if s.Path == want.Caller.Path && o.Offset == want.Caller.Offset {
				for _, f := range bindings[o.ID].DomainFacts {
					if f.Kind == "message_publish" {
						t.Fatal("wrapper promoted to synthetic direct publisher", want.ID)
					}
				}
			}
		}
	}
	for _, want := range gold.Paths {
		variants := byProject[want.Seed.Project]
		if len(variants) == 0 {
			t.Fatal("missing seed context")
		}
		for _, variant := range variants {
			selection := []string{variant}
			if want.Caller.Project != want.Seed.Project {
				choices := byProject[want.Caller.Project]
				if len(choices) != 1 {
					t.Fatal("expected unique caller context")
				}
				selection = append(selection, choices[0])
			}
			sort.Strings(selection)
			selected := map[string]bool{}
			for _, id := range selection {
				selected[id] = true
			}
			for _, negative := range []bool{false, true} {
				message := want.Message
				if negative {
					for _, other := range gold.Paths {
						if other.ID != want.ID {
							message = other.Message
						}
					}
				}
				args, _ := json.Marshal(map[string]any{"symbol_id": applicationSymbol(message).ID, "context_ids": selection, "limit": 20})
				wire, e := tool.Call(context.Background(), args)
				if e != nil {
					t.Fatal(e)
				}
				full, _ := json.Marshal(wire)
				structured, _ := json.Marshal(wire["structuredContent"])
				var got mcp.CompilerContractPathsResult
				if json.Unmarshal(structured, &got) != nil {
					t.Fatal("invalid path response")
				}
				reports = append(reports, report{want.ID, negative, selection, len(full), len(structured), got})
				if (got.Status != "ok" && got.Status != "no_recorded_paths") || got.SnapshotID != resolved.ID || got.ArtifactSHA256 != artifacts[0].SHA256 || got.PathSemantics != "candidate_static" || got.EvidenceScope != "compile_time" || got.SeedsTruncated || got.PathsTruncated || !got.TotalIsExact || !reflect.DeepEqual(got.ContextIDs, selection) || got.RowsVisited > 10000 || got.Records > 20 {
					t.Fatalf("incomplete/invalid path query: %+v", got)
				}
				for _, seed := range got.Seeds {
					validateBinding(seed.Source, selected)
					if seed.Fact.Kind != "message_publish" || seed.Contract.ID != applicationSymbol(message).ID {
						t.Fatal("invalid path seed")
					}
				}
				if !negative && (len(got.Paths) != 1 || len(got.Seeds) != 1) {
					t.Fatalf("expected one complete source path/seed, got %d/%d", len(got.Paths), len(got.Seeds))
				}
				for _, path := range got.Paths {
					validateBinding(path.Caller, selected)
					validateBinding(path.Seed.Source, selected)
					if negative {
						if location(path.Caller) == want.Caller {
							t.Error("wrong message joined to caller", want.ID)
						}
						continue
					}
					impl := path.Implementation
					if impl == nil || path.Seed.Owner == nil {
						t.Fatal("missing implementation/owner proof")
					}
					validateBinding(impl.Source, selected)
					if impl.Kind != "interface_method_implementation" || impl.Rule != "csharp-interface-v1" || impl.EvidenceScope != "compile_time" || impl.Source.ContextID != path.Seed.Source.ContextID || impl.Source.SourceSnapshotID != path.Seed.Source.SourceSnapshotID || path.Caller.Symbol.ID != impl.InterfaceMember.ID || impl.Source.Symbol.ID != path.Seed.Owner.ID || path.Caller.EnclosingSymbolID != path.CallerOwner.ID {
						t.Fatal("invalid implementation join")
					}
					found := false
					for _, f := range bindings[impl.Source.OccurrenceID].ImplementationFacts {
						if f.InterfaceSymbolID == impl.InterfaceMember.ID && f.ImplementingTypeSymbolID == impl.ImplementingType.ID {
							found = true
						}
					}
					if !found {
						t.Fatal("implementation absent from audit")
					}
					observed := applicationPathCase{Message: applicationKey(path.Seed.Contract), Caller: location(path.Caller), CallerOwner: applicationKey(path.CallerOwner), InterfaceMember: applicationKey(impl.InterfaceMember), ImplementationType: applicationKey(impl.ImplementingType), ImplementationMethod: applicationKey(*impl.Source.Symbol), ImplementationDeclaration: location(impl.Source), Seed: location(path.Seed.Source), SeedKind: path.Seed.Fact.Kind, SeedRule: path.Seed.Fact.Rule, Classification: "static_candidate"}
					if !applicationPathMatches(want, observed) {
						t.Fatalf("source path mismatch %s: %+v", want.ID, observed)
					}
				}
			}
		}
	}
	t.Logf("wrapper_path_queries=%d static_paths_expected=%d", len(reports), len(reports)/2)
}
