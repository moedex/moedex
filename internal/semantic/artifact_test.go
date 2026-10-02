package semantic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func fixture() *Artifact {
	s := SourceSnapshot{Repo: "repo", InputFingerprint: sum("source roster")}
	s.ID = s.ComputeID()
	f := Source{SnapshotID: s.ID, Path: "Shared.cs", RawSHA256: sum("\ufeffπ Target"), ByteSize: uint64(len("\ufeffπ Target"))}
	f.ID = f.ComputeID()
	c := BuildContext{SnapshotID: s.ID, Project: "A/A.csproj", Capture: "{\n \"define\": \"FAST\"\n}", Extractor: "roslyn", ExtractorVersion: "1", Status: "complete", SourceIDs: []string{f.ID}}
	c.InputFingerprint = sum(c.Capture)
	c.ID = c.ComputeID()
	symbol := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A", Descriptor: "T:Target`1", DescriptorKind: "documentation_comment_id"}}
	symbol.ID = symbol.ComputeID()
	o := Occurrence{SourceID: f.ID, ContextID: c.ID, Role: "reference", Kind: "name", Offset: 6, Length: 6}
	o.ID = o.ComputeID()
	b := Binding{OccurrenceID: o.ID, Status: "resolved", SymbolID: symbol.ID, Method: "semantic_model", Extractor: c.Extractor, ExtractorVersion: c.ExtractorVersion}
	b.ID = b.ComputeID()
	return &Artifact{Version: FormatVersion, Snapshots: []SourceSnapshot{s}, Sources: []Source{f}, Contexts: []BuildContext{c}, Symbols: []Symbol{symbol}, Occurrences: []Occurrence{o}, Bindings: []Binding{b}}
}

func TestArtifactRoundtripExactCaptureAndExclusiveWrite(t *testing.T) {
	a := fixture()
	p := filepath.Join(t.TempDir(), "semantic.json")
	if e := Write(p, a); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(p)
	got, e := Read(p, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a, got) {
		t.Fatalf("roundtrip mismatch: %#v", got)
	}
	if !got.Complete() {
		t.Fatal("complete artifact rejected")
	}
	if e := Write(p, a); e == nil {
		t.Fatal("overwrite accepted")
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(original, after) {
		t.Fatal("existing artifact mutated")
	}
	p2 := filepath.Join(t.TempDir(), "semantic.json")
	if e := Write(p2, a); e != nil {
		t.Fatal(e)
	}
	second, _ := os.ReadFile(p2)
	if !bytes.Equal(original, second) {
		t.Fatal("nondeterministic bytes")
	}
}

func TestStableSymbolsAndContextualOccurrences(t *testing.T) {
	a := fixture()
	c := a.Contexts[0]
	c.Project = "B/B.csproj"
	c.Capture = `{"define":"SLOW"}`
	c.InputFingerprint = sum(c.Capture)
	c.ID = c.ComputeID()
	a.Contexts = append(a.Contexts, c)
	o := a.Occurrences[0]
	o.ContextID = c.ID
	o.ID = o.ComputeID()
	a.Occurrences = append(a.Occurrences, o)
	s := a.Symbols[0]
	s.Key.Namespace = "repo/B"
	s.ID = s.ComputeID()
	a.Symbols = append(a.Symbols, s)
	b := a.Bindings[0]
	b.OccurrenceID = o.ID
	b.SymbolID = s.ID
	b.ID = b.ComputeID()
	a.Bindings = append(a.Bindings, b)
	if e := a.Validate(); e != nil {
		t.Fatal(e)
	}
	if a.Occurrences[0].ID == o.ID || a.Bindings[0].ID == b.ID {
		t.Fatal("shared bytes erased project context")
	}
	first := a.Symbols[0]
	changed := first
	changed.Key.Descriptor = "T:Target`2"
	if changed.ComputeID() == first.ID {
		t.Fatal("arity erased")
	}
	c2 := c
	c2.Capture = `{"define":"FAST"}`
	c2.InputFingerprint = sum(c2.Capture)
	if c2.ComputeID() == c.ID {
		t.Fatal("configuration omitted from context")
	}
	if first.ComputeID() != first.ID {
		t.Fatal("stable symbol changed with build context")
	}
}

