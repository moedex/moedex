package cluster

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSidecarRoundTripAndGenerationBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clusters.json")
	want := Sidecar{
		Version: SidecarVersion, Generation: 7, Status: StatusAvailable,
		ObservedNodes: 2, ObservedEdges: 1, EligibleNodes: 2, EligibleEdges: 1, Cap: 10,
		Clusters: []Cluster{{ClusterID: 1, Label: "svc", MemberCount: 2, Members: []Node{{ID: "a"}, {ID: "b"}}}},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("sidecar mode = %v, %v; want 0644", info, err)
	}
	got, err := Load(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("sidecar round trip:\n got  %#v\n want %#v", *got, want)
	}
	if _, err := Load(path, 8); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("generation mismatch error = %v, want stale", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".clusters-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("atomic save left temporary files: %v, %v", matches, err)
	}
}

func TestSidecarRejectsUnknownFieldsAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clusters.json")
	if _, err := Load(path, 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing sidecar error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"generation":1,"status":"available","clusters":[],"future":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, 1); err == nil {
		t.Fatal("unknown sidecar field accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"generation":1,"status":"available","clusters":[]} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, 1); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestSidecarRejectsPartialOverCapAndUnorderedMembers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clusters.json")
	partial := Sidecar{Version: SidecarVersion, Generation: 1, Status: StatusOverCap,
		ObservedNodes: 2, EligibleNodes: 2, Cap: 1, Clusters: []Cluster{{ClusterID: 1, Label: "svc", MemberCount: 1, Members: []Node{{ID: "a"}}}}}
	if err := Save(path, partial); err == nil {
		t.Fatal("partial over-cap communities accepted")
	}
	unordered := Sidecar{Version: SidecarVersion, Generation: 1, Status: StatusAvailable,
		ObservedNodes: 2, EligibleNodes: 2, Cap: 2, Clusters: []Cluster{{ClusterID: 1, Label: "svc", MemberCount: 2, Members: []Node{{ID: "b"}, {ID: "a"}}}}}
	if err := Save(path, unordered); err == nil {
		t.Fatal("unordered sidecar members accepted")
	}
	partialUnderCovered := Sidecar{Version: SidecarVersion, Generation: 1, Status: StatusUnderCovered,
		ObservedNodes: 202, ObservedEdges: 1, EligibleNodes: 2, EligibleEdges: 1, Cap: 250,
		Clusters: []Cluster{{ClusterID: 1, Label: "svc", MemberCount: 2, Members: []Node{{ID: "a"}, {ID: "b"}}}}}
	if err := Save(path, partialUnderCovered); err == nil {
		t.Fatal("under-covered communities accepted as representative")
	}
}
