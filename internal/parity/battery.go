package parity

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Bucket names the query category (AC-D2 a–i).
type Bucket string

const (
	BCommonLiteral   Bucket = "a-common-literal"
	BRareLiteral     Bucket = "b-rare-literal"
	BPhraseLiteral   Bucket = "c-phrase-literal"
	BMetaLiteral     Bucket = "d-metachar-literal"
	BSubTrigram      Bucket = "e-sub-trigram"
	BHighFrequency   Bucket = "f-high-frequency"
	BRegex           Bucket = "g-regex"
	BCaseInsensitive Bucket = "h-case-insensitive"
	BUnicode         Bucket = "i-unicode"
)

// AllBuckets is the ordered set of categories the battery must cover.
var AllBuckets = []Bucket{
	BCommonLiteral, BRareLiteral, BPhraseLiteral, BMetaLiteral, BSubTrigram,
	BHighFrequency, BRegex, BCaseInsensitive, BUnicode,
}

// Query is one battery query plus the flags that pin every oracle to identical
// matching semantics. moedex, the gold scan, and ripgrep are each driven from
// these fields (see oracle adapters); the mapping is the whole reason regex and
// case/unicode variants can be compared exactly.
//
//   - Literal:    fixed-string search (rg -F; bytes.Contains; search.Literal).
//   - IgnoreCase: case-insensitive (rg -i; Go (?i)). Kept in Unicode mode so
//     rg's folding matches Go's (?i) Unicode simple folding.
//   - NoUnicode:  rg --no-unicode, making \w \d \s \b ASCII-only to match Go's
//     RE2 (whose shorthand classes are always ASCII). Set on every regex query
//     that has no unescaped '.', and never together with IgnoreCase (folding
//     needs Unicode) — the generator enforces this.
type Query struct {
	ID         int
	Bucket     Bucket
	Pattern    string
	Literal    bool
	IgnoreCase bool
	NoUnicode  bool
}

func (q Query) String() string {
	flags := ""
	if q.Literal {
		flags += "F"
	}
	if q.IgnoreCase {
		flags += "i"
	}
	if q.NoUnicode {
		flags += "U"
	}
	if flags != "" {
		flags = "[" + flags + "]"
	}
	return fmt.Sprintf("#%d %s %s%s", q.ID, q.Bucket, flags, strconvQuote(q.Pattern))
}

