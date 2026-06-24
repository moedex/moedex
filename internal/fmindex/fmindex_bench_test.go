package fmindex

import (
	"math/rand"
	"testing"
	"time"

	"moedex/internal/index"
)

// codeFixture builds a deterministic, code-like byte corpus of roughly the
// requested size by repeating a small set of realistic Go-ish lines with light
// variation. Deterministic (seeded) so size/latency numbers are reproducible.
// It contains no 0x00, matching the FM-index precondition and moedex's
// NUL-free indexed content.
func codeFixture(approxBytes int) []byte {
	lines := []string{
		"func (ix *Index) Postings(t trigram.Trigram) []Posting {\n",
		"\tif ix.pp != nil {\n",
		"\t\treturn ix.pp.Postings(t)\n",
		"\t}\n",
		"\treturn ix.postings[t]\n",
		"}\n",
		"// Posting records that a trigram begins at Offset within Blob.\n",
		"type Posting struct {\n",
		"\tBlob   uint64\n",
		"\tOffset int\n",
		"}\n",
		"import (\n\t\"bytes\"\n\t\"sort\"\n)\n",
		"for i := 0; i+trigram.N <= len(buf); i++ {\n",
	}
	rng := rand.New(rand.NewSource(20260624))
	var buf []byte
	for len(buf) < approxBytes {
		buf = append(buf, lines[rng.Intn(len(lines))]...)
	}
	return buf
}

// TestSizeReport measures, on one deterministic fixture, the FM-index in-memory
// size, the trigram-index on-disk posting size (the exact accounting
// diskstore.Save uses), and the raw corpus size — and logs them as multiples of
// the corpus. It asserts nothing brittle to fixture entropy: this is an honest
// measurement, framed per research/fm-index-cold-tier.md (do NOT claim FM <
// trigram; ratios are corpus-dependent). The only assertions are sanity
// (sizes positive, FM-index correct on a sampled literal) so the numbers can't
// be silently meaningless.
func TestSizeReport(t *testing.T) {
	corpus := codeFixture(1 << 20) // ~1 MiB
	rawSize := len(corpus)

	fm, err := Build(corpus)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	fmSize := fm.approxMemBytes()

	// Trigram index over the SAME bytes. Posting-section size uses diskstore's
	// exact per-trigram accounting: 3 (trigram) + 8 (encLen) + len(enc).
	ix := index.New()
	ix.AddFile("repo", "fixture.go", "/fixture.go", "deadbeef", corpus)
	var postingBytes, postingSectionBytes int
	for _, tg := range ix.Trigrams() {
		enc := index.EncodePostings(ix.Postings(tg))
		postingBytes += len(enc)
		postingSectionBytes += 3 + 8 + len(enc)
	}

	// Sanity: FM-index actually works on this fixture (a literal known present).
	probe := []byte("Posting")
	if fm.Count(probe) == 0 {
		t.Fatalf("fixture sanity: Count(%q) == 0, expected > 0", probe)
	}
	if len(fm.Locate(probe)) != fm.Count(probe) {
		t.Fatalf("fixture sanity: Locate/Count disagree for %q", probe)
	}
	if fmSize <= 0 || postingBytes <= 0 {
		t.Fatalf("sizes must be positive: fm=%d posting=%d", fmSize, postingBytes)
	}

	// Coarser-checkpoint FM-index over the same bytes, to show the rank table is
	// a tunable knob, not an inherent cost — the BWT itself is ~1x corpus.
	fmCoarse, err := BuildWithParams(corpus, 4096, DefaultSASample)
	if err != nil {
		t.Fatalf("BuildWithParams(coarse) error: %v", err)
	}

	t.Logf("=== SIZE REPORT (one ~1 MiB code-like fixture; entropy-dependent, NOT a portable constant) ===")
	t.Logf("raw corpus:                          %9d bytes  (1.00x)", rawSize)
	t.Logf("FM-index in-mem, cp=%d (default):    %9d bytes  (%.2fx corpus)", DefaultSACheckpoint, fmSize, ratio(fmSize, rawSize))
	t.Logf("  breakdown: BWT=%d  checkpoints=%d  sampledSA=%d", fm.n, len(fm.checkpoint)*alphabetSize*8, len(fm.sampledSA)*2*8)
	t.Logf("FM-index in-mem, cp=4096 (coarse):   %9d bytes  (%.2fx corpus)", fmCoarse.approxMemBytes(), ratio(fmCoarse.approxMemBytes(), rawSize))
	t.Logf("  breakdown: BWT=%d  checkpoints=%d  sampledSA=%d", fmCoarse.n, len(fmCoarse.checkpoint)*alphabetSize*8, len(fmCoarse.sampledSA)*2*8)
	t.Logf("trigram postings payload only:       %9d bytes  (%.2fx corpus)", postingBytes, ratio(postingBytes, rawSize))
	t.Logf("trigram postings section (w/ ovh):   %9d bytes  (%.2fx corpus)", postingSectionBytes, ratio(postingSectionBytes, rawSize))
	t.Logf("NOTE: the trigram numbers EXCLUDE the separately-stored blob content")
	t.Logf("      (~1.00x corpus); the FM-index doubles as that content store.")
	t.Logf("      Per the research note, do NOT read this as FM < trigram. The")
	t.Logf("      checkpoint table dominates the default FM size; that table is a")
	t.Logf("      knob (cp=4096 shrinks it ~64x at the cost of rank latency) and a")
	t.Logf("      wavelet tree would replace it entirely. The BWT itself is ~1x")
	t.Logf("      corpus. Ratios depend on fixture entropy and are not portable.")
}

