package query

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"moedex/internal/index"
	"moedex/internal/ingest"
)

// TestSelectivityMeasurement quantifies the Cox reduction's improvement over the
// slice-1 required-literals reduction by counting candidate blobs each returns
// for a set of representative code-search regexes. Fewer candidates = better
// selectivity (less verification work). It builds the corpus from ~/TCGitlab if
// present and skips cleanly otherwise.
func TestSelectivityMeasurement(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	base := filepath.Join(home, "TCGitlab")
	if _, err := os.Stat(base); err != nil {
		t.Skip("corpus not present at ~/TCGitlab")
	}

	repos := []string{
		filepath.Join(base, "Services.Registrar", "TC.PushApi"),
		filepath.Join(base, "Services.Registrar", "TC.SslApi"),
	}
	ix := index.New()
	nFiles := 0
	for _, r := range repos {
		if _, err := os.Stat(r); err != nil {
			continue
		}
		fs, err := ingest.Repo(filepath.Base(r), r)
		if err != nil {
			t.Fatalf("ingest %s: %v", r, err)
		}
		for _, f := range fs {
			ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			nFiles++
		}
	}
	if ix.NumBlobs() == 0 {
		t.Skip("no indexable repos found under ~/TCGitlab/Services.Registrar")
	}
	total := ix.NumBlobs()
	t.Logf("corpus: %d files -> %d blobs", nFiles, total)

	// Representative code-search regexes: char classes, alternations, boundary
	// joins, optional pieces — exactly the cases the Cox reduction improves.
	queries := []string{
		`public\s+class`,
		`namespace\s+[A-Za-z.]+`,
		`using\s+[A-Za-z.]+;`,
		`(get|set);`,
		`[Tt]oken`,
		`class\s+[A-Za-z]+`,
		`[A-Z][a-z]+Service`,
		`[A-Z][a-z]+Exception`,
		`I[A-Z][a-z]+Repository`,
		`async\s+Task`,
		`return\s+await`,
		`new\s+[A-Z][a-zA-Z]+\(`,
		`Ssl(Service|Order|Cert)`,
		`Http(Get|Post|Put|Delete)`,
		`config\.[A-Za-z]+`,
		`\[(Http|Route|Api)`,
		`catch\s*\(`,
		`throw\s+new`,
	}

	t.Logf("%-34s %8s %8s %8s %10s", "pattern", "OLD", "NEW", "all", "new/all%")
	var sumOld, sumNew int
	for _, pat := range queries {
		oldQ, err := fromRegexpRequiredLiterals(pat)
		if err != nil {
			t.Errorf("old reduce %q: %v", pat, err)
			continue
		}
		newQ, err := FromRegexp(pat)
		if err != nil {
			t.Errorf("new reduce %q: %v", pat, err)
			continue
		}

		t0 := time.Now()
		oldC := len(oldQ.Eval(ix))
		oldDur := time.Since(t0)

		t1 := time.Now()
		newC := len(newQ.Eval(ix))
		newDur := time.Since(t1)

		sumOld += oldC
		sumNew += newC
		pct := 100.0 * float64(newC) / float64(total)
		t.Logf("%-34s %8d %8d %8d %9.1f%%  (old %v, new %v)  q=%s",
			pat, oldC, newC, total, pct, oldDur.Round(time.Microsecond), newDur.Round(time.Microsecond), newQ.String())

		// Safety floor: the Cox reduction must never select MORE blobs than the
		// required-literals reduction (it strictly adds constraints). If it does,
		// something is unsound or non-monotonic.
		if newC > oldC {
			t.Errorf("pattern %q: NEW (%d) selected more candidates than OLD (%d)", pat, newC, oldC)
		}
	}
	t.Logf("TOTAL candidate blobs across %d queries: OLD=%d  NEW=%d  reduction=%.1f%%",
		len(queries), sumOld, sumNew, 100.0*float64(sumOld-sumNew)/float64(maxInt(sumOld, 1)))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
