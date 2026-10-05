package blobstore

// Corpus-scale compaction-GC PARITY gate (the orchestrator's definitive run over
// REAL configured content). It is the corpus-driven sibling of TestCompactionParity
// (the self-contained fixture gate): same proof, but the dead blobs are created from a
// bounded MUTABLE COPY of real polyglot repos rather than a synthetic git fixture.
//
// HARD CONSTRAINT — it NEVER mutates the source corpus (~/.moedex-managed). The gate needs to
// "remove a repo" + "churn a file" to create real dead content, so it copies a bounded
// subset of repos (including their .git) into a fresh t.TempDir() and operates ONLY on
// that copy. The source is read once (DiscoverRepos + a recursive copy) and never written.
//
// GATING (mirrors TestCASExportParityCorpus): skipped unless MOEDEX_COMPACT_PARITY_CORPUS
// is set. MOEDEX_COMPACT_PARITY_MAXREPOS caps the subset (default 60 — enough real
// polyglot content to be multi-shard and meaningful, but fast and disk-cheap; NOT a
// multi-GB full-corpus copy).
//
// Run it with:
//
//	MOEDEX_COMPACT_PARITY_CORPUS=$HOME/.moedex-managed \
//	  go test -count=1 -timeout 0 -run TestCompactionParityCorpus ./internal/blobstore/
//
// (MOEDEX_COMPACT_PARITY_MAXREPOS=N overrides the 60-repo cap; 0 = every repo — only
// for a deliberate full-corpus run, which copies the whole corpus to temp.)

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/ingest"
	"moedex/internal/parity"
	server "moedex/internal/serve"
)

// compactCorpusRoot resolves the SOURCE corpus to copy from, or "" to skip.
func compactCorpusRoot(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("MOEDEX_COMPACT_PARITY_CORPUS"); r != "" {
		return r
	}
	return ""
}

