package semanticindex

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func contractFixture() *semantic.Artifact {
	a := domainFixture()
	c := a.Contexts[0]
	c.Project = "B.csproj"
	c.ID = c.ComputeID()
	a.Contexts = append(a.Contexts, c)
	o := a.Occurrences[1]
	o.ContextID = c.ID
	o.Offset = 20
	o.ID = o.ComputeID()
	b := a.Bindings[1]
	b.OccurrenceID = o.ID
	b.ID = b.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	a.Bindings = append(a.Bindings, b)
	owner := semantic.Symbol{Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A", DescriptorKind: "documentation_comment_id", Descriptor: "T:Owner"}}
	owner.ID = owner.ComputeID()
	a.Symbols = append(a.Symbols, owner)
	for i := range a.Bindings {
		if len(a.Bindings[i].DomainFacts) > 0 {
			a.Bindings[i].EnclosingSymbolID = owner.ID
			a.Bindings[i].ID = a.Bindings[i].ComputeID()
		}
	}
	return a
}
func TestContractImpactExactScopesAndRoles(t *testing.T) {
	a := contractFixture()
	x, _ := openFixture(t, a)
	symbol := a.Symbols[0].ID
	choices, err := x.ContractImpact(context.Background(), symbol, nil, 100)
	if err != nil || !choices.Supported || choices.Contract == nil || len(choices.Contexts) != 2 || len(choices.Matches) != 0 || choices.Truncated {
		t.Fatalf("discovery %+v %v", choices, err)
	}
	limited, err := x.ContractImpact(context.Background(), symbol, nil, 1)
	if err != nil || len(limited.Contexts) != 1 || !limited.Truncated {
		t.Fatalf("limited %+v %v", limited, err)
	}
	ids := []string{a.Contexts[1].ID, a.Contexts[0].ID}
	got, err := x.ContractImpact(context.Background(), symbol, ids, 100)
	if err != nil || len(got.Matches) != 2 || got.PostingsVisited != 4 || got.Truncated {
		t.Fatalf("selected %+v %v", got, err)
	}
	for _, match := range got.Matches {
		if !reflect.DeepEqual(match.TargetRoles, []string{"service", "implementation"}) || match.DomainFactIndex != 0 || match.Owner == nil || match.Owner.Key.Descriptor != "T:Owner" {
			t.Fatalf("match %+v", match)
		}
	}
	one, err := x.ContractImpact(context.Background(), symbol, ids, 1)
	if err != nil || len(one.Matches) != 1 || !one.Truncated {
		t.Fatalf("one %+v %v", one, err)
	}
	one, err = x.ContractImpact(context.Background(), symbol, []string{a.Contexts[0].ID}, 100)
	if err != nil || len(one.Matches) != 1 || one.Truncated || one.Matches[0].Result.Context.ID != a.Contexts[0].ID {
		t.Fatalf("exact context %+v %v", one, err)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if got.Contract.Key.Descriptor != "T:A" || got.Matches[0].Owner.Key.Descriptor != "T:Owner" {
		t.Fatal("results not owned")
	}
}
func TestContractImpactRejectsUnknownAndMixedContexts(t *testing.T) {
	a := contractFixture()
	a.Contexts[1].Project = a.Contexts[0].Project
	a.Contexts[1].Capture = `{"variant":"x"}`
	a.Contexts[1].InputFingerprint = hash(a.Contexts[1].Capture)
	old := a.Contexts[1].ID
	a.Contexts[1].ID = a.Contexts[1].ComputeID()
	for i := range a.Occurrences {
		if a.Occurrences[i].ContextID == old {
			a.Occurrences[i].ContextID = a.Contexts[1].ID
			prior := a.Occurrences[i].ID
			a.Occurrences[i].ID = a.Occurrences[i].ComputeID()
			for j := range a.Bindings {
				if a.Bindings[j].OccurrenceID == prior {
					a.Bindings[j].OccurrenceID = a.Occurrences[i].ID
					a.Bindings[j].ID = a.Bindings[j].ComputeID()
				}
			}
		}
	}
	x, _ := openFixture(t, a)
	for _, ids := range [][]string{{"context:" + hash("unknown")}, {a.Contexts[0].ID, a.Contexts[1].ID}, {a.Contexts[0].ID, a.Contexts[0].ID}} {
		got, err := x.ContractImpact(context.Background(), a.Symbols[0].ID, ids, 100)
		if !errors.Is(err, ErrContractContextSelection) || len(got.Matches) > 0 {
			t.Fatalf("accepted scopes %v: %+v %v", ids, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.ContractImpact(ctx, a.Symbols[0].ID, nil, 100); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

// Convert only a no-domain fixture. Genuine v2 domain bytes are tested separately.
func legacyIndexBytes(current []byte, version uint64) []byte {
	b := append(append([]byte(nil), current[:legacyHeaderSize]...), current[headerSize:]...)
	count := legacySectionCount
	if version == 2 {
		count = domainSectionCount
	}
	clear(b[120+count*16 : legacyHeaderSize])
	le.PutUint64(b[8:], version)
	le.PutUint64(b[16:], uint64(len(b)))
	for section := 0; section < count; section++ {
		at := 120 + section*16
		le.PutUint64(b[at:], le.Uint64(b[at:])-uint64(headerSize-legacyHeaderSize))
	}
	checksum := sha256.Sum256(b[legacyHeaderSize:])
	copy(b[24:56], checksum[:])
	return b
}
func TestContractImpactLegacyUnavailable(t *testing.T) {
	x, path := openFixture(t, fixture(3))
	x.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		path := filepath.Join(t.TempDir(), "old")
		if err := os.WriteFile(path, legacyIndexBytes(data, version), 0600); err != nil {
			t.Fatal(err)
		}
		old, err := Open(path, provenance(), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := old.ContractImpact(context.Background(), fixture(3).Symbols[0].ID, nil, 100)
		old.Close()
		if err != nil || got.Supported {
			t.Fatalf("legacy supported %+v %v", got, err)
		}
	}
}
func TestContractImpactGenuineV2(t *testing.T) {
	path := filepath.Join("testdata", "domain-v2.msi")
	x, err := Open(path, provenance(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if x.version != 2 {
		t.Fatal("not genuine v2 input")
	}
	a := domainFixture()
	rows, err := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 10}, 1)
	if err != nil || len(rows.Results) != 1 || len(rows.Results[0].Binding.DomainFacts) != 1 {
		t.Fatalf("legacy domains lost: %+v %v", rows, err)
	}
	got, err := x.ContractImpact(context.Background(), a.Symbols[0].ID, nil, 100)
	if err != nil || got.Supported {
		t.Fatal("legacy reverse lookup should be unavailable")
	}
}
func TestContractPostingsRejectCorruptionAndOmissions(t *testing.T) {
	x, path := openFixture(t, contractFixture())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func([]byte) []byte{
		"missing": func(b []byte) []byte {
			b = b[:len(b)-int(widths[contractPostings])]
			le.PutUint64(b[128+contractPostings*16:], x.sections[contractPostings].n-1)
			return b
		},
		"duplicate": func(b []byte) []byte {
			copy(b[x.sections[contractPostings].off+32:], b[x.sections[contractPostings].off:x.sections[contractPostings].off+32])
			return b
		},
		"extra": func(b []byte) []byte {
			b = append(b, b[len(b)-32:]...)
			le.PutUint64(b[128+contractPostings*16:], x.sections[contractPostings].n+1)
			return b
		},
		"relabel": func(b []byte) []byte {
			le.PutUint64(b[x.sections[contractPostings].off:], (x.field(contractPostings, 0, 0)+1)%x.sections[symbols].n)
			return b
		},
		"role": func(b []byte) []byte { le.PutUint64(b[x.sections[contractPostings].off+24:], 99); return b },
		"target": func(b []byte) []byte {
			le.PutUint64(b[x.sections[contractPostings].off:], x.sections[symbols].n)
			return b
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := change(append([]byte(nil), data...))
			le.PutUint64(b[16:], uint64(len(b)))
			sum := sha256.Sum256(b[headerSize:])
			copy(b[24:56], sum[:])
			path := filepath.Join(t.TempDir(), "bad")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			bad, err := Open(path, provenance(), Limits{})
			if err == nil {
				bad.Close()
				t.Fatal("corrupt reverse postings accepted")
			}
		})
	}
}

func TestContractImpactSkipsDenseUnselectedContexts(t *testing.T) {
	const references = ContractWorkLimit + 1
	a := contractFixture()
	a.Sources[0].ByteSize = uint64((references + 10) * 10)
	a.Sources[0].ID = a.Sources[0].ComputeID()
	contextIDs := map[string]string{}
	for i := range a.Contexts {
		old := a.Contexts[i].ID
		a.Contexts[i].SourceIDs = []string{a.Sources[0].ID}
		a.Contexts[i].ID = a.Contexts[i].ComputeID()
		contextIDs[old] = a.Contexts[i].ID
	}
	for i := range a.Occurrences {
		a.Occurrences[i].SourceID = a.Sources[0].ID
		a.Occurrences[i].ContextID = contextIDs[a.Occurrences[i].ContextID]
		a.Occurrences[i].ID = a.Occurrences[i].ComputeID()
		a.Bindings[i].OccurrenceID = a.Occurrences[i].ID
		a.Bindings[i].ID = a.Bindings[i].ComputeID()
	}
	dense, sparse := a.Contexts[0].ID, a.Contexts[1].ID
	if dense > sparse {
		dense, sparse = sparse, dense
	}
	for i := 0; i < references; i++ {
		o := a.Occurrences[1]
		o.ContextID = dense
		o.Offset = uint64((i + 4) * 10)
		o.ID = o.ComputeID()
		b := a.Bindings[1]
		b.OccurrenceID = o.ID
		b.ID = b.ComputeID()
		a.Occurrences = append(a.Occurrences, o)
		a.Bindings = append(a.Bindings, b)
	}
	x, _ := openFixture(t, a)
	choices, err := x.ContractImpact(context.Background(), a.Symbols[0].ID, nil, 100)
	if err != nil || len(choices.Contexts) != 2 || choices.PostingsVisited != 2 || choices.Truncated {
		t.Fatalf("dense discovery %+v %v", choices, err)
	}
	got, err := x.ContractImpact(context.Background(), a.Symbols[0].ID, []string{sparse}, 100)
	if err != nil || len(got.Matches) != 1 || got.PostingsVisited != 2 || got.Truncated {
		t.Fatalf("selected seek %+v %v", got, err)
	}
	bounded, err := x.ContractImpact(context.Background(), a.Symbols[0].ID, []string{dense}, 3)
	if err != nil || len(bounded.Matches) != 3 || bounded.PostingsVisited != 6 || !bounded.Truncated {
		t.Fatalf("result bound %+v %v", bounded, err)
	}
}

func TestContractImpactByteBudgetPrecedesOwnerMaterialization(t *testing.T) {
	a := contractFixture()
	owner := &a.Symbols[len(a.Symbols)-1]
	old := owner.ID
	owner.Key.Descriptor = "T:" + strings.Repeat("O", MaxQueryBytes)
	owner.ID = owner.ComputeID()
	for i := range a.Bindings {
		if a.Bindings[i].EnclosingSymbolID == old {
			a.Bindings[i].EnclosingSymbolID = owner.ID
			a.Bindings[i].ID = a.Bindings[i].ComputeID()
		}
	}
	x, _ := openFixture(t, a)
	got, err := x.ContractImpact(context.Background(), a.Symbols[0].ID, []string{a.Contexts[0].ID}, 100)
	if err == nil || !strings.Contains(err.Error(), "byte budget") || len(got.Matches) != 0 {
		t.Fatalf("owner budget %+v %v", got, err)
	}
}
