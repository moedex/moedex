package index

import (
	"reflect"
	"testing"

	"moedex/internal/trigram"
)

// TestIndexedGramDefaultAllTrue: a default New()+AddFile build is the
// all-trigram path — IndexedGram is universally true (even for trigrams the
// corpus never contained), and Selective() is false. This is the universal-true
// contract the query/search layers rely on to pay nothing on the default path.
func TestIndexedGramDefaultAllTrue(t *testing.T) {
	ix := New()
	ix.AddFile("r", "a.txt", "/abs/a.txt", "sha1", []byte("hello world"))

	if ix.Selective() {
		t.Fatalf("default build must not be Selective()")
	}
	// Present trigram, and a trigram the corpus never had: both read as indexed.
	for _, s := range []string{"hel", "rld", "zzz", "QQQ"} {
		if !ix.IndexedGram(tg(s)) {
			t.Errorf("IndexedGram(%q) = false, want true on the all-trigram build", s)
		}
	}
	if ix.SelectedGrams() != nil {
		t.Errorf("SelectedGrams() = non-nil, want nil for all-trigram build")
	}
}

// TestSelectiveDropsFrequentGram: with a near-universal gram and a rare gram,
// the selector drops the frequent one (IndexedGram false, no postings) and keeps
// the rare one (IndexedGram true, postings present and correct).
func TestSelectiveDropsFrequentGram(t *testing.T) {
	// "xyz" appears in all 4 blobs (df=4/4=1.0); "qrs" appears in 1 blob
	// (df=1/4=0.25). Threshold 0.5 keeps qrs, drops xyz.
	b := NewSelective(FrequencyThresholdSelector{MaxDocFraction: 0.5})
	b.AddFile("r", "0", "/abs/0", "s0", []byte("xyz aaa"))
	b.AddFile("r", "1", "/abs/1", "s1", []byte("xyz bbb"))
	b.AddFile("r", "2", "/abs/2", "s2", []byte("xyz ccc"))
	b.AddFile("r", "3", "/abs/3", "s3", []byte("xyz qrs")) // qrs only here
	ix := b.Finalize()

	if !ix.Selective() {
		t.Fatalf("selective build must report Selective()")
	}
	if ix.IndexedGram(tg("xyz")) {
		t.Errorf("frequent gram \"xyz\" (df=1.0) must be dropped, got IndexedGram true")
	}
	if got := ix.Postings(tg("xyz")); got != nil {
		t.Errorf("dropped gram must have no postings, got %v", got)
	}
	if !ix.IndexedGram(tg("qrs")) {
		t.Errorf("rare gram \"qrs\" (df=0.25) must be kept, got IndexedGram false")
	}
	if got := ix.Postings(tg("qrs")); !reflect.DeepEqual(got, []Posting{{Blob: 3, Offset: 4}}) {
		t.Errorf("kept gram \"qrs\" postings = %v, want [{3 4}]", got)
	}
}

// TestSelectiveRareGramPostingsExact: a kept gram's postings are byte-for-byte
// identical to what the all-trigram builder produces. Selection changes
// membership, never the postings of a kept gram.
func TestSelectiveRareGramPostingsExact(t *testing.T) {
	files := []struct{ sha, content string }{
		{"s0", "abcabc xyz\nrare token here"},
		{"s1", "xyz xyz xyz only frequent"},
		{"s2", "another xyz line abc"},
	}
	eager := New()
	for i, f := range files {
		eager.AddFile("r", itoa(i), "/abs/"+itoa(i), f.sha, []byte(f.content))
	}

	b := NewSelective(FrequencyThresholdSelector{MaxDocFraction: 0.5})
	for i, f := range files {
		b.AddFile("r", itoa(i), "/abs/"+itoa(i), f.sha, []byte(f.content))
	}
	sel := b.Finalize()

	// "abc" appears in blobs 0 and 2 (df=2/3=0.67 > 0.5) -> dropped.
	// "rar" appears only in blob 0 (df=1/3=0.33) -> kept; its postings must match.
	for _, kept := range []string{"rar", "are", "tok", "oke", "ken"} {
		want := eager.Postings(tg(kept))
		got := sel.Postings(tg(kept))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("kept gram %q postings differ:\n got=%v\nwant=%v", kept, got, want)
		}
		if !sel.IndexedGram(tg(kept)) {
			t.Errorf("gram %q should be kept (rare)", kept)
		}
	}
}

