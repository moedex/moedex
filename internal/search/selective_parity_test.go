package search_test

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"moedex/internal/index"
	"moedex/internal/search"
)

// buildSelectiveIndex mirrors buildIndex (positional_test.go) but routes through
// the selective builder, dropping any trigram with document frequency above frac.
func buildSelectiveIndex(blobs map[string]string, frac float64) *index.Index {
	b := index.NewSelective(index.FrequencyThresholdSelector{MaxDocFraction: frac})
	names := make([]string, 0, len(blobs))
	for n := range blobs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(blobs[n])))
		b.AddFile("r", n, "/abs/"+n, sha, []byte(blobs[n]))
	}
	return b.Finalize()
}

func gotRegexIx(t *testing.T, ix *index.Index, pattern string) []string {
	t.Helper()
	ms, err := search.Regex(context.Background(), ix, pattern)
	if err != nil {
		t.Fatalf("Regex(%q): %v", pattern, err)
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s:%d", strings.TrimPrefix(m.AbsPath, "/abs/"), m.Line))
	}
	sort.Strings(out)
	return out
}

func gotLiteralIx(t *testing.T, ix *index.Index, q string) []string {
	t.Helper()
	ms, err := search.Literal(context.Background(), ix, q)
	if err != nil {
		t.Fatalf("Literal(%q): %v", q, err)
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s:%d", strings.TrimPrefix(m.AbsPath, "/abs/"), m.Line))
	}
	sort.Strings(out)
	return out
}

