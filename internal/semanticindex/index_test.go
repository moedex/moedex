package semanticindex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func fixture(n int) *semantic.Artifact {
	s := semantic.SourceSnapshot{Repo: "repo", InputFingerprint: hash("sources")}
	s.ID = s.ComputeID()
	src := semantic.Source{SnapshotID: s.ID, Path: "src/A.cs", RawSHA256: hash("class A {}"), ByteSize: uint64(n*10 + 10)}
	src.ID = src.ComputeID()
	c := semantic.BuildContext{SnapshotID: s.ID, Project: "A.csproj", Capture: "{}", InputFingerprint: hash("{}"), Extractor: "roslyn", ExtractorVersion: "1", Status: "complete", SourceIDs: []string{src.ID}}
	c.ID = c.ComputeID()
	sy := semantic.Symbol{Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A", Descriptor: "T:A", DescriptorKind: "documentation_comment_id"}}
	sy.ID = sy.ComputeID()
	a := &semantic.Artifact{Version: 1, Snapshots: []semantic.SourceSnapshot{s}, Contexts: []semantic.BuildContext{c}, Sources: []semantic.Source{src}, Symbols: []semantic.Symbol{sy}}
	for i := 0; i < n; i++ {
		o := semantic.Occurrence{SourceID: src.ID, ContextID: c.ID, Role: "reference", Kind: "name", Offset: uint64(i * 10), Length: 1}
		if i%2 == 0 {
			o.Role = "declaration"
			o.Kind = "declaration"
		}
		o.ID = o.ComputeID()
		b := semantic.Binding{OccurrenceID: o.ID, Status: "resolved", SymbolID: sy.ID, Method: "semantic_model", Extractor: c.Extractor, ExtractorVersion: c.ExtractorVersion}
		b.ID = b.ComputeID()
		a.Occurrences = append(a.Occurrences, o)
		a.Bindings = append(a.Bindings, b)
	}
	return a
}
func provenance() Provenance { return Provenance{hash("artifact"), hash("corpus")} }
func openFixture(t *testing.T, a *semantic.Artifact) (*Index, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "index")
	if e := Build(p, a, provenance()); e != nil {
		t.Fatal(e)
	}
	x, e := Open(p, provenance(), Limits{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { x.Close() })
	return x, p
}
func TestLookupAndOwnedResults(t *testing.T) {
	a := fixture(4)
	x, p := openFixture(t, a)
	ctx := context.Background()
	got, e := x.Bindings(ctx, Position{Repo: "repo", Path: "src/A.cs", Offset: 10}, 1)
	if e != nil || len(got.Results) != 1 || got.Results[0].Binding.SymbolID != a.Symbols[0].ID {
		t.Fatalf("lookup %#v %v", got, e)
	}
	miss, e := x.Bindings(ctx, Position{Repo: "repo", Path: "src/A.cs", Offset: 11}, 1)
	if e != nil || len(miss.Results) != 0 {
		t.Fatal("position not exact")
	}
	defs, e := x.Definitions(ctx, a.Symbols[0].ID, Filter{}, 1)
	if e != nil || len(defs.Results) != 1 || !defs.Truncated {
		t.Fatalf("defs %#v %v", defs, e)
	}
	filtered, e := x.Definitions(ctx, a.Symbols[0].ID, Filter{PathPrefix: "sr"}, 100)
	if e != nil || len(filtered.Results) != 0 {
		t.Fatal("prefix not segment based")
	}
	_, ok, e := x.SourceContext(ctx, "repo", "src/A.cs", a.Contexts[0].ID)
	if e != nil || !ok {
		t.Fatal("source membership missing")
	}
	old, _ := os.ReadFile(p)
	q := filepath.Join(t.TempDir(), "index")
	if e := Build(q, a, provenance()); e != nil {
		t.Fatal(e)
	}
	now, _ := os.ReadFile(q)
	if !bytes.Equal(old, now) {
		t.Fatal("nondeterministic index")
	}
	if Build(p, a, provenance()) == nil {
		t.Fatal("overwritten")
	}
	if e := x.Close(); e != nil {
		t.Fatal(e)
	}
	if got.Results[0].Symbol.Key.Descriptor != "T:A" || got.Results[0].Source.Path != "src/A.cs" {
		t.Fatal("strings not owned")
	}
	if _, e := x.Bindings(ctx, Position{}, 1); e == nil {
		t.Fatal("closed lookup accepted")
	}
}
func TestCorruptIndexRejected(t *testing.T) {
	x, p := openFixture(t, fixture(4))
	raw, _ := os.ReadFile(p)
	changes := map[string]func([]byte){"offset": func(b []byte) { le.PutUint64(b[120+facts*16:], ^uint64(0)) }, "count": func(b []byte) { le.PutUint64(b[128+facts*16:], ^uint64(0)) }, "foreign key": func(b []byte) { le.PutUint64(b[x.sections[facts].off+16:], ^uint64(0)) }, "duplicate posting": func(b []byte) {
		copy(b[x.sections[positions].off+8:], b[x.sections[positions].off:x.sections[positions].off+8])
	}, "reserved": func(b []byte) { b[319] = 1 }, "membership": func(b []byte) { le.PutUint64(b[x.sections[memberships].off+8:], 99) }}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), raw...)
			change(b)
			h := sha256.Sum256(b[headerSize:])
			copy(b[24:], h[:])
			q := filepath.Join(t.TempDir(), "bad")
			os.WriteFile(q, b, 0600)
			if y, e := Open(q, provenance(), Limits{}); e == nil {
				y.Close()
				t.Fatal("corruption accepted")
			}
		})
	}
	if _, e := Open(p, Expected{hash("other"), hash("corpus")}, Limits{}); e == nil {
		t.Fatal("provenance ignored")
	}
	if _, e := Open(p, provenance(), Limits{MaxBytes: 1}); e == nil {
		t.Fatal("size ignored")
	}
}
func TestResourceLimitsAndIncomplete(t *testing.T) {
	a := fixture(1)
	a.Symbols[0].Key.Descriptor = strings.Repeat("A", MaxQueryBytes)
	a.Symbols[0].ID = a.Symbols[0].ComputeID()
	a.Bindings[0].SymbolID = a.Symbols[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	x, _ := openFixture(t, a)
	if _, e := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs"}, 1); e == nil {
		t.Fatal("unbounded descriptor materialized")
	}
	for _, n := range []int{0, -1, 101} {
		if _, e := x.Definitions(context.Background(), a.Symbols[0].ID, Filter{}, n); e == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, e := x.SourceContexts(ctx, "repo", "src/A.cs", 1); e == nil {
		t.Fatal("cancel ignored")
	}
	a = fixture(0)
	a.Contexts[0].Status = "incomplete"
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	if Build(filepath.Join(t.TempDir(), "bad"), a, provenance()) == nil {
		t.Fatal("incomplete built")
	}
}
func BenchmarkIndex(b *testing.B) {
	for _, n := range []int{10000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			a := fixture(n)
			p := filepath.Join(b.TempDir(), "idx")
			if e := Build(p, a, provenance()); e != nil {
				b.Fatal(e)
			}
			st, _ := os.Stat(p)

			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				x, e := Open(p, provenance(), Limits{})
				if e != nil {
					b.Fatal(e)
				}
				x.Close()
			}
			b.ReportMetric(float64(st.Size()), "filebytes")
		})
	}
}

