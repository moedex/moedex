package eval

import (
	"fmt"
	"os"
	"path/filepath"

	"moedex/internal/corpus/catalog"
	"moedex/internal/index"
	"moedex/internal/ingest"
)

// CorpusRepo names one repo to ingest into a pooled eval index, plus a sampling
// predicate. RelDir is relative to the caller-configured corpus root.
// Repo is the label passed to ingest. Keep is a per-file predicate
// over the repo-relative path: it bounds the indexed universe so the eval builds
// fast and so the gold RelPaths line up exactly with what is indexed.
type CorpusRepo struct {
	Repo   string
	RelDir string
	Keep   func(relPath string) bool
}

// goldCorpusRepos builds the selected universe from the external dataset.
func goldCorpusRepos() []CorpusRepo {
	d := configuredCorpusDataset()
	repos := make([]CorpusRepo, 0, len(d.Repos))
	for _, sample := range d.Repos {
		repos = append(repos, CorpusRepo{Repo: sample.Repo, RelDir: sample.RelDir, Keep: sample.keep})
	}
	return repos
}

// CorpusRoot resolves the root directory that holds the gold-corpus repos.
// MOEDEX_CORPUS_ROOT overrides MOEDEX_CORPUS. Otherwise the managed-corpus
// default is used when no override is provided. Returns ("", false) when no usable
// root exists so callers can t.Skip cleanly.
func CorpusRoot() (string, bool) {
	for _, root := range []string{os.Getenv("MOEDEX_CORPUS_ROOT"), os.Getenv("MOEDEX_CORPUS")} {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err == nil {
			return root, true
		}
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	for _, name := range []string{catalog.DefaultCorpusDirName} {
		root := filepath.Join(home, name)
		if _, err := os.Stat(root); err == nil {
			return root, true
		}
	}
	return "", false
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
	if os.Getenv("MOEDEX_EVAL_DATASET") == "" {
		return nil, 0, nil, false
	}
	repos := goldCorpusRepos()
	root, found := CorpusRoot()
	if !found {
		return nil, 0, nil, false
	}
	ix, n, perRepo, err := BuildPooledIndex(root, repos)
	if err != nil || n == 0 {
		return nil, 0, perRepo, false
	}
	return ix, n, perRepo, true
}
