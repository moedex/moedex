package servecmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"moedex/internal/mcp"
	"moedex/internal/semantic"
	"moedex/internal/snapshot"
)

type applicationGold struct {
	PredecessorSHA256 string               `json:"predecessor_sha256,omitempty"`
	Upstream          string               `json:"upstream"`
	FrozenAt          string               `json:"frozen_at_utc"`
	Provenance        string               `json:"provenance"`
	ScopeNotes        []string             `json:"scope_notes"`
	Version           int                  `json:"version"`
	Repo              string               `json:"repo"`
	Commit            string               `json:"commit"`
	Contexts          []applicationContext `json:"contexts"`
	Cases             []applicationCase    `json:"cases"`
	ReviewedClosure   struct {
		Projects     []string          `json:"projects"`
		Paths        []string          `json:"paths"`
		SourceHashes map[string]string `json:"source_sha256"`
	} `json:"reviewed_closure"`
}
type applicationContext struct {
	Project       string `json:"project"`
	Configuration string `json:"configuration"`
	Framework     string `json:"framework"`
}
type applicationTarget struct {
	Role   string             `json:"role"`
	Symbol semantic.SymbolKey `json:"symbol"`
}
type applicationCase struct {
	Rule             string              `json:"rule,omitempty"`
	ID               string              `json:"id"`
	Classification   string              `json:"classification"`
	Reason           string              `json:"reason,omitempty"`
	Kind             string              `json:"kind"`
	Target           semantic.SymbolKey  `json:"target"`
	ExpectedTargets  []applicationTarget `json:"expected_targets"`
	MatchedRoles     []string            `json:"matched_roles,omitempty"`
	Owner            *semantic.SymbolKey `json:"owner,omitempty"`
	OwnerExpectation string              `json:"owner_expectation,omitempty"`
	Project          string              `json:"project"`
	Path             string              `json:"path"`
	ByteOffset       uint64              `json:"byte_offset"`
	ByteLength       uint64              `json:"byte_length"`
	SourceSHA256     string              `json:"source_sha256"`
	Anchor           string              `json:"anchor"`
	Token            string              `json:"token"`
	Lifetime         string              `json:"lifetime,omitempty"`
	Table            string              `json:"table,omitempty"`
	Schema           string              `json:"schema,omitempty"`
}
type applicationObservation struct {
	Rule      string              `json:"rule"`
	Project   string              `json:"project"`
	Path      string              `json:"path"`
	Offset    uint64              `json:"byte_offset"`
	Length    uint64              `json:"byte_length"`
	SourceSHA string              `json:"source_sha256"`
	Kind      string              `json:"kind"`
	Targets   []applicationTarget `json:"targets"`
	Owner     *semantic.SymbolKey `json:"owner,omitempty"`
	Lifetime  string              `json:"lifetime,omitempty"`
	Table     string              `json:"table,omitempty"`
	Schema    string              `json:"schema,omitempty"`
}
type applicationCaseResult struct {
	ID               string              `json:"id"`
	Classification   string              `json:"classification"`
	Reason           string              `json:"reason,omitempty"`
	Observed         bool                `json:"observed"`
	ExpectedContexts []string            `json:"expected_contexts"`
	ObservedContexts []string            `json:"observed_contexts"`
	MissingContexts  []string            `json:"missing_contexts"`
	BindingStatuses  map[string]string   `json:"binding_statuses"`
	BindingStates    map[string][]string `json:"binding_states"`
}
type applicationObservedRecord struct {
	Observation applicationObservation `json:"observation"`
	Contexts    []string               `json:"contexts"`
}
type applicationReport struct {
	AuditFacts                int                         `json:"audit_facts"`
	ServedFacts               int                         `json:"served_facts"`
	MissingAuditFacts         []string                    `json:"missing_audit_facts"`
	UnexpectedServedFacts     []string                    `json:"unexpected_served_facts"`
	OwnerDescriptorAssertions int                         `json:"owner_descriptor_assertions"`
	OwnerUnasserted           int                         `json:"owner_unasserted"`
	Observations              []applicationObservedRecord `json:"observations"`
	EvaluationCompleted       bool                        `json:"evaluation_completed"`
	Failure                   string                      `json:"failure,omitempty"`
	GoldSHA256                string                      `json:"gold_sha256"`
	Repo                      string                      `json:"repo"`
	Commit                    string                      `json:"commit"`
	SnapshotID                string                      `json:"snapshot_id"`
	ArtifactSHA256            string                      `json:"artifact_sha256"`
	CorpusFingerprint         string                      `json:"corpus_fingerprint"`
	ScoringPolicy             string                      `json:"scoring_policy"`
	SupportedExpected         int                         `json:"supported_expected"`
	TruePositive              int                         `json:"true_positive"`
	FalseNegative             int                         `json:"false_negative"`
	FalsePositive             int                         `json:"false_positive"`
	Precision                 float64                     `json:"precision"`
	Recall                    float64                     `json:"recall"`
	UnsupportedPatterns       int                         `json:"unsupported_patterns"`
	UnsupportedObserved       int                         `json:"unsupported_observed"`
	UnsupportedMissed         int                         `json:"unsupported_missed"`
	NegativeCases             int                         `json:"negative_cases"`
	NegativeViolations        int                         `json:"negative_violations"`
	ContextExpected           int                         `json:"context_expected"`
	ContextObserved           int                         `json:"context_observed"`
	ContextMissed             int                         `json:"context_missed"`
	Cases                     []applicationCaseResult     `json:"cases"`
	Unexpected                []applicationObservation    `json:"unexpected_observations"`
	OutsideClosure            []applicationObservation    `json:"outside_reviewed_closure"`
	QueryHashes               map[string]string           `json:"query_hashes"`
	ContextProjects           map[string]string           `json:"context_projects"`
}