func TestCandidateMaterializationBudget(t *testing.T) {
	a := fixture(1)
	b := &a.Bindings[0]
	b.Status = "ambiguous"
	b.SymbolID = ""
	for i := 0; i < 2200; i++ {
		s := a.Symbols[0]
		s.Key.Descriptor = fmt.Sprint(i) + strings.Repeat("X", 1024)
		s.ID = s.ComputeID()
		a.Symbols = append(a.Symbols, s)
		b.CandidateSymbolIDs = append(b.CandidateSymbolIDs, s.ID)
	}
	sort.Strings(b.CandidateSymbolIDs)
	b.ID = b.ComputeID()
	x, _ := openFixture(t, a)
	if _, e := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs"}, 1); e == nil {
		t.Fatal("candidate payload exceeded budget")
	}
}
func TestSourceContextPastDiscoveryLimit(t *testing.T) {
	a := fixture(0)
	for i := 0; i < 110; i++ {
		c := a.Contexts[0]
		c.Project = fmt.Sprintf("Project%d.csproj", i)
		c.ID = c.ComputeID()
		a.Contexts = append(a.Contexts, c)
	}
	x, _ := openFixture(t, a)
	got, truncated, e := x.SourceContexts(context.Background(), "repo", "src/A.cs", 100)
	if e != nil || !truncated || len(got) != 100 {
		t.Fatal("discovery cap")
	}
	found := map[string]bool{}
	for _, r := range got {
		found[r.Context.ID] = true
	}
	for _, c := range a.Contexts {
		if !found[c.ID] {
			r, ok, e := x.SourceContext(context.Background(), "repo", "src/A.cs", c.ID)
			if e != nil || !ok || r.Context.ID != c.ID {
				t.Fatal("selected context starved by cap")
			}
			return
		}
	}
	t.Fatal("no outside context")
}
func TestBuildRejectsInvalidUTF8(t *testing.T) {
	a := fixture(1)
	a.Symbols[0].Key.Descriptor = string([]byte{0xff})
	a.Symbols[0].ID = a.Symbols[0].ComputeID()
	a.Bindings[0].SymbolID = a.Symbols[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	if e := a.Validate(); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "bad")
	if Build(p, a, provenance()) == nil {
		t.Fatal("invalid UTF8 built")
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("invalid output published")
	}
}
func BenchmarkBindingLookup(b *testing.B) {
	a := fixture(10000)
	p := filepath.Join(b.TempDir(), "idx")
	if e := Build(p, a, provenance()); e != nil {
		b.Fatal(e)
	}
	x, e := Open(p, provenance(), Limits{})
	if e != nil {
		b.Fatal(e)
	}
	defer x.Close()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		r, e := x.Bindings(context.Background(), Position{Repo: "repo", Path: "src/A.cs", Offset: 50000}, 1)
		if e != nil || len(r.Results) != 1 {
			b.Fatal(e)
		}
	}
}
