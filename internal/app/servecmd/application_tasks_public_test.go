package servecmd

import (
	"context"
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

type taskEvidence struct {
	Context, Repo, Project, Snapshot string
	Source                           mcp.CompilerContractLocation
	API                              mcp.CompilerSymbol
	Owner                            *mcp.CompilerSymbol
	Roles                            []string
	Fact                             mcp.CompilerDomainFact
}
type taskDefinition struct {
	Context, Repo, Project, Snapshot string
	Source                           mcp.CompilerContractLocation
	Symbol                           mcp.CompilerSymbol
}
type taskArm struct {
	Calls           int                       `json:"calls"`
	WireBytes       int                       `json:"complete_response_bytes"`
	StructuredBytes int                       `json:"structured_content_bytes"`
	Evidence        map[string]taskEvidence   `json:"evidence"`
	Definitions     map[string]taskDefinition `json:"definitions"`
}
type taskDiscovery struct {
	Task                   string                `json:"task"`
	Baseline               taskArm               `json:"baseline"`
	Candidate              taskArm               `json:"candidate"`
	BaselineContexts       []mcp.CompilerContext `json:"baseline_contexts"`
	CandidateContexts      []mcp.CompilerContext `json:"candidate_contexts"`
	DefinitionOnlyContexts []string              `json:"definition_only_contexts"`
}

type taskPair struct {
	Task       string   `json:"task"`
	Contexts   []string `json:"contexts"`
	Baseline   taskArm  `json:"baseline"`
	Candidate  taskArm  `json:"candidate"`
	Equivalent bool     `json:"equivalent"`
}

func taskLocation(b mcp.CompilerBinding) mcp.CompilerContractLocation {
	return mcp.CompilerContractLocation{OccurrenceID: b.OccurrenceID, SourceID: b.SourceID, Path: b.Path, RawSHA256: b.RawSHA256, ByteOffset: b.ByteOffset, ByteLength: b.ByteLength, Generated: b.Generated, ReferenceKind: b.ReferenceKind, Role: b.Role, BindingStatus: b.BindingStatus, Method: b.Method, Extractor: b.Extractor, ExtractorVersion: b.ExtractorVersion}
}
func taskKey(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestPublicApplicationImpactTasks(t *testing.T) {
	index := os.Getenv("MOEDEX_APPLICATION_INDEX")
	if index == "" {
		t.Skip("set MOEDEX_APPLICATION_INDEX for paired published-snapshot tasks")
	}
	tasks, gold := readApplicationTasks(t)
	resolved, err := snapshot.Resolve(index)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Legacy || resolved.Manifest == nil {
		t.Fatal("immutable snapshot required")
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
			t.Fatal("source pin mismatch")
		}
	}
	byProject := map[string][]string{}
	for _, want := range gold.Contexts {
		for _, c := range audit.Contexts {
			var capture struct {
				Global map[string]string `json:"global_properties"`
			}
			if err := json.Unmarshal([]byte(c.Capture), &capture); err != nil {
				t.Fatal(err)
			}
			if c.Project == want.Project && capture.Global["Configuration"] == want.Configuration && capture.Global["TargetFramework"] == want.Framework {
				byProject[c.Project] = append(byProject[c.Project], c.ID)
			}
		}
		if len(byProject[want.Project]) == 0 {
			t.Fatal("missing task context", want.Project)
		}
		sort.Strings(byProject[want.Project])
	}
	// Cartesian selections retain every same-project alternative, one per call.
	selections := [][]string{{}}
	for _, want := range gold.Contexts {
		var next [][]string
		for _, sel := range selections {
			for _, id := range byProject[want.Project] {
				next = append(next, append(append([]string(nil), sel...), id))
			}
		}
		selections = next
	}
	if len(selections) > 32 {
		t.Fatal("context variant evaluation bound exceeded")
	}
	holder := compilerLeaseHolder(t, resolved)
	tools := map[string]mcp.ToolHandler{}
	for _, tool := range mcp.CompilerTools(holder) {
		tools[tool.Name()] = tool
	}
	var reports []taskPair
	var discovery []taskDiscovery
	defer func() {
		if p := os.Getenv("MOEDEX_APPLICATION_TASK_REPORT"); p != "" {
			d, _ := json.MarshalIndent(discovery, "", "  ")
			if err := os.WriteFile(p+".discovery.json", append(d, '\n'), 0600); err != nil {
				t.Error(err)
			}
			b, _ := json.MarshalIndent(reports, "", "  ")
			if err := os.WriteFile(p, append(b, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	call := func(arm *taskArm, name string, args any, out any) {
		t.Helper()
		arm.Calls++
		b, _ := json.Marshal(args)
		tool := tools[name]
		if tool == nil {
			t.Fatal("tool missing", name)
		}
		wire, err := tool.Call(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		full, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		structured, err := json.Marshal(wire["structuredContent"])
		if err != nil {
			t.Fatal(err)
		}
		arm.WireBytes += len(full)
		arm.StructuredBytes += len(structured)
		if err := json.Unmarshal(structured, out); err != nil {
			t.Fatal(err)
		}
		if arm.Calls > tasks.PairedProtocol.Limits.Calls || arm.WireBytes > tasks.PairedProtocol.Limits.Bytes {
			t.Fatal("task budget exceeded", name, arm.Calls, arm.WireBytes)
		}
	}
	valid := func(status, snap, artifact string, truncated bool) {
		t.Helper()
		if (status != "ok" && status != "no_recorded_evidence" && status != "no_definition") || snap != resolved.ID || artifact != artifacts[0].SHA256 || truncated {
			t.Fatalf("task response failed status=%s snapshot=%s truncated=%v", status, snap, truncated)
		}
	}
	for _, task := range tasks.Tasks {
		d := taskDiscovery{Task: task.ID}
		contract := applicationSymbol(task.Target).ID
		var oldDiscovery mcp.CompilerContractResult
		var newDiscovery mcp.CompilerContractContextResult
		call(&d.Baseline, "compiler_contract_impact", map[string]any{"symbol_id": contract}, &oldDiscovery)
		call(&d.Candidate, "compiler_contract_context", map[string]any{"symbol_id": contract}, &newDiscovery)
		if oldDiscovery.Status != "context_required" || newDiscovery.Status != "context_required" || oldDiscovery.Truncated || newDiscovery.ContextsTruncated || oldDiscovery.SnapshotID != resolved.ID || newDiscovery.SnapshotID != resolved.ID || oldDiscovery.ArtifactSHA256 != artifacts[0].SHA256 || newDiscovery.ArtifactSHA256 != artifacts[0].SHA256 {
			t.Fatal("invalid or truncated discovery")
		}
		d.BaselineContexts, d.CandidateContexts = oldDiscovery.Contexts, newDiscovery.Contexts
		oldIDs, newIDs := map[string]bool{}, map[string]bool{}
		for _, c := range oldDiscovery.Contexts {
			oldIDs[c.ContextID] = true
		}
		for _, c := range newDiscovery.Contexts {
			newIDs[c.ContextID] = true
			if !oldIDs[c.ContextID] {
				d.DefinitionOnlyContexts = append(d.DefinitionOnlyContexts, c.ContextID)
			}
		}
		for id := range oldIDs {
			if !newIDs[id] {
				t.Error("discovery lost evidence context", id)
			}
		}
		for _, def := range task.Definitions {
			for _, id := range byProject[def.Project] {
				if !newIDs[id] {
					t.Error("discovery lost definition context", id)
				}
			}
		}
		discovery = append(discovery, d)
		for _, selection := range selections {
			pair := taskPair{Task: task.ID, Contexts: selection, Baseline: taskArm{Evidence: map[string]taskEvidence{}, Definitions: map[string]taskDefinition{}}, Candidate: taskArm{Evidence: map[string]taskEvidence{}, Definitions: map[string]taskDefinition{}}}
			contract := applicationSymbol(task.Target).ID
			var old mcp.CompilerContractResult
			call(&pair.Baseline, "compiler_contract_impact", map[string]any{"symbol_id": contract, "context_ids": selection, "limit": tasks.PairedProtocol.Limits.Evidence}, &old)
			valid(old.Status, old.SnapshotID, old.ArtifactSHA256, old.Truncated || !old.TotalIsExact)
			for _, p := range old.Paths {
				if p.Source.Symbol == nil {
					t.Fatal("missing API")
				}
				v := taskEvidence{Context: p.Source.ContextID, Repo: p.Source.Repo, Project: p.Source.Project, Snapshot: p.Source.SourceSnapshotID, Source: taskLocation(p.Source), API: *p.Source.Symbol, Owner: p.Owner, Roles: p.MatchedRoles, Fact: p.Fact}
				pair.Baseline.Evidence[taskKey(v)] = v
			}
			for _, id := range selection {
				var defs mcp.CompilerResult
				call(&pair.Baseline, "compiler_definitions", map[string]any{"symbol_id": contract, "context_id": id, "limit": tasks.PairedProtocol.Limits.Definitions}, &defs)
				valid(defs.Status, defs.SnapshotID, defs.ArtifactSHA256, defs.Truncated)
				for _, d := range defs.Results {
					if d.Symbol == nil || d.Symbol.ID != contract {
						t.Fatal("wrong definition symbol")
					}
					v := taskDefinition{d.ContextID, d.Repo, d.Project, d.SourceSnapshotID, taskLocation(d), *d.Symbol}
					pair.Baseline.Definitions[taskKey(v)] = v
				}
			}
			var compact mcp.CompilerContractContextResult
			call(&pair.Candidate, "compiler_contract_context", map[string]any{"symbol_id": contract, "context_ids": selection, "evidence_limit": tasks.PairedProtocol.Limits.Evidence, "definition_limit": tasks.PairedProtocol.Limits.Definitions}, &compact)
			valid(compact.Status, compact.SnapshotID, compact.ArtifactSHA256, compact.EvidenceTruncated || compact.DefinitionsTruncated || compact.ContextsTruncated || !compact.TotalIsExact)
			symbols := map[string]mcp.CompilerSymbol{}
			for _, s := range compact.Symbols {
				if applicationSymbol(applicationKey(s)).ID != s.ID {
					t.Fatal("symbol identity mismatch")
				}
				symbols[s.ID] = s
			}
			symbol := func(id string) mcp.CompilerSymbol {
				t.Helper()
				s, ok := symbols[id]
				if !ok {
					t.Fatal("missing descriptor", id)
				}
				return s
			}
			for _, g := range compact.Groups {
				for _, e := range g.Evidence {
					fact := mcp.CompilerDomainFact{Kind: e.Fact.Kind, Rule: e.Fact.Rule, EvidenceScope: e.Fact.EvidenceScope, Lifetime: e.Fact.Lifetime, Table: e.Fact.Table, Schema: e.Fact.Schema}
					for _, target := range e.Fact.Targets {
						fact.Targets = append(fact.Targets, mcp.CompilerDomainTarget{Role: target.Role, Symbol: symbol(target.SymbolID)})
					}
					v := taskEvidence{Context: g.ContextID, Repo: g.Repo, Project: g.Project, Snapshot: g.SourceSnapshotID, Source: e.Source, API: symbol(e.APISymbolID), Roles: e.MatchedRoles, Fact: fact}
					if g.OwnerID != "" {
						owner := symbol(g.OwnerID)
						v.Owner = &owner
					}
					pair.Candidate.Evidence[taskKey(v)] = v
				}
			}
			for _, d := range compact.Definitions {
				v := taskDefinition{d.ContextID, d.Repo, d.Project, d.SourceSnapshotID, d.Source, symbol(compact.ContractID)}
				pair.Candidate.Definitions[taskKey(v)] = v
			}
			pair.Equivalent = reflect.DeepEqual(pair.Baseline.Evidence, pair.Candidate.Evidence) && reflect.DeepEqual(pair.Baseline.Definitions, pair.Candidate.Definitions)
			reports = append(reports, pair)
			if !pair.Equivalent {
				t.Errorf("task %s evidence/definition parity differs", task.ID)
			}
			for _, arm := range []*taskArm{&pair.Baseline, &pair.Candidate} {
				verifyTaskSourceGold(t, task, gold, selection, byProject, arm)
			}
			t.Logf("task=%s variants=%v baseline_calls=%d candidate_calls=%d baseline_wire=%d candidate_wire=%d baseline_structured=%d candidate_structured=%d equivalent=%v", task.ID, selection, pair.Baseline.Calls, pair.Candidate.Calls, pair.Baseline.WireBytes, pair.Candidate.WireBytes, pair.Baseline.StructuredBytes, pair.Candidate.StructuredBytes, pair.Equivalent)
		}
	}
}

func verifyTaskSourceGold(t *testing.T, task applicationTask, gold applicationGold, selection []string, byProject map[string][]string, arm *taskArm) {
	t.Helper()
	selected := map[string]bool{}
	for _, id := range selection {
		selected[id] = true
	}
	expected := map[string]bool{}
	cases := map[string]applicationCase{}
	for _, c := range gold.Cases {
		cases[c.ID] = c
	}
	for _, id := range task.Expected {
		c := cases[id]
		for _, ctx := range byProject[c.Project] {
			if selected[ctx] {
				expected[id+"/"+ctx] = false
			}
		}
	}
	for _, e := range arm.Evidence {
		if !selected[e.Context] || e.Repo != gold.Repo || e.Fact.EvidenceScope != "compile_time" {
			t.Error("evidence context/scope mismatch")
		}
		o := applicationObservation{Rule: e.Fact.Rule, Project: e.Project, Path: e.Source.Path, Offset: e.Source.ByteOffset, Length: e.Source.ByteLength, SourceSHA: e.Source.RawSHA256, Kind: e.Fact.Kind, Lifetime: e.Fact.Lifetime, Table: e.Fact.Table, Schema: e.Fact.Schema}
		for _, target := range e.Fact.Targets {
			o.Targets = append(o.Targets, applicationTarget{Role: target.Role, Symbol: applicationKey(target.Symbol)})
		}
		if e.Owner != nil {
			k := applicationKey(*e.Owner)
			o.Owner = &k
		}
		matched := false
		for _, id := range task.Expected {
			c := cases[id]
			if applicationMatches(c, o) {
				key := id + "/" + e.Context
				if _, ok := expected[key]; ok {
					expected[key] = true
					matched = true
				}
			}
		}
		if !matched {
			t.Errorf("unexpected task evidence %+v", o)
		}
	}
	for id, found := range expected {
		if !found {
			t.Error("missing task source evidence", id)
		}
	}
	wantDefs := map[string]bool{}
	for i, d := range task.Definitions {
		for _, ctx := range byProject[d.Project] {
			if selected[ctx] {
				wantDefs[fmt.Sprintf("%d/%s", i, ctx)] = false
			}
		}
	}
	for _, d := range arm.Definitions {
		matched := false
		for i, w := range task.Definitions {
			if d.Project == w.Project && d.Source.Path == w.Path && d.Source.RawSHA256 == w.RawSHA256 && d.Source.ByteOffset == w.Offset && d.Source.ByteLength == w.Length && applicationKey(d.Symbol) == task.Target {
				key := fmt.Sprintf("%d/%s", i, d.Context)
				if _, ok := wantDefs[key]; ok {
					wantDefs[key] = true
					matched = true
				}
			}
		}
		if !matched {
			t.Error("unexpected task definition", d)
		}
	}
	for id, found := range wantDefs {
		if !found {
			t.Error("missing task definition", id)
		}
	}
}
