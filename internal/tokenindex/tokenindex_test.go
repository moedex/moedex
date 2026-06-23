package tokenindex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/index"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "  \t\n", nil},
		{"punctuation only", ".,;:!", nil},
		{"single word", "client", []string{"client"}},
		{"uppercases fold", "CLIENT", []string{"client"}},
		{"split on punctuation", "ssl_api.client", []string{"ssl", "api", "client"}},
		{"split on slashes/dashes", "ssl-api/client", []string{"ssl", "api", "client"}},
		// Two subtokens [foo,bar]: windows by start asc, then length asc:
		// start0 -> foo, foobar ; start1 -> bar.
		{"camelCase", "fooBar", []string{"foo", "foobar", "bar"}},
		// Three subtokens [ssl,api,client]: start0 -> ssl,sslapi,sslapiclient;
		// start1 -> api,apiclient; start2 -> client.
		{"PascalCase three", "SslApiClient", []string{"ssl", "sslapi", "sslapiclient", "api", "apiclient", "client"}},
		{"acronym then word", "HTTPServer", []string{"http", "httpserver", "server"}},
		{"acronym mid", "parseHTTPRequest", []string{"parse", "parsehttp", "parsehttprequest", "http", "httprequest", "request"}},
		{"digits stay attached", "sha256sum", []string{"sha256sum"}},
		{"version token", "v2", []string{"v2"}},
		{"base64 single word", "base64", []string{"base64"}},
		{"unicode letters", "naïveapproach", []string{"naïveapproach"}},
		{"unicode word boundary", "café_müller", []string{"café", "müller"}},
		// QuickBrown is one run -> joins by start then len; the and fox are plain.
		{"mixed sentence", "the QuickBrown fox", []string{"the", "quick", "quickbrown", "brown", "fox"}},
		// The recall-fix case: the intermediate join "reissue" is now emitted.
		// Order: start asc, then length asc.
		{"intermediate join", "ReIssueCertificate", []string{
			"re", "reissue", "reissuecertificate",
			"issue", "issuecertificate",
			"certificate",
		}},
		// Window-length cap: 5 subtokens [ab,cd,ef,gh,ij], windows up to length 4
		// per start, plus the full run appended once at the end. The would-be
		// length-5 window from start0 is skipped by the cap, then re-added as the
		// full run. Order is start asc, then length asc.
		{"window cap len4", "AbCdEfGhIj", []string{
			"ab", "abcd", "abcdef", "abcdefgh", // start0, len 1..4 (len5 capped)
			"cd", "cdef", "cdefgh", "cdefghij", // start1, len 1..4
			"ef", "efgh", "efghij", // start2, len 1..3 (run ends)
			"gh", "ghij", // start3, len 1..2
			"ij",          // start4, len 1
			"abcdefghij",  // full run (len 5), appended after the cap
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokenize([]byte(tc.in))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Tokenize(%q)\n  got  %#v\n  want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// contains reports whether s appears in toks.
func contains(toks []string, s string) bool {
	for _, t := range toks {
		if t == s {
			return true
		}
	}
	return false
}

// TestSymmetricJoinMatch is the core recall-fix assertion: a multi-word query
// must meet a single camel/Pascal identifier because Tokenize emits the
// contiguous joins on BOTH sides (it is the single source of truth).
func TestSymmetricJoinMatch(t *testing.T) {
	// ReIssueCertificate must emit the intermediate join "reissue" and the
	// length-1 "certificate".
	idTok := Tokenize([]byte("ReIssueCertificate"))
	for _, want := range []string{"reissue", "certificate"} {
		if !contains(idTok, want) {
			t.Errorf("Tokenize(ReIssueCertificate)=%v missing %q", idTok, want)
		}
	}
	// Query "reissue certificate" tokenizes to two plain words; both must be in
	// the identifier's token set (set intersection includes both).
	q := Tokenize([]byte("reissue certificate"))
	for _, term := range q {
		if !contains(idTok, term) {
			t.Errorf("query term %q from %v not matched by identifier tokens %v", term, q, idTok)
		}
	}
	if !contains(q, "reissue") || !contains(q, "certificate") {
		t.Fatalf("query did not tokenize as expected: %v", q)
	}

	// Second corpus identifier: SslOrderRefunded met by query "order refunded".
	id2 := Tokenize([]byte("SslOrderRefunded"))
	q2 := Tokenize([]byte("order refunded"))
	for _, term := range q2 {
		if !contains(id2, term) {
			t.Errorf("query term %q from %v not matched by %v", term, q2, id2)
		}
	}
	// And the join "orderrefunded" is queryable too.
	if !contains(id2, "orderrefunded") {
		t.Errorf("Tokenize(SslOrderRefunded)=%v missing join %q", id2, "orderrefunded")
	}
}

// buildCorpus constructs a small index with explicit contents.
func buildCorpus(t *testing.T, contents []string) *index.Index {
	t.Helper()
	ix := index.New()
	for i, c := range contents {
		ix.AddFile("repo", "f"+string(rune('0'+i)), "/abs", "sha"+string(rune('0'+i)), []byte(c))
	}
	return ix
}

func TestBuildStatsByHand(t *testing.T) {
	// Corpus chosen so every statistic is hand-computable.
	// blob 0: "cat dog cat"      -> tokens: cat, dog, cat               (len 3)
	// blob 1: "dog bird"         -> tokens: dog, bird                   (len 2)
	// blob 2: "fooBar cat"       -> tokens: foo, bar, foobar, cat       (len 4)
	ix := buildCorpus(t, []string{
		"cat dog cat",
		"dog bird",
		"fooBar cat",
	})
	ti := Build(ix)

	if ti.NumDocs() != 3 {
		t.Errorf("NumDocs = %d, want 3", ti.NumDocs())
	}
	// doc lengths
	if ti.DocLen(0) != 3 {
		t.Errorf("DocLen(0) = %d, want 3", ti.DocLen(0))
	}
	if ti.DocLen(1) != 2 {
		t.Errorf("DocLen(1) = %d, want 2", ti.DocLen(1))
	}
	if ti.DocLen(2) != 4 {
		t.Errorf("DocLen(2) = %d, want 4", ti.DocLen(2))
	}
	// avg = (3+2+4)/3 = 3.0
	if ti.AvgDocLen() != 3.0 {
		t.Errorf("AvgDocLen = %v, want 3.0", ti.AvgDocLen())
	}

	// document frequencies
	// cat: blobs 0 and 2 -> 2
	if got := ti.DocFreq("cat"); got != 2 {
		t.Errorf("DocFreq(cat) = %d, want 2", got)
	}
	// dog: blobs 0 and 1 -> 2
	if got := ti.DocFreq("dog"); got != 2 {
		t.Errorf("DocFreq(dog) = %d, want 2", got)
	}
	// bird: blob 1 only -> 1
	if got := ti.DocFreq("bird"); got != 1 {
		t.Errorf("DocFreq(bird) = %d, want 1", got)
	}
	// foobar/foo/bar: blob 2 only -> 1 each
	for _, term := range []string{"foo", "bar", "foobar"} {
		if got := ti.DocFreq(term); got != 1 {
			t.Errorf("DocFreq(%s) = %d, want 1", term, got)
		}
	}

	// term frequencies
	// cat in blob 0 appears twice
	if got := ti.TermFreq("cat", 0); got != 2 {
		t.Errorf("TermFreq(cat,0) = %d, want 2", got)
	}
	if got := ti.TermFreq("cat", 2); got != 1 {
		t.Errorf("TermFreq(cat,2) = %d, want 1", got)
	}
	if got := ti.TermFreq("dog", 0); got != 1 {
		t.Errorf("TermFreq(dog,0) = %d, want 1", got)
	}
	// fooBar contributes tf=1 to each of foo, bar, foobar in blob 2
	for _, term := range []string{"foo", "bar", "foobar"} {
		if got := ti.TermFreq(term, 2); got != 1 {
			t.Errorf("TermFreq(%s,2) = %d, want 1", term, got)
		}
	}
}

func TestEdgeCases(t *testing.T) {
	// Single empty blob.
	ix := buildCorpus(t, []string{""})
	ti := Build(ix)
	if ti.NumDocs() != 1 {
		t.Errorf("NumDocs = %d, want 1", ti.NumDocs())
	}
	if ti.DocLen(0) != 0 {
		t.Errorf("DocLen(0) = %d, want 0", ti.DocLen(0))
	}
	if ti.AvgDocLen() != 0 {
		t.Errorf("AvgDocLen = %v, want 0", ti.AvgDocLen())
	}

	// Absent term returns zero from both accessors.
	if got := ti.DocFreq("nope"); got != 0 {
		t.Errorf("DocFreq(nope) = %d, want 0", got)
	}
	if got := ti.TermFreq("nope", 0); got != 0 {
		t.Errorf("TermFreq(nope,0) = %d, want 0", got)
	}
	// Unknown blob.
	if got := ti.DocLen(999); got != 0 {
		t.Errorf("DocLen(999) = %d, want 0", got)
	}

	// Single-doc corpus with content.
	ix2 := buildCorpus(t, []string{"alpha beta alpha"})
	ti2 := Build(ix2)
	if ti2.NumDocs() != 1 {
		t.Errorf("NumDocs = %d, want 1", ti2.NumDocs())
	}
	if ti2.AvgDocLen() != 3.0 {
		t.Errorf("AvgDocLen = %v, want 3.0", ti2.AvgDocLen())
	}
	if got := ti2.DocFreq("alpha"); got != 1 {
		t.Errorf("DocFreq(alpha) = %d, want 1", got)
	}
	if got := ti2.TermFreq("alpha", 0); got != 2 {
		t.Errorf("TermFreq(alpha,0) = %d, want 2", got)
	}
}

func TestEmptyCorpus(t *testing.T) {
	ti := Build(index.New())
	if ti.NumDocs() != 0 {
		t.Errorf("NumDocs = %d, want 0", ti.NumDocs())
	}
	if ti.AvgDocLen() != 0 {
		t.Errorf("AvgDocLen = %v, want 0", ti.AvgDocLen())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ix := buildCorpus(t, []string{
		"cat dog cat",
		"dog bird",
		"fooBar cat",
		"", // empty blob included
	})
	orig := Build(ix)

	path := filepath.Join(t.TempDir(), "tokens.tki")
	if err := Save(orig, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Scalar accessors.
	if orig.NumDocs() != loaded.NumDocs() {
		t.Errorf("NumDocs: orig %d loaded %d", orig.NumDocs(), loaded.NumDocs())
	}
	if orig.AvgDocLen() != loaded.AvgDocLen() {
		t.Errorf("AvgDocLen: orig %v loaded %v", orig.AvgDocLen(), loaded.AvgDocLen())
	}

	// Every doc length.
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		if orig.DocLen(id) != loaded.DocLen(id) {
			t.Errorf("DocLen(%d): orig %d loaded %d", id, orig.DocLen(id), loaded.DocLen(id))
		}
	}

	// Every term: df and per-blob tf.
	for term, post := range orig.postings {
		if orig.DocFreq(term) != loaded.DocFreq(term) {
			t.Errorf("DocFreq(%s): orig %d loaded %d", term, orig.DocFreq(term), loaded.DocFreq(term))
		}
		for id := range post {
			if orig.TermFreq(term, id) != loaded.TermFreq(term, id) {
				t.Errorf("TermFreq(%s,%d): orig %d loaded %d",
					term, id, orig.TermFreq(term, id), loaded.TermFreq(term, id))
			}
		}
	}

	// Loaded index reports the same term set size.
	if len(orig.postings) != len(loaded.postings) {
		t.Errorf("term count: orig %d loaded %d", len(orig.postings), len(loaded.postings))
	}
}

func TestLoadBadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.tki")
	if err := os.WriteFile(path, []byte("XXXXgarbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error loading file with bad magic, got nil")
	}
}
