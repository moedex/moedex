package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/symbol"
)

// readMetaMap reads a sidecar's .meta as a raw map so a test can assert on field
// PRESENCE, not just value (the token meta must not carry `extractors` at all).
func readMetaMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path + ".meta")
	if err != nil {
		t.Fatalf("read %s.meta: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s.meta: %v", path, err)
	}
	return m
}

// writeMetaMap writes a mutated .meta back, for the tamper cases below.
func writeMetaMap(t *testing.T, path string, m map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(path+".meta", raw, 0o644); err != nil {
		t.Fatalf("write %s.meta: %v", path, err)
	}
}

// sidecarVersionDir builds a one-shard corpus and opens it once, so the token and
// symbol sidecars exist on disk. It returns the dir and the two sidecar paths.
func sidecarVersionDir(t *testing.T) (dir, tokenPath, symbolPath string) {
	t.Helper()
	dir = t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"billing/charge.cs": "namespace Billing\n{\n    public class Charger\n    {\n        public int Charge(int amount)\n        {\n            throw new ArgumentException(\"nope\");\n        }\n    }\n}\n",
		"util/strings.go":   "package util\n\nfunc TrimSpace(s string) string { return s }\n",
	})
	first, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank build: %v", err)
	}
	defer first.Close()
	if first.SymbolsFromCache() {
		t.Fatal("first open should BUILD the symbol sidecar")
	}
	return dir, filepath.Join(dir, "corpus-tokens.tki"), filepath.Join(dir, "corpus-symbols.sym")
}

// TestSidecarMetaRecordsExtractorsVersion pins what lands on disk: the symbol
// sidecar records the extractor version it was built with, and the token sidecar —
// which has no extractor dependency — omits the field entirely, so its .meta stays
// byte-compatible with one written before the field existed.
func TestSidecarMetaRecordsExtractorsVersion(t *testing.T) {
	_, tokenPath, symbolPath := sidecarVersionDir(t)

	symMeta := readMetaMap(t, symbolPath)
	got, ok := symMeta["extractors"]
	if !ok {
		t.Fatalf("symbol .meta has no extractors field: %v", symMeta)
	}
	if want := float64(symbol.ExtractorsVersion); got != want {
		t.Errorf("symbol .meta extractors = %v, want %v", got, want)
	}

	tokMeta := readMetaMap(t, tokenPath)
	if _, ok := tokMeta["extractors"]; ok {
		t.Errorf("token .meta must omit extractors (no extractor dependency): %v", tokMeta)
	}
	// Both still carry the corpus identity fields.
	for _, m := range []map[string]any{symMeta, tokMeta} {
		for _, k := range []string{"fingerprint", "num_blobs"} {
			if _, ok := m[k]; !ok {
				t.Errorf("meta %v missing %q", m, k)
			}
		}
	}
}

// TestSymbolSidecarInvalidatedByExtractorsVersion is the behavior this field
// exists for: the corpus is UNCHANGED (so the fingerprint still matches) but the
// extractors that produced the cache are not the current ones, so the symbol
// sidecar must be rebuilt rather than served. The token sidecar, unaffected by
// extractors, must still load from cache.
func TestSymbolSidecarInvalidatedByExtractorsVersion(t *testing.T) {
	dir, _, symbolPath := sidecarVersionDir(t)

	// Pretend the cache was written by a different extractor generation.
	m := readMetaMap(t, symbolPath)
	m["extractors"] = symbol.ExtractorsVersion + 1
	writeMetaMap(t, symbolPath, m)

	second, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank reload: %v", err)
	}
	defer second.Close()
	if second.SymbolsFromCache() {
		t.Error("symbol sidecar from a different extractors version must be REBUILT, not served")
	}
	if !second.TokensFromCache() {
		t.Error("token sidecar must still load from cache — it has no extractor dependency")
	}

	// The rebuild re-persists with the current version, so the next open is warm.
	if got := readMetaMap(t, symbolPath)["extractors"]; got != float64(symbol.ExtractorsVersion) {
		t.Errorf("rebuilt symbol .meta extractors = %v, want %v", got, symbol.ExtractorsVersion)
	}
	third, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank third: %v", err)
	}
	defer third.Close()
	if !third.SymbolsFromCache() {
		t.Error("after the rebuild re-persisted a current version, the symbol sidecar should load")
	}
}

// TestPreVersioningSymbolSidecarIsRebuilt covers the real-world case that
// motivated the field: sidecars already on disk from before it existed carry no
// version at all. Their absent field reads as 0, which never matches a current
// version, so they are rebuilt — which is exactly right, since they predate the
// extractor fixes.
func TestPreVersioningSymbolSidecarIsRebuilt(t *testing.T) {
	dir, _, symbolPath := sidecarVersionDir(t)

	m := readMetaMap(t, symbolPath)
	delete(m, "extractors") // a .meta as written before this field existed
	writeMetaMap(t, symbolPath, m)

	reopened, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank reload: %v", err)
	}
	defer reopened.Close()
	if reopened.SymbolsFromCache() {
		t.Error("a pre-versioning symbol sidecar must be rebuilt, not served")
	}
}

// TestBuildSidecarsWritesExtractorsVersion checks the OFFLINE path (the indexer's
// pre-warm) agrees with OpenRank: the sidecars it writes must be warm for a
// daemon boot, which requires the same version stamp.
func TestBuildSidecarsWritesExtractorsVersion(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"a.go": "package a\n\nfunc Alpha() {}\n",
	})
	tokenPath, symbolPath, err := BuildSidecars(dir)
	if err != nil {
		t.Fatalf("BuildSidecars: %v", err)
	}
	if got := readMetaMap(t, symbolPath)["extractors"]; got != float64(symbol.ExtractorsVersion) {
		t.Errorf("BuildSidecars symbol .meta extractors = %v, want %v", got, symbol.ExtractorsVersion)
	}
	if _, ok := readMetaMap(t, tokenPath)["extractors"]; ok {
		t.Error("BuildSidecars token .meta must omit extractors")
	}

	// The daemon must find both caches warm — that is the point of pre-warming.
	rc, err := OpenRank(context.Background(), dir, RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("OpenRank after BuildSidecars: %v", err)
	}
	defer rc.Close()
	if !rc.SymbolsFromCache() || !rc.TokensFromCache() {
		t.Errorf("pre-warmed sidecars not reused: symbols cached=%v tokens cached=%v",
			rc.SymbolsFromCache(), rc.TokensFromCache())
	}
}
