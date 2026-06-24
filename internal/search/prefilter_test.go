package search_test

import (
	"context"
	"crypto/sha1"
	"fmt"
	"testing"

	"moedex/internal/index"
	"moedex/internal/search"
)

func addBlob(ix *index.Index, name, content string) {
	sha := fmt.Sprintf("%x", sha1.Sum([]byte(content)))
	ix.AddFile("r", name, "/abs/"+name, sha, []byte(content))
}

// TestPrefilterSoundness_Alternation proves the regex literal prefilter never
// drops a true match. For an alternation like "(foo|bar)baz" there is NO single
// literal that begins every match ("foobaz" and "barbaz" share no required
// prefix), so regexp.LiteralPrefix returns empty and the prefilter must be
// DISABLED. A naive implementation that grabbed "foo" (the first branch) as a
// prefilter literal would wrongly skip the "barbaz" line. We assert both
// matches are found.
func TestPrefilterSoundness_Alternation(t *testing.T) {
	ix := index.New()
	addBlob(ix, "a.txt", "the foobaz token\n")
	addBlob(ix, "b.txt", "another barbaz here\n")

	got, err := search.Regex(context.Background(), ix,"(foo|bar)baz")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("prefilter dropped a real match: expected 2 (foobaz + barbaz), got %d: %v", len(got), got)
	}
}

// TestPrefilterSoundness_AlternationOfLiterals proves the multi-literal
// prefilter for a top-level alternation finds matches reachable through ANY
// branch. "handler|response|payload" yields the required-literal set
// {handler,response,payload}; a line must be kept if it contains any one of
// them. We plant each branch on a different line and assert all three are found
// (a buggy filter that only checked the first branch would drop two).
func TestPrefilterSoundness_AlternationOfLiterals(t *testing.T) {
	ix := index.New()
	addBlob(ix, "a.txt", "x handler x\n")
	addBlob(ix, "b.txt", "y response y\n")
	addBlob(ix, "c.txt", "z payload z\n")
	addBlob(ix, "d.txt", "none of the keywords here\n")

	got, err := search.Regex(context.Background(), ix,"handler|response|payload")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("alternation prefilter dropped branches: expected 3, got %d: %v", len(got), got)
	}
}

// TestPrefilterSoundness_LeadingClass proves a pattern starting with a character
// class also disables the prefilter. "[Tt]oken" has no required literal prefix,
// so both "Token" and "token" lines must be found.
func TestPrefilterSoundness_LeadingClass(t *testing.T) {
	ix := index.New()
	addBlob(ix, "a.txt", "a Token line\n")
	addBlob(ix, "b.txt", "a token line\n")

	got, err := search.Regex(context.Background(), ix,"[Tt]oken")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("prefilter dropped a case variant: expected 2, got %d: %v", len(got), got)
	}
}

// TestPrefilterCorrect_RequiredPrefix confirms the prefilter, when it IS active
// (a genuine required prefix), still finds matches whose literal appears
// mid-line (not at the line start). "func_[0-9]+" has required prefix "func_";
// a match can appear anywhere on the line, so searching the whole line for the
// literal (not anchoring at column 0) is necessary and sufficient.
func TestPrefilterCorrect_RequiredPrefix(t *testing.T) {
	ix := index.New()
	addBlob(ix, "a.txt", "   call func_42(x)\n") // literal mid-line, real match
	addBlob(ix, "b.txt", "no marker on this line\n")

	got, err := search.Regex(context.Background(), ix,"func_[0-9]+")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 match (func_42 mid-line), got %d: %v", len(got), got)
	}
	if got[0].Line != 1 {
		t.Fatalf("expected match on line 1, got line %d", got[0].Line)
	}
}

// TestPrefilterSoundness_CaseInsensitive proves the folded-literal prefilter
// (Phase 2) never drops a true (?i) match — including a match reached through a
// non-ASCII Unicode fold variant (long-s U+017F), which the prefilter must not
// reject. Every case variant of "password" must be found; the unrelated line
// must not inflate the count.
func TestPrefilterSoundness_CaseInsensitive(t *testing.T) {
	ix := index.New()
	addBlob(ix, "lower.txt", "reset password now\n")
	addBlob(ix, "upper.txt", "the PASSWORD field\n")
	addBlob(ix, "mixed.txt", "a PaSsWoRd here\n")
	addBlob(ix, "longs.txt", "user paſſword reset\n") // (?i)password matches via s<->ſ
	addBlob(ix, "none.txt", "totally unrelated text\n")

	got, err := search.Regex(context.Background(), ix,"(?i)password")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("(?i)password: prefilter dropped a true match: want 4 variant lines, got %d: %v", len(got), got)
	}
}

