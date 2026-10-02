package semanticindex

import (
	"context"
	"testing"
)

func TestDependencyBundleContextIdentitySurvivesCompactIndex(t *testing.T) {
	a := fixture(2)
	oldContext := a.Contexts[0].ID
	a.Contexts[0].DependencyBundleSHA256 = hash("verified dependency manifest")
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	if a.Contexts[0].ID == oldContext {
		t.Fatal("dependency evidence did not change context identity")
	}
	for i := range a.Occurrences {
		a.Occurrences[i].ContextID = a.Contexts[0].ID
		a.Occurrences[i].ID = a.Occurrences[i].ComputeID()
		a.Bindings[i].OccurrenceID = a.Occurrences[i].ID
		a.Bindings[i].ID = a.Bindings[i].ComputeID()
	}
	x, _ := openFixture(t, a)
	ctx := context.Background()
	got, err := x.Bindings(ctx, Position{Repo: "repo", Path: "src/A.cs", Offset: 10}, 100)
	if err != nil || len(got.Results) != 1 || got.Results[0].Occurrence.ContextID != a.Contexts[0].ID {
		t.Fatalf("bundle context identity lost: %#v %v", got, err)
	}
	if _, ok, err := x.SourceContext(ctx, "repo", "src/A.cs", a.Contexts[0].ID); err != nil || !ok {
		t.Fatalf("bundle context membership lost: %v", err)
	}
	if _, ok, err := x.SourceContext(ctx, "repo", "src/A.cs", oldContext); err != nil || ok {
		t.Fatalf("unverified old context accepted: %v", err)
	}
}