func ratio(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// approxMemBytes is a transparent, lower-bound estimate of the FM-index's
// in-memory footprint: BWT bytes + checkpoint table + sampled-SA map payload +
// the fixed C[] table. It excludes Go map/slice header overhead, so it is a
// floor, not an exact RSS figure — and the report says so.
func (fm *FMIndex) approxMemBytes() int {
	bwt := len(fm.bwt)
	checkpoints := len(fm.checkpoint) * alphabetSize * 8 // int per symbol per checkpoint
	sampled := len(fm.sampledSA) * 2 * 8                 // key+value ints per entry
	ctable := (alphabetSize + 1) * 8
	return bwt + checkpoints + sampled + ctable
}

// rareLiteral and commonLiteral exercise the count/locate asymmetry: a rare
// literal (a long, near-unique line, a handful of hits) vs a common one (the tab
// byte, tens of thousands of hits). Both occur in the fixture. The point is that
// Count cost is flat regardless, while Locate cost scales with the hit count
// (each hit pays an LF-walk to the nearest SA sample).
var (
	rareLiteral   = []byte("func (ix *Index) Postings(t trigram.Trigram) []Posting {\n")
	commonLiteral = []byte("\t")
)

func benchFixture() (*FMIndex, []byte) {
	corpus := codeFixture(1 << 20)
	fm, err := Build(corpus)
	if err != nil {
		panic(err)
	}
	return fm, corpus
}

func BenchmarkCountRare(b *testing.B) {
	fm, _ := benchFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fm.Count(rareLiteral)
	}
}

func BenchmarkCountCommon(b *testing.B) {
	fm, _ := benchFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fm.Count(commonLiteral)
	}
}

func BenchmarkLocateRare(b *testing.B) {
	fm, _ := benchFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fm.Locate(rareLiteral)
	}
}

func BenchmarkLocateCommon(b *testing.B) {
	fm, _ := benchFixture()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fm.Locate(commonLiteral)
	}
}

// TestLocateAsymmetry makes the count-cheap / locate-expensive asymmetry visible
// in plain `go test -v` output (no benchmark harness needed): it logs the hit
// count and a wall-clock per-op figure for Count vs Locate on a rare and a
// common literal. It asserts only correctness (Locate length == Count) so it is
// not timing-brittle; the timings are reported for the record, framed honestly.
func TestLocateAsymmetry(t *testing.T) {
	fm, _ := benchFixture()
	for _, lit := range []struct {
		name string
		p    []byte
	}{
		{"rare (long line)", rareLiteral},
		{"common (tab byte)", commonLiteral},
	} {
		cnt := fm.Count(lit.p)
		loc := fm.Locate(lit.p)
		if len(loc) != cnt {
			t.Fatalf("%s: Locate len %d != Count %d", lit.name, len(loc), cnt)
		}

		countNs := timePerOp(func() { fm.Count(lit.p) })
		locateNs := timePerOp(func() { fm.Locate(lit.p) })
		t.Logf("%-18s hits=%-6d  Count=%8.0f ns/op  Locate=%10.0f ns/op  (Locate/Count=%.0fx)",
			lit.name, cnt, countNs, locateNs, locateNs/countNs)
	}
	t.Logf("ASYMMETRY: Count is ~flat (O(pattern), independent of hits); Locate")
	t.Logf("           scales with hit count (each hit pays an LF-walk to the")
	t.Logf("           nearest SA sample). This is the structural property the")
	t.Logf("           research note flags as the reason FM-locate is a sidegrade")
	t.Logf("           vs trigram intersection on common code literals.")
}

// timePerOp runs fn enough times to get a stable per-call nanosecond figure.
func timePerOp(fn func()) float64 {
	const reps = 200
	start := time.Now()
	for i := 0; i < reps; i++ {
		fn()
	}
	return float64(time.Since(start).Nanoseconds()) / reps
}
