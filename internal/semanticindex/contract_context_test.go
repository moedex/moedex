package semanticindex

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func contextContractFixture() *semantic.Artifact {
	a := contractFixture()
	c := a.Contexts[0]
	c.Project = "Contracts.csproj"
	c.ID = c.ComputeID()
	a.Contexts = append(a.Contexts, c)
	o := a.Occurrences[0]
	o.ContextID = c.ID
	o.ID = o.ComputeID()
	b := a.Bindings[0]
	b.OccurrenceID = o.ID
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	return a
}
func TestContractContextDefinitionDiscoveryAndEvidenceParity(t *testing.T) {
	a := contextContractFixture()
	x, _ := openFixture(t, a)
	symbol := a.Symbols[0].ID
	discovery, err := x.ContractContext(context.Background(), symbol, nil, 80, 20)
	if err != nil || len(discovery.Contexts) != 3 || len(discovery.Matches) != 0 || len(discovery.Definitions) != 0 || discovery.ContextsTruncated {
		t.Fatalf("discovery %+v %v", discovery, err)
	}
	var ids []string
	for _, c := range a.Contexts {
		ids = append(ids, c.ID)
	}
	got, err := x.ContractContext(context.Background(), symbol, ids, 80, 20)
	if err != nil || len(got.Matches) != 2 || len(got.Definitions) != 3 || got.EvidenceTruncated || got.DefinitionsTruncated {
		t.Fatalf("combined %+v %v", got, err)
	}
	old, err := x.ContractImpact(context.Background(), symbol, ids, 80)
	if err != nil || !reflect.DeepEqual(old.Matches, got.Matches) {
		t.Fatal("evidence parity", err)
	}
	defs := map[string]Result{}
	for _, id := range ids {
		q, err := x.Definitions(context.Background(), symbol, Filter{BuildContextID: id}, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range q.Results {
			defs[r.Occurrence.ID] = r
		}
	}
	for _, r := range got.Definitions {
		if !reflect.DeepEqual(defs[r.Occurrence.ID], r) {
			t.Fatal("definition parity")
		}
		delete(defs, r.Occurrence.ID)
	}
	if len(defs) != 0 {
		t.Fatal("missing definition")
	}
	limited, err := x.ContractContext(context.Background(), symbol, ids, 1, 1)
	if err != nil || len(limited.Matches) != 1 || len(limited.Definitions) != 1 || !limited.EvidenceTruncated || !limited.DefinitionsTruncated {
		t.Fatalf("reserved limits %+v %v", limited, err)
	}
	x.Close()
	if got.Definitions[0].Source.Path == "" || got.Matches[0].Owner.Key.Descriptor != "T:Owner" {
		t.Fatal("ownership")
	}
	if _, err := x.ContractContext(context.Background(), symbol, ids, 80, 20); err == nil {
		t.Fatal("closed index accepted")
	}
}
func TestContractContextCancellationAndArguments(t *testing.T) {
	a := contextContractFixture()
	x, _ := openFixture(t, a)
	defer x.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.ContractContext(ctx, a.Symbols[0].ID, nil, 80, 20); err != context.Canceled {
		t.Fatal(err)
	}
	for _, limits := range [][2]int{{0, 20}, {80, 21}, {100, 1}, {int(^uint(0) >> 1), 2}} {
		if _, err := x.ContractContext(context.Background(), a.Symbols[0].ID, nil, limits[0], limits[1]); err == nil {
			t.Fatal("bad limit accepted", limits)
		}
	}
	if _, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID, a.Contexts[0].ID}, 80, 20); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestContractContextSharedDefinitionWorkBound(t *testing.T) {
	a := fixture(20004)
	c := a.Contexts[0]
	c.Project = "NoDefinitions.csproj"
	c.ID = c.ComputeID()
	a.Contexts = append(a.Contexts, c)
	x, _ := openFixture(t, a)
	defer x.Close()
	got, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{c.ID}, 80, 20)
	if err != nil || got.DefinitionRowsVisited != ContractWorkLimit || !got.DefinitionsTruncated || len(got.Definitions) != 0 {
		t.Fatalf("unselected scan escaped bound %+v %v", got, err)
	}
	discovery, err := x.ContractContext(context.Background(), a.Symbols[0].ID, nil, 80, 20)
	if err != nil || !discovery.ContextsTruncated || discovery.DefinitionRowsVisited != ContractWorkLimit || len(discovery.Contexts) != 1 {
		t.Fatalf("discovery bound %+v %v", discovery, err)
	}
}
func TestContractContextSharedMaterializationBudget(t *testing.T) {
	a := fixture(8)
	a.Symbols[0].Key.Descriptor = "T:" + strings.Repeat("A", 250000)
	a.Symbols[0].ID = a.Symbols[0].ComputeID()
	for i := range a.Bindings {
		a.Bindings[i].SymbolID = a.Symbols[0].ID
		a.Bindings[i].ID = a.Bindings[i].ComputeID()
	}
	x, _ := openFixture(t, a)
	defer x.Close()
	if q, err := x.Definitions(context.Background(), a.Symbols[0].ID, Filter{}, 4); err != nil || len(q.Results) != 4 {
		t.Fatalf("baseline definition budget %d %v", len(q.Results), err)
	}
	q, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 80, 20)
	if err == nil || !strings.Contains(err.Error(), "byte budget") || len(q.Definitions) != 0 {
		t.Fatalf("shared budget %+v %v", q, err)
	}
}

