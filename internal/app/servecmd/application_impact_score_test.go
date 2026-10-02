package servecmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/semantic"
)

func TestApplicationScoringSeparatesMissesExtrasGapsAndContexts(t *testing.T) {
	key := semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/Contracts.csproj", Descriptor: "T:Notice", DescriptorKind: "documentation_comment_id"}
	supported := applicationCase{ID: "supported", Classification: "supported", Project: "App.csproj", Path: "App.cs", ByteOffset: 1, ByteLength: 1, SourceSHA256: "sha", Kind: "message_publish", Target: key, ExpectedTargets: []applicationTarget{{Role: "message", Symbol: key}}, OwnerExpectation: "synthetic owner"}
	missing := supported
	missing.ID = "missing"
	missing.ByteOffset = 2
	unsupported := supported
	unsupported.ID = "unsupported"
	unsupported.Classification = "unsupported"
	unsupported.ByteOffset = 3
	unsupported.ExpectedTargets = nil
	negative := supported
	negative.ID = "negative"
	negative.Classification = "negative"
	negative.ByteOffset = 4
	gold := applicationGold{Cases: []applicationCase{supported, missing, unsupported, negative}}
	gold.ReviewedClosure.Projects = []string{"App.csproj"}
	gold.ReviewedClosure.Paths = []string{"App.cs"}
	observations := map[string]applicationObservation{}
	contexts := map[string]map[string]bool{}
	add := func(c applicationCase, ids ...string) {
		o := applicationExpected(c)
		if len(o.Targets) == 0 {
			o.Targets = supported.ExpectedTargets
		}
		sig := applicationSignature(o)
		observations[sig] = o
		if contexts[sig] == nil {
			contexts[sig] = map[string]bool{}
		}
		for _, id := range ids {
			contexts[sig][id] = true
		}
	}
	add(supported, "debug", "alternate")
	add(supported, "debug")   // Repeated target/context query does not inflate source precision.
	add(unsupported, "debug") // Undeclared fact shape remains an unexpected output, never a successful negative.
	add(negative, "debug")
	extra := supported
	extra.ByteOffset = 5
	add(extra, "debug")
	var report applicationReport
	scoreApplication(gold, observations, contexts, map[string][]string{"App.csproj": {"debug", "alternate"}}, &report)
	if report.SupportedExpected != 2 || report.TruePositive != 1 || report.FalseNegative != 1 || report.FalsePositive != 3 {
		t.Fatalf("wrong supported metrics %+v", report)
	}
	if report.UnsupportedPatterns != 1 || report.UnsupportedObserved != 1 || report.UnsupportedMissed != 0 || report.NegativeViolations != 1 {
		t.Fatalf("gaps hidden %+v", report)
	}
	if report.ContextExpected != 4 || report.ContextObserved != 2 || report.ContextMissed != 2 || report.Precision != 0.25 || report.Recall != 0.5 {
		t.Fatalf("context/ratio inflation %+v", report)
	}
	if len(report.Observations) != 4 || len(report.Cases[0].ObservedContexts) != 2 {
		t.Fatal("duplicate query inflated observation count")
	}
}
func TestApplicationScoringWrongQualifiedTargetIsFalseJoin(t *testing.T) {
	want := applicationCase{ID: "one", Classification: "supported", Project: "A", Path: "A.cs", Kind: "message_publish", ExpectedTargets: []applicationTarget{{Role: "message", Symbol: semantic.SymbolKey{Namespace: "contracts-one", Descriptor: "T:Notice"}}}}
	actual := applicationExpected(want)
	actual.Targets = append([]applicationTarget(nil), actual.Targets...)
	actual.Targets[0].Symbol.Namespace = "contracts-two"
	sig := applicationSignature(actual)
	gold := applicationGold{Cases: []applicationCase{want}}
	gold.ReviewedClosure.Paths = []string{"A.cs"}
	gold.ReviewedClosure.Projects = []string{"A"}
	var report applicationReport
	scoreApplication(gold, map[string]applicationObservation{sig: actual}, map[string]map[string]bool{sig: {"ctx": true}}, map[string][]string{"A": {"ctx"}}, &report)
	if report.TruePositive != 0 || report.FalsePositive != 1 || report.FalseNegative != 1 {
		t.Fatalf("short-name false join hidden %+v", report)
	}
}
func TestApplicationFrozenGoldSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "application-outbox-20261001", "source-gold.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gold applicationGold
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&gold); err != nil {
		t.Fatal(err)
	}
	if gold.Version != 1 || len(gold.Cases) != 19 || len(gold.ReviewedClosure.SourceHashes) != 26 {
		t.Fatal("unexpected frozen fixture shape")
	}
}

