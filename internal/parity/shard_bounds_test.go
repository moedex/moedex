package parity

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/ingest"
)

func boundedManifest(t *testing.T, root string, target int64, selector index.GramSelector) *Manifest {
	t.Helper()
	work := t.TempDir()
	if _, e := Build(Config{Root: root, WorkDir: work, Seed: 1, ShardBytes: target, Selector: selector}); e != nil {
		t.Fatal(e)
	}
	m, e := LoadManifest(filepath.Join(work, "shards", ManifestName))
	if e != nil {
		t.Fatal(e)
	}
	return m
}

// This oracle counts every path independently of content dedup and compares the
// exact bytes behind each file reference, detecting duplicated carried shards.
func shardFiles(t *testing.T, m *Manifest) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, s := range m.Shards {
		blobs, e := diskstore.LoadBlobs(s.Path)
		if e != nil {
			t.Fatal(e)
		}
		var n int64
		files := 0
		for _, b := range blobs {
			for _, f := range b.Files {
				key := f.Repo + "/" + f.RelPath
				if _, ok := got[key]; ok {
					t.Fatalf("duplicate occurrence %s", key)
				}
				got[key] = string(b.Content)
				n += int64(len(b.Content))
				files++
			}
		}
		if n != s.ContentBytes {
			t.Fatalf("manifest bytes %d actual %d", s.ContentBytes, n)
		}
		if s.ContentBytes > m.ShardBytes && files != 1 {
			t.Fatalf("oversize shard contains %d files", files)
		}
	}
	return got
}
func TestSingleRepositoryShardBounds(t *testing.T) {
	for _, selective := range []bool{false, true} {
		t.Run(fmt.Sprint(selective), func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{}
			want := map[string]string{}
			for i := 0; i < 5; i++ {
				name := fmt.Sprintf("%d.cs", i)
				files[name] = fmt.Sprint(i) + strings.Repeat("x", 39)
				want["repo/"+name] = files[name]
			}
			commitGitRepo(t, filepath.Join(root, "repo"), files)
			var sel index.GramSelector
			if selective {
				sel = index.FrequencyThresholdSelector{MaxDocFraction: 0.5}
			}
			m := boundedManifest(t, root, 100, sel)
			if m.ShardBytes != 100 || len(m.Shards) != 3 {
				t.Fatalf("target/shards: %d/%d", m.ShardBytes, len(m.Shards))
			}
			for _, s := range m.Shards {
				if len(s.Repos) != 1 || s.Repos[0] != filepath.Join(root, "repo") {
					t.Fatal("repo membership lost")
				}
			}
			if got := shardFiles(t, m); !reflect.DeepEqual(got, want) {
				t.Fatal("content mismatch")
			}
		})
	}
}
func TestOversizeFileStandsAlone(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	commitGitRepo(t, repo, map[string]string{"a.cs": strings.Repeat("A", 150), "b.cs": strings.Repeat("B", 40), "c.cs": strings.Repeat("C", 40)})
	old := boundedManifest(t, root, 100, nil)
	if len(old.Shards) != 2 {
		t.Fatalf("shards %d", len(old.Shards))
	}
	shardFiles(t, old)
	refreshed, e := Rebuild(old, Changes{Changed: []string{repo}}, t.TempDir(), time.Now(), ingest.Head)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(shardFiles(t, old), shardFiles(t, refreshed)) {
		t.Fatal("oversize refresh differs")
	}
}
func TestRebuildClosesOverOverlappingRepositoryShards(t *testing.T) {
	root := t.TempDir()
	a, b, c := filepath.Join(root, "A"), filepath.Join(root, "B"), filepath.Join(root, "C")
	commitGitRepo(t, a, map[string]string{"a.cs": strings.Repeat("A", 40)})
	commitGitRepo(t, b, map[string]string{"b1.cs": strings.Repeat("B", 40), "b2.cs": strings.Repeat("b", 40)})
	commitGitRepo(t, c, map[string]string{"c.cs": strings.Repeat("C", 40)})
	d := filepath.Join(root, "D")
	commitGitRepo(t, d, map[string]string{"d.cs": strings.Repeat("D", 150)})
	old := boundedManifest(t, root, 100, nil)
	originalD, e := os.ReadFile(old.Shards[len(old.Shards)-1].Path)
	if e != nil {
		t.Fatal(e)
	}
	if len(old.Shards) != 3 || !reflect.DeepEqual(old.Shards[0].Repos, []string{a, b}) || !reflect.DeepEqual(old.Shards[1].Repos, []string{b, c}) {
		t.Fatalf("overlap fixture not formed: %+v", old.Shards)
	}
	writeFiles(t, a, map[string]string{"a.cs": strings.Repeat("Z", 40)})
	gitCommitAll(t, a, "change A")
	refreshed, e := Rebuild(old, Changes{Changed: []string{a}}, t.TempDir(), time.Now(), ingest.Head)
	if e != nil {
		t.Fatal(e)
	}
	carriedD := false
	for _, shard := range refreshed.Shards {
		if reflect.DeepEqual(shard.Repos, []string{d}) {
			data, e := os.ReadFile(shard.Path)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(data, originalD) {
				t.Fatal("disjoint shard rewritten")
			}
			carriedD = true
		}
	}
	if !carriedD {
		t.Fatal("disjoint shard not carried")
	}
	clean := boundedManifest(t, root, 100, nil)
	if !reflect.DeepEqual(shardFiles(t, refreshed), shardFiles(t, clean)) {
		t.Fatal("refresh differs from clean rebuild")
	}
	if refreshed.ShardBytes != 100 {
		t.Fatal("lost target")
	}
	// Deleting the bridging repository must remove it from every old shard.
	removed, e := Rebuild(old, Changes{Removed: []string{b}}, t.TempDir(), time.Now(), ingest.Head)
	if e != nil {
		t.Fatal(e)
	}
	got := shardFiles(t, removed)
	want := map[string]string{"A/a.cs": strings.Repeat("Z", 40), "C/c.cs": strings.Repeat("C", 40), "D/d.cs": strings.Repeat("D", 150)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bridge removal survivors: %+v", got)
	}
	for _, head := range removed.Heads {
		if head.Dir == b {
			t.Fatal("removed repo head retained")
		}
	}
}
func TestLegacyManifestUsesDefaultShardTarget(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	commitGitRepo(t, repo, map[string]string{"a.cs": "class A {}"})
	old := boundedManifest(t, root, 100, nil)
	old.ShardBytes = 0
	m, e := Rebuild(old, Changes{Changed: []string{repo}}, t.TempDir(), time.Now(), ingest.Head)
	if e != nil {
		t.Fatal(e)
	}
	if m.ShardBytes != DefaultShardBytes {
		t.Fatal("legacy target not defaulted")
	}
}

func TestDuplicateBytesAcrossShardBoundaries(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	content := strings.Repeat("X", 40)
	commitGitRepo(t, a, map[string]string{"a.cs": content, "b.cs": content, "c.cs": content})
	commitGitRepo(t, b, map[string]string{"d.cs": content})
	m := boundedManifest(t, root, 100, nil)
	if len(m.Shards) != 2 || !reflect.DeepEqual(m.Shards[0].Repos, []string{a}) || !reflect.DeepEqual(m.Shards[1].Repos, []string{a, b}) {
		t.Fatalf("wrong shared-byte shard attribution: %+v", m.Shards)
	}
	want := map[string]string{"A/a.cs": content, "A/b.cs": content, "A/c.cs": content, "B/d.cs": content}
	if got := shardFiles(t, m); !reflect.DeepEqual(got, want) {
		t.Fatal("dedup lost file references")
	}
	for _, shard := range m.Shards {
		blobs, e := diskstore.LoadBlobs(shard.Path)
		if e != nil {
			t.Fatal(e)
		}
		if len(blobs) != 1 {
			t.Fatal("within-shard dedup lost")
		}
	}
}
