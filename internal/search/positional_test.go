package search_test

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"

	"crypto/sha1"

	"moedex/internal/index"
	"moedex/internal/search"
)

// goldRegex is an oracle: it runs the real regexp engine over every line of
// every blob with no trigram/positional filtering at all. The positional fast
// path (and the content-scan path) must return exactly this set, or it has
// dropped or invented a match. It mirrors the parity harness's in-process gold,
// at unit scale, so soundness regressions surface in `go test`.
func goldRegex(t *testing.T, blobs map[string]string, pattern string) []string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	var out []string
	for name, content := range blobs {
		for i, line := range strings.Split(content, "\n") {
			// strings.Split keeps a trailing "" after a final '\n'; ripgrep (and
			// moedex) do not emit that line, so skip the empty tail.
			if line == "" && i == strings.Count(content, "\n") {
				continue
			}
			if re.MatchString(line) {
				out = append(out, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}
	sort.Strings(out)
	return out
}

func gotRegex(t *testing.T, ix *index.Index, pattern string) []string {
	t.Helper()
	ms, err := search.Regex(ix, pattern)
	if err != nil {
		t.Fatalf("Regex(%q): %v", pattern, err)
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		// AbsPath is "/abs/<name>" here; reduce to <name> for comparison.
		out = append(out, fmt.Sprintf("%s:%d", strings.TrimPrefix(m.AbsPath, "/abs/"), m.Line))
	}
	sort.Strings(out)
	return out
}

func buildIndex(blobs map[string]string) *index.Index {
	ix := index.New()
	// Deterministic add order so blob IDs are stable across runs.
	names := make([]string, 0, len(blobs))
	for n := range blobs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sha := fmt.Sprintf("%x", sha1.Sum([]byte(blobs[n])))
		ix.AddFile("r", n, "/abs/"+n, sha, []byte(blobs[n]))
	}
	return ix
}

// TestPositionalEqualsGold is the soundness net for the positional verify path:
// across literal, alternation, and case-insensitive shapes — including the
// Unicode fold traps (long-s, Kelvin) — search.Regex must equal the brute-force
// gold. The positional path returns a superset of candidate lines and RE2
// verifies, so any divergence is a real bug.
func TestPositionalEqualsGold(t *testing.T) {
	blobs := map[string]string{
		"a.go":    "func Razavi() {}\nvar itemAnswer = 1\n// nothing here\n",
		"b.go":    "RAZAVI is shouting\nItemAnswer mixed\nrazavi lower\n",
		"c.go":    "the password field\nPASSWORD reset\nPaSsWoRd here\n",
		"d.txt":   "user paſſword via long-s\nKelvin K vs K sign\n",
		"e.min.js": strings.Repeat("var x=function(y){return z(y)};\n", 50) +
			"var Razavi_0 = itemAnswer(0);\n" +
			strings.Repeat("var q=compute(w);\n", 50),
		"f.go": "plain text only\nhandler response payload\n",
		"g.go": "MemberInfo here\nmem only\ninfo only\nmemberinfo too\n",
	}
	ix := buildIndex(blobs)

	patterns := []string{
		`(?i)razavi`,
		`(?i)razavi|itemanswer`,
		`(?i)password`,
		`(?i)memberinfo`,
		`Razavi`,
		`Razavi|itemAnswer`,
		`itemAnswer`,
		`(?i)kelvin`,
		// k/s-dirty folded literal: no clean trigram, so it routes to the
		// content scan; must still equal gold (Unicode fold via long-s).
		`(?i)password|hess`,
		// short 3-char literal alternation (trigram-length, positional path).
		`foo|bar`,
		`the`,
	}
	for _, pat := range patterns {
		want := goldRegex(t, blobs, pat)
		got := gotRegex(t, ix, pat)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("pattern %q\n got=%v\nwant=%v", pat, got, want)
		}
	}
}