func TestApplicationScoringPartialContextCannotHideMiss(t *testing.T) {
	c := applicationCase{ID: "one", Classification: "supported", Project: "A", Path: "A.cs", Kind: "message_publish", ExpectedTargets: []applicationTarget{{Role: "message", Symbol: semantic.SymbolKey{Descriptor: "T:N"}}}}
	o := applicationExpected(c)
	sig := applicationSignature(o)
	gold := applicationGold{Cases: []applicationCase{c}}
	gold.ReviewedClosure.Paths = []string{"A.cs"}
	gold.ReviewedClosure.Projects = []string{"A"}
	var report applicationReport
	scoreApplication(gold, map[string]applicationObservation{sig: o}, map[string]map[string]bool{sig: {"debug": true}}, map[string][]string{"A": {"debug", "alternate"}}, &report)
	if report.TruePositive != 1 || report.FalseNegative != 0 || report.ContextObserved != 1 || report.ContextMissed != 1 || len(report.Cases[0].MissingContexts) != 1 || report.Cases[0].MissingContexts[0] != "alternate" {
		t.Fatalf("partial context hidden: %+v", report)
	}
}

func TestApplicationSuccessorGoldPreservesBaselineAndOnlyPromotesEF(t *testing.T) {
	base := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "application-outbox-20261001")
	read := func(name string) ([]byte, applicationGold) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		var g applicationGold
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err := d.Decode(&g); err != nil {
			t.Fatal(err)
		}
		return b, g
	}
	oldBytes, old := read("source-gold.json")
	_, next := read("source-gold-v2.json")
	if got := fmt.Sprintf("%x", sha256.Sum256(oldBytes)); got != "b91cdf38bbeb036274ff689618056e43c712c62f73a7f659726869a1437c3605" || next.PredecessorSHA256 != got {
		t.Fatal("baseline changed")
	}
	if next.Version != 2 || len(old.Cases) != len(next.Cases) {
		t.Fatal("successor structure")
	}
	promoted := 0
	for i, c := range next.Cases {
		prior := old.Cases[i]
		if c.ID != prior.ID || c.Project != prior.Project || c.Path != prior.Path || c.ByteOffset != prior.ByteOffset || c.ByteLength != prior.ByteLength || c.SourceSHA256 != prior.SourceSHA256 || c.Target != prior.Target {
			t.Fatal("source expectation drift", c.ID)
		}
		if c.Classification != prior.Classification {
			promoted++
			want := map[string]string{"entity-set": "storage_entity_use", "entity-model": "storage_entity_mapping"}[c.ID]
			if want == "" || prior.Classification != "unsupported" || c.Classification != "supported" || c.Kind != want || c.Rule != "csharp-framework-v2" || len(c.ExpectedTargets) != 1 || c.ExpectedTargets[0].Role != "entity" || c.ExpectedTargets[0].Symbol != c.Target {
				t.Fatal("invalid promotion", c.ID)
			}
		} else if c.Kind != prior.Kind || c.Rule != "csharp-framework-v1" {
			t.Fatal("unrelated rule drift", c.ID)
		}
	}
	if promoted != 2 {
		t.Fatal("promotion count", promoted)
	}
}

