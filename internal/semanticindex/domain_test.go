package semanticindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func domainFixture() *semantic.Artifact {
	a := fixture(3)
	c := &a.Contexts[0]
	c.Extractor = "msbuild-roslyn"
	c.ExtractorVersion = "2"
	c.ID = c.ComputeID()
	api := semantic.Symbol{Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "assembly", Namespace: "Microsoft.Extensions.DependencyInjection.Abstractions, Version=1.0.0.0", DescriptorKind: "documentation_comment_id", Descriptor: "M:Microsoft.Extensions.DependencyInjection.ServiceCollectionServiceExtensions.AddScoped``2(Microsoft.Extensions.DependencyInjection.IServiceCollection)"}}
	api.ID = api.ComputeID()
	a.Symbols = append(a.Symbols, api)
	for i := range a.Occurrences {
		o := &a.Occurrences[i]
		o.ContextID = c.ID
		o.ID = o.ComputeID()
		b := &a.Bindings[i]
		b.OccurrenceID = o.ID
		b.Extractor = c.Extractor
		b.ExtractorVersion = c.ExtractorVersion
		b.Method = "roslyn-semantic-model"
		if i == 1 {
			b.SymbolID = api.ID
			b.DomainFacts = []semantic.DomainFact{{Kind: "di_registration", Rule: "csharp-framework-v1", EvidenceScope: "compile_time", Lifetime: "scoped", Targets: []semantic.DomainTarget{{Role: "service", SymbolID: a.Symbols[0].ID}, {Role: "implementation", SymbolID: a.Symbols[0].ID}}}}
		}
		b.ID = b.ComputeID()
	}
	return a
}
func TestDomainFactRoundTripOwnedAndExactPosition(t *testing.T) {
	a := domainFixture()
	x, _ := openFixture(t, a)
	got, err := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 10, BuildContextID: a.Contexts[0].ID}, 1)
	if err != nil || len(got.Results) != 1 {
		t.Fatalf("lookup %v %v", got, err)
	}
	result := got.Results[0]
	if !reflect.DeepEqual(result.Binding.DomainFacts, a.Bindings[1].DomainFacts) || len(result.DomainSymbols) != 1 || result.DomainSymbols[0] != a.Symbols[0] {
		t.Fatalf("domain mismatch: %#v", result)
	}
	miss, err := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 11}, 1)
	if err != nil || len(miss.Results) != 0 {
		t.Fatal("domain lookup widened position")
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if result.Binding.DomainFacts[0].Targets[0].SymbolID != a.Symbols[0].ID || result.DomainSymbols[0].Key.Descriptor != "T:A" {
		t.Fatal("result did not own strings")
	}
}
func TestDomainFactLegacyV1Index(t *testing.T) {
	a := fixture(3)
	x, p := openFixture(t, a)
	x.Close()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data = legacyIndexBytes(data, 1)
	old := filepath.Join(t.TempDir(), "v1")
	if err := os.WriteFile(old, data, 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := Open(old, provenance(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	got, err := legacy.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 10}, 1)
	if err != nil || len(got.Results) != 1 || len(got.Results[0].Binding.DomainFacts) != 0 {
		t.Fatalf("legacy: %v %v", got, err)
	}
}
func TestDomainFactCorruptIndexes(t *testing.T) {
	x, p := openFixture(t, domainFixture())
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func([]byte){
		"fact row":   func(b []byte) { le.PutUint64(b[x.sections[domainFacts].off:], x.sections[facts].n) },
		"payload id": func(b []byte) { le.PutUint64(b[x.sections[domainFacts].off+8:], x.sections[strdir].n) },
		"scope":      func(b []byte) { i := bytes.Index(b, []byte("compile_time")); copy(b[i:], "runtime_time") },
		"project API lookalike": func(b []byte) {
			fact := x.field(domainFacts, 0, 0)
			for row := uint64(0); row < x.sections[symbols].n; row++ {
				if x.text(x.field(symbols, row, 2)) == "project" {
					le.PutUint64(b[x.sections[facts].off+fact*widths[facts]+9*8:], row+1)
					return
				}
			}
			t.Fatal("missing project symbol fixture")
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), data...)
			change(b)
			h := sha256.Sum256(b[headerSize:])
			copy(b[24:56], h[:])
			path := filepath.Join(t.TempDir(), "bad")
			os.WriteFile(path, b, 0600)
			got, err := Open(path, provenance(), Limits{})
			if err == nil {
				got.Close()
				t.Fatal("accepted corrupt domain")
			}
		})
	}
}
func TestDomainTargetsRespectResultByteBudget(t *testing.T) {
	a := domainFixture()
	target := &a.Symbols[0]
	target.Key.Descriptor = "T:" + strings.Repeat("A", MaxQueryBytes)
	old := target.ID
	target.ID = target.ComputeID()
	for i := range a.Bindings {
		b := &a.Bindings[i]
		if b.SymbolID == old {
			b.SymbolID = target.ID
		}
		for j := range b.DomainFacts {
			for k := range b.DomainFacts[j].Targets {
				b.DomainFacts[j].Targets[k].SymbolID = target.ID
			}
		}
		b.ID = b.ComputeID()
	}
	x, _ := openFixture(t, a)
	got, err := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 10}, 1)
	if err == nil || !strings.Contains(err.Error(), "byte budget") || len(got.Results) != 0 {
		t.Fatalf("unbounded domain target output: %v %v", got, err)
	}
}
