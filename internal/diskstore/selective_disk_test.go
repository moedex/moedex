package diskstore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"moedex/internal/index"
	"moedex/internal/search"
	"moedex/internal/trigram"
)

func dtg(s string) trigram.Trigram { return trigram.Trigram{s[0], s[1], s[2]} }

func buildSelective(frac float64, contents ...string) *index.Index {
	b := index.NewSelective(index.FrequencyThresholdSelector{MaxDocFraction: frac})
	for i, c := range contents {
		sha := gitBlobSHA([]byte(c))
		b.AddFile("r", string(rune('a'+i)), "/abs/"+string(rune('a'+i)), sha, []byte(c))
	}
	return b.Finalize()
}

// TestSaveLoadRoundTripSelection: a selective index Save -> Load preserves the
// IndexedGram answers AND the postings of kept grams. The MOEDEX04 magic/header
// must be written and parsed.
func TestSaveLoadRoundTripSelection(t *testing.T) {
	orig := buildSelective(0.5, "xyz alpha", "xyz beta", "xyz rare gamma")
	if !orig.Selective() {
		t.Fatalf("precondition: index must be selective")
	}
	path := filepath.Join(t.TempDir(), "sel.idx")
	if err := Save(orig, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Header must be MOEDEX04.
	data, _ := os.ReadFile(path)
	if string(data[0:8]) != "MOEDEX04" {
		t.Fatalf("selective shard magic = %q, want MOEDEX04", data[0:8])
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Selective() {
		t.Errorf("loaded index lost its selective flag")
	}
	// IndexedGram must agree for kept and dropped grams.
	assertSameMembership(t, orig, loaded)
	// Kept gram postings must round-trip exactly.
	for _, tt := range orig.Trigrams() {
		if !reflect.DeepEqual(orig.Postings(tt), loaded.Postings(tt)) {
			t.Errorf("postings for %q differ after round-trip", tt.String())
		}
	}
}

// TestLoadMmapRoundTripSelection: the mmap loader must reconstruct IndexedGram
// from the SELECTION section and serve kept-gram postings off the mapping.
func TestLoadMmapRoundTripSelection(t *testing.T) {
	orig := buildSelective(0.5, "xyz alpha", "xyz beta", "xyz rare gamma")
	path := filepath.Join(t.TempDir(), "sel.idx")
	if err := Save(orig, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, closer, err := LoadMmap(path)
	if err != nil {
		t.Fatalf("LoadMmap: %v", err)
	}
	defer closer.Close()

	if !loaded.Selective() {
		t.Errorf("mmap-loaded selective index lost its selective flag")
	}
	assertSameMembership(t, orig, loaded)
	for _, tt := range orig.Trigrams() {
		if !reflect.DeepEqual(orig.Postings(tt), loaded.Postings(tt)) {
			t.Errorf("mmap postings for %q differ", tt.String())
		}
	}
}

// TestSelectiveDiskQueryParity: querying the reloaded selective shard (both Load
// and LoadMmap) returns the same matches as querying the in-memory selective
// index. End-to-end parity through persistence.
func TestSelectiveDiskQueryParity(t *testing.T) {
	contents := []string{
		"package main\nfunc Handler() {}\nfoobar baz\n",
		"xyz xyz xyz frequent everywhere\n",
		"another foobar line\nxyz again\n",
	}
	orig := buildSelective(0.5, contents...)
	path := filepath.Join(t.TempDir(), "sel.idx")
	if err := Save(orig, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mm, closer, err := LoadMmap(path)
	if err != nil {
		t.Fatalf("LoadMmap: %v", err)
	}
	defer closer.Close()

	for _, pat := range []string{"foobar", "Handler", "xyz", "func", ".", "(?i)foobar"} {
		want := regexLocs(t, orig, pat)
		if got := regexLocs(t, loaded, pat); !reflect.DeepEqual(got, want) {
			t.Errorf("Load query %q: got=%v want=%v", pat, got, want)
		}
		if got := regexLocs(t, mm, pat); !reflect.DeepEqual(got, want) {
			t.Errorf("LoadMmap query %q: got=%v want=%v", pat, got, want)
		}
	}
}

// TestLoadLegacyShardAllIndexed: a default (all-trigram) build still writes
// MOEDEX03 and loads as all-indexed — IndexedGram universally true, Selective()
// false. This is the back-compat guarantee for existing servable shard dirs.
func TestLoadLegacyShardAllIndexed(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "a", "/abs/a", gitBlobSHA([]byte("hello world abc")), []byte("hello world abc"))
	path := filepath.Join(t.TempDir(), "legacy.idx")
	if err := Save(ix, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// A default build MUST write the legacy MOEDEX03 magic (no format churn).
	data, _ := os.ReadFile(path)
	if string(data[0:8]) != "MOEDEX03" {
		t.Fatalf("default build magic = %q, want MOEDEX03 (no format change on default path)", data[0:8])
	}

	for _, loadFn := range []struct {
		name string
		load func() (*index.Index, func())
	}{
		{"Load", func() (*index.Index, func()) {
			ld, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			return ld, func() {}
		}},
		{"LoadMmap", func() (*index.Index, func()) {
			ld, c, err := LoadMmap(path)
			if err != nil {
				t.Fatalf("LoadMmap: %v", err)
			}
			return ld, func() { c.Close() }
		}},
	} {
		ld, done := loadFn.load()
		if ld.Selective() {
			t.Errorf("%s: legacy shard must NOT be Selective()", loadFn.name)
		}
		for _, s := range []string{"hel", "abc", "zzz", "QQQ"} {
			if !ld.IndexedGram(dtg(s)) {
				t.Errorf("%s: legacy shard IndexedGram(%q) = false, want true (all-indexed)", loadFn.name, s)
			}
		}
		done()
	}
}

// TestSelectiveDroppedGramHasNoPostingsOnDisk: a dropped frequent gram must read
// as not-indexed after reload, with no postings — so a query treats it as
// force-scan, never as zero.
func TestSelectiveDroppedGramHasNoPostingsOnDisk(t *testing.T) {
	orig := buildSelective(0.4, "xyz a", "xyz b", "xyz c", "xyz d") // xyz df=1.0 -> dropped
	if orig.IndexedGram(dtg("xyz")) {
		t.Fatalf("precondition: xyz should be dropped")
	}
	path := filepath.Join(t.TempDir(), "sel.idx")
	if err := Save(orig, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.IndexedGram(dtg("xyz")) {
		t.Errorf("reloaded dropped gram xyz must read as not-indexed")
	}
	if got := loaded.Postings(dtg("xyz")); got != nil {
		t.Errorf("reloaded dropped gram must have no postings, got %v", got)
	}
}

func assertSameMembership(t *testing.T, a, b *index.Index) {
	t.Helper()
	// Check every gram in a's keep-set plus a few never-present grams.
	for g := range a.SelectedGrams() {
		if a.IndexedGram(g) != b.IndexedGram(g) {
			t.Errorf("membership for %q differs: a=%v b=%v", g.String(), a.IndexedGram(g), b.IndexedGram(g))
		}
	}
	for _, s := range []string{"zzz", "QQQ", "###"} {
		if a.IndexedGram(dtg(s)) != b.IndexedGram(dtg(s)) {
			t.Errorf("membership for absent %q differs: a=%v b=%v", s, a.IndexedGram(dtg(s)), b.IndexedGram(dtg(s)))
		}
	}
}

func regexLocs(t *testing.T, ix *index.Index, pat string) []string {
	t.Helper()
	ms, err := search.Regex(context.Background(), ix, pat)
	if err != nil {
		t.Fatalf("Regex(%q): %v", pat, err)
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.AbsPath+":"+itoa(m.Line))
	}
	sort.Strings(out)
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