// goldLiteral is the brute-force literal oracle: every line containing q.
func goldLiteral(blobs map[string]string, q string) []string {
	var out []string
	for name, content := range blobs {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if line == "" && i == len(lines)-1 {
				continue
			}
			if strings.Contains(line, q) {
				out = append(out, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}
	sort.Strings(out)
	return out
}

// parityCorpus is a small polyglot fixture exercising literals, alternations,
// folded literals, concat boundaries, and a frequent-gram-heavy minified line.
func parityCorpus() map[string]string {
	return map[string]string{
		"a.go":  "package main\nfunc Handler(w http.ResponseWriter) {}\nvar itemAnswer = 1\n",
		"b.cs":  "namespace TC.SslApi;\npublic class SslService {\n  string Token;\n  void get() {}\n}\n",
		"c.txt": "the quick brown fox\nfoobar baz qux\nFOOBAR upper line\nFooBar mixed case\n",
		"d.js": strings.Repeat("var x=function(y){return z(y)};\n", 30) +
			"var Razavi_0 = itemAnswer(0);\n" +
			strings.Repeat("var q=compute(w);\n", 30),
		"e.txt": "alpha beta gamma\ndelta epsilon zeta\nrazavi lower here\nRAZAVI shouting\n",
		"f.txt": "handler response payload\nmem only\ninfo only\nmemberinfo too\n",
		"g.sql": "SELECT col FROM users WHERE id = ?\nINSERT INTO logs VALUES (1)\n",
	}
}

// parityPatterns is the regex battery: literals, alternations, char classes,
// folded (?i), concat boundary cases, and degenerate All patterns.
func parityPatterns() []string {
	return []string{
		"foobar",
		"Handler",
		"public",
		"class",
		"SslService",
		"[ab]cd",
		"foo|bar",
		"(get|set)",
		`func\s+[A-Z]`,
		"(?i)foobar",
		"(?i)razavi",
		"(?i)razavi|itemanswer",
		"(?i)token",
		"namespace",
		"return",
		"alpha",
		"epsilon",
		"http.ResponseWriter",
		"memberinfo",
		"(?i)memberinfo",
		"SELECT",
		"FROM users",
		"response payload",
		".",
		"qux",
		"itemAnswer",
	}
}

// TestRegexParityUnderSelection is the ripgrep-parity invariant restated for the
// selective path: Regex(selectiveIndex) == Regex(allTrigramIndex) == ground
// truth, across several df thresholds (including aggressive ones that drop most
// common grams). A divergence is a real parity break.
func TestRegexParityUnderSelection(t *testing.T) {
	blobs := parityCorpus()
	allTri := buildIndex(blobs) // from positional_test.go
	for _, frac := range []float64{0.05, 0.1, 0.25, 0.5, 0.9, 1.0} {
		sel := buildSelectiveIndex(blobs, frac)
		for _, pat := range parityPatterns() {
			gold := goldRegex(t, blobs, pat) // from positional_test.go
			allGot := gotRegexIx(t, allTri, pat)
			selGot := gotRegexIx(t, sel, pat)
			if strings.Join(allGot, ",") != strings.Join(gold, ",") {
				t.Fatalf("all-trigram diverged from gold (test bug) pat=%q\n got=%v\nwant=%v", pat, allGot, gold)
			}
			if strings.Join(selGot, ",") != strings.Join(gold, ",") {
				t.Errorf("frac=%.2f pattern %q: selective != gold\n selective=%v\n gold=%v", frac, pat, selGot, gold)
			}
		}
	}
}

// TestLiteralParityUnderSelection: the Literal() path (begin/end-gram positional
// intersection) must equal the brute-force literal oracle even when begin/end
// grams are deselected (forcing the sub-trigram scan fallback).
func TestLiteralParityUnderSelection(t *testing.T) {
	blobs := parityCorpus()
	literals := []string{
		"foobar", "Handler", "Token", "namespace", "SslService",
		"function", "response", "memberinfo", "SELECT", "qux", "x", // "x" < trigram
	}
	for _, frac := range []float64{0.05, 0.1, 0.5, 1.0} {
		sel := buildSelectiveIndex(blobs, frac)
		for _, lit := range literals {
			gold := goldLiteral(blobs, lit)
			got := gotLiteralIx(t, sel, lit)
			if strings.Join(got, ",") != strings.Join(gold, ",") {
				t.Errorf("frac=%.2f literal %q: selective Literal != gold\n got=%v\nwant=%v", frac, lit, got, gold)
			}
		}
	}
}

// TestPositionalFallbackWhenDriverDeselected proves graceful degradation: a
// pattern whose only trigram driver was deselected still returns all matches
// (forcing the scan fallback rather than silently dropping). We make the driver
// gram near-universal so frac 0.1 drops it, then confirm correctness AND that the
// search did NOT take the positional fast path (it degraded to scan/All).
func TestPositionalFallbackWhenDriverDeselected(t *testing.T) {
	var sb strings.Builder
	// "abc" on most lines -> near-universal -> dropped at low frac. Only one line
	// has the full target literal "abcZ".
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "abc filler line %d\n", i)
	}
	sb.WriteString("the abcZ target appears once\n")
	blobs := map[string]string{"big.txt": sb.String()}

	sel := buildSelectiveIndex(blobs, 0.1)
	pat := "abcZ"
	gold := goldRegex(t, blobs, pat)

	ms, stats, err := search.RegexWithStats(context.Background(), sel, pat)
	if err != nil {
		t.Fatalf("RegexWithStats: %v", err)
	}
	got := make([]string, 0, len(ms))
	for _, m := range ms {
		got = append(got, fmt.Sprintf("%s:%d", strings.TrimPrefix(m.AbsPath, "/abs/"), m.Line))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(gold, ",") {
		t.Fatalf("deselected-driver fallback wrong\n got=%v\nwant=%v", got, gold)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 match (abcZ), got %d: %v", len(got), got)
	}
	// The driver was deselected, so the search MUST NOT have used the positional
	// fast path — it degraded to the (sound) content scan.
	if stats.LineFilter == "positional" {
		t.Errorf("expected scan fallback (driver deselected), but took positional path; stats=%+v", stats)
	}
}

// TestRegexParityFuzzUnderSelection is a randomized differential: random blobs
// and random literal/alternation patterns, comparing the selective index against
// the gold oracle across random df thresholds. This is the parity invariant
// exercised cheaply over many cases to catch selection edge bugs.
func TestRegexParityFuzzUnderSelection(t *testing.T) {
	rng := rand.New(rand.NewSource(20260624))
	alphabet := "abcdefghijklmnop_0123456789"
	randToken := func(minLen int) string {
		n := minLen + rng.Intn(5)
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	for trial := 0; trial < 150; trial++ {
		target := randToken(3)
		blobs := map[string]string{}
		for bi := 0; bi < 4; bi++ {
			var sb strings.Builder
			for li := 0; li < 5; li++ {
				if rng.Intn(3) == 0 {
					fmt.Fprintf(&sb, "x %s y\n", target)
				} else {
					fmt.Fprintf(&sb, "%s %s\n", randToken(2), randToken(2))
				}
			}
			blobs[fmt.Sprintf("b%d.txt", bi)] = sb.String()
		}
		frac := 0.05 + rng.Float64()*0.95 // 0.05..1.0
		sel := buildSelectiveIndex(blobs, frac)

		pat := target
		if rng.Intn(2) == 0 {
			pat += "|" + randToken(3)
		}
		gold := goldRegex(t, blobs, pat)
		got := gotRegexIx(t, sel, pat)
		if strings.Join(got, ",") != strings.Join(gold, ",") {
			t.Fatalf("trial %d frac=%.3f pattern %q\n got=%v\nwant=%v\nblobs=%v", trial, frac, pat, got, gold, blobs)
		}
	}
}
