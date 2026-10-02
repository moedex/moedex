package indexcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func TestSemanticWorkspaceMap(t *testing.T) {
	a := &semantic.Artifact{Snapshots: []semantic.SourceSnapshot{{ID: "one"}, {ID: "two"}}}
	base := t.TempDir()
	roots, err := decodeSemanticWorkspaces([]byte(`{"one":"a","two":"b"}`), base, a)
	if err != nil || roots["one"] != filepath.Join(base, "a") || roots["two"] != filepath.Join(base, "b") {
		t.Fatalf("roots=%v err=%v", roots, err)
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `{"one":"a"}`, `{"one":"a","two":"b","extra":"c"}`, `{"one":"a","one":"b","two":"c"}`, `{"one":null,"two":"b"}`, `{"one":"","two":"b"}`, `{"one":3,"two":"b"}`, `{"one":"a","two":"b"} {}`, `{"one":"a","two":"b"`} {
		if _, err := decodeSemanticWorkspaces([]byte(raw), base, a); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	path := filepath.Join(base, "huge.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := semanticWorkspaces(path, a); err == nil {
		t.Fatal("accepted oversized map")
	}
}

func TestSnapshotSemanticWorkspaceFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--corpus", t.TempDir(), "--semantic-workspaces", "map.json"},
		{"--corpus", t.TempDir(), "--semantic-artifact", "artifact.json", "--semantic-workspace", "root", "--semantic-workspaces", "map.json"},
	} {
		if err := runSnapshotBuild(args); err == nil {
			t.Fatal("accepted inconsistent workspace flags")
		}
	}
}