func TestArtifactRejectsCorruptionAndLimits(t *testing.T) {
	a := fixture()
	p := filepath.Join(t.TempDir(), "a.json")
	if e := Write(p, a); e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(p)
	var env envelope
	if e := json.Unmarshal(data, &env); e != nil {
		t.Fatal(e)
	}
	tests := map[string][]byte{"truncated": data[:len(data)-1], "trailing": append(append([]byte{}, data...), []byte(" {}")...)}
	bad := env
	bad.Payload = append([]byte{}, bad.Payload...)
	bad.Payload[0] = '['
	tests["hash"], _ = json.Marshal(bad)
	bad = env
	bad.Version++
	tests["version"], _ = json.Marshal(bad)
	tests["unknown"] = append([]byte(`{"surprise":true,`), data[1:]...)
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			q := filepath.Join(t.TempDir(), "bad")
			os.WriteFile(q, b, 0600)
			if _, e := Read(q, Limits{}); e == nil {
				t.Fatal("accepted invalid artifact")
			}
		})
	}
	if _, e := Read(p, Limits{MaxBytes: int64(len(data) - 1)}); e == nil {
		t.Fatal("ignored size bound")
	}
	if _, e := Read(p, Limits{MaxBytes: -1}); e == nil {
		t.Fatal("accepted negative bound")
	}
}

func TestValidationRejectsFalseProvenance(t *testing.T) {
	tests := map[string]func(*Artifact){
		"identity":  func(a *Artifact) { a.Symbols[0].Key.Descriptor = "T:Other" },
		"traversal": func(a *Artifact) { a.Sources[0].Path = "../Outside.cs"; a.Sources[0].ID = a.Sources[0].ComputeID() },
		"span overflow": func(a *Artifact) {
			a.Occurrences[0].Offset = ^uint64(0)
			a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
		},
		"wrong membership": func(a *Artifact) { a.Contexts[0].SourceIDs = nil; a.Contexts[0].ID = a.Contexts[0].ComputeID() },
		"capture digest":   func(a *Artifact) { a.Contexts[0].Capture = `{"define":"OTHER"}` },
		"unknown symbol":   func(a *Artifact) { a.Bindings[0].SymbolID = "absent"; a.Bindings[0].ID = a.Bindings[0].ComputeID() },
		"duplicate":        func(a *Artifact) { a.Symbols = append(a.Symbols, a.Symbols[0]) },
		"false complete": func(a *Artifact) {
			a.Contexts[0].Issues = []string{"missing dependency"}
			a.Contexts[0].ID = a.Contexts[0].ComputeID()
		},
		"ambiguous resolved": func(a *Artifact) { a.Bindings[0].Status = "ambiguous"; a.Bindings[0].ID = a.Bindings[0].ComputeID() },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			a := fixture()
			mutate(a)
			if a.Validate() == nil {
				t.Fatal("accepted invalid provenance")
			}
			p := filepath.Join(t.TempDir(), "invalid")
			if Write(p, a) == nil {
				t.Fatal("persisted invalid artifact")
			}
			if _, e := os.Stat(p); !os.IsNotExist(e) {
				t.Fatal("invalid write published file")
			}
		})
	}
}

