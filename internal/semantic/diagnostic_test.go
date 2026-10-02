package semantic

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func diagnosticFixture() *Artifact {
	a := fixture()
	a.Contexts[0].Status = "incomplete"
	a.Contexts[0].ID = a.Contexts[0].ComputeID()
	a.Occurrences[0].ContextID = a.Contexts[0].ID
	a.Occurrences[0].ID = a.Occurrences[0].ComputeID()
	a.Bindings[0].OccurrenceID = a.Occurrences[0].ID
	a.Bindings[0].ID = a.Bindings[0].ComputeID()
	d := Diagnostic{ContextID: a.Contexts[0].ID, Code: "CS0103", Severity: "error", Message: "Name 'Target' does not exist", SourceID: a.Sources[0].ID, Offset: 6, Length: 6}
	d.ID = d.ComputeID()
	u := Diagnostic{ContextID: a.Contexts[0].ID, Code: "MSB1009", Severity: "error", Message: "Project file does not exist"}
	u.ID = u.ComputeID()
	a.Diagnostics = []Diagnostic{d, u}
	return a
}

func TestDiagnosticRoundtrip(t *testing.T) {
	a := diagnosticFixture()
	p := filepath.Join(t.TempDir(), "diagnostics")
	if e := Write(p, a); e != nil {
		t.Fatal(e)
	}
	got, e := Read(p, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a.Diagnostics, got.Diagnostics) {
		t.Fatal("diagnostic evidence changed")
	}
	if got.Complete() {
		t.Fatal("error artifact is complete")
	}
	for _, severity := range []string{"warning", "info", "hidden"} {
		a := fixture()
		d := Diagnostic{ContextID: a.Contexts[0].ID, Code: "CS1591", Severity: severity, Message: "Missing documentation", SourceID: a.Sources[0].ID, Offset: a.Sources[0].ByteSize}
		d.ID = d.ComputeID()
		a.Diagnostics = []Diagnostic{d}
		if e := a.Validate(); e != nil {
			t.Fatalf("%s EOF insertion: %v", severity, e)
		}
	}
}

func TestDiagnosticValidation(t *testing.T) {
	tests := map[string]func(*Diagnostic){
		"missing context":   func(d *Diagnostic) { d.ContextID = "unknown" },
		"missing source":    func(d *Diagnostic) { d.SourceID = "unknown" },
		"locationless span": func(d *Diagnostic) { d.SourceID = "" },
		"overflow":          func(d *Diagnostic) { d.Offset = ^uint64(0) },
		"length":            func(d *Diagnostic) { d.Length = ^uint64(0) },
		"severity":          func(d *Diagnostic) { d.Severity = "fatal" },
		"code":              func(d *Diagnostic) { d.Code = "" },
		"message":           func(d *Diagnostic) { d.Message = "" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			a := diagnosticFixture()
			change(&a.Diagnostics[0])
			a.Diagnostics[0].ID = a.Diagnostics[0].ComputeID()
			if a.Validate() == nil {
				t.Fatal("invalid diagnostic accepted")
			}
		})
	}
	a := fixture()
	d := Diagnostic{ContextID: a.Contexts[0].ID, Code: "CS0001", Severity: "error", Message: "Failed"}
	d.ID = d.ComputeID()
	a.Diagnostics = []Diagnostic{d}
	if a.Validate() == nil {
		t.Fatal("complete context accepted error")
	}
	a = diagnosticFixture()
	a.Diagnostics = append(a.Diagnostics, a.Diagnostics[0])
	if a.Validate() == nil {
		t.Fatal("duplicate diagnostic accepted")
	}
}

func TestOptionalSnapshotProvenanceSyntax(t *testing.T) {
	for _, s := range []string{"", "1", "123456789012345678901234567890"} {
		if !validProjectID(s) {
			t.Fatalf("valid project ID %q", s)
		}
	}
	for _, s := range []string{"0", "01", "-1", "+1", "abc", "1.0", " 1", "1\x00"} {
		if validProjectID(s) {
			t.Fatalf("invalid project ID %q", s)
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 40), strings.Repeat("B", 64)} {
		if !validCommit(s) {
			t.Fatalf("valid commit %q", s)
		}
	}
	for _, s := range []string{"main", "abc", strings.Repeat("g", 40), strings.Repeat("a", 41)} {
		if validCommit(s) {
			t.Fatalf("invalid commit %q", s)
		}
	}
	for _, field := range []string{"project", "commit"} {
		a := fixture()
		if field == "project" {
			a.Snapshots[0].ProjectID = "unknown"
		} else {
			a.Snapshots[0].Commit = "main"
		}
		a.Snapshots[0].ID = a.Snapshots[0].ComputeID()
		if e := a.Validate(); e == nil || !strings.Contains(e.Error(), "invalid snapshot") {
			t.Fatalf("%s validation: %v", field, e)
		}
	}
}