// TestPositionalRouting pins which shapes take the positional path versus the
// content scan, so a future change that silently disables the fast path (or
// wrongly routes an unbounded pattern through it) is caught.
func TestPositionalRouting(t *testing.T) {
	ix := buildIndex(map[string]string{
		"a.go": "Razavi itemAnswer handler response [alpha] αβγ\n",
	})
	cases := []struct {
		pat        string
		positional bool
	}{
		{`(?i)razavi|itemanswer`, true}, // CI alternation of rare tokens -> postings
		{`foo|bar`, true},               // 3-char literals are trigram-length
		{`(?i)abc|xyz`, true},           // CI 3-char literals
		{`Razavi|itemAnswer`, false},    // len>3 literals: content scan (no decomposition)
		{`handler|response`, false},     // len>3 literals: content scan
		{`func_[0-9]+`, false},          // required prefix len>3
		{`[α-ω]+`, false},               // rune class
		{`.`, false},                    // no required literal
	}
	for _, c := range cases {
		_, stats, err := search.RegexWithStats(ix, c.pat)
		if err != nil {
			t.Fatalf("RegexWithStats(%q): %v", c.pat, err)
		}
		isPos := stats.LineFilter == "positional"
		if isPos != c.positional {
			t.Errorf("pattern %q: positional=%v (LineFilter=%q), want positional=%v", c.pat, isPos, stats.LineFilter, c.positional)
		}
	}
}

// TestPositionalFallbackOnCommonDriver proves that when a driver trigram is too
// common (its posting list exceeds the positional cap), the query still returns
// correct results — by falling back to the content scan rather than reading a
// huge posting list. We synthesize a trigram on nearly every line so the cap is
// exceeded, then confirm correctness against gold.
func TestPositionalFallbackOnCommonDriver(t *testing.T) {
	var sb strings.Builder
	// "xyz" on most lines makes the (?i)xyz driver common; the cap forces the
	// content-scan fallback. A handful of lines actually match the full literal.
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "noise xyz line %d\n", i)
	}
	sb.WriteString("the XyZabc target line\n")
	blobs := map[string]string{"big.txt": sb.String()}
	ix := buildIndex(blobs)

	pat := `(?i)xyzabc`
	want := goldRegex(t, blobs, pat)
	got := gotRegex(t, ix, pat)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("common-driver fallback wrong\n got=%v\nwant=%v", got, want)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 match (XyZabc), got %d: %v", len(got), got)
	}
}

// TestPositionalFuzzEqualsGold is a randomized differential: random blobs and
// random case-insensitive literal alternations, checked against the gold oracle.
// This is the same contract the full-corpus parity gate enforces, exercised
// cheaply over many small cases to catch fold/positional edge bugs.
func TestPositionalFuzzEqualsGold(t *testing.T) {
	rng := rand.New(rand.NewSource(20260623))
	alphabet := "abcdefGHIJklmnoPQRStuvwXYZ_0123456789"
	randToken := func() string {
		n := 3 + rng.Intn(6)
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(b)
	}
	for trial := 0; trial < 200; trial++ {
		// A few blobs, each a few lines mixing random tokens and a target token.
		target := randToken()
		blobs := map[string]string{}
		for bi := 0; bi < 3; bi++ {
			var sb strings.Builder
			for li := 0; li < 5; li++ {
				switch rng.Intn(4) {
				case 0: // plant the target in a random case
					tok := []byte(strings.ToLower(target))
					for i := range tok {
						if rng.Intn(2) == 0 && tok[i] >= 'a' && tok[i] <= 'z' {
							tok[i] -= 32
						}
					}
					fmt.Fprintf(&sb, "x %s y\n", tok)
				default:
					fmt.Fprintf(&sb, "%s %s %s\n", randToken(), randToken(), randToken())
				}
			}
			blobs[fmt.Sprintf("b%d.txt", bi)] = sb.String()
		}
		ix := buildIndex(blobs)
		pat := "(?i)" + regexp.QuoteMeta(strings.ToLower(target))
		if rng.Intn(2) == 0 {
			pat += "|" + regexp.QuoteMeta(strings.ToLower(randToken()))
		}
		want := goldRegex(t, blobs, pat)
		got := gotRegex(t, ix, pat)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("trial %d pattern %q\n got=%v\nwant=%v\nblobs=%v", trial, pat, got, want, blobs)
		}
	}
}