// compactMaxRepos caps the copied subset (default 60; 0 = all).
func compactMaxRepos() int {
	if v := os.Getenv("MOEDEX_COMPACT_PARITY_MAXREPOS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 60
}

// TestCompactionParityCorpus is the corpus-scale compaction parity gate.
func TestCompactionParityCorpus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	src := compactCorpusRoot(t)
	if src == "" {
		t.Skip("corpus not present (set MOEDEX_COMPACT_PARITY_CORPUS to the source, e.g. $HOME/.moedex-managed)")
	}

	// --- Discover the SOURCE repos (read-only) and take a deterministic first-N. ------
	allRepos, err := ingest.DiscoverRepos(src)
	if err != nil {
		t.Fatalf("discover source repos: %v", err)
	}
	if len(allRepos) == 0 {
		t.Skip("source corpus has no git repos")
	}
	cap := compactMaxRepos()
	subset := allRepos
	if cap > 0 && cap < len(subset) {
		subset = subset[:cap]
	}

	// --- COPY the subset (including .git) into a fresh MUTABLE corpus. NEVER touch src.
	corpus := t.TempDir()
	var copied []string // mutable repo dirs, in the same deterministic order
	for _, repoDir := range subset {
		dst := filepath.Join(corpus, filepath.Base(repoDir))
		// Disambiguate any basename collisions (two repos with the same leaf dir name)
		// so the copy is faithful and complete.
		for i := 1; dirExists(dst); i++ {
			dst = filepath.Join(corpus, filepath.Base(repoDir)+"_"+strconv.Itoa(i))
		}
		if err := copyTree(dst, repoDir); err != nil {
			t.Fatalf("copy repo %s -> %s: %v", repoDir, dst, err)
		}
		copied = append(copied, dst)
	}
	if len(copied) < 2 {
		t.Skipf("need >=2 copied repos to remove one and churn another (got %d)", len(copied))
	}

	const shardBytes = 256 << 10 // small => multiple shards on the subset (exercise merge path)

	// --- Pick repos deterministically, and inject UNIQUE markers into the LIVE corpus
	// BEFORE the baseline build, so the removed repo's marker and the churn file's OLD
	// content are genuinely live in the baseline stores — and thus genuinely DEAD (a real
	// reclaim) once removed / churned away. churn the FIRST copied repo, remove the LAST.
	churnRepo := copied[0]
	removeRepo := copied[len(copied)-1]

	churnRel := pickChurnableFile(t, churnRepo)
	if churnRel == "" {
		t.Skip("could not find a churnable text file in the first copied repo")
	}
	churnOldNeedle := "MOEDEX_COMPACT_CHURN_OLD_" + filepath.Base(churnRel)
	churnNewNeedle := "MOEDEX_COMPACT_CHURN_NEW_" + filepath.Base(churnRel)
	removedNeedle := "MOEDEX_COMPACT_REMOVED_" + filepath.Base(removeRepo)

	// Inject the OLD churn marker into the churn file, and the removed-repo marker into a
	// file of the to-be-removed repo, and COMMIT — these are now baseline-live content.
	injectMarkerCommit(t, churnRepo, churnRel, "\n// "+churnOldNeedle+"\n")
	removeRel := pickChurnableFile(t, removeRepo)
	if removeRel == "" {
		t.Skip("could not find a markable file in the to-be-removed repo")
	}
	injectMarkerCommit(t, removeRepo, removeRel, "\n// "+removedNeedle+"\n")

	// --- BASELINE: CAS + deduped served dir over the mutable copy (markers now live). -
	casDir := filepath.Join(corpus, ".cas")
	if _, err := BuildCAS(corpus, casDir); err != nil {
		t.Fatalf("BuildCAS: %v", err)
	}
	dedupDir := filepath.Join(t.TempDir(), "deduped")
	if _, _, err := ExportDedupedShardDir(casDir, dedupDir, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir baseline: %v", err)
	}
	shardCount := len(globShards(t, dedupDir))
	t.Logf("CORPUS-SCALE: copied %d repos from %s, %d deduped shards", len(copied), src, shardCount)
	if shardCount < 2 {
		t.Logf("WARNING: only %d shard(s) — raise MOEDEX_COMPACT_PARITY_MAXREPOS for a real multi-shard run", shardCount)
	}

	// --- Create REAL dead content vs the baseline: (1) churn the churn file (its OLD
	// marker content -> dead blob, NEW marker content -> live), (2) rm -rf the last repo
	// (its blobs incl. removedNeedle -> dead).
	churnAbs := filepath.Join(churnRepo, churnRel)
	churnNew := churnNewNeedle
	cur, err := os.ReadFile(churnAbs)
	if err != nil {
		t.Fatalf("read churn file: %v", err)
	}
	// Rewrite the file REMOVING the OLD marker line and adding the NEW marker, so the
	// baseline (old-marker) blob goes genuinely DEAD and the OLD marker no longer
	// surfaces anywhere, while the NEW marker becomes live.
	body := strings.ReplaceAll(string(cur), "// "+churnOldNeedle+"\n", "")
	body += "\n" + churnSentinel + " " + churnNewNeedle + "\n"
	if err := os.WriteFile(churnAbs, []byte(body), 0o644); err != nil {
		t.Fatalf("write churn new: %v", err)
	}
	gitCommitAll(t, churnRepo, "churn: replace content")

	if err := os.RemoveAll(removeRepo); err != nil {
		t.Fatalf("rm removeRepo: %v", err)
	}

	// Refresh the CAS: must report EXACTLY the one removal.
	cm, err := LoadBlobManifest(filepath.Join(casDir, BlobManifestName))
	if err != nil {
		t.Fatal(err)
	}
	_, ds, err := RefreshCAS(cm, corpus, casDir)
	if err != nil {
		t.Fatalf("RefreshCAS: %v", err)
	}
	if len(ds.RemovedRepos) != 1 || ds.RemovedRepos[0] != removeRepo {
		t.Fatalf("RefreshCAS removed %v, want exactly [%s]", ds.RemovedRepos, removeRepo)
	}
	// Delta re-export so blobs.dat carries the dead content forward.
	if _, _, err := RefreshDedupedShardDir(casDir, dedupDir, shardBytes); err != nil {
		t.Fatalf("RefreshDedupedShardDir: %v", err)
	}

	// --- Capture PRE-compaction served match sets (deduped dir + CAS-export view). ----
	preDedup, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open deduped (pre): %v", err)
	}
	preCASExport := filepath.Join(t.TempDir(), "pre-cas-export")
	if _, _, err := ExportDedupedShardDir(casDir, preCASExport, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir (pre CAS view): %v", err)
	}
	preCAS, err := server.Open(preCASExport)
	if err != nil {
		t.Fatalf("server.Open CAS export (pre): %v", err)
	}

	casBefore := casStoredBytes(t, casDir)
	dedupBefore := storeContentBytes(t, filepath.Join(dedupDir, diskstore.ContentStoreName))

	// --- COMPACT BOTH STORES. ---------------------------------------------------------
	casSt, err := CompactCAS(casDir)
	if err != nil {
		t.Fatalf("CompactCAS: %v", err)
	}
	dedupSt, err := CompactDedupedShardDir(dedupDir)
	if err != nil {
		t.Fatalf("CompactDedupedShardDir: %v", err)
	}
	t.Logf("CAS compact: kept %d/%dB, reclaimed %d/%dB (%dB->%dB)",
		casSt.LiveBlobs, casSt.LiveBytes, casSt.DeadBlobs, casSt.DeadBytes, casSt.BeforeBytes, casSt.AfterBytes)
	t.Logf("deduped compact: kept %d/%dB, reclaimed %d/%dB (%dB->%dB), %d shards",
		dedupSt.LiveBlobs, dedupSt.LiveBytes, dedupSt.DeadBlobs, dedupSt.DeadBytes, dedupSt.BeforeBytes, dedupSt.AfterBytes, dedupSt.Shards)

	// The gate is meaningless if nothing was dead.
	if casSt.DeadBytes <= 0 {
		t.Fatalf("CAS compaction reclaimed no dead bytes (%d) — removal/churn did not create dead blobs", casSt.DeadBytes)
	}
	if dedupSt.DeadBytes <= 0 {
		t.Fatalf("deduped compaction reclaimed no dead bytes (%d)", dedupSt.DeadBytes)
	}
	if casSt.AfterBytes >= casBefore {
		t.Errorf("CAS not smaller: before %d, after %d", casBefore, casSt.AfterBytes)
	}
	if dedupSt.AfterBytes >= dedupBefore {
		t.Errorf("deduped not smaller: before %d, after %d", dedupBefore, dedupSt.AfterBytes)
	}

	// --- Open POST-compaction served dirs. --------------------------------------------
	postDedup, err := server.Open(dedupDir)
	if err != nil {
		t.Fatalf("server.Open deduped (post): %v", err)
	}
	defer postDedup.Close()
	postCASExport := filepath.Join(t.TempDir(), "post-cas-export")
	if _, _, err := ExportDedupedShardDir(casDir, postCASExport, shardBytes); err != nil {
		t.Fatalf("ExportDedupedShardDir (post CAS view): %v", err)
	}
	postCAS, err := server.Open(postCASExport)
	if err != nil {
		t.Fatalf("server.Open CAS export (post): %v", err)
	}
	defer postCAS.Close()

	// --- Fresh DIRECT parity build over the post-change mutable corpus (the oracle). --
	work := t.TempDir()
	const seed = 17
	built, err := parity.Build(parity.Config{
		Root:       corpus,
		WorkDir:    work,
		Seed:       seed,
		ShardBytes: shardBytes,
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("parity.Build (direct, post-change): %v", err)
	}
	if built.IngestedRepos == 0 {
		t.Skip("no repos ingested from copied corpus")
	}
	bat := parity.Generate(built.Pool, seed)
	if len(bat.Queries) == 0 {
		t.Fatal("empty battery")
	}
	directDir := filepath.Join(work, "shards")
	direct, err := server.Open(directDir)
	if err != nil {
		t.Fatalf("server.Open direct dir: %v", err)
	}
	defer direct.Close()

	// --- Per-query parity: postCAS==preCAS, postDedup==preDedup, post==direct, vs-rg. -
	rg := newRG(t, built.MirrorDir)
	var (
		mismatchCASPre, mismatchDedupPre         int
		mismatchCASDir, mismatchDedupDir         int
		underCAS, overCAS, underDedup, overDedup int
		checkedVsRG, rgSkipped                   int
	)
	for _, q := range bat.Queries {
		preCASLocs := corpusQuery(t, preCAS, q)
		postCASLocs := corpusQuery(t, postCAS, q)
		preDedupLocs := corpusQuery(t, preDedup, q)
		postDedupLocs := corpusQuery(t, postDedup, q)
		directLocs := corpusQuery(t, direct, q)

		if miss, extra := diffLocs(preCASLocs, postCASLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchCASPre++
			if mismatchCASPre <= 8 {
				t.Errorf("compacted CAS != pre-compaction CAS for %s: miss %d extra %d", q, len(miss), len(extra))
			}
		}
		if miss, extra := diffLocs(preDedupLocs, postDedupLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchDedupPre++
			if mismatchDedupPre <= 8 {
				t.Errorf("compacted deduped != pre-compaction deduped for %s: miss %d extra %d", q, len(miss), len(extra))
			}
		}
		dedupOK := true
		if miss, extra := diffLocs(directLocs, postCASLocs); len(miss) > 0 || len(extra) > 0 {
			mismatchCASDir++
			if mismatchCASDir <= 8 {
				t.Errorf("compacted CAS != direct for %s: miss %d extra %d", q, len(miss), len(extra))
			}
		}
		if miss, extra := diffLocs(directLocs, postDedupLocs); len(miss) > 0 || len(extra) > 0 {
			dedupOK = false
			mismatchDedupDir++
			if mismatchDedupDir <= 8 {
				t.Errorf("compacted deduped != direct for %s: miss %d extra %d", q, len(miss), len(extra))
			}
		}
		if !dedupOK {
			continue
		}
		want, ok, rgErr := rg.run(t, q, built.FT)
		if !ok {
			if rgErr {
				rgSkipped++
			}
			continue
		}
		checkedVsRG++
		if miss, extra := diffLocs(want, postCASLocs); len(miss) > 0 {
			underCAS++
		} else if len(extra) > 0 {
			overCAS++
		}
		if miss, extra := diffLocs(want, postDedupLocs); len(miss) > 0 {
			underDedup++
		} else if len(extra) > 0 {
			overDedup++
		}
	}

	t.Logf("corpus compaction parity: %d queries; CAS!=pre=%d dedup!=pre=%d CAS!=direct=%d dedup!=direct=%d; vs-rg checked=%d under(cas=%d dedup=%d) over(cas=%d dedup=%d) rg-skipped=%d",
		len(bat.Queries), mismatchCASPre, mismatchDedupPre, mismatchCASDir, mismatchDedupDir, checkedVsRG, underCAS, underDedup, overCAS, overDedup, rgSkipped)
	if mismatchCASPre > 0 || mismatchDedupPre > 0 {
		t.Fatalf("FAIL: compaction CHANGED the live match set (CAS=%d dedup=%d) — a live blob was dropped (SACRED) or content shifted", mismatchCASPre, mismatchDedupPre)
	}
	if mismatchCASDir > 0 || mismatchDedupDir > 0 {
		t.Fatalf("FAIL: compacted store diverged from a fresh direct build (CAS=%d dedup=%d)", mismatchCASDir, mismatchDedupDir)
	}
	if underCAS > 0 || underDedup > 0 {
		t.Fatalf("FAIL: compacted store UNDER-APPROXIMATED ripgrep (cas=%d dedup=%d) — SACRED parity violation", underCAS, underDedup)
	}
	if overCAS > 0 || overDedup > 0 {
		t.Fatalf("FAIL: compacted store OVER-APPROXIMATED ripgrep (cas=%d dedup=%d)", overCAS, overDedup)
	}
	preCAS.Close()
	preDedup.Close()

	// --- Targeted: dead content GONE, live content PRESENT, on the compacted deduped dir.
	mustNotFindLiteral(t, postDedup, removedNeedle)  // removed repo's unique content
	mustNotFindLiteral(t, postDedup, churnOldNeedle) // the churned-away old marker line
	// The churned-in NEW content MUST surface (a live blob added by the churn).
	if ms, _, err := postDedup.Literal(context.Background(), churnNew); err != nil {
		t.Fatalf("Literal(churnNew): %v", err)
	} else if len(ms) == 0 {
		t.Errorf("churned-in new content %q does not surface post-compaction", churnNew)
	}
	// A SURVIVOR repo's content MUST still surface: pick a middle copied repo (neither
	// churned nor removed) and assert a token from it is found.
	if len(copied) >= 3 {
		survivor := copied[len(copied)/2]
		if needle := firstIdentifierIn(t, survivor); needle != "" {
			if ms, _, err := postDedup.Literal(context.Background(), needle); err != nil {
				t.Fatalf("Literal(survivor): %v", err)
			} else if len(ms) == 0 {
				t.Errorf("survivor repo %s token %q does not surface post-compaction (live content dropped)", survivor, needle)
			}
		}
	}
}