func applicationFactSignature(occurrence string, fact semantic.DomainFact) string {
	b, _ := json.Marshal(fact)
	return occurrence + "/" + string(b)
}

func applicationSymbol(k semantic.SymbolKey) semantic.Symbol {
	s := semantic.Symbol{Key: k}
	s.ID = s.ComputeID()
	return s
}
func applicationKey(s mcp.CompilerSymbol) semantic.SymbolKey {
	return semantic.SymbolKey{Language: s.Language, NamespaceKind: s.NamespaceKind, Namespace: s.Namespace, Descriptor: s.Descriptor, DescriptorKind: s.DescriptorKind}
}
func applicationSignature(o applicationObservation) string { b, _ := json.Marshal(o); return string(b) }
func applicationExpected(c applicationCase) applicationObservation {
	rule := c.Rule
	if rule == "" {
		rule = "csharp-framework-v1"
	}
	return applicationObservation{Rule: rule, Project: c.Project, Path: c.Path, Offset: c.ByteOffset, Length: c.ByteLength, SourceSHA: c.SourceSHA256, Kind: c.Kind, Targets: c.ExpectedTargets, Owner: c.Owner, Lifetime: c.Lifetime, Table: c.Table, Schema: c.Schema}
}
func applicationSite(o applicationObservation) string {
	return fmt.Sprintf("%s\x00%s\x00%d", o.Project, o.Path, o.Offset)
}

