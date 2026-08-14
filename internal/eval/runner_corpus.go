package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"moedex/internal/index"
	"moedex/internal/ingest"
)

// CorpusRepo names one repo to ingest into a pooled eval index, plus a sampling
// predicate. RelDir is the repo directory relative to the corpus root (~/TCGitlab
// by default). Repo is the label passed to ingest. Keep is a per-file predicate
// over the repo-relative path: it bounds the indexed universe so the eval builds
// fast and so the gold RelPaths line up exactly with what is indexed.
type CorpusRepo struct {
	Repo   string
	RelDir string
	Keep   func(relPath string) bool
}

// goldCorpusRepos describes the three repos CorpusGold() is labeled against and
// the sampling predicate documented in gold_corpus.go. Centralized here so the
// gold labels and the indexed universe cannot drift apart.
func goldCorpusRepos() []CorpusRepo {
	return []CorpusRepo{
		{
			Repo:   "TC.SslApi",
			RelDir: filepath.Join("Services.Registrar", "TC.SslApi"),
			// App C# under src/, EXCLUDING the test project and generated build
			// output. This mirrors the TS sample's *.spec.ts/e2e exclusion: test files
			// are not implementation, and C# unit tests are named after the method
			// under test (CheckAllOrderStatuses_...), so indexing them pollutes every
			// method-name query with an unlabeled distractor. obj/bin hold generated
			// .cs (AssemblyInfo, etc.) — pure noise.
			Keep: func(p string) bool {
				if !strings.HasPrefix(p, "src/") || !strings.HasSuffix(p, ".cs") {
					return false
				}
				if strings.Contains(p, ".Tests/") || strings.Contains(p, "/obj/") || strings.Contains(p, "/bin/") {
					return false
				}
				return true
			},
		},
		{
			Repo:   "dropcatchadminui",
			RelDir: filepath.Join("UIs.Internal", "dropcatchadminui"),
			// App source .ts only: exclude tests (*.spec.ts) and e2e/.
			Keep: func(p string) bool {
				if !strings.HasPrefix(p, "src/") || !strings.HasSuffix(p, ".ts") {
					return false
				}
				if strings.HasSuffix(p, ".spec.ts") || strings.HasPrefix(p, "e2e/") {
					return false
				}
				return true
			},
		},
		{
			Repo:   "mysql-scripts",
			RelDir: filepath.Join("Mysql.Utilities", "mysql-scripts"),
			// All .sql (this repo's .sql files are small, granular scripts).
			Keep: func(p string) bool { return strings.HasSuffix(p, ".sql") },
		},
		{
			Repo:   "hugedomains",
			RelDir: filepath.Join("coldfusion", "hugedomains"),
			// The _inc/ include library: action/UDF .cfm files, each a small
			// purpose-named script that defines or uses a <cffunction>. Cohesive and
			// granular (a query maps to a specific file), and it exercises the CF
			// symbol arm (CFExtractor pulls <cffunction> names). Excludes the rest of
			// the ~2200-file app, which is page templates, not labelable units.
			Keep: func(p string) bool {
				return strings.HasPrefix(p, "_inc/") && strings.HasSuffix(p, ".cfm")
			},
		},
	}
}

// CorpusRoot resolves the root directory that holds the gold-corpus repos.
// MOEDEX_CORPUS_ROOT overrides; otherwise ~/TCGitlab. Returns ("", false) when no
// usable root exists, so callers can t.Skip cleanly (mirrors the corpus-absent
// skip in TestTCSslApiMeasurement).
func CorpusRoot() (string, bool) {
	if root := os.Getenv("MOEDEX_CORPUS_ROOT"); root != "" {
		if _, err := os.Stat(root); err == nil {
			return root, true
		}
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	root := filepath.Join(home, "TCGitlab")
	if _, err := os.Stat(root); err != nil {
		return "", false
	}
	return root, true
}

// BuildPooledIndex ingests each repo under root, keeps only the files its Keep
// predicate accepts, and indexes the union into one index. It returns the index,
// the number of indexed files, and a per-repo kept-count map (for logging /
// asserting the sample is non-empty). A repo that fails to ingest (e.g. not a git
// repo, or absent) contributes zero files rather than failing the whole build, so
// a partial corpus still yields a usable index; callers should check counts and
// skip when the union is empty.
func BuildPooledIndex(root string, repos []CorpusRepo) (*index.Index, int, map[string]int, error) {
	for _, repo := range repos {
		dir := filepath.Join(root, repo.RelDir)
		if _, err := ingest.AIPrivacyFingerprint(dir); err != nil {
			return nil, 0, nil, fmt.Errorf("privacy preflight: %w", err)
		}
	}
	ix := index.New()
	perRepo := make(map[string]int, len(repos))
	total := 0
	for _, r := range repos {
		dir := filepath.Join(root, r.RelDir)
		files, err := ingest.Repo(r.Repo, dir)
		if err != nil {
			if ingest.IsPrivacyPolicyError(err) {
				return nil, 0, nil, fmt.Errorf("privacy preflight: %w", err)
			}
			perRepo[r.Repo] = 0
			continue
		}
		kept := 0
		for _, f := range files {
			if r.Keep != nil && !r.Keep(f.RelPath) {
				continue
			}
			ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			kept++
		}
		perRepo[r.Repo] = kept
		total += kept
	}
	return ix, total, perRepo, nil
}

// BuildGoldCorpusIndex is the convenience wrapper: resolve the root, then build
// the pooled index over goldCorpusRepos(). Returns ok=false when the corpus root
// is absent so the caller can skip.
func BuildGoldCorpusIndex() (ix *index.Index, n int, perRepo map[string]int, ok bool) {
	root, found := CorpusRoot()
	if !found {
		return nil, 0, nil, false
	}
	ix, n, perRepo, err := BuildPooledIndex(root, goldCorpusRepos())
	if err != nil || n == 0 {
		return nil, 0, perRepo, false
	}
	return ix, n, perRepo, true
}