// TestPrefilterSoundness_CaseInsensitiveShortDirtySpan proves the folded
// prefilter can use a clean short span from a k/s-dirty literal without dropping
// Unicode fold matches. "hess" has no clean trigram because every trigram
// touches s, but "he" is a required clean folded span.
func TestPrefilterSoundness_CaseInsensitiveShortDirtySpan(t *testing.T) {
	ix := index.New()
	addBlob(ix, "lower.txt", "hess found\n")
	addBlob(ix, "upper.txt", "HESS found\n")
	addBlob(ix, "longs.txt", "heſſ found\n")
	addBlob(ix, "none.txt", "haze found\n")

	got, err := search.Regex(context.Background(), ix,"(?i)hess")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("(?i)hess: prefilter dropped a true folded match: want 3, got %d: %v", len(got), got)
	}
}

func TestPrefilterSoundness_CaseInsensitiveMultiPosition(t *testing.T) {
	ix := index.New()
	addBlob(ix, "member.txt", "mem only\ninfo only\nmemberinfo ok\nunrelated text\n")

	got, stats, err := search.RegexWithStats(context.Background(), ix,`(?i)MemberInfo`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("multi-position folded prefilter dropped a true match: want 1, got %d: %v", len(got), got)
	}
	if stats.LinesRE2 >= stats.CandidateLines {
		t.Fatalf("multi-position folded prefilter did not reduce RE2 lines: stats=%+v", stats)
	}
}

func TestPrefilterSoundness_UnicodeCharClass(t *testing.T) {
	ix := index.New()
	addBlob(ix, "alpha.txt", "start αβγ end\n")
	addBlob(ix, "omega.txt", "range ends at ω\n")
	addBlob(ix, "upper.txt", "uppercase Omega only Ω\n")
	addBlob(ix, "latin.txt", "plain latin text\n")

	got, stats, err := search.RegexWithStats(context.Background(), ix,`[α-ω]+`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("[α-ω]+ prefilter dropped/added matches: want 2, got %d: %v", len(got), got)
	}
	if stats.LineFilter == "none" {
		t.Fatalf("[α-ω]+ did not get a bounded Unicode class prefilter: stats=%+v", stats)
	}
	if stats.LinesRE2 >= stats.CandidateLines {
		t.Fatalf("Unicode class prefilter did not reduce RE2 lines: stats=%+v", stats)
	}
}

func TestPrefilterSoundness_UnicodeClassEdges(t *testing.T) {
	ix := index.New()
	addBlob(ix, "accent.txt", "accent é here\n")
	addBlob(ix, "capital.txt", "capital É here\n")
	addBlob(ix, "umlaut.txt", "umlaut ü here\n")
	addBlob(ix, "latin.txt", "plain latin text\n")

	got, stats, err := search.RegexWithStats(context.Background(), ix,`[é-ü]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("[é-ü] prefilter dropped/added matches: want 2, got %d: %v", len(got), got)
	}
	if stats.LineFilter == "none" {
		t.Fatalf("[é-ü] did not get a bounded Unicode class prefilter: stats=%+v", stats)
	}

	got, _, err = search.RegexWithStats(context.Background(), ix,`foo|[é-ü]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("foo|[é-ü] prefilter dropped/added Unicode branch matches: want 2, got %d: %v", len(got), got)
	}

	got, _, err = search.RegexWithStats(context.Background(), ix,`(?i)[é]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("(?i)[é] should remain sound with folded class prefilter disabled: want 2, got %d: %v", len(got), got)
	}
}

func TestPrefilterSoundness_ConjunctiveConcat(t *testing.T) {
	ix := index.New()
	addBlob(ix, "both.txt", "public final class User\n")
	addBlob(ix, "public.txt", "public final enum User\n")
	addBlob(ix, "class.txt", "private final class User\n")

	got, err := search.Regex(context.Background(), ix,`public\s+final\s+class`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("conjunctive prefilter should keep only the true matching line: got %d: %v", len(got), got)
	}
}
