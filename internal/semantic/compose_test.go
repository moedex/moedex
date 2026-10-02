package semantic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func cloneComposeArtifact(t *testing.T, a *Artifact) *Artifact {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var out Artifact
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}
func composeEnvelope(t *testing.T, a *Artifact) []byte {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	out, err := json.Marshal(envelope{Format, FormatVersion, hex.EncodeToString(digest[:]), raw})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func composeProject(project string) *Artifact {
	a := fixture()
	dep := a.Contexts[0]
	dep.Project = "Dependency/Dependency.csproj"
	dep.ID = dep.ComputeID()
	c := &a.Contexts[0]
	c.Project = project
	c.DependencyContextIDs = []string{dep.ID}
	c.ID = c.ComputeID()
	a.Occurrences[0].ContextID = c.ID
	a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
	a.Bindings[0].OccurrenceID = a.Occurrences[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	diagnostic := Diagnostic{ContextID: c.ID, Code: "TEST01", Severity: "warning", Message: "captured warning"}
	diagnostic.ID = diagnostic.ComputeID()
	a.Diagnostics = []Diagnostic{diagnostic}
	a.Contexts = append(a.Contexts, dep)
	return a
}
func TestComposeDeterministicCompleteUnion(t *testing.T) {
	a, b := composeProject("A/A.csproj"), composeProject("B/B.csproj")
	var expected []byte
	for _, inputs := range [][]*Artifact{{a, b}, {b, a}, {a, b, a}, {b, a, b}} {
		out, err := Compose(context.Background(), inputs, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Snapshots) != 1 || len(out.Sources) != 1 || len(out.Symbols) != 1 || len(out.Contexts) != 3 || len(out.Occurrences) != 2 || len(out.Bindings) != 2 || len(out.Diagnostics) != 2 || !out.Complete() {
			t.Fatalf("bad union %#v", out)
		}
		for _, c := range out.Contexts {
			if c.Project != "Dependency/Dependency.csproj" && len(c.DependencyContextIDs) != 1 {
				t.Fatal("lost dependency")
			}
		}
		if !sort.SliceIsSorted(out.Contexts, func(i, j int) bool { return out.Contexts[i].ID < out.Contexts[j].ID }) {
			t.Fatal("not sorted")
		}
		raw := composeEnvelope(t, out)
		if expected == nil {
			expected = raw
		} else if !bytes.Equal(raw, expected) {
			t.Fatal("input order/repeats changed output")
		}
	}
}
func TestComposeOwnsNestedValues(t *testing.T) {
	a := fixture()
	f := &a.Sources[0]
	f.Generated = true
	f.Content = []byte("\ufeffπ Target")
	f.ID = f.ComputeID()
	c := &a.Contexts[0]
	c.SourceIDs = []string{f.ID}
	c.ID = c.ComputeID()
	a.Occurrences[0].SourceID = f.ID
	a.Occurrences[0].ContextID = c.ID
	a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
	a.Bindings[0].OccurrenceID = a.Occurrences[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	before := cloneComposeArtifact(t, a)
	out, err := Compose(context.Background(), []*Artifact{a}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	out.Sources[0].Content[0] = 0
	out.Contexts[0].SourceIDs[0] = "changed"
	out.Symbols[0].Key.Descriptor = "changed"
	if !reflect.DeepEqual(a, before) {
		t.Fatal("composition aliases or mutates inputs")
	}
}
func TestComposeRejectsContradictionsAndIncompleteInputs(t *testing.T) {
	a := fixture()
	conflict := cloneComposeArtifact(t, a)
	conflict.Bindings[0].Status = "unresolved"
	conflict.Bindings[0].SymbolID = ""
	conflict.Bindings[0].ID = conflict.Bindings[0].ComputeID()
	if err := conflict.Validate(); err != nil {
		t.Fatal(err)
	}
	if out, err := Compose(context.Background(), []*Artifact{a, conflict}, Limits{}); err == nil || out != nil {
		t.Fatal("accepted conflicting bindings at one occurrence")
	}
	sameID := cloneComposeArtifact(t, a)
	sameID.Contexts[0].Capture += " "
	if out, err := Compose(context.Background(), []*Artifact{a, sameID}, Limits{}); err == nil || !strings.Contains(err.Error(), "contradictory record") || out != nil {
		t.Fatalf("audit conflict %v %v", out, err)
	}
	incomplete := cloneComposeArtifact(t, a)
	incomplete.Contexts[0].Status = "incomplete"
	for _, inputs := range [][]*Artifact{nil, {nil}, {incomplete}, {fixture(), nil}} {
		if out, err := Compose(context.Background(), inputs, Limits{}); err == nil || out != nil {
			t.Fatal("accepted invalid inputs")
		}
	}
	many := make([]*Artifact, MaxComposeInputs+1)
	for i := range many {
		many[i] = a
	}
	if _, err := Compose(context.Background(), many, Limits{}); err == nil {
		t.Fatal("accepted excessive inputs")
	}
}
func TestComposeExactEnvelopeAndAggregateByteBounds(t *testing.T) {
	a := fixture()
	encoded := composeEnvelope(t, a)
	n := int64(len(encoded))
	raw, _ := json.Marshal(a)
	if composeEnvelopeSize(int64(len(raw))) != n {
		t.Fatal("envelope size estimate is not exact")
	}
	if _, err := Compose(context.Background(), []*Artifact{a}, Limits{MaxBytes: n}); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(context.Background(), []*Artifact{a}, Limits{MaxBytes: n - 1}); err == nil {
		t.Fatal("accepted one byte below exact envelope size")
	}
	if _, err := Compose(context.Background(), []*Artifact{a, a}, Limits{MaxBytes: 2 * n}); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(context.Background(), []*Artifact{a, a}, Limits{MaxBytes: 2*n - 1}); err == nil {
		t.Fatal("duplicate inputs escaped aggregate budget")
	}
	for _, limit := range []int64{-1, DefaultMaxBytes + 1} {
		if _, err := Compose(context.Background(), []*Artifact{a}, Limits{MaxBytes: limit}); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
}

type composeCancelContext struct {
	context.Context
	remaining int
}

func (c *composeCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
func TestComposeCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{canceled, &composeCancelContext{Context: context.Background(), remaining: 6}} {
		out, err := Compose(ctx, []*Artifact{fixture(), fixture()}, Limits{})
		if !errors.Is(err, context.Canceled) || out != nil {
			t.Fatalf("cancel %v %v", out, err)
		}
	}
}
func TestReadFromSharesStrictBoundedEnvelopeValidation(t *testing.T) {
	a := fixture()
	raw := composeEnvelope(t, a)
	got, err := ReadFrom(bytes.NewReader(raw), Limits{MaxBytes: int64(len(raw))})
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("read %v %v", got, err)
	}
	if _, err := ReadFrom(bytes.NewReader(raw), Limits{MaxBytes: int64(len(raw) - 1)}); err == nil {
		t.Fatal("reader byte bound ignored")
	}
	if _, err := ReadFrom(bytes.NewReader(append(raw, []byte(" {}")...)), Limits{}); err == nil {
		t.Fatal("trailing envelope accepted")
	}
}
