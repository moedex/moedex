package graphserve

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"moedex/internal/graph/diskgraph"
	"moedex/internal/symbol"
)

func catalogCacheFixture() *graphSnapshot {
	a := diskgraph.Key{BlobSHA: "abcdef", SymbolOffset: 4}
	b := diskgraph.Key{BlobSHA: "abcdef", SymbolOffset: 9}
	c := diskgraph.Key{BlobSHA: "other", SymbolOffset: 0}
	return &graphSnapshot{symbols: &SymbolCorpus{corpus: symbol.NewCorpus()}, nodes: map[diskgraph.Key]nodeMetadata{
		a: {Symbol: "Thing", Kind: "Type", Locations: []GraphLocation{{Repo: "r", Path: "x.cs", AbsPath: "/r/x.cs", Line: 2, BlobSHA: a.BlobSHA}, {Repo: "s", Path: "y.cs", AbsPath: "/s/y.cs", Line: 2, BlobSHA: a.BlobSHA}}},
		b: {Kind: "Occurrence", Locations: []GraphLocation{}}, c: {Kind: "File", Locations: nil},
	}, bySymbol: map[string][]diskgraph.Key{"Thing": {a}, "alias": {b, a}, "nil": nil, "empty": {}}, byPath: map[string][]locatedNode{"r/x.cs": {{line: 2, key: a}, {line: 3, key: b}}, "s/y.cs": {{line: 2, key: a}}, "nil": nil, "empty": {}}}
}

func TestCatalogCacheRoundTrip(t *testing.T) {
	s := catalogCacheFixture()
	identity := sha256.Sum256([]byte("graph+fullsymbolidentity"))
	path := filepath.Join(t.TempDir(), "cache")
	if err := s.writeCatalogCache(path, identity); err != nil {
		t.Fatal(err)
	}
	got := &graphSnapshot{symbols: &SymbolCorpus{corpus: symbol.NewCorpus()}}
	if err := got.readCatalogCache(path, identity); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.nodes, got.nodes) || !reflect.DeepEqual(s.bySymbol, got.bySymbol) || !reflect.DeepEqual(s.byPath, got.byPath) {
		t.Fatal("catalog values, order, or nil slices changed")
	}
	different := identity
	different[0] ^= 1
	if err := got.readCatalogCache(path, different); err == nil {
		t.Fatal("accepted stale identity")
	}
}

func TestCatalogCacheCorruptionNeverPublishes(t *testing.T) {
	s := catalogCacheFixture()
	identity := sha256.Sum256([]byte("identity"))
	path := filepath.Join(t.TempDir(), "cache")
	if err := s.writeCatalogCache(path, identity); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	check := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		got := catalogCacheFixture()
		before := got.nodes
		if err := got.readCatalogCache(path, identity); err == nil {
			t.Fatal("accepted malformed cache")
		}
		if !reflect.DeepEqual(got.nodes, before) {
			t.Fatal("published partial catalog")
		}
	}
	for i := 0; i < len(data); i++ {
		check(data[:i])
	}
	for _, i := range []int{0, 8, 40, catalogCacheHeader, len(data) - 1} {
		bad := append([]byte(nil), data...)
		bad[i] ^= 1
		check(bad)
	}
	// A correctly checksummed malicious count cannot trigger large allocation.
	payload := make([]byte, 10)
	n := binary.PutUvarint(payload, ^uint64(0))
	payload = payload[:n]
	bad := append(append([]byte(nil), data[:catalogCacheHeader]...), payload...)
	digest := sha256.Sum256(payload)
	copy(bad[40:72], digest[:])
	check(bad)
	// Trailing junk with a valid checksum is still rejected.
	bad = append(append([]byte(nil), data...), 0)
	digest = sha256.Sum256(bad[catalogCacheHeader:])
	copy(bad[40:72], digest[:])
	check(bad)
}

func TestCatalogCacheAtomicConcurrentWriters(t *testing.T) {
	s := catalogCacheFixture()
	identity := sha256.Sum256([]byte("identity"))
	path := filepath.Join(t.TempDir(), "cache")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.writeCatalogCache(path, identity); err != nil {
				t.Error(err)
			}
			got := &graphSnapshot{symbols: &SymbolCorpus{corpus: symbol.NewCorpus()}}
			if err := got.readCatalogCache(path, identity); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".graph-catalog-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files: %v %v", leftovers, err)
	}
}

func TestCatalogCacheUnavailableAndNonRegular(t *testing.T) {
	s := catalogCacheFixture()
	identity := sha256.Sum256([]byte("identity"))
	dir := t.TempDir()
	if err := s.writeCatalogCache(filepath.Join(dir, "absent", "cache"), identity); err == nil {
		t.Fatal("unexpected success")
	}
	if err := s.readCatalogCache(dir, identity); err == nil {
		t.Fatal("accepted directory")
	}
	target := filepath.Join(dir, "target")
	if err := s.writeCatalogCache(target, identity); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := s.readCatalogCache(link, identity); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestCatalogCacheIdentityBindsGraphAndOrderedInputs(t *testing.T) {
	s := catalogCacheFixture()
	s.buildID = "same-generation-graph-a"
	// Synthetic shards need no content: OpenSymbols supplies independently
	// verified content/file/extractor fingerprints, not filesystem timestamps.
	s.symbols.idxs = append(s.symbols.idxs, nil, nil)
	s.symbols.cacheFingerprints = [][32]byte{sha256.Sum256([]byte("shard-a")), sha256.Sum256([]byte("shard-b"))}
	before, ok := s.catalogCacheIdentity()
	if !ok {
		t.Fatal("missing identity")
	}
	s.buildID = "same-generation-graph-b"
	after, _ := s.catalogCacheIdentity()
	if before == after {
		t.Fatal("graph bytes not bound")
	}
	s.buildID = "same-generation-graph-a"
	s.symbols.cacheFingerprints[0], s.symbols.cacheFingerprints[1] = s.symbols.cacheFingerprints[1], s.symbols.cacheFingerprints[0]
	after, _ = s.catalogCacheIdentity()
	if before == after {
		t.Fatal("shard order not bound")
	}
	s.symbols.cacheFingerprints = nil
	if _, ok := s.catalogCacheIdentity(); ok {
		t.Fatal("missing corpus identity accepted")
	}
}