func TestIncompleteAndAmbiguousAreDiagnosticArtifacts(t *testing.T) {
	a := fixture()
	a.Contexts[0].Status = "incomplete"
	a.Contexts[0].Issues = []string{"CS0121"}
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	a.Occurrences[0].ContextID = a.Contexts[0].ID
	a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
	s := a.Symbols[0]
	s.Key.Descriptor = "T:Target`2"
	s.ID = s.ComputeID()
	a.Symbols = append(a.Symbols, s)
	b := &a.Bindings[0]
	b.OccurrenceID = a.Occurrences[0].ID
	b.Status = "ambiguous"
	b.SymbolID = ""
	b.CandidateSymbolIDs = []string{a.Symbols[0].ID, s.ID}
	sort.Strings(b.CandidateSymbolIDs)
	b.ID = b.ComputeID()
	if e := a.Validate(); e != nil {
		t.Fatal(e)
	}
	if a.Complete() {
		t.Fatal("diagnostic marked complete")
	}
	p := filepath.Join(t.TempDir(), "diagnostic")
	if e := Write(p, a); e != nil {
		t.Fatal(e)
	}
	got, e := Read(p, Limits{})
	if e != nil || got.Complete() {
		t.Fatalf("diagnostic roundtrip: %v", e)
	}
}

func TestGeneratedSourceRetainsExactBytes(t *testing.T) {
	a := fixture()
	s := &a.Sources[0]
	s.Generated = true
	s.Content = []byte("\ufeffπ Target")
	s.ID = s.ComputeID()
	a.Contexts[0].SourceIDs = []string{s.ID}
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	a.Occurrences[0].SourceID = s.ID
	a.Occurrences[0].ContextID = a.Contexts[0].ID
	a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
	a.Bindings[0].OccurrenceID = a.Occurrences[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	p := filepath.Join(t.TempDir(), "generated")
	if e := Write(p, a); e != nil {
		t.Fatal(e)
	}
	got, e := Read(p, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got.Sources[0].Content, s.Content) {
		t.Fatal("generated bytes lost")
	}
	s.Content[0] = 'x'
	if a.Validate() == nil {
		t.Fatal("accepted corrupt generated source bytes")
	}
}

func TestBindingEnclosingSymbolRoundtrip(t *testing.T) {
	a := fixture()
	owner := Symbol{Key: SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/A", Descriptor: "M:Caller.Run", DescriptorKind: "documentation_comment_id"}}
	owner.ID = owner.ComputeID()
	a.Symbols = append(a.Symbols, owner)
	previousID := a.Bindings[0].ID
	a.Bindings[0].EnclosingSymbolID = owner.ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	if previousID == a.Bindings[0].ID {
		t.Fatal("enclosing evidence omitted from binding identity")
	}
	p := filepath.Join(t.TempDir(), "enclosing")
	if e := Write(p, a); e != nil {
		t.Fatal(e)
	}
	got, e := Read(p, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if got.Bindings[0].EnclosingSymbolID != owner.ID {
		t.Fatal("enclosing evidence lost")
	}
	a.Bindings[0].EnclosingSymbolID = "symbol:outside-artifact"
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	if a.Validate() == nil {
		t.Fatal("unknown enclosing symbol accepted")
	}
}

func TestEveryOccurrenceRequiresExplicitBinding(t *testing.T) {
	for _, role := range []string{"declaration", "reference"} {
		for _, status := range []string{"complete", "incomplete"} {
			t.Run(role+"/"+status, func(t *testing.T) {
				a := fixture()
				a.Contexts[0].Status = status
				a.Contexts[0].ID = a.Contexts[0].ComputeID()
				o := &a.Occurrences[0]
				o.ContextID = a.Contexts[0].ID
				o.Role = role
				if role == "declaration" {
					o.Kind = "declaration"
				}
				o.ID = o.ComputeID()
				a.Bindings = nil
				if a.Validate() == nil {
					t.Fatal("missing binding status accepted")
				}
				if Write(filepath.Join(t.TempDir(), "missing"), a) == nil {
					t.Fatal("missing binding persisted")
				}
				b := Binding{OccurrenceID: o.ID, Status: "unresolved", Method: "semantic_model", Extractor: a.Contexts[0].Extractor, ExtractorVersion: a.Contexts[0].ExtractorVersion}
				b.ID = b.ComputeID()
				a.Bindings = []Binding{b}
				if e := a.Validate(); e != nil {
					t.Fatalf("explicit unresolved rejected: %v", e)
				}
			})
		}
	}
}
