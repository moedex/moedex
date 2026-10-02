package servecmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/snapshot"
)

const implementationDiscoveryGoldSHA = "a3e991360154e516b6c151e44cfff3c1f1928b72fa22a134e5c3bbab7c9ca261"

type implementationDiscoveryGold struct {
	Corpora map[string]struct {
		Commit        string `json:"commit"`
		Configuration string `json:"configuration"`
		Framework     string `json:"framework"`
	} `json:"corpora"`
	Cases []struct {
		ID            string             `json:"id"`
		Corpus        string             `json:"corpus"`
		Interface     semantic.SymbolKey `json:"interface_member"`
		Projects      []string           `json:"expected_discovery_projects"`
		Contexts      int                `json:"expected_context_alternatives"`
		Relationships []struct {
			Method semantic.SymbolKey      `json:"implementation_method"`
			Type   semantic.SymbolKey      `json:"implementing_type"`
			Source applicationPathLocation `json:"source"`
		} `json:"expected_implementations"`
	} `json:"positive_cases"`
	Control struct {
		ID        string                  `json:"id"`
		Corpus    string                  `json:"corpus"`
		Interface semantic.SymbolKey      `json:"interface_member"`
		Source    applicationPathLocation `json:"interface_source"`
		Projects  []string                `json:"explicit_query_projects"`
	} `json:"no_recorded_control"`
}