func strconvQuote(s string) string {
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// GoRegexSource returns the Go regexp source a non-literal (or case-insensitive
// literal) query is compiled to — the exact pattern the moedex adapter routes to
// search.Regex. Exported so external harnesses (e.g. the cas-export parity gate)
// drive a server.Corpus with byte-identical semantics to the in-package oracles.
func (q Query) GoRegexSource() string { return q.goRegexSource() }

// goRegexSource returns the Go regexp source moedex and gold compile for a
// query — exactly mirroring how the moedex adapter routes the query.
func (q Query) goRegexSource() string {
	switch {
	case q.Literal && q.IgnoreCase:
		return "(?i)" + regexp.QuoteMeta(q.Pattern)
	case q.Literal:
		return regexp.QuoteMeta(q.Pattern) // (used only for validation)
	case q.IgnoreCase:
		return "(?i)" + q.Pattern
	default:
		return q.Pattern
	}
}

// Battery is the generated query set plus its per-bucket index.
type Battery struct {
	Queries []Query
	Seed    int64
}

// BucketCounts returns the number of queries per bucket.
func (b *Battery) BucketCounts() map[Bucket]int {
	m := map[Bucket]int{}
	for _, q := range b.Queries {
		m[q.Bucket]++
	}
	return m
}

// Generate builds a deterministic, seed-reproducible battery from corpus-derived
// terms. Same pool (i.e. same fixed corpus) + same seed ⇒ identical battery.
//
// Target sizes per bucket sum to > 1000; each bucket is also seeded with a few
// fixed queries so it is non-empty even on a tiny corpus. Every generated regex
// is compiled here; an uncompilable pattern is a generator bug and panics rather
// than being silently dropped (we never shrink the battery to dodge a query).
func Generate(pool *TermPool, seed int64) *Battery {
	g := &gen{rng: rand.New(rand.NewSource(seed)), seen: map[string]bool{}}

	toks := pool.sortedTokens() // ascending frequency, then name
	n := len(toks)

	// Frequency bands over the sampled tokens.
	var rare, common, high []string
	if n > 0 {
		// rare: lowest-frequency quartile of usable tokens; high: top tokens.
		usable := filterTokens(toks, pool, 3, 24, true)
		un := len(usable)
		if un > 0 {
			rare = usable[:max1(un/4)]
			common = usable[un/4 : un-max1(un/10)]
			if len(common) == 0 {
				common = usable
			}
			high = usable[un-max1(un/10):]
		}
	}

	// (a) common literals
	for _, t := range g.pick(common, 150) {
		g.add(BCommonLiteral, t, true, false, false)
	}
	g.fixed(BCommonLiteral, []string{"public", "return", "string", "import", "class", "function"}, true, false, false)

	// (b) rare literals
	for _, t := range g.pick(rare, 150) {
		g.add(BRareLiteral, t, true, false, false)
	}
	g.fixed(BRareLiteral, []string{"ZZUNIQUEDUPTOKEN", "moedexRareNeedle42"}, true, false, false)

	// (c) multi-word / phrase literals
	for _, p := range g.pick(pool.phrases, 130) {
		g.add(BPhraseLiteral, p, true, false, false)
	}
	g.fixed(BPhraseLiteral, []string{"public class", "using System", "return null"}, true, false, false)

	// (d) literals containing regex metacharacters (matched literally)
	for _, m := range g.pick(pool.metas, 130) {
		g.add(BMetaLiteral, m, true, false, false)
	}
	g.fixed(BMetaLiteral, []string{"a.b", "[0]", "(x)", "i++", "=> {", "$value", "a|b", "\\n"}, true, false, false)

	// (e) sub-trigram (1–2 chars: the trigram filter cannot pre-filter these).
	// Operator/punctuation digraphs (bounded frequency) plus a few short slices.
	g.fixed(BSubTrigram, []string{
		"->", "=>", "==", "!=", ">=", "<=", "&&", "||", "::", "</", "/>", "*/", "/*",
		"){", "](", "()", "[]", "{}", ";", ",", "Q", "Z", "~", "@", "#",
	}, true, false, false)
	for _, t := range g.pick(rare, 30) {
		if len(t) >= 2 {
			g.add(BSubTrigram, t[:2], true, false, false)
		}
	}

	// (f) high-frequency terms (exercise any match-cap path; moedex has none, so
	// these must still return EVERY match).
	for _, t := range g.pick(high, 40) {
		g.add(BHighFrequency, t, true, false, false)
	}
	g.fixed(BHighFrequency, []string{"the", "and", "var", "for", "new"}, true, false, false)

	// (g) regex: alternation, char classes, anchors, word boundaries.
	rxToks := filterTokens(toks, pool, 3, 12, false) // ASCII-letter-only, mid length
	g.regexShapes(rxToks)

	// (h) case-insensitive variants (literals + a few alternations; kept in
	// Unicode mode so rg -i folding matches Go (?i)).
	for _, t := range g.pick(common, 80) {
		g.add(BCaseInsensitive, t, true, true, false)
	}
	g.fixed(BCaseInsensitive, []string{"PUBLIC", "Class", "Return", "NULL"}, true, true, false)
	for _, pair := range g.pickPairs(rxToks, 30) {
		g.add(BCaseInsensitive, pair[0]+"|"+pair[1], false, true, false)
	}

	// (i) unicode / multibyte literals and classes.
	for _, u := range g.pick(pool.unicodes, 55) {
		g.add(BUnicode, u, true, false, false)
	}
	g.fixed(BUnicode, []string{"café", "Σ", "→", "—", "你好", "naïve", "©"}, true, false, false)
	// Unicode character classes (Unicode mode on, so '.'/ranges align with Go).
	g.add(BUnicode, `[α-ω]+`, false, false, false)
	g.add(BUnicode, `\p{Greek}`, false, false, false)
	g.add(BUnicode, `[é-ü]`, false, false, false)

	// Stable order + IDs.
	sort.SliceStable(g.out, func(i, j int) bool {
		if g.out[i].Bucket != g.out[j].Bucket {
			return g.out[i].Bucket < g.out[j].Bucket
		}
		return g.out[i].Pattern < g.out[j].Pattern
	})
	for i := range g.out {
		g.out[i].ID = i
	}
	return &Battery{Queries: g.out, Seed: seed}
}

type gen struct {
	rng  *rand.Rand
	out  []Query
	seen map[string]bool
}

// add registers a query, deduping by (bucket, pattern, flags) and validating
// regex compilation.
func (g *gen) add(b Bucket, pattern string, literal, ic, nu bool) {
	if pattern == "" {
		return
	}
	if !utf8.ValidString(pattern) {
		panic(fmt.Sprintf("battery generated invalid UTF-8 pattern %q", pattern))
	}
	key := fmt.Sprintf("%s\x00%s\x00%v%v%v", b, pattern, literal, ic, nu)
	if g.seen[key] {
		return
	}
	g.seen[key] = true
	q := Query{Bucket: b, Pattern: pattern, Literal: literal, IgnoreCase: ic, NoUnicode: nu}
	if !literal {
		if _, err := regexp.Compile(q.goRegexSource()); err != nil {
			panic(fmt.Sprintf("battery generated invalid regex %q: %v", pattern, err))
		}
	}
	g.out = append(g.out, q)
}

func (g *gen) fixed(b Bucket, pats []string, literal, ic, nu bool) {
	for _, p := range pats {
		g.add(b, p, literal, ic, nu)
	}
}

// regexShapes instantiates the fixed structural shapes with corpus tokens.
func (g *gen) regexShapes(toks []string) {
	add := g.add
	// Anchors, classes, word boundaries → ASCII shorthand semantics (NoUnicode).
	for _, t := range g.pick(toks, 28) {
		add(BRegex, `^[\t ]*`+t, false, false, true) // anchored start, optional indent
	}
	for _, t := range g.pick(toks, 28) {
		add(BRegex, t+`$`, false, false, true) // anchored end
	}
	for _, t := range g.pick(toks, 28) {
		add(BRegex, `\b`+t+`\b`, false, false, true) // word boundary
	}
	for _, t := range g.pick(toks, 28) {
		add(BRegex, t+`[(){};,]`, false, false, true) // followed by punctuation class
	}
	for _, t := range g.pick(toks, 20) {
		add(BRegex, t+`[ \t]`, false, false, true) // followed by whitespace class
	}
	// Alternation of literals.
	for _, tr := range g.pickTriples(toks, 30) {
		add(BRegex, tr[0]+"|"+tr[1]+"|"+tr[2], false, false, true)
	}
	for _, tr := range g.pickTriples(toks, 20) {
		add(BRegex, `\b(`+tr[0]+`|`+tr[1]+`)\b`, false, false, true)
	}
	// '.' any-char: keep Unicode mode (codepoint) to align with Go's rune '.'.
	for _, t := range g.pick(toks, 28) {
		if len(t) >= 4 {
			add(BRegex, t[:1]+"."+t[2:], false, false, false) // interior any-char
		}
	}
	for _, pair := range g.pickPairs(toks, 24) {
		add(BRegex, pair[0]+`.*`+pair[1], false, false, false) // co-occurrence on a line
	}
	// Digit char class (>8 members → degrades to All; exercises full-scan verify).
	g.fixed(BRegex, []string{`[0-9][0-9][0-9][0-9]`, `0x[0-9a-fA-F]+`, `v[0-9]+`}, false, false, true)
}

// pick returns up to n deterministically-shuffled distinct items from src.
func (g *gen) pick(src []string, n int) []string {
	if len(src) == 0 {
		return nil
	}
	idx := g.rng.Perm(len(src))
	if n > len(idx) {
		n = len(idx)
	}
	out := make([]string, 0, n)
	for _, i := range idx[:n] {
		out = append(out, src[i])
	}
	return out
}

func (g *gen) pickPairs(src []string, n int) [][2]string {
	if len(src) < 2 {
		return nil
	}
	var out [][2]string
	for k := 0; k < n; k++ {
		a := src[g.rng.Intn(len(src))]
		b := src[g.rng.Intn(len(src))]
		if a == b {
			continue
		}
		out = append(out, [2]string{a, b})
	}
	return out
}

func (g *gen) pickTriples(src []string, n int) [][3]string {
	if len(src) < 3 {
		return nil
	}
	var out [][3]string
	for k := 0; k < n; k++ {
		a := src[g.rng.Intn(len(src))]
		b := src[g.rng.Intn(len(src))]
		c := src[g.rng.Intn(len(src))]
		if a == b || b == c || a == c {
			continue
		}
		out = append(out, [3]string{a, b, c})
	}
	return out
}

// filterTokens keeps tokens within [minLen,maxLen]; when letterDigitsOK is
// false, only ASCII-letter tokens are kept (clean regex instantiation).
func filterTokens(toks []string, pool *TermPool, minLen, maxLen int, letterDigitsOK bool) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if len(t) < minLen || len(t) > maxLen {
			continue
		}
		if !letterDigitsOK && !asciiLettersOnly(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func asciiLettersOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return len(s) > 0
}

func max1(x int) int {
	if x < 1 {
		return 1
	}
	return x
}

// AssertBuckets returns an error if any required bucket is empty or the total is
// below the floor. (AC-D2.)
func (b *Battery) AssertBuckets(floor int) error {
	counts := b.BucketCounts()
	var empty []string
	for _, bk := range AllBuckets {
		if counts[bk] == 0 {
			empty = append(empty, string(bk))
		}
	}
	if len(empty) > 0 {
		return fmt.Errorf("empty battery buckets: %s", strings.Join(empty, ", "))
	}
	if len(b.Queries) < floor {
		return fmt.Errorf("battery has %d queries, below floor %d", len(b.Queries), floor)
	}
	return nil
}
