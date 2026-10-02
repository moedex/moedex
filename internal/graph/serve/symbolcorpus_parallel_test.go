package graphserve

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

func TestShardSymbolsParallelMatchesSerial(t *testing.T) {
	idxs := []*index.Index{index.New(), index.New(), index.New(), index.New(), index.New(), nil}
	fixtures := []struct {
		shard              int
		path, sha, content string
	}{
		{0, "a.go", "go", "package a\nfunc Shared() {}\nfunc Caller() { Shared() }\n"},
		{1, "a.cs", "cs", "class A {\n public void Shared() {}\n public void Caller() { Shared(); }\n}\n"},
		{2, "a.ts", "ts", "export function Shared() {}\n"},
		// Content identity is deliberately shared with another extension. The first
		// file continues to select the extractor, even when a later file is C#.
		{3, "a.txt", "same", "class Ignored { public void Hidden() {} }"},
		{3, "b.cs", "same", "class Ignored { public void Hidden() {} }"},
	}
	for _, f := range fixtures {
		idxs[f.shard].AddFileUnindexed("repo", f.path, f.path, f.sha, []byte(f.content))
	}
	want := make([]*symbol.Index, len(idxs))
	for i, ix := range idxs {
		want[i] = symbol.BuildMulti(ix)
	}
	for _, workers := range []int{1, 2, 4, 20} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			got := buildShardSymbols(idxs, workers)
			for shard, ix := range idxs {
				if got[shard].NumBlobs() != want[shard].NumBlobs() || got[shard].NumRefBlobs() != want[shard].NumRefBlobs() {
					t.Fatalf("shard %d counts differ", shard)
				}
				if ix == nil {
					continue
				}
				for blob := uint64(0); blob < uint64(ix.NumBlobs()); blob++ {
					if !reflect.DeepEqual(got[shard].Symbols(blob), want[shard].Symbols(blob)) {
						t.Fatalf("shard %d blob %d definitions differ", shard, blob)
					}
					if !reflect.DeepEqual(got[shard].Refs(blob), want[shard].Refs(blob)) {
						t.Fatalf("shard %d blob %d references differ", shard, blob)
					}
				}
			}
		})
	}
}

func TestOpenSymbolsParallelPublishesSortedShards(t *testing.T) {
	dir := buildDedupedDir(t, map[string]map[string]string{
		"a": {"a.go": "package a\nfunc Shared() {}\n"},
		"b": {"b.txt": "no symbols"},
		"c": {"c.cs": "class C {\n public void Shared() {}\n public void Caller() { Shared(); }\n}\n"},
		"d": {"d.go": "package a\nfunc Shared() {}\n"},
	})
	got, err := OpenSymbols(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	paths, err := globShards(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := symbol.NewCorpus()
	for i, ix := range got.idxs {
		want.AddShard(filepath.Base(paths[i]), symbol.BuildMulti(ix))
	}
	for i, p := range paths {
		if got.Merged().ShardName(i) != filepath.Base(p) {
			t.Fatalf("shard %d identity changed", i)
		}
	}
	for _, name := range []string{"Shared", "Caller", "C", "Missing"} {
		if !reflect.DeepEqual(got.Merged().References(name), want.References(name)) {
			t.Fatalf("%s references differ", name)
		}
		if !reflect.DeepEqual(got.Merged().Definitions(name), want.Definitions(name)) {
			t.Fatalf("%s definitions differ", name)
		}
	}
	if got.Merged().ShardIndex(1).NumBlobs() != 0 {
		t.Fatal("empty-symbol shard lost its slot")
	}
}

// Ingest retains the raw Git identity while stripping the BOM from indexed
// bytes. Opening symbols must use those bytes without rehashing or renormalizing.
func TestOpenSymbolsPreservesNormalizedBlobIdentity(t *testing.T) {
	dir := t.TempDir()
	content := []byte("class C {\n public void Main() {}\n}\n")
	raw := append([]byte{0xef, 0xbb, 0xbf}, content...)
	sha := diskstore.GitBlobSHA1(raw)
	ix := index.New()
	ix.AddFileUnindexed("repo", "a.cs", "/repo/a.cs", sha, content)
	if err := diskstore.Save(ix, filepath.Join(dir, "000.idx")); err != nil {
		t.Fatal(err)
	}
	sc, err := OpenSymbols(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	defs := sc.Definitions("Main")
	if len(defs) != 1 || defs[0].SHA != sha || defs[0].Start != bytes.Index(content, []byte("Main")) {
		t.Fatalf("normalized identity changed: %+v", defs)
	}
	if !bytes.Equal(sc.ShardBlob(0, 0).Content, content) {
		t.Fatal("indexed bytes changed")
	}
}