// This source-authored oracle consumes an already published production snapshot.
// Capture/import/composition/publication remain owned by their existing commands.
func TestPublicApplicationImpact(t *testing.T) {
	indexDir, goldPath, sourceRoot := os.Getenv("MOEDEX_APPLICATION_INDEX"), os.Getenv("MOEDEX_APPLICATION_GOLD"), os.Getenv("MOEDEX_APPLICATION_SOURCE_ROOT")
	if indexDir == "" || goldPath == "" || sourceRoot == "" {
		t.Skip("set MOEDEX_APPLICATION_INDEX/GOLD/SOURCE_ROOT for pinned real-application gate")
	}
	goldBytes, err := os.ReadFile(goldPath)
	if err != nil {
		t.Fatal(err)
	}
	var gold applicationGold
	dec := json.NewDecoder(bytes.NewReader(goldBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&gold); err != nil {
		t.Fatal(err)
	}
	if (gold.Version != 1 && gold.Version != 2 && gold.Version != 3 && gold.Version != 4) || gold.Repo == "" || gold.Commit == "" || len(gold.Contexts) == 0 || len(gold.Cases) == 0 {
		t.Fatal("incomplete frozen gold")
	}
	report := applicationReport{Repo: gold.Repo, Commit: gold.Commit, GoldSHA256: fmt.Sprintf("%x", sha256.Sum256(goldBytes)), ScoringPolicy: "Unique source observations; captured context alternatives queried separately and reported separately. Unsupported patterns are coverage gaps, never successful negative recall. Exact qualified identities only; no runtime edges.", QueryHashes: map[string]string{}, ContextProjects: map[string]string{}}
	defer func() {
		if !report.EvaluationCompleted {
			report.Failure = "acceptance stopped before completed scoring; inspect test output"
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if path := os.Getenv("MOEDEX_APPLICATION_REPORT"); path != "" {
			if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	resolved, err := snapshot.Resolve(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Legacy || resolved.Manifest == nil {
		t.Fatal("requires published immutable snapshot")
	}
	component := resolved.Manifest.Components[snapshot.SemanticComponent]
	if len(component.Artifacts) != 1 {
		t.Fatal("missing audit artifact")
	}
	report.SnapshotID = resolved.ID
	report.ArtifactSHA256 = component.Artifacts[0].SHA256
	report.CorpusFingerprint = resolved.Manifest.CorpusFingerprint
	audit, err := semantic.Read(filepath.Join(resolved.Root, filepath.FromSlash(component.Artifacts[0].Path)), semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range audit.Snapshots {
		if s.Repo != gold.Repo || s.Commit != gold.Commit {
			t.Fatalf("capture pin differs: %+v", s)
		}
	}
	byProject := map[string][]string{}
	selected := map[string]bool{}
	for _, want := range gold.Contexts {
		for _, c := range audit.Contexts {
			if c.Project != want.Project {
				continue
			}
			var capture struct {
				GlobalProperties map[string]string `json:"global_properties"`
			}
			if err := json.Unmarshal([]byte(c.Capture), &capture); err != nil {
				t.Fatal(err)
			}
			if capture.GlobalProperties["Configuration"] != want.Configuration || capture.GlobalProperties["TargetFramework"] != want.Framework {
				continue
			}
			if !selected[c.ID] {
				selected[c.ID] = true
				byProject[c.Project] = append(byProject[c.Project], c.ID)
				report.ContextProjects[c.ID] = c.Project
			}
		}
		if len(byProject[want.Project]) == 0 {
			t.Fatalf("gold context not captured: %+v", want)
		}
	}
	for project := range byProject {
		sort.Strings(byProject[project])
	}
	closurePaths, closureProjects := map[string]bool{}, map[string]bool{}
	for _, p := range gold.ReviewedClosure.Paths {
		closurePaths[p] = true
	}
	for _, p := range gold.ReviewedClosure.Projects {
		closureProjects[p] = true
	}
	readSource := func(path string) []byte {
		t.Helper()
		if !fs.ValidPath(path) {
			t.Fatalf("invalid source path %q", path)
		}
		b, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, path := range gold.ReviewedClosure.Paths {
		raw := readSource(path)
		if gold.ReviewedClosure.SourceHashes[path] == "" || gold.ReviewedClosure.SourceHashes[path] != fmt.Sprintf("%x", sha256.Sum256(raw)) {
			t.Fatalf("reviewed closure bytes differ: %s", path)
		}
	}
	ids := map[string]bool{}
	targets := map[string]bool{}
	for _, c := range gold.Cases {
		if ids[c.ID] || c.ID == "" {
			t.Fatal("duplicate/empty gold case")
		}
		ids[c.ID] = true
		if c.Classification != "supported" && c.Classification != "unsupported" && c.Classification != "negative" {
			t.Fatalf("invalid classification %s", c.ID)
		}
		if !closurePaths[c.Path] || !closureProjects[c.Project] || len(byProject[c.Project]) == 0 {
			t.Fatalf("case outside reviewed/context closure: %s", c.ID)
		}
		raw := readSource(c.Path)
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != c.SourceSHA256 || c.ByteOffset > uint64(len(raw)) || c.ByteLength > uint64(len(raw))-c.ByteOffset || string(raw[c.ByteOffset:c.ByteOffset+c.ByteLength]) != c.Token {
			t.Fatalf("frozen source/span differs: %s", c.ID)
		}
		if c.Anchor == "" || bytes.Count(raw, []byte(c.Anchor)) != 1 || c.ByteOffset < uint64(bytes.Index(raw, []byte(c.Anchor))) || c.ByteOffset+c.ByteLength > uint64(bytes.Index(raw, []byte(c.Anchor))+len(c.Anchor)) {
			t.Fatalf("anchor must uniquely contain token: %s", c.ID)
		}
		if c.Classification == "supported" && (len(c.ExpectedTargets) == 0 || (c.Owner == nil && c.OwnerExpectation == "")) {
			t.Fatalf("supported expectation lacks target/owner: %s", c.ID)
		}
		if c.Target.Descriptor != "" {
			targets[applicationSymbol(c.Target).ID] = true
		}
	}
	occurrences := map[string]semantic.Occurrence{}
	for _, o := range audit.Occurrences {
		occurrences[o.ID] = o
	}
	auditFacts := map[string]bool{}
	servedFacts := map[string]bool{}
	// Observed targets only expand the evaluation query inventory; they never
	// supply expected labels, so a newly emitted wrong target is still scored.
	for _, b := range audit.Bindings {
		if selected[occurrences[b.OccurrenceID].ContextID] {
			for _, f := range b.DomainFacts {
				auditFacts[applicationFactSignature(b.OccurrenceID, f)] = true
				for _, target := range f.Targets {
					targets[target.SymbolID] = true
				}
			}
		}
	}
	holder := compilerLeaseHolder(t, resolved)
	tools := map[string]mcp.ToolHandler{}
	for _, tool := range mcp.CompilerTools(holder) {
		tools[tool.Name()] = tool
	}
	call := func(name, label string, args any, out any) {
		t.Helper()
		b, _ := json.Marshal(args)
		wire, err := tools[name].Call(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(wire["structuredContent"])
		if err != nil {
			t.Fatal(err)
		}
		report.QueryHashes[label] = fmt.Sprintf("%x", sha256.Sum256(payload))
		if err := json.Unmarshal(payload, out); err != nil {
			t.Fatal(err)
		}
	}
	observations := map[string]applicationObservation{}
	observedContexts := map[string]map[string]bool{}
	targetIDs := make([]string, 0, len(targets))
	for id := range targets {
		targetIDs = append(targetIDs, id)
	}
	sort.Strings(targetIDs)
	contextIDs := make([]string, 0, len(selected))
	for id := range selected {
		contextIDs = append(contextIDs, id)
	}
	sort.Strings(contextIDs)
	for _, target := range targetIDs {
		for _, contextID := range contextIDs {
			var result mcp.CompilerContractResult
			call("compiler_contract_impact", target+"/"+contextID, map[string]any{"symbol_id": target, "context_ids": []string{contextID}, "limit": 100}, &result)
			if result.Status != "ok" && result.Status != "no_recorded_evidence" {
				t.Fatalf("contract lookup %s", result.Status)
			}
			if result.Truncated || !result.TotalIsExact || result.SnapshotID != report.SnapshotID || result.ArtifactSHA256 != report.ArtifactSHA256 || result.EvidenceScope != "compile_time" {
				t.Fatal("inexact or unbound contract result")
			}
			for _, p := range result.Paths {
				if p.Contract.ID != target || p.Source.ContextID != contextID || p.Source.Repo != gold.Repo || p.Fact.EvidenceScope != "compile_time" || (p.Fact.Rule != "csharp-framework-v1" && p.Fact.Rule != "csharp-framework-v2" && p.Fact.Rule != "csharp-framework-v3" && p.Fact.Rule != "csharp-framework-v4") {
					t.Fatal("false identity/context join")
				}
				if p.Owner != nil {
					if p.Owner.ID != p.Source.EnclosingSymbolID || applicationSymbol(applicationKey(*p.Owner)).ID != p.Owner.ID {
						t.Fatal("owner differs from binding provenance")
					}
				} else if p.Source.EnclosingSymbolID != "" {
					t.Fatal("bound owner descriptor missing")
				}
				fact := semantic.DomainFact{Kind: p.Fact.Kind, Rule: p.Fact.Rule, EvidenceScope: p.Fact.EvidenceScope, Lifetime: p.Fact.Lifetime, Table: p.Fact.Table, Schema: p.Fact.Schema}
				for _, dt := range p.Fact.Targets {
					fact.Targets = append(fact.Targets, semantic.DomainTarget{Role: dt.Role, SymbolID: dt.Symbol.ID})
				}
				servedFacts[applicationFactSignature(p.Source.OccurrenceID, fact)] = true
				var roles []string
				for _, dt := range p.Fact.Targets {
					if applicationSymbol(applicationKey(dt.Symbol)).ID != dt.Symbol.ID {
						t.Fatal("domain symbol key identity differs")
					}
					if dt.Symbol.ID == target {
						roles = append(roles, dt.Role)
					}
				}
				if !slices.Equal(roles, p.MatchedRoles) {
					t.Fatal("contract matched roles differ from exact target identity")
				}
				raw := readSource(p.Source.Path)
				if p.Source.RawSHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
					t.Fatal("evidence source digest differs")
				}
				o := applicationObservation{Rule: p.Fact.Rule, Project: p.Source.Project, Path: p.Source.Path, Offset: p.Source.ByteOffset, Length: p.Source.ByteLength, SourceSHA: p.Source.RawSHA256, Kind: p.Fact.Kind, Lifetime: p.Fact.Lifetime, Table: p.Fact.Table, Schema: p.Fact.Schema}
				for _, target := range p.Fact.Targets {
					o.Targets = append(o.Targets, applicationTarget{Role: target.Role, Symbol: applicationKey(target.Symbol)})
				}
				if p.Owner != nil {
					key := applicationKey(*p.Owner)
					o.Owner = &key
				}
				signature := applicationSignature(o)
				observations[signature] = o
				if observedContexts[signature] == nil {
					observedContexts[signature] = map[string]bool{}
				}
				observedContexts[signature][contextID] = true
			}
		}
	}
	report.AuditFacts, report.ServedFacts = len(auditFacts), len(servedFacts)
	for key := range auditFacts {
		if !servedFacts[key] {
			report.MissingAuditFacts = append(report.MissingAuditFacts, key)
		}
	}
	for key := range servedFacts {
		if !auditFacts[key] {
			report.UnexpectedServedFacts = append(report.UnexpectedServedFacts, key)
		}
	}
	sort.Strings(report.MissingAuditFacts)
	sort.Strings(report.UnexpectedServedFacts)
	scoreApplication(gold, observations, observedContexts, byProject, &report)
	for ci, c := range gold.Cases {
		for _, contextID := range byProject[c.Project] {
			var binding mcp.CompilerResult
			call("compiler_binding_at", c.ID+"/"+contextID, map[string]any{"repo": gold.Repo, "path": c.Path, "byte_offset": c.ByteOffset, "context_id": contextID, "raw_sha256": c.SourceSHA256, "limit": 100}, &binding)
			if binding.SnapshotID != report.SnapshotID || binding.ArtifactSHA256 != report.ArtifactSHA256 || binding.Truncated {
				t.Fatal("binding provenance/inexactness")
			}
			report.Cases[ci].BindingStatuses[contextID] = binding.Status
			states := map[string]bool{}
			for _, row := range binding.Results {
				if row.Path != c.Path || row.ByteOffset != c.ByteOffset || row.RawSHA256 != c.SourceSHA256 || row.ContextID != contextID {
					t.Fatal("binding row scope mismatch")
				}
				states[row.BindingStatus] = true
			}
			for state := range states {
				report.Cases[ci].BindingStates[contextID] = append(report.Cases[ci].BindingStates[contextID], state)
			}
			sort.Strings(report.Cases[ci].BindingStates[contextID])
		}
	}
	report.EvaluationCompleted = true
	t.Logf("real_application supported=%d TP=%d FN=%d FP=%d precision=%.4f recall=%.4f context_misses=%d unsupported=%d unsupported_missed=%d negative_violations=%d outside_closure=%d", report.SupportedExpected, report.TruePositive, report.FalseNegative, report.FalsePositive, report.Precision, report.Recall, report.ContextMissed, report.UnsupportedPatterns, report.UnsupportedMissed, report.NegativeViolations, len(report.OutsideClosure))
	if len(report.MissingAuditFacts) > 0 || len(report.UnexpectedServedFacts) > 0 || report.FalseNegative > 0 || report.FalsePositive > 0 || report.ContextMissed > 0 || report.NegativeViolations > 0 || len(report.OutsideClosure) > 0 {
		t.Error("real application source gold mismatch; inspect persisted report")
	}
}

func TestApplicationObservationIdentitySeparatesNamespaceOwnerAndSource(t *testing.T) {
	base := applicationObservation{Project: "A.csproj", Path: "A.cs", Offset: 7, Kind: "message_publish", Targets: []applicationTarget{{Role: "message", Symbol: semantic.SymbolKey{Namespace: "repo/A", Descriptor: "T:Notice"}}}}
	a := applicationSignature(base)
	changed := base
	changed.Targets = append([]applicationTarget(nil), base.Targets...)
	changed.Targets[0].Symbol.Namespace = "repo/B"
	if a == applicationSignature(changed) {
		t.Fatal("same-name contract identities joined")
	}
	changed = base
	changed.Offset++
	if a == applicationSignature(changed) {
		t.Fatal("source sites joined")
	}
	if strings.Contains(a, "context_id") {
		t.Fatal("source observation score duplicates captured contexts")
	}
}

func applicationMatches(c applicationCase, o applicationObservation) bool {
	if c.Owner == nil {
		o.Owner = nil
	} // Explicitly unasserted synthetic entrypoint owner.
	return applicationSignature(applicationExpected(c)) == applicationSignature(o)
}
func scoreApplication(gold applicationGold, observations map[string]applicationObservation, observedContexts map[string]map[string]bool, byProject map[string][]string, report *applicationReport) {
	accepted := map[string]bool{}
	for _, c := range gold.Cases {
		contexts := map[string]bool{}
		cr := applicationCaseResult{ID: c.ID, Classification: c.Classification, Reason: c.Reason, ExpectedContexts: append([]string(nil), byProject[c.Project]...), BindingStatuses: map[string]string{}, BindingStates: map[string][]string{}}
		for sig, o := range observations {
			matches := applicationMatches(c, o)
			if c.Classification == "unsupported" && len(c.ExpectedTargets) == 0 {
				matches = applicationSite(o) == applicationSite(applicationExpected(c)) && o.Kind == c.Kind
				if matches && c.Target.Descriptor != "" {
					matches = false
					for _, target := range o.Targets {
						if target.Symbol == c.Target {
							matches = true
						}
					}
				}
			}
			if matches {
				if c.Classification != "negative" && len(c.ExpectedTargets) > 0 {
					accepted[sig] = true
				}
				for id := range observedContexts[sig] {
					contexts[id] = true
				}
			}
		}
		for _, id := range cr.ExpectedContexts {
			if contexts[id] {
				cr.ObservedContexts = append(cr.ObservedContexts, id)
			} else {
				cr.MissingContexts = append(cr.MissingContexts, id)
			}
		}
		cr.Observed = len(cr.ObservedContexts) > 0
		switch c.Classification {
		case "supported":
			if c.Owner != nil {
				report.OwnerDescriptorAssertions++
			} else {
				report.OwnerUnasserted++
			}
			report.SupportedExpected++
			report.ContextExpected += len(cr.ExpectedContexts)
			report.ContextObserved += len(cr.ObservedContexts)
			report.ContextMissed += len(cr.MissingContexts)
			if cr.Observed {
				report.TruePositive++
			} else {
				report.FalseNegative++
			}
		case "unsupported":
			report.UnsupportedPatterns++
			if cr.Observed {
				report.UnsupportedObserved++
			} else {
				report.UnsupportedMissed++
			}
		case "negative":
			report.NegativeCases++
			for _, o := range observations {
				if applicationSite(o) == applicationSite(applicationExpected(c)) {
					report.NegativeViolations++
					break
				}
			}
		}
		report.Cases = append(report.Cases, cr)
	}
	paths, projects := map[string]bool{}, map[string]bool{}
	for _, p := range gold.ReviewedClosure.Paths {
		paths[p] = true
	}
	for _, p := range gold.ReviewedClosure.Projects {
		projects[p] = true
	}
	signatures := make([]string, 0, len(observations))
	for s := range observations {
		signatures = append(signatures, s)
	}
	sort.Strings(signatures)
	for _, sig := range signatures {
		o := observations[sig]
		captured := []string{}
		for id := range observedContexts[sig] {
			captured = append(captured, id)
		}
		sort.Strings(captured)
		report.Observations = append(report.Observations, applicationObservedRecord{Observation: o, Contexts: captured})
		if !projects[o.Project] || !paths[o.Path] {
			report.OutsideClosure = append(report.OutsideClosure, o)
			continue
		}
		if !accepted[sig] {
			report.FalsePositive++
			report.Unexpected = append(report.Unexpected, o)
		}
	}
	if report.TruePositive+report.FalsePositive > 0 {
		report.Precision = float64(report.TruePositive) / float64(report.TruePositive+report.FalsePositive)
	}
	if report.SupportedExpected > 0 {
		report.Recall = float64(report.TruePositive) / float64(report.SupportedExpected)
	}
}