// TestSelectiveContentUnchanged: blob content, SHAs, FileRefs, and dedup are
// identical between selective and all-trigram builds. Selection only touches the
// posting/membership layer.
func TestSelectiveContentUnchanged(t *testing.T) {
	build := func(b interface {
		AddFile(repo, rel, abs, sha string, content []byte)
	}) {
		b.AddFile("r", "a.txt", "/abs/a.txt", "dup", []byte("shared content xyz"))
		b.AddFile("r", "b.txt", "/abs/b.txt", "dup", []byte("shared content xyz")) // dedup
		b.AddFile("r", "c.txt", "/abs/c.txt", "uniq", []byte("unique line abc"))
	}
	eager := New()
	build(eager)
	bld := NewSelective(FrequencyThresholdSelector{MaxDocFraction: 0.5})
	build(bld)
	sel := bld.Finalize()

	if eager.NumBlobs() != sel.NumBlobs() {
		t.Fatalf("NumBlobs differ: eager=%d sel=%d", eager.NumBlobs(), sel.NumBlobs())
	}
	for id := uint64(0); id < uint64(eager.NumBlobs()); id++ {
		eb, sbl := eager.Blob(id), sel.Blob(id)
		if eb.SHA != sbl.SHA {
			t.Errorf("blob %d SHA differ: %q vs %q", id, eb.SHA, sbl.SHA)
		}
		if !reflect.DeepEqual(eb.Content, sbl.Content) {
			t.Errorf("blob %d content differ", id)
		}
		if !reflect.DeepEqual(eb.Files, sbl.Files) {
			t.Errorf("blob %d files differ: %v vs %v", id, eb.Files, sbl.Files)
		}
	}
	// The dedup blob must carry both files in both builds.
	if got := len(sel.Blob(0).Files); got != 2 {
		t.Errorf("dedup blob should have 2 files, got %d", got)
	}
}

// TestSelectiveNilSelectorKeepsAll: a nil selector keeps every gram and records
// the index as all-indexed (selected==nil), so it behaves byte-identically to a
// default build — same trigram set, same postings, universal IndexedGram.
func TestSelectiveNilSelectorKeepsAll(t *testing.T) {
	content := []byte("the quick brown fox jumps")
	eager := New()
	eager.AddFile("r", "a", "/abs/a", "s", content)
	bld := NewSelective(nil)
	bld.AddFile("r", "a", "/abs/a", "s", content)
	sel := bld.Finalize()

	if sel.Selective() {
		t.Errorf("nil-selector build must record all-indexed (not Selective)")
	}
	if sel.SelectedGrams() != nil {
		t.Errorf("nil-selector build must have selected==nil")
	}
	// Same materialized trigrams and postings as the eager build.
	for _, tt := range eager.Trigrams() {
		if !reflect.DeepEqual(eager.Postings(tt), sel.Postings(tt)) {
			t.Errorf("postings for %q differ between eager and nil-selector build", tt.String())
		}
	}
	if len(eager.Trigrams()) != len(sel.Trigrams()) {
		t.Errorf("trigram count differs: eager=%d sel=%d", len(eager.Trigrams()), len(sel.Trigrams()))
	}
}

// TestSelectiveFracOneKeepsEverything: MaxDocFraction=1.0 keeps every gram (every
// df <= numBlobs), but the index is still recorded Selective with an explicit
// keep-set equal to all materialized grams.
func TestSelectiveFracOneKeepsEverything(t *testing.T) {
	b := NewSelective(FrequencyThresholdSelector{MaxDocFraction: 1.0})
	b.AddFile("r", "a", "/abs/a", "s0", []byte("alpha beta gamma"))
	b.AddFile("r", "b", "/abs/b", "s1", []byte("beta gamma delta"))
	ix := b.Finalize()

	if !ix.Selective() {
		t.Fatalf("frac=1.0 still uses the selective path (explicit keep-set)")
	}
	for _, tt := range ix.Trigrams() {
		if !ix.IndexedGram(tt) {
			t.Errorf("frac=1.0 must keep every occurring gram; %q reported not indexed", tt.String())
		}
	}
}

// TestFrequencyThresholdSelectorBoundary checks the keep predicate at the exact
// df == frac*numBlobs boundary (inclusive keep) and clamping of out-of-range
// fractions.
func TestFrequencyThresholdSelectorBoundary(t *testing.T) {
	df := map[trigram.Trigram]int{
		tg("aaa"): 5,  // df=0.5 at numBlobs=10
		tg("bbb"): 6,  // df=0.6
		tg("ccc"): 10, // df=1.0
		tg("ddd"): 1,  // df=0.1
	}
	keep := FrequencyThresholdSelector{MaxDocFraction: 0.5}.Select(df, 10)
	want := map[trigram.Trigram]bool{tg("aaa"): true, tg("ddd"): true} // df<=5
	for g := range df {
		_, kept := keep[g]
		if kept != want[g] {
			t.Errorf("gram %q: kept=%v, want %v (df=%d, thr=5)", g.String(), kept, want[g], df[g])
		}
	}
	// frac>1 clamps to 1 (keep all); frac<0 clamps to 0 (keep only df==0, none here).
	if got := len(FrequencyThresholdSelector{MaxDocFraction: 2.0}.Select(df, 10)); got != len(df) {
		t.Errorf("frac=2.0 should clamp to keep-all (%d), got %d", len(df), got)
	}
	if got := len(FrequencyThresholdSelector{MaxDocFraction: -1.0}.Select(df, 10)); got != 0 {
		t.Errorf("frac=-1.0 should clamp to keep-none, got %d", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