// --- corpus-copy + churn helpers (self-contained; no rg-machinery duplication) ------

// dirExists reports whether p exists and is a directory.
func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// copyTree recursively copies src into dst (files, dirs, symlinks-as-targets-skipped),
// preserving regular file contents and mode bits — enough to faithfully reproduce a
// git repo (its .git included) into a mutable temp corpus. Uses `cp -a` when available
// (fast, preserves .git hardlinks/perms), falling back to a pure-Go walk.
func copyTree(dst, src string) error {
	if cp, err := exec.LookPath("cp"); err == nil {
		// cp -a SRC DST copies SRC as DST (DST must not exist). Reliable on macOS/Linux.
		if out, err := exec.Command(cp, "-a", src, dst).CombinedOutput(); err == nil {
			return nil
		} else {
			_ = out // fall through to the pure-Go copy on any cp failure
		}
	}
	return copyTreeGo(dst, src)
}

// copyTreeGo is the dependency-free recursive copy fallback.
func copyTreeGo(dst, src string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTreeGo(filepath.Join(dst, e.Name()), filepath.Join(src, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil // skip symlinks (git mirrors rarely use them; safe to omit)
	}
	if !fi.Mode().IsRegular() {
		return nil // skip devices/sockets/etc.
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, fi.Mode().Perm())
}

// globShards returns the sorted *.idx shard paths under dir (the server's signal).
func globShards(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// churnSentinel prefixes the NEW churn marker line so it is visually attributable.
const churnSentinel = "// MOEDEX_COMPACT_CHURN_NEW"

// pickChurnableFile returns the rel path of the first ingestable text file in repoDir
// (ingest order is stable/deterministic), or "" if the repo has no ingestable file.
func pickChurnableFile(t *testing.T, repoDir string) string {
	t.Helper()
	files, err := ingest.Repo(filepath.Base(repoDir), repoDir)
	if err != nil || len(files) == 0 {
		return ""
	}
	return files[0].RelPath
}

// injectMarkerCommit appends marker to repoDir/rel and commits it, making the marker
// genuinely live in any build over the corpus AFTER this call.
func injectMarkerCommit(t *testing.T, repoDir, rel, marker string) {
	t.Helper()
	abs := filepath.Join(repoDir, rel)
	cur, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read %s for marker inject: %v", abs, err)
	}
	if err := os.WriteFile(abs, append(cur, []byte(marker)...), 0o644); err != nil {
		t.Fatalf("inject marker into %s: %v", abs, err)
	}
	gitCommitAll(t, repoDir, "inject unique marker")
}

// firstIdentifierIn returns a reasonably-unique literal token from repoDir's first
// ingestable file — a contiguous run of >=6 identifier characters — to assert a survivor
// repo's content still surfaces. Returns "" if none found (the caller skips the check).
func firstIdentifierIn(t *testing.T, repoDir string) string {
	t.Helper()
	files, err := ingest.Repo(filepath.Base(repoDir), repoDir)
	if err != nil || len(files) == 0 {
		return ""
	}
	content := string(files[0].Content)
	run := make([]rune, 0, 32)
	flush := func() string {
		if len(run) >= 6 {
			return string(run)
		}
		return ""
	}
	for _, r := range content {
		isIdent := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
		if isIdent {
			run = append(run, r)
			continue
		}
		if tok := flush(); tok != "" {
			return tok
		}
		run = run[:0]
	}
	return flush()
}
