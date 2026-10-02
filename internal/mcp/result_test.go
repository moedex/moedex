package mcp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/diskstore"
)

func TestSnapshotIdentityNormalizeIsDeterministic(t *testing.T) {
	got := (SnapshotIdentity{
		Cacheable:       true,
		BlobSHAs:        []string{"b", "a", "b", ""},
		UnanchoredPaths: []string{"/z", "/a", "/z"},
	}).Normalize()
	if got.Cacheable {
		t.Fatal("identity with unanchored paths must not be cacheable")
	}
	if !reflect.DeepEqual(got.BlobSHAs, []string{"a", "b"}) {
		t.Fatalf("blob_shas=%v", got.BlobSHAs)
	}
	if !reflect.DeepEqual(got.UnanchoredPaths, []string{"/a", "/z"}) {
		t.Fatalf("unanchored_paths=%v", got.UnanchoredPaths)
	}
}

func TestHashFilesUsesGitBlobSHAAndReportsUnreadablePaths(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "readable.go")
	missing := filepath.Join(dir, "missing.go")
	content := []byte("package fixture\n")
	if err := os.WriteFile(readable, content, 0o600); err != nil {
		t.Fatal(err)
	}

	hashes, unanchored := HashFiles(context.Background(), []string{missing, readable, readable}, 2)
	if got, want := hashes[readable], diskstore.GitBlobSHA1(content); got != want {
		t.Fatalf("blob sha=%q want %q", got, want)
	}
	if !reflect.DeepEqual(unanchored, []string{missing}) {
		t.Fatalf("unanchored=%v", unanchored)
	}
	identity := navigationTestIdentity(hashes, unanchored)
	if identity.Cacheable {
		t.Fatal("unreadable returned content must force cacheable=false")
	}
}

func navigationTestIdentity(hashes map[string]string, unanchored []string) SnapshotIdentity {
	identity := SnapshotIdentity{Cacheable: len(unanchored) == 0, UnanchoredPaths: unanchored}
	for _, sha := range hashes {
		identity.BlobSHAs = append(identity.BlobSHAs, sha)
	}
	return identity.Normalize()
}

func TestMergeSnapshotIdentitiesPreservesDistinctGraphCorpus(t *testing.T) {
	got := MergeSnapshotIdentities(
		SnapshotIdentity{Cacheable: true, CorpusFingerprint: "rank", BlobSHAs: []string{"rank-sha"}},
		SnapshotIdentity{Cacheable: true, CorpusFingerprint: "graph", GraphGeneration: 5, GraphBuildID: "build", BlobSHAs: []string{"graph-sha"}},
	)
	if got.CorpusFingerprint != "rank" || got.GraphCorpusFingerprint != "graph" || got.GraphGeneration != 5 || got.GraphBuildID != "build" {
		t.Fatalf("merged identity=%+v", got)
	}
	if !reflect.DeepEqual(got.BlobSHAs, []string{"graph-sha", "rank-sha"}) {
		t.Fatalf("blob_shas=%v", got.BlobSHAs)
	}
	if got.Cacheable {
		t.Fatal("conflicting corpus generations must not be cacheable")
	}
}

func TestSnapshotIdentityCorpusCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name        string
		graphCorpus string
		conflict    bool
	}{
		{"same source", "source", false},
		{"legacy missing fingerprint", "", false},
		{"different source", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := SnapshotIdentity{Cacheable: true, CorpusFingerprint: "source", GraphCorpusFingerprint: tc.graphCorpus, GraphGeneration: 99, GraphBuildID: "new-build"}
			if identity.HasCorpusConflict() != tc.conflict {
				t.Fatalf("unexpected compatibility: %+v", identity)
			}
			if got := identity.Normalize(); got.Cacheable == tc.conflict {
				t.Fatalf("unexpected cacheability: %+v", got)
			}
		})
	}
}

func TestStructuredErrorsAreNeverCacheableAndIdentifyServer(t *testing.T) {
	result := StructuredToolError("invalid_arguments", "bad input", SnapshotIdentity{
		Cacheable: true, CorpusFingerprint: "corpus", GraphGeneration: 5, GraphBuildID: "build",
	})
	snapshot := resultSnapshot(result)
	if snapshot.Cacheable {
		t.Fatal("tool error must never be cacheable")
	}
	if snapshot.CorpusFingerprint != "corpus" || snapshot.GraphGeneration != 5 || snapshot.GraphBuildID != "build" {
		t.Fatalf("error lost acquired snapshot identity: %+v", snapshot)
	}
	meta := result["_meta"].(map[string]interface{})
	server, ok := meta[ServerMetaKey].(ServerIdentity)
	if !ok || server.Version == "" {
		t.Fatalf("server identity=%#v", meta[ServerMetaKey])
	}
}
