package semanticindex

import (
	"context"
	"errors"
	"testing"
)

func TestEvidencePathBoundsScopeAndOwnership(t *testing.T) {
	a := fixture(240)
	x, _ := openFixture(t, a)
	id := a.Symbols[0].ID
	ids := []string{a.Contexts[0].ID}
	q, err := x.EvidencePath(context.Background(), id, ids, 3)
	if err != nil || !q.Supported || !q.Truncated || len(q.Records) != 3 || q.RowsVisited != 4 {
		t.Fatalf("%+v %v", q, err)
	}
	if _, err = x.EvidencePath(context.Background(), id, nil, 3); !errors.Is(err, ErrContractContextSelection) {
		t.Fatal(err)
	}
	if _, err = x.EvidencePath(context.Background(), id, append(ids, ids...), 3); !errors.Is(err, ErrContractContextSelection) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = x.EvidencePath(ctx, id, ids, 3); err != context.Canceled {
		t.Fatal(err)
	}
	x.Close()
	if q.Records[0].Symbol.ID != id {
		t.Fatal("records not owned")
	}
}
func TestEvidencePathWorkCapIncludesSkippedContexts(t *testing.T) {
	a := fixture(22000)
	other := a.Contexts[0]
	other.Project = "Other.csproj"
	other.ID = other.ComputeID()
	a.Contexts = append(a.Contexts, other)
	x, _ := openFixture(t, a)
	q, err := x.EvidencePath(context.Background(), a.Symbols[0].ID, []string{other.ID}, 100)
	if err != nil || !q.Truncated || q.RowsVisited != ContractWorkLimit || len(q.Records) != 0 {
		t.Fatalf("%+v %v", q, err)
	}
}