func TestApplicationScoringRejectsWrongRule(t *testing.T) {
	c := applicationCase{ID: "entity", Classification: "supported", Project: "A", Path: "A.cs", Kind: "storage_entity_use", Rule: "csharp-framework-v2", OwnerExpectation: "unasserted", ExpectedTargets: []applicationTarget{{Role: "entity", Symbol: semantic.SymbolKey{Descriptor: "T:Entity"}}}}
	o := applicationExpected(c)
	o.Rule = "csharp-framework-v1"
	sig := applicationSignature(o)
	g := applicationGold{Cases: []applicationCase{c}}
	g.ReviewedClosure.Paths = []string{"A.cs"}
	g.ReviewedClosure.Projects = []string{"A"}
	var r applicationReport
	scoreApplication(g, map[string]applicationObservation{sig: o}, map[string]map[string]bool{sig: {"ctx": true}}, map[string][]string{"A": {"ctx"}}, &r)
	if r.TruePositive != 0 || r.FalseNegative != 1 || r.FalsePositive != 1 {
		t.Fatalf("wrong rule accepted: %+v", r)
	}
}

func TestApplicationV3GoldOnlyPromotesContextRegistrations(t *testing.T) {
	base := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "application-outbox-20261001")
	read := func(name, want string) applicationGold {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(base, name))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatal("frozen gold changed", name)
		}
		var g applicationGold
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err := d.Decode(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	const priorSHA = "2b0bab11f6bc2f90c4a7dbacba518b14004a2673732a2d3160abb8cbaf5f2052"
	prior := read("source-gold-v2.json", priorSHA)
	next := read("source-gold-v3.json", "29848c766c675f261ef2204eaf93ab74753432705da5622c4f46f6f57f26cece")
	if next.Version != 3 || next.PredecessorSHA256 != priorSHA || len(next.Cases) != len(prior.Cases) || !reflect.DeepEqual(next.ReviewedClosure, prior.ReviewedClosure) {
		t.Fatal("successor source drift")
	}
	promoted := 0
	for i, c := range next.Cases {
		old := prior.Cases[i]
		if c.ID != "api-dbcontext" && c.ID != "worker-dbcontext" {
			if !reflect.DeepEqual(c, old) {
				t.Fatal("unrelated case changed", c.ID)
			}
			continue
		}
		promoted++
		if old.Classification != "unsupported" || c.Classification != "supported" || c.Kind != "storage_context_registration" || c.Rule != "csharp-framework-v3" || c.Lifetime != "scoped" || c.ByteOffset != old.ByteOffset || c.ByteLength != old.ByteLength || c.Path != old.Path || c.Project != old.Project || c.SourceSHA256 != old.SourceSHA256 || c.Target != old.Target {
			t.Fatal("invalid context registration promotion", c.ID)
		}
		want := []applicationTarget{{Role: "service", Symbol: c.Target}, {Role: "implementation", Symbol: c.Target}}
		if !reflect.DeepEqual(c.ExpectedTargets, want) || !reflect.DeepEqual(c.MatchedRoles, []string{"service", "implementation"}) || c.Table != "" || c.Schema != "" {
			t.Fatal("invalid context registration targets/scalars", c.ID)
		}
	}
	if promoted != 2 {
		t.Fatal("promotion count", promoted)
	}
}

func TestApplicationContextRegistrationScoringRejectsLifetimeAndRoleDrift(t *testing.T) {
	target := semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/Contexts.csproj", Descriptor: "T:Context", DescriptorKind: "documentation_comment_id"}
	c := applicationCase{ID: "context-registration", Classification: "supported", Project: "App.csproj", Path: "App.cs", Kind: "storage_context_registration", Rule: "csharp-framework-v3", Lifetime: "scoped", OwnerExpectation: "unasserted", ExpectedTargets: []applicationTarget{{Role: "service", Symbol: target}, {Role: "implementation", Symbol: target}}}
	for _, kind := range []string{"lifetime", "missing-role", "wrong-rule", "table-inference"} {
		t.Run(kind, func(t *testing.T) {
			o := applicationExpected(c)
			switch kind {
			case "lifetime":
				o.Lifetime = "singleton"
			case "missing-role":
				o.Targets = o.Targets[:1]
			case "wrong-rule":
				o.Rule = "csharp-framework-v1"
			case "table-inference":
				o.Table = "Contexts"
			}
			sig := applicationSignature(o)
			g := applicationGold{Cases: []applicationCase{c}}
			g.ReviewedClosure.Paths = []string{"App.cs"}
			g.ReviewedClosure.Projects = []string{"App.csproj"}
			var r applicationReport
			scoreApplication(g, map[string]applicationObservation{sig: o}, map[string]map[string]bool{sig: {"ctx": true}}, map[string][]string{"App.csproj": {"ctx"}}, &r)
			if r.TruePositive != 0 || r.FalseNegative != 1 || r.FalsePositive != 1 {
				t.Fatalf("incorrect registration accepted: %+v", r)
			}
		})
	}
}

