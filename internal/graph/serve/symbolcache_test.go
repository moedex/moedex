package graphserve

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/index"
	"moedex/internal/symbol"
)

func symbolCacheSource() *index.Index {
	return index.Restore([]index.BlobData{{SHA: "intentionally-untrusted", Content: []byte("class Foo { void Run() { Run(); } }"), Files: []index.FileRef{{Repo: "repo", RelPath: "file.cs", AbsPath: "/repo/file.cs"}}}}, nil)
}

func TestGraphSymbolCacheRoundTripAndCorruption(t *testing.T) {
	ix := symbolCacheSource()
	fingerprint := graphSymbolShardFingerprint(ix)
	path := filepath.Join(t.TempDir(), "source.graph-symbols")
	want := symbol.BuildMulti(ix)
	if err := writeGraphSymbolCache(path, fingerprint, want); err != nil {
		t.Fatal(err)
	}
	got, err := readGraphSymbolCache(path, fingerprint, ix)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Symbols(0), want.Symbols(0)) || !reflect.DeepEqual(got.Refs(0), want.Refs(0)) {
		t.Fatal("cache round trip changed ordered records")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for end := 0; end < len(original); end++ {
		if err := os.WriteFile(path, original[:end], 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGraphSymbolCache(path, fingerprint, ix); err == nil {
			t.Fatalf("accepted truncated cache at %d", end)
		}
	}
	for pos := range original {
		mutated := bytes.Clone(original)
		mutated[pos] ^= 0x40
		if err := os.WriteFile(path, mutated, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGraphSymbolCache(path, fingerprint, ix); err == nil {
			t.Fatalf("accepted corrupt byte at %d", pos)
		}
	}
	// A valid checksum is insufficient when the symbol structure is malformed.
	for _, payload := range [][]byte{[]byte("SYM1\x00"), []byte("SYM2\x00\x00junk"), []byte("SYM2\x01\x00\xff\xff\xff\xff\x7f")} {
		data := bytes.Clone(original[:graphSymbolCacheHeader])
		binary.LittleEndian.PutUint64(data[40:48], uint64(len(payload)))
		sum := sha256.Sum256(payload)
		copy(data[48:], sum[:])
		data = append(data, payload...)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGraphSymbolCache(path, fingerprint, ix); err == nil {
			t.Fatal("accepted checksum-valid malformed payload")
		}
	}
}

func TestGraphSymbolCacheValidatesCorpusRanges(t *testing.T) {
	ix := symbolCacheSource()
	fingerprint := graphSymbolShardFingerprint(ix)
	for _, bad := range []struct {
		blob uint64
		end  int
	}{{99, 3}, {0, 999}} {
		cached := symbol.NewIndex()
		cached.Set(bad.blob, []symbol.Symbol{{Name: "Foo", Kind: symbol.Type, NameStart: 0, NameEnd: 3, BodyStart: 0, BodyEnd: bad.end}})
		path := filepath.Join(t.TempDir(), "cache")
		if err := writeGraphSymbolCache(path, fingerprint, cached); err != nil {
			t.Fatal(err)
		}
		if _, err := readGraphSymbolCache(path, fingerprint, ix); err == nil {
			t.Fatal("accepted unknown blob or out-of-bounds range")
		}
	}
}

func TestGraphSymbolFingerprintUsesLoadedContentAndMetadata(t *testing.T) {
	want := graphSymbolShardFingerprint(symbolCacheSource())
	changes := map[string]func(*index.Blob){
		"same length content and same SHA": func(b *index.Blob) { copy(b.Content[6:9], "Bar") },
		"first file language":              func(b *index.Blob) { b.Files[0].RelPath = "file.ts" },
		"file absence":                     func(b *index.Blob) { b.Files = nil },
		"repository":                       func(b *index.Blob) { b.Files[0].Repo = "other" },
		"absolute path":                    func(b *index.Blob) { b.Files[0].AbsPath = "/other/file.cs" },
		"SHA metadata":                     func(b *index.Blob) { b.SHA = "other" },
		"additional file":                  func(b *index.Blob) { b.Files = append(b.Files, index.FileRef{Repo: "vendored", RelPath: "file.cs"}) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			ix := symbolCacheSource()
			change(ix.Blob(0))
			if graphSymbolShardFingerprint(ix) == want {
				t.Fatal("fingerprint ignored changed extraction/catalog input")
			}
		})
	}
}

func TestGraphSymbolCacheWriteFailureKeepsServing(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "one.idx", map[string]string{"one.go": "package p\nfunc Run() { Run() }"})
	path := graphSymbolCachePath(filepath.Join(dir, "one.idx"))
	// Directory at the destination forces rename failure even as root.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		sc, err := OpenSymbols(dir)
		if err != nil {
			t.Fatal(err)
		}
		if sc.cacheHits != 0 || len(sc.References("Run")) != 2 {
			t.Fatal("failed cache write changed extraction or falsely reported hit")
		}
		if err := sc.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if files, _ := filepath.Glob(filepath.Join(dir, ".graph-symbols-*")); len(files) != 0 {
		t.Fatalf("temporary files survived failed publication: %v", files)
	}
}

func TestGraphSymbolCacheLegacyAndDedupedOrderedHits(t *testing.T) {
	for _, deduped := range []bool{false, true} {
		dir := t.TempDir()
		if deduped {
			dir = buildDedupedDir(t, symbolRepos)
		} else {
			buildShard(t, dir, "a.idx", map[string]string{"a.go": "package p\nfunc Run() { Charge() }"})
			buildShard(t, dir, "b.idx", map[string]string{"b.go": "package p\nfunc Charge() {}"})
			buildShard(t, dir, "c.idx", map[string]string{"none.txt": "nothing"})
		}
		cold, err := OpenSymbols(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cold.Close() })
		if cold.cacheHits != 0 {
			t.Fatal("unexpected first-open hit")
		}
		warm, err := OpenSymbols(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { warm.Close() })
		if warm.cacheHits != warm.NumShards() || !reflect.DeepEqual(cold.References("Charge"), warm.References("Charge")) {
			t.Fatal("warm cache changed shard ordering, identity, references, or missed empty shard")
		}
	}
}