func TestContractContextDefinitionOnlyDiscoveryBound(t *testing.T) {
	a := fixture(1)
	for i := 0; i < MaxContractContexts; i++ {
		c := a.Contexts[0]
		c.Project = fmt.Sprintf("Contract%d.csproj", i)
		c.ID = c.ComputeID()
		a.Contexts = append(a.Contexts, c)
		o := a.Occurrences[0]
		o.ContextID = c.ID
		o.ID = o.ComputeID()
		b := a.Bindings[0]
		b.OccurrenceID = o.ID
		b.ID = b.ComputeID()
		a.Occurrences = append(a.Occurrences, o)
		a.Bindings = append(a.Bindings, b)
	}
	x, _ := openFixture(t, a)
	defer x.Close()
	got, err := x.ContractContext(context.Background(), a.Symbols[0].ID, nil, 80, 20)
	if err != nil || !got.ContextsTruncated || len(got.Contexts) != MaxContractContexts || len(got.Matches) != 0 || len(got.Definitions) != 0 {
		t.Fatalf("definition-only discovery %+v %v", got, err)
	}
	selected, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{a.Contexts[len(a.Contexts)-1].ID}, 80, 20)
	if err != nil || len(selected.Definitions) != 1 || selected.DefinitionsTruncated || len(selected.Matches) != 0 {
		t.Fatalf("definition-only selection %+v %v", selected, err)
	}
}

func TestContractContextRejectsDefinitionOnlyVariants(t *testing.T) {
	a := fixture(1)
	c := a.Contexts[0]
	c.Capture = `{"variant":"other"}`
	c.InputFingerprint = hash(c.Capture)
	c.ID = c.ComputeID()
	a.Contexts = append(a.Contexts, c)
	o := a.Occurrences[0]
	o.ContextID = c.ID
	o.ID = o.ComputeID()
	b := a.Bindings[0]
	b.OccurrenceID = o.ID
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	x, _ := openFixture(t, a)
	defer x.Close()
	discovery, err := x.ContractContext(context.Background(), a.Symbols[0].ID, nil, 80, 20)
	if err != nil || len(discovery.Contexts) != 2 || discovery.ContextsTruncated {
		t.Fatalf("definition variants not discoverable: %+v %v", discovery, err)
	}
	for _, id := range []string{a.Contexts[0].ID, c.ID} {
		got, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{id}, 80, 20)
		if err != nil || len(got.Definitions) != 1 || got.Definitions[0].Context.ID != id || got.DefinitionsTruncated {
			t.Fatalf("individual definition variant: %+v %v", got, err)
		}
	}
	got, err := x.ContractContext(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID, c.ID}, 80, 20)
	if !errors.Is(err, ErrContractContextSelection) || len(got.Definitions) != 0 {
		t.Fatalf("mixed definition-only variants admitted: %+v %v", got, err)
	}
}