// Source-authored API regression, not a new held-out or natural-language task
// benchmark. Discovery calls, rejected selections and legacy queries all count.
func TestPublicImplementationDiscovery(t *testing.T) {
	inputPath, reportPath := os.Getenv("MOEDEX_IMPLEMENTATION_QUERY_INPUTS"), os.Getenv("MOEDEX_IMPLEMENTATION_QUERY_REPORT")
	if inputPath == "" || reportPath == "" {
		t.Skip("set MOEDEX_IMPLEMENTATION_QUERY_INPUTS/REPORT for published reverse implementation gate")
	}
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	goldPath := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "implementation-discovery-20261001", "query-gold.json")
	raw := read(goldPath)
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != implementationDiscoveryGoldSHA {
		t.Fatal("reverse query gold changed")
	}
	var gold implementationDiscoveryGold
	if err := json.Unmarshal(raw, &gold); err != nil {
		t.Fatal(err)
	}
	var inputs map[string]struct {
		Index  string `json:"index"`
		Legacy string `json:"legacy_index"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(read(inputPath), &inputs); err != nil {
		t.Fatal(err)
	}
	type corpus struct {
		resolved  snapshot.Resolved
		tool      mcp.ToolHandler
		legacy    mcp.ToolHandler
		contexts  map[string]semantic.BuildContext
		sources   map[string]semantic.Source
		byProject map[string][]string
		artifact  string
	}
	corpora := map[string]corpus{}
	toolFor := func(resolved snapshot.Resolved) mcp.ToolHandler {
		t.Helper()
		for _, tool := range mcp.CompilerTools(compilerLeaseHolder(t, resolved)) {
			if tool.Name() == "compiler_implementations" {
				return tool
			}
		}
		t.Fatal("reverse implementation tool unavailable")
		return nil
	}
	for name, expected := range gold.Corpora {
		input := inputs[name]
		resolved, err := snapshot.Resolve(input.Index)
		if err != nil || resolved.Manifest == nil || resolved.Manifest.Components[snapshot.SemanticIndexComponent].Version != 5 {
			t.Fatal("published index v5 required", name, err)
		}
		legacy, err := snapshot.Resolve(input.Legacy)
		if err != nil || legacy.Manifest == nil || legacy.Manifest.Components[snapshot.SemanticIndexComponent].Version != 4 {
			t.Fatal("preserved index v4 required", name, err)
		}
		artifacts := resolved.Manifest.Components[snapshot.SemanticComponent].Artifacts
		oldArtifacts := legacy.Manifest.Components[snapshot.SemanticComponent].Artifacts
		if len(artifacts) != 1 || len(oldArtifacts) != 1 || artifacts[0].SHA256 != oldArtifacts[0].SHA256 {
			t.Fatal("reverse rebuild changed native artifact", name)
		}
		audit, err := semantic.Read(filepath.Join(resolved.Root, artifacts[0].Path), semantic.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		c := corpus{resolved: resolved, tool: toolFor(resolved), legacy: toolFor(legacy), artifact: artifacts[0].SHA256,
			contexts: map[string]semantic.BuildContext{}, sources: map[string]semantic.Source{}, byProject: map[string][]string{}}
		for _, s := range audit.Snapshots {
			if s.Repo != name || s.Commit != expected.Commit {
				t.Fatal("corpus pin mismatch", name)
			}
		}
		for _, s := range audit.Sources {
			c.sources[s.ID] = s
		}
		for _, context := range audit.Contexts {
			var capture struct {
				Properties map[string]string `json:"global_properties"`
			}
			if json.Unmarshal([]byte(context.Capture), &capture) != nil || context.Status != "complete" || context.ExtractorVersion != "6" || capture.Properties["Configuration"] != expected.Configuration || capture.Properties["TargetFramework"] != expected.Framework {
				t.Fatal("incomplete or different compiler context", name)
			}
			c.contexts[context.ID] = context
			c.byProject[context.Project] = append(c.byProject[context.Project], context.ID)
		}
		for project := range c.byProject {
			sort.Strings(c.byProject[project])
		}
		corpora[name] = c
	}
	type query struct {
		Case   string                            `json:"case"`
		Corpus string                            `json:"corpus"`
		Phase  string                            `json:"phase"`
		Args   map[string]any                    `json:"args"`
		Bytes  int                               `json:"complete_response_bytes"`
		SHA    string                            `json:"complete_sha256"`
		Result mcp.CompilerImplementationsResult `json:"result"`
	}
	report := struct {
		GoldSHA               string  `json:"gold_sha256"`
		Completed             bool    `json:"completed"`
		PositiveCases         int     `json:"positive_cases"`
		SelectedPositives     int     `json:"selected_positive_queries"`
		AlternativeRejections int     `json:"alternative_rejections"`
		LegacyChecks          int     `json:"legacy_checks"`
		Queries               []query `json:"queries"`
	}{GoldSHA: implementationDiscoveryGoldSHA}
	defer func() {
		b, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(reportPath, append(b, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	call := func(name, id, phase string, args map[string]any) mcp.CompilerImplementationsResult {
		t.Helper()
		c := corpora[name]
		tool := c.tool
		if phase == "legacy" {
			tool = c.legacy
		}
		arg, _ := json.Marshal(args)
		wire, err := tool.Call(context.Background(), arg)
		if err != nil {
			t.Fatal(err)
		}
		full, _ := json.Marshal(wire)
		content, _ := json.Marshal(wire["structuredContent"])
		var got mcp.CompilerImplementationsResult
		if err := json.Unmarshal(content, &got); err != nil {
			t.Fatal(err)
		}
		report.Queries = append(report.Queries, query{id, name, phase, args, len(full), fmt.Sprintf("%x", sha256.Sum256(full)), got})
		if got.ArtifactSHA256 != c.artifact || got.EvidenceScope != "compile_time" {
			t.Fatal("unbound reverse response", id)
		}
		if phase != "legacy" && got.SnapshotID != c.resolved.ID {
			t.Fatal("wrong serving snapshot", id)
		}
		if got.RowsVisited > 10000 || got.WorkLimit != 10000 || got.MaterializationByteLimit != 2<<20 || got.Truncated {
			t.Fatal("reverse application response exceeded limits or truncated", id)
		}
		return got
	}
	verifySource := func(name string, source applicationPathLocation) {
		t.Helper()
		b := read(filepath.Join(inputs[name].Source, filepath.FromSlash(source.Path)))
		if fmt.Sprintf("%x", sha256.Sum256(b)) != source.SHA {
			t.Fatal("frozen source changed", source.Path)
		}
	}
	legacyChecked := map[string]bool{}
	for _, want := range gold.Cases {
		c := corpora[want.Corpus]
		symbolID := applicationSymbol(want.Interface).ID
		for _, relationship := range want.Relationships {
			verifySource(want.Corpus, relationship.Source)
		}
		discovery := call(want.Corpus, want.ID, "discovery", map[string]any{"symbol_id": symbolID})
		if discovery.Status != "context_required" || len(discovery.Matches) != 0 || discovery.Interface == nil || applicationKey(*discovery.Interface) != want.Interface {
			t.Fatal("reverse discovery did not preserve qualified subject", want.ID)
		}
		var ids, expectedIDs []string
		for _, project := range want.Projects {
			expectedIDs = append(expectedIDs, c.byProject[project]...)
		}
		sort.Strings(expectedIDs)
		if len(expectedIDs) != want.Contexts {
			t.Fatal("captured alternative count differs from gold", want.ID)
		}
		for _, choice := range discovery.Contexts {
			ctx, ok := c.contexts[choice.ContextID]
			if !ok || ctx.Project != choice.Project || choice.Repo != want.Corpus || !slices.Contains(want.Projects, choice.Project) {
				t.Fatal("unrecorded discovery choice", want.ID)
			}
			validSource := false
			for _, sourceID := range ctx.SourceIDs {
				s := c.sources[sourceID]
				validSource = validSource || s.Path == choice.Path && s.RawSHA256 == choice.RawSHA256
			}
			if !validSource {
				t.Fatal("discovery source not in compiler context", want.ID)
			}
			ids = append(ids, choice.ContextID)
		}
		sort.Strings(ids)
		if !reflect.DeepEqual(ids, expectedIDs) {
			t.Fatal("missing/extra discovery contexts", want.ID)
		}
		for _, id := range ids {
			got := call(want.Corpus, want.ID, "selected", map[string]any{"symbol_id": symbolID, "context_ids": []string{id}, "limit": 100})
			if got.Status != "ok" || !got.TotalIsExact || got.Count != len(got.Matches) || !reflect.DeepEqual(got.ContextIDs, []string{id}) || got.Interface == nil || applicationKey(*got.Interface) != want.Interface {
				t.Fatal("invalid selected implementation response", want.ID)
			}
			if len(got.Matches) != len(want.Relationships) {
				t.Fatal("missing/extra implementations", want.ID)
			}
			for _, match := range got.Matches {
				b := match.Source
				if b.ContextID != id || b.SourceSnapshotID != c.contexts[id].SnapshotID || b.Repo != want.Corpus || b.Symbol == nil || b.BindingStatus != "resolved" || b.Role != "declaration" || b.ReferenceKind != "declaration" || b.ExtractorVersion != "6" {
					t.Fatal("implementation declaration provenance differs", want.ID)
				}
				if match.ImplementationFactIndex < 0 || match.ImplementationFactIndex >= len(b.ImplementationFacts) {
					t.Fatal("invalid fact ordinal")
				}
				fact := b.ImplementationFacts[match.ImplementationFactIndex]
				if fact.InterfaceSymbolID != symbolID || fact.Kind != "interface_method_implementation" || fact.Rule != "csharp-interface-v1" || fact.EvidenceScope != "compile_time" {
					t.Fatal("wrong compiler relationship", want.ID)
				}
				sy := map[string]semantic.SymbolKey{}
				for _, s := range b.ImplementationSymbols {
					key := applicationKey(s)
					if applicationSymbol(key).ID != s.ID {
						t.Fatal("invalid qualified implementation symbol")
					}
					sy[s.ID] = key
				}
				if sy[symbolID] != want.Interface {
					t.Fatal("wrong interface descriptor", want.ID)
				}
				matched := false
				for _, expected := range want.Relationships {
					location := applicationPathLocation{Project: b.Project, Path: b.Path, Offset: b.ByteOffset, Length: b.ByteLength, SHA: b.RawSHA256}
					matched = matched || location == expected.Source && applicationKey(*b.Symbol) == expected.Method && sy[fact.ImplementingTypeSymbolID] == expected.Type
				}
				if !matched {
					t.Fatal("implementation differs from source-authored gold", want.ID)
				}
			}
			report.SelectedPositives++
		}
		if len(ids) > 1 {
			got := call(want.Corpus, want.ID, "reject-alternatives", map[string]any{"symbol_id": symbolID, "context_ids": ids})
			if got.Status != "invalid_context_selection" || len(got.Matches) != 0 {
				t.Fatal("merged incompatible alternatives", want.ID)
			}
			report.AlternativeRejections++
		}
		if !legacyChecked[want.Corpus] {
			got := call(want.Corpus, want.ID, "legacy", map[string]any{"symbol_id": symbolID})
			if got.Status != "index_upgrade_required" || got.TotalIsExact || len(got.Matches) != 0 {
				t.Fatal("legacy absence reported as supported")
			}
			legacyChecked[want.Corpus] = true
			report.LegacyChecks++
		}
		report.PositiveCases++
	}
	control := gold.Control
	verifySource(control.Corpus, control.Source)
	symbolID := applicationSymbol(control.Interface).ID
	discovery := call(control.Corpus, control.ID, "unsupported-discovery", map[string]any{"symbol_id": symbolID})
	if discovery.Status != "context_required" || len(discovery.Contexts) != 0 || len(discovery.Matches) != 0 {
		t.Fatal("unsupported bridge gained reverse records")
	}
	var ids []string
	for _, project := range control.Projects {
		choices := corpora[control.Corpus].byProject[project]
		if len(choices) != 1 {
			t.Fatal("unsupported control project closure changed")
		}
		ids = append(ids, choices[0])
	}
	sort.Strings(ids)
	got := call(control.Corpus, control.ID, "unsupported-selected", map[string]any{"symbol_id": symbolID, "context_ids": ids})
	if got.Status != "no_recorded_evidence" || !got.TotalIsExact || got.Count != 0 || len(got.Matches) != 0 {
		t.Fatal("unsupported bridge gained reverse records")
	}
	report.Completed = true
}
