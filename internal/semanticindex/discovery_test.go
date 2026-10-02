package semanticindex

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFileSymbolsDeclarationsScopeAndBounds(t *testing.T) {
	a := fixture(6)
	x, _ := openFixture(t, a)
	ctx := context.Background()
	q, err := x.FileSymbols(ctx, "repo", "src/A.cs", "T:A", 100)
	if err != nil || q.Truncated || len(q.Results) != 3 || q.RowsVisited != 6 {
		t.Fatalf("%+v %v", q, err)
	}
	for i, r := range q.Results {
		if r.Occurrence.Offset != uint64(i*20) || r.Symbol.ID != a.Symbols[0].ID {
			t.Fatalf("wrong declaration: %+v", r)
		}
	}
	again, err := x.FileSymbols(ctx, "repo", "src/A.cs", "T:A", 100)
	if err != nil || !reflect.DeepEqual(q, again) {
		t.Fatal("unstable discovery")
	}
	limited, err := x.FileSymbols(ctx, "repo", "src/A.cs", "", 1)
	if err != nil || len(limited.Results) != 1 || !limited.Truncated {
		t.Fatalf("%+v %v", limited, err)
	}
	for _, p := range []struct{ repo, path, query string }{{"other", "src/A.cs", ""}, {"repo", "src/A.cs/more", ""}, {"repo", "src/A.cs", "t:a"}} {
		got, err := x.FileSymbols(ctx, p.repo, p.path, p.query, 100)
		if err != nil || len(got.Results) != 0 || got.Truncated {
			t.Fatalf("scope leaked: %+v %v", got, err)
		}
	}
	for _, query := range []string{"bad\nquery", strings.Repeat("x", 257)} {
		if _, err := x.FileSymbols(ctx, "repo", "src/A.cs", query, 100); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if _, err := x.FileSymbols(ctx, "repo", "../A.cs", "", 100); err == nil {
		t.Fatal("invalid path accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := x.FileSymbols(canceled, "repo", "src/A.cs", "", 100); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if q.Results[0].Symbol.Key.Descriptor != "T:A" {
		t.Fatal("result did not own strings")
	}
	if _, err := x.FileSymbols(ctx, "repo", "src/A.cs", "", 100); err == nil {
		t.Fatal("closed lookup accepted")
	}
}

func TestFileSymbolsWorkLimitIncludesNonmatches(t *testing.T) {
	x, _ := openFixture(t, fixture(ContractWorkLimit+2))
	q, err := x.FileSymbols(context.Background(), "repo", "src/A.cs", "absent", 100)
	if err != nil || len(q.Results) != 0 || !q.Truncated || q.RowsVisited != ContractWorkLimit {
		t.Fatalf("%+v %v", q, err)
	}
}

func TestFileSymbolsKeepsContextAlternatives(t *testing.T) {
	a := fixture(2)
	c := a.Contexts[0]
	c.Capture = `{"variant":true}`
	c.InputFingerprint = hash(c.Capture)
	c.ID = c.ComputeID()
	a.Contexts = append(a.Contexts, c)
	for i := 0; i < 2; i++ {
		o := a.Occurrences[i]
		o.ContextID = c.ID
		o.ID = o.ComputeID()
		a.Occurrences = append(a.Occurrences, o)
		b := a.Bindings[i]
		b.OccurrenceID = o.ID
		b.ID = b.ComputeID()
		a.Bindings = append(a.Bindings, b)
	}
	x, _ := openFixture(t, a)
	q, err := x.FileSymbols(context.Background(), "repo", "src/A.cs", "A", 100)
	if err != nil || len(q.Results) != 2 || q.Truncated || q.Results[0].Context.ID == q.Results[1].Context.ID || q.Results[0].Symbol.ID != q.Results[1].Symbol.ID {
		t.Fatalf("%+v %v", q, err)
	}
}

func TestFileSymbolsMaterializationTruncation(t *testing.T) {
	a := fixture(200)
	a.Symbols[0].Key.Descriptor = "T:" + strings.Repeat("A", 32768)
	a.Symbols[0].ID = a.Symbols[0].ComputeID()
	for i := range a.Bindings {
		a.Bindings[i].SymbolID = a.Symbols[0].ID
		a.Bindings[i].ID = a.Bindings[i].ComputeID()
	}
	x, _ := openFixture(t, a)
	q, err := x.FileSymbols(context.Background(), "repo", "src/A.cs", "", 100)
	if err != nil || !q.Truncated || len(q.Results) == 0 || len(q.Results) >= 100 {
		t.Fatalf("count=%d truncated=%v err=%v", len(q.Results), q.Truncated, err)
	}
}
