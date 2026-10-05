package search_test

import (
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/search"
)

// loc is a (file, line) location — the granularity at which we assert parity
// with ripgrep's default line-oriented matching.
type loc struct {
	path string
	line int
}

// Regex queries are kept to the ASCII subset where Go's RE2 and ripgrep's Rust
// regex agree. In particular \w is ASCII-only in Go but Unicode-aware in rg, so
// we never apply \w-style classes to the non-ASCII synthetic content.
var (
	literalQueries = []string{
		"public",
		"namespace",
		"using System",
		"café_au_lait",     // exercises multi-byte rune offsets
		"Σumма",            // non-ASCII begin/end grams
		"ZZUNIQUEDUPTOKEN", // present only in deduped twin files
		"x",                // length < trigram size: full-scan path
	}
	regexQueries = []string{
		`public\s+class`,
		`namespace\s+[A-Za-z.]+`,
		`using\s+[A-Za-z.]+;`,
		`(get|set);`, // alternation -> OR of trigram queries
		`[Tt]oken`,   // leading char class -> degrades to All, must stay correct
		`class\s+[A-Za-z]+`,
		`.`, // matches every non-empty line: All-query degradation
	}
)

func TestDedup(t *testing.T) {
	ix := index.New()
	ix.AddFile("r", "a.cs", "/abs/a.cs", "samehash", []byte("hello world\nfoo bar\n"))
	ix.AddFile("r", "b.cs", "/abs/b.cs", "samehash", []byte("hello world\nfoo bar\n"))
	ix.AddFile("r", "c.cs", "/abs/c.cs", "otherhash", []byte("different\n"))

	if ix.NumBlobs() != 2 {
		t.Fatalf("identical content should dedupe to 1 blob (+1 other) = 2; got %d", ix.NumBlobs())
	}
	// A match in the shared blob must expand to both files.
	got, err := search.Literal(context.Background(), ix, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected match in both deduped files, got %d: %v", len(got), got)
	}
}

func TestParitySynthetic(t *testing.T) {
	requireRipgrep(t)
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("a.cs", "namespace Example.SslApi;\npublic class SslService {\n  public string Token;\n  void get() {}\n}\n")
	write("b.cs", "using System;\nusing System.Text;\nclass Other { get; set; }\n")
	write("unicode.txt", "let prix = café_au_lait;\nΣumма = Δ + ß\nplain ascii line\n")
	// Two byte-identical files exercising the dedup-expand path.
	dup := "marker line\nZZUNIQUEDUPTOKEN appears here\n"
	write("dup1.cs", dup)
	write("dup2.cs", dup)

	ix, files := indexDir(t, "synthetic", dir)
	runParity(t, ix, files)
}

func TestParityCorpus(t *testing.T) {
	requireRipgrep(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	base := os.Getenv("MOEDEX_CORPUS")
	if base == "" {
		base = filepath.Join(home, ".moedex-managed")
	}
	if _, err := os.Stat(base); err != nil {
		t.Skip("corpus not present at ~/.moedex-managed")
	}

	repos, err := ingest.DiscoverRepos(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) == 0 {
		t.Skip("no repositories in configured corpus")
	}
	if len(repos) > 2 {
		repos = repos[:2]
	}
	ix := index.New()
	var files []string
	for _, r := range repos {
		if _, err := os.Stat(r); err != nil {
			t.Skipf("repo not present: %s", r)
		}
		fs, err := ingest.Repo(filepath.Base(r), r)
		if err != nil {
			t.Fatalf("ingest %s: %v", r, err)
		}
		for _, f := range fs {
			ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			files = append(files, f.AbsPath)
		}
	}
	t.Logf("indexed %d files into %d blobs", len(files), ix.NumBlobs())
	runParity(t, ix, files)
}

// indexDir indexes every file under dir (no git needed; SHA = sha1 of content
// so identical files dedupe) and returns the index plus the abs path universe.
func indexDir(t *testing.T, repo, dir string) (*index.Index, []string) {
	t.Helper()
	ix := index.New()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sha := fmt.Sprintf("%x", sha1.Sum(content))
		ix.AddFile(repo, rel, p, sha, content)
		files = append(files, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ix, files
}

// runParity asserts moedex's match set equals ripgrep's for every query.
func runParity(t *testing.T, ix *index.Index, files []string) {
	t.Helper()
	for _, q := range literalQueries {
		want := ripgrep(t, q, true, files)
		lm, err := search.Literal(context.Background(), ix, q)
		if err != nil {
			t.Errorf("literal %q: moedex error: %v", q, err)
			continue
		}
		got := toLocs(lm)
		assertEqual(t, "literal "+strconv.Quote(q), want, got)
	}
	for _, q := range regexQueries {
		want := ripgrep(t, q, false, files)
		m, err := search.Regex(context.Background(), ix, q)
		if err != nil {
			t.Errorf("regex %q: moedex error: %v", q, err)
			continue
		}
		assertEqual(t, "regex "+strconv.Quote(q), want, toLocs(m))
	}
}

func toLocs(ms []search.Match) map[loc]bool {
	out := map[loc]bool{}
	for _, m := range ms {
		out[loc{m.AbsPath, m.Line}] = true
	}
	return out
}

// ripgrep runs rg over exactly the given files and returns matching locations.
// Explicit file args bypass rg's ignore logic, so the universe matches ours.
func ripgrep(t *testing.T, pattern string, literal bool, files []string) map[loc]bool {
	t.Helper()
	args := []string{"--no-config", "--case-sensitive", "--no-heading", "--color=never", "--with-filename", "-n"}
	if literal {
		args = append(args, "-F")
	}
	args = append(args, "-e", pattern, "--")
	args = append(args, files...)

	out, err := exec.Command("rg", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return map[loc]bool{} // no matches
		}
		t.Fatalf("rg %q: %v", pattern, err)
	}

	res := map[loc]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		// "path:lineno:content" — paths in our corpus contain no ':'.
		c1 := strings.IndexByte(line, ':')
		rest := line[c1+1:]
		c2 := strings.IndexByte(rest, ':')
		n, err := strconv.Atoi(rest[:c2])
		if err != nil {
			t.Fatalf("rg %q: unparseable line %q", pattern, line)
		}
		res[loc{line[:c1], n}] = true
	}
	return res
}

func assertEqual(t *testing.T, name string, want, got map[loc]bool) {
	t.Helper()
	var missing, extra []string
	for l := range want {
		if !got[l] {
			missing = append(missing, fmt.Sprintf("%s:%d", l.path, l.line))
		}
	}
	for l := range got {
		if !want[l] {
			extra = append(extra, fmt.Sprintf("%s:%d", l.path, l.line))
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		t.Logf("%s: %d matches, parity OK", name, len(want))
		return
	}
	sort.Strings(missing)
	sort.Strings(extra)
	t.Errorf("%s: parity MISMATCH\n  rg has, moedex missing (%d): %s\n  moedex has, rg missing (%d): %s",
		name, len(missing), sample(missing), len(extra), sample(extra))
}

func sample(s []string) string {
	if len(s) > 8 {
		return strings.Join(s[:8], ", ") + fmt.Sprintf(" ... (+%d)", len(s)-8)
	}
	return strings.Join(s, ", ")
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
}