func TestApplicationV4GoldOnlyPromotesStateMachineConfiguration(t *testing.T) {
	base := filepath.Join("..", "..", "..", "research", "semantic-intelligence", "results", "application-outbox-20261001")
	read := func(name, hash string) applicationGold {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(base, name))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != hash {
			t.Fatal("frozen gold drift")
		}
		var g applicationGold
		if e := json.Unmarshal(b, &g); e != nil {
			t.Fatal(e)
		}
		return g
	}
	prior := read("source-gold-v3.json", "29848c766c675f261ef2204eaf93ab74753432705da5622c4f46f6f57f26cece")
	next := read("source-gold-v4.json", "35744683f93f9ae7fb83ef17455b11da7d38a067951feb26acf0ad415e1d8586")
	if next.Version != 4 || next.PredecessorSHA256 != "29848c766c675f261ef2204eaf93ab74753432705da5622c4f46f6f57f26cece" || !reflect.DeepEqual(next.ReviewedClosure, prior.ReviewedClosure) || len(next.Cases) != len(prior.Cases) {
		t.Fatal("source closure drift")
	}
	kinds := map[string]string{"saga-event": "message_event_configuration", "fluent-publish-AddEventAttendee": "message_publish_configuration", "fluent-publish-SendRegistrationEmail": "message_publish_configuration"}
	for i, c := range next.Cases {
		old := prior.Cases[i]
		kind := kinds[c.ID]
		if kind != "" {
			old.Classification = "supported"
			old.Kind = kind
			old.Rule = "csharp-framework-v4"
			old.Reason = ""
			old.ExpectedTargets = []applicationTarget{{Role: "message", Symbol: c.Target}}
			old.MatchedRoles = []string{"message"}
		}
		if !reflect.DeepEqual(c, old) {
			t.Fatal("unrelated gold drift", c.ID)
		}
	}
}

func TestApplicationConfigurationCannotScoreAsRuntimePublishOrConsumer(t *testing.T) {
	for _, kind := range []string{"message_publish_configuration", "message_event_configuration"} {
		t.Run(kind, func(t *testing.T) {
			target := semantic.SymbolKey{Descriptor: "T:Message"}
			c := applicationCase{ID: "configuration", Classification: "supported", Project: "App.csproj", Path: "App.cs", Kind: kind, Rule: "csharp-framework-v4", OwnerExpectation: "unasserted", ExpectedTargets: []applicationTarget{{Role: "message", Symbol: target}}}
			o := applicationExpected(c)
			o.Kind = "message_publish"
			if kind == "message_event_configuration" {
				o.Kind = "message_consumer"
			}
			sig := applicationSignature(o)
			g := applicationGold{Cases: []applicationCase{c}}
			g.ReviewedClosure.Paths = []string{"App.cs"}
			g.ReviewedClosure.Projects = []string{"App.csproj"}
			var r applicationReport
			scoreApplication(g, map[string]applicationObservation{sig: o}, map[string]map[string]bool{sig: {"ctx": true}}, map[string][]string{"App.csproj": {"ctx"}}, &r)
			if r.TruePositive != 0 || r.FalsePositive != 1 || r.FalseNegative != 1 {
				t.Fatal("configuration misclassified", r)
			}
		})
	}
}
