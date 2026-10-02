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

const webhooksGoldSHA = "8d703f654a091d8c803b173612d3af4401b7e1034a1eb2ba20d34d3f00ac4f62"

type webhooksCase struct {
	ID                   string              `json:"id"`
	Classification       string              `json:"classification"`
	Family               string              `json:"family"`
	Kind                 string              `json:"kind"`
	Rule                 string              `json:"rule"`
	Lifetime             string              `json:"lifetime"`
	Owner                *semantic.SymbolKey `json:"owner"`
	Target               semantic.SymbolKey  `json:"target"`
	ImplementationMethod semantic.SymbolKey  `json:"implementation_method"`
	InterfaceMember      semantic.SymbolKey  `json:"interface_member"`
	ImplementingType     semantic.SymbolKey  `json:"implementing_type"`
	Targets              []applicationTarget `json:"targets"`
	ForbiddenKinds       []string            `json:"forbidden_kinds"`
	Source               struct {
		Project string `json:"project"`
		Path    string `json:"path"`
		Offset  uint64 `json:"byte_offset"`
		Length  uint64 `json:"byte_length"`
		SHA     string `json:"source_sha256"`
	} `json:"source"`
}

// This successor replays the frozen source labels through the public evidence
// projection. The first holdout's six semantic successes and three declaration-
// only results are preserved with the original gate in its evidence directory.
func TestPublicWebhooksHoldout(t *testing.T) {
	index, sourceRoot, output := os.Getenv("MOEDEX_WEBHOOKS_INDEX"), os.Getenv("MOEDEX_WEBHOOKS_SOURCE"), os.Getenv("MOEDEX_WEBHOOKS_REPORT")
	if index == "" || sourceRoot == "" || output == "" {
		t.Skip("set MOEDEX_WEBHOOKS_INDEX/SOURCE/REPORT for independent published holdout")
	}
	workerVersion := os.Getenv("MOEDEX_WEBHOOKS_WORKER_VERSION")
	if workerVersion == "" {
		workerVersion = "6"
	}
	if workerVersion != "6" && workerVersion != "7" && workerVersion != "8" && workerVersion != "9" && (workerVersion != "10" && (workerVersion != "11" && (workerVersion != "12" && (workerVersion != "13" && (workerVersion != "14" && (workerVersion != "15" && (workerVersion != "16" && (workerVersion != "17" && workerVersion != "18")))))))) {
		t.Fatal("unsupported expected worker version")
	}
	goldPath := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "eshop-webhooks-holdout-20261001", "source-gold-v2.json")
	raw, err := os.ReadFile(goldPath)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != webhooksGoldSHA {
		t.Fatal("frozen Webhooks gold differs", err)
	}
	var gold struct {
		Repo          string `json:"repo"`
		Commit        string `json:"commit"`
		Configuration string `json:"configuration"`
		Framework     string `json:"framework"`
		Closure       struct {
			Projects []string          `json:"projects"`
			Hashes   map[string]string `json:"source_sha256"`
		} `json:"reviewed_closure"`
		Cases []webhooksCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &gold); err != nil {
		t.Fatal(err)
	}
	for path, want := range gold.Closure.Hashes {
		content, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(path)))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(content)) != want {
			t.Fatal("holdout source changed", path, err)
		}
	}
	resolved, err := snapshot.Resolve(index)
	if err != nil || resolved.Legacy || resolved.Manifest == nil {
		t.Fatal("immutable snapshot required", err)
	}
	artifacts := resolved.Manifest.Components[snapshot.SemanticComponent].Artifacts
	if len(artifacts) != 1 {
		t.Fatal("one semantic artifact required")
	}
	audit, err := semantic.Read(filepath.Join(resolved.Root, filepath.FromSlash(artifacts[0].Path)), semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range audit.Snapshots {
		if snapshot.Repo != gold.Repo || snapshot.Commit != gold.Commit {
			t.Fatal("holdout snapshot pin mismatch")
		}
	}
	byProject := map[string][]string{}
	for _, c := range audit.Contexts {
		var capture struct {
			Properties map[string]string `json:"global_properties"`
		}
		if json.Unmarshal([]byte(c.Capture), &capture) != nil || c.Status != "complete" || c.ExtractorVersion != workerVersion || capture.Properties["Configuration"] != gold.Configuration || capture.Properties["TargetFramework"] != gold.Framework {
			t.Fatal("holdout requires complete unchanged worker6 contexts")
		}
		byProject[c.Project] = append(byProject[c.Project], c.ID)
	}
	projects := make([]string, 0, len(byProject))
	for p := range byProject {
		projects = append(projects, p)
		sort.Strings(byProject[p])
	}
	sort.Strings(projects)
	sort.Strings(gold.Closure.Projects)
	if !reflect.DeepEqual(projects, gold.Closure.Projects) {
		t.Fatal("holdout project closure mismatch")
	}
	tools := map[string]mcp.ToolHandler{}
	for _, candidate := range mcp.CompilerTools(compilerLeaseHolder(t, resolved)) {
		tools[candidate.Name()] = candidate
	}
	if tools["compiler_binding_at"] == nil || tools["compiler_definitions"] == nil {
		t.Fatal("compiler binding/definition tools unavailable")
	}
	type query struct {
		Case          string             `json:"case"`
		Phase         string             `json:"phase"`
		CompleteBytes int                `json:"complete_bytes"`
		WireSHA       string             `json:"complete_sha256"`
		Result        mcp.CompilerResult `json:"result"`
	}
	report := struct {
		GoldSHA         string            `json:"gold_sha256"`
		ArtifactSHA     string            `json:"artifact_sha256"`
		Projection      string            `json:"projection"`
		Completed       bool              `json:"completed"`
		SemanticCases   int               `json:"public_semantic_cases"`
		DeclarationOnly int               `json:"implementation_declaration_only"`
		NegativeCases   int               `json:"negative_cases"`
		CoverageGaps    int               `json:"coverage_gaps"`
		Cases           map[string]string `json:"case_outcomes"`
		Queries         []query           `json:"queries"`
	}{GoldSHA: webhooksGoldSHA, ArtifactSHA: artifacts[0].SHA256, Projection: "implementation_evidence_v1_successor", Cases: map[string]string{}}
	defer func() {
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(output, append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	call := func(id, phase string, args map[string]any) mcp.CompilerResult {
		t.Helper()
		arg, _ := json.Marshal(args)
		tool := tools["compiler_binding_at"]
		if phase == "definition" {
			tool = tools["compiler_definitions"]
		}
		wire, err := tool.Call(context.Background(), arg)
		if err != nil {
			t.Fatal(err)
		}
		full, _ := json.Marshal(wire)
		content, _ := json.Marshal(wire["structuredContent"])
		var result mcp.CompilerResult
		if err := json.Unmarshal(content, &result); err != nil {
			t.Fatal(err)
		}
		report.Queries = append(report.Queries, query{id, phase, len(full), fmt.Sprintf("%x", sha256.Sum256(full)), result})
		if result.Truncated || result.SnapshotID != resolved.ID || result.ArtifactSHA256 != artifacts[0].SHA256 {
			t.Fatal("unbound or truncated holdout response")
		}
		return result
	}
	for _, want := range gold.Cases {
		args := map[string]any{"repo": gold.Repo, "path": want.Source.Path, "byte_offset": want.Source.Offset, "limit": 100}
		discovery := call(want.ID, "discovery", args)
		var foundContexts []string
		for _, c := range discovery.Contexts {
			if c.Project == want.Source.Project && c.RawSHA256 == want.Source.SHA && c.Path == want.Source.Path && c.Repo == gold.Repo {
				foundContexts = append(foundContexts, c.ContextID)
			}
		}
		sort.Strings(foundContexts)
		if len(foundContexts) == 0 || !reflect.DeepEqual(foundContexts, byProject[want.Source.Project]) {
			t.Fatal("incomplete holdout discovery", want.ID)
		}
		for _, id := range foundContexts {
			args["context_id"], args["raw_sha256"] = id, want.Source.SHA
			result := call(want.ID, "selected", args)
			if result.Status != "ok" || len(result.Results) != 1 {
				t.Fatal("expected unique exact binding", want.ID, result.Status, len(result.Results))
			}
			got := result.Results[0]
			if got.Project != want.Source.Project || got.Path != want.Source.Path || got.RawSHA256 != want.Source.SHA || got.ByteOffset != want.Source.Offset || got.ByteLength != want.Source.Length || got.ContextID != id || got.Repo != gold.Repo || got.BindingStatus != "resolved" || got.Symbol == nil || got.ExtractorVersion != workerVersion {
				t.Fatal("holdout binding provenance differs", want.ID)
			}
			if want.Owner != nil && got.EnclosingSymbolID != applicationSymbol(*want.Owner).ID {
				t.Fatal("holdout binding owner differs", want.ID)
			}
			if want.Classification == "negative" {
				for _, fact := range got.DomainFacts {
					if slices.Contains(want.ForbiddenKinds, fact.Kind) {
						t.Fatal("HTTP dispatch mislabeled messaging", want.ID)
					}
				}
			} else if want.Classification == "unsupported" {
				if len(got.DomainFacts) != 0 {
					t.Fatal("coverage gap gained unexpected assertion; independent review required", want.ID)
				}
			} else {
				switch want.Family {
				case "domain":
					if len(got.DomainFacts) != 1 {
						t.Fatal("expected exactly one domain assertion", want.ID)
					}
					fact := got.DomainFacts[0]
					var targets []applicationTarget
					for _, target := range fact.Targets {
						targets = append(targets, applicationTarget{target.Role, applicationKey(target.Symbol)})
					}
					if fact.Kind != want.Kind || fact.Rule != want.Rule || fact.EvidenceScope != "compile_time" || fact.Lifetime != want.Lifetime || !reflect.DeepEqual(targets, want.Targets) {
						t.Fatal("holdout domain assertion differs", want.ID)
					}
				case "resolved_call":
					if applicationKey(*got.Symbol) != want.Target || got.Role != "reference" || got.ReferenceKind != "invocation" {
						t.Fatal("holdout interface call differs", want.ID)
					}
				case "implementation":
					if applicationKey(*got.Symbol) != want.ImplementationMethod || got.Role != "declaration" {
						t.Fatal("holdout implementation declaration differs", want.ID)
					}
					if len(got.ImplementationFacts) != 1 || len(got.ImplementationSymbols) != 2 {
						t.Fatal("expected exact public implementation evidence", want.ID)
					}
					fact := got.ImplementationFacts[0]
					if fact.Kind != want.Kind || fact.Rule != want.Rule || fact.EvidenceScope != "compile_time" || fact.InterfaceSymbolID != applicationSymbol(want.InterfaceMember).ID || fact.ImplementingTypeSymbolID != applicationSymbol(want.ImplementingType).ID {
						t.Fatal("public implementation relationship differs", want.ID)
					}
					symbols := map[string]semantic.SymbolKey{}
					for _, symbol := range got.ImplementationSymbols {
						key := applicationKey(symbol)
						if applicationSymbol(key).ID != symbol.ID {
							t.Fatal("implementation symbol identity differs", want.ID)
						}
						symbols[symbol.ID] = key
					}
					if symbols[fact.InterfaceSymbolID] != want.InterfaceMember || symbols[fact.ImplementingTypeSymbolID] != want.ImplementingType {
						t.Fatal("implementation symbol key differs", want.ID)
					}
					definition := call(want.ID, "definition", map[string]any{"repo": gold.Repo, "context_id": id, "symbol_id": got.Symbol.ID, "limit": 100})
					if definition.Status != "ok" || len(definition.Results) != 1 || !reflect.DeepEqual(definition.Results[0], got) {
						t.Fatal("definition lookup lost implementation evidence", want.ID)
					}
				default:
					t.Fatal("unknown frozen family")
				}
			}
		}
		switch {
		case want.Classification == "negative":
			report.NegativeCases++
			report.Cases[want.ID] = "no_forbidden_assertion_observed"
		case want.Classification == "unsupported":
			report.CoverageGaps++
			report.Cases[want.ID] = "coverage_gap"
		default:
			report.SemanticCases++
			report.Cases[want.ID] = "supported_exact_occurrence"
		}
	}
	report.Completed = true
}
