// Package parity is moedex's full-corpus exact-match retrieval parity harness.
//
// It indexes an entire corpus of git repos (sharded, to stay under the in-RAM
// posting wall), generates a large seed-reproducible query battery, and checks
// moedex's match set against ground truth. ripgrep is the immovable ground-truth
// oracle; an independent in-process Go-regexp/literal scan ("gold") adjudicates
// any moedex-vs-ripgrep divergence (distinguishing a real moedex bug from a
// documented RE2-vs-Rust-regex semantics quirk); Zoekt is an optional
// competitive differential. Matches are compared at (file, line) granularity —
// see the package README / PARITY-REPORT for why byte-spans are not used.
//
// The corpus is treated as strictly read-only: files are read, never written.
// All harness scratch (per-shard index files, the rg content mirror) lives under
// a caller-provided work directory.
package parity

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/ingest"
)

// FileTable is the indexed file set F: the single source of truth for scope.
// Every file moedex selected gets a stable integer fileID (its position here);
// ripgrep, gold, and Zoekt all report into this same space so set comparison is
// unambiguous. AbsPath is the canonical identity (globally unique).
type FileTable struct {
	abs   []string       // fileID -> absolute path
	repo  []string       // fileID -> repo label
	rel   []string       // fileID -> repo-relative path
	byAbs map[string]int // absolute path -> fileID (dedup)
}

func newFileTable() *FileTable { return &FileTable{byAbs: map[string]int{}} }

// add registers an absolute path, returning its fileID and whether it was new.
func (ft *FileTable) add(repo, rel, abs string) (int, bool) {
	if id, ok := ft.byAbs[abs]; ok {
		return id, false
	}
	id := len(ft.abs)
	ft.abs = append(ft.abs, abs)
	ft.repo = append(ft.repo, repo)
	ft.rel = append(ft.rel, rel)
	ft.byAbs[abs] = id
	return id, true
}

// Len is |F|.
func (ft *FileTable) Len() int { return len(ft.abs) }

// IDOf returns the fileID for an absolute path, or -1 if absent.
func (ft *FileTable) IDOf(abs string) int {
	if id, ok := ft.byAbs[abs]; ok {
		return id
	}
	return -1
}

// Abs/Repo/Rel return per-fileID metadata.
func (ft *FileTable) Abs(id int) string  { return ft.abs[id] }
func (ft *FileTable) Repo(id int) string { return ft.repo[id] }
func (ft *FileTable) Rel(id int) string  { return ft.rel[id] }

// SkippedRepo records a repo that could not be ingested, with the reason.
type SkippedRepo struct {
	Dir    string
	Reason string
}

// Built is the result of a full sharded index build over a corpus.
type Built struct {
	Root          string
	Repos         []string // every discovered repo dir
	IngestedRepos int
	Skipped       []SkippedRepo
	GitEntryCount int // `find -name .git | wc -l` ground truth

	Shards    []string // on-disk shard index paths
	MirrorDir string   // dir of materialized F content (rg scope)
	FT        *FileTable

	Pool         *TermPool
	NumFiles     int // |F|
	ContentBytes int64

	BuildWall    time.Duration
	BuildPeakRSS int64 // bytes (process high-water mark right after build)
}

// Config controls a parity run's build.
type Config struct {
	Root       string // corpus root
	WorkDir    string // scratch root (shards + mirror live here)
	Seed       int64
	ShardBytes int64 // target indexed-content bytes per shard (build RAM control)
	MaxRepos   int   // 0 = all; >0 caps repo count (small-scale tests)
	Verbose    bool
	// Selector, when non-nil, routes every shard through the opt-in selective
	// (FREE-style) builder (index.NewSelective/AddFile/Finalize) instead of the
	// eager all-trigram index.New()+AddFile path. The retrieval/adjudication path
	// is unchanged: a deselected gram forces a full scan via IndexedGram, so the
	// gold oracle still proves moedex ⊆ gold (no under-approximation). When nil,
	// the build is byte-identical to the default all-trigram parity build.
	Selector index.GramSelector
	Logf     func(string, ...any)
}

func (c *Config) logf(format string, a ...any) {
	if c.Logf != nil {
		c.Logf(format, a...)
	}
}

// DefaultShardBytes keeps a single shard's in-RAM posting map well under the
// 16GB wall: ~150MB of indexed content builds with a measured peak RSS of
// ~6.5GB (≈40× content, the builder's posting-map cost), leaving generous
// headroom on a 16GB/no-swap machine. Larger shards mean fewer files but a
// higher peak; this value is the validated safe default.
const DefaultShardBytes = 150 << 20

// Build discovers every repo under cfg.Root, ingests them into a sequence of
// on-disk shards (building one shard in RAM at a time, then freeing it), and
// along the way materializes each selected file's indexed content into a flat
// mirror (the exact byte scope ripgrep searches) and samples corpus terms for
// the battery. Repos that fail to ingest are recorded, not fatal.
func Build(cfg Config) (*Built, error) {
	if cfg.ShardBytes <= 0 {
		cfg.ShardBytes = DefaultShardBytes
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}
	mirrorDir := filepath.Join(cfg.WorkDir, "mirror")
	shardDir := filepath.Join(cfg.WorkDir, "shards")
	for _, d := range []string{mirrorDir, shardDir} {
		if err := os.RemoveAll(d); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	gitCount, err := ingest.CountGitEntries(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("count .git: %w", err)
	}
	repos, err := ingest.DiscoverRepos(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("discover repos: %w", err)
	}
	if cfg.MaxRepos > 0 && cfg.MaxRepos < len(repos) {
		repos = repos[:cfg.MaxRepos]
	}
	cfg.logf("discovered %d repos (.git entries: %d)", len(repos), gitCount)

	b := &Built{
		Root:          cfg.Root,
		Repos:         repos,
		GitEntryCount: gitCount,
		MirrorDir:     mirrorDir,
		FT:            newFileTable(),
		Pool:          newTermPool(cfg.Seed),
	}

	if cfg.Selector != nil {
		cfg.logf("selective index enabled: %s", cfg.Selector.Describe())
	}

	start := time.Now()
	// mb accumulates repo->shard membership + per-repo git HEAD so a freshness
	// sidecar manifest can be written after the build (see manifest.go).
	mb := newManifestBuilder(cfg.Root, shardDir)
	var (
		sb         = index.NewBuildTarget(cfg.Selector)
		shardBytes int64
		shardIdx   int
		anyInShard bool
		flushShard func() error
	)
	flushShard = func() error {
		if !anyInShard {
			return nil
		}
		// Finalize materializes the per-shard *Index: the eager path returns the
		// index it has been writing into; the selective path runs the second pass
		// (apply selector, emit kept-gram postings). diskstore.Save then writes a
		// MOEDEX04 shard whenever the index is selective (SelectedGrams != nil) and
		// a plain MOEDEX03 shard otherwise — LoadMmap reconstructs IndexedGram from
		// the persisted keep-set, so the scan phase is selective-aware end to end.
		ix := sb.Finalize()
		path := filepath.Join(shardDir, fmt.Sprintf("shard-%04d.idx", shardIdx))
		if err := diskstore.Save(ix, path); err != nil {
			return fmt.Errorf("save shard %d: %w", shardIdx, err)
		}
		b.Shards = append(b.Shards, path)
		mb.flushed()
		cfg.logf("  flushed shard %d: %d blobs, %.1f MB content", shardIdx, ix.NumBlobs(), float64(shardBytes)/1e6)
		shardIdx++
		sb = index.NewBuildTarget(cfg.Selector)
		shardBytes = 0
		anyInShard = false
		runtime.GC() // release the builder's posting map before the next shard
		return nil
	}

	for _, repo := range repos {
		files, err := ingest.Repo(filepath.Base(repo), repo)
		if err != nil {
			b.Skipped = append(b.Skipped, SkippedRepo{Dir: repo, Reason: err.Error()})
			continue
		}
		b.IngestedRepos++
		head, _ := ingest.Head(repo) // "" if unreadable; recorded as-is
		mb.recordHead(repo, filepath.Base(repo), head)
		for _, f := range files {
			id, isNew := b.FT.add(f.Repo, f.RelPath, f.AbsPath)
			if !isNew {
				continue // same abspath already indexed (nested-repo overlap)
			}
			if err := writeMirror(mirrorDir, id, f.Content); err != nil {
				return nil, fmt.Errorf("mirror %s: %w", f.AbsPath, err)
			}
			sb.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
			b.Pool.observe(f.Content)
			b.ContentBytes += int64(len(f.Content))
			shardBytes += int64(len(f.Content))
			mb.noteBlob(repo, int64(len(f.Content)))
			anyInShard = true
		}
		if shardBytes >= cfg.ShardBytes {
			if err := flushShard(); err != nil {
				return nil, err
			}
		}
	}
	if err := flushShard(); err != nil {
		return nil, err
	}

	// Emit the freshness manifest alongside the shards. Non-fatal on write
	// error: the index itself is already persisted and usable.
	manifest := mb.finalize(b.Shards, time.Now())
	if err := WriteManifest(filepath.Join(shardDir, ManifestName), manifest); err != nil {
		cfg.logf("WARNING: failed to write freshness manifest: %v", err)
	}

	b.NumFiles = b.FT.Len()
	b.BuildWall = time.Since(start)
	b.BuildPeakRSS = maxRSS()
	cfg.logf("build done: |F|=%d, %.1f MB content, %d shards, %s, peakRSS %.1f MB",
		b.NumFiles, float64(b.ContentBytes)/1e6, len(b.Shards), b.BuildWall.Round(time.Millisecond),
		float64(b.BuildPeakRSS)/1e6)
	return b, nil
}

// writeMirror materializes one file's indexed content under mirrorDir as a flat,
// integer-named file (bucketed 1000-per-dir to keep directories reasonable). The
// content is exactly what moedex indexed (BOM already stripped by ingest), so
// ripgrep scanning the mirror searches byte-identical bytes over exactly F.
func writeMirror(mirrorDir string, id int, content []byte) error {
	bucket := filepath.Join(mirrorDir, fmt.Sprintf("%03d", id/1000))
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bucket, fmt.Sprintf("%d", id)), content, 0o644)
}

// mirrorPathToID maps a mirror path (as ripgrep reports it) back to a fileID by
// parsing the trailing integer basename. Returns -1 if it doesn't parse.
func mirrorPathToID(p string) int {
	base := filepath.Base(p)
	id := 0
	if len(base) == 0 {
		return -1
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		if c < '0' || c > '9' {
			return -1
		}
		id = id*10 + int(c-'0')
	}
	return id
}

// maxRSS returns the process's peak resident set size in bytes. On darwin
// ru_maxrss is already in bytes; on linux it is kilobytes.
func maxRSS() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "linux" {
		return int64(ru.Maxrss) * 1024
	}
	return int64(ru.Maxrss)
}

// --- term sampling for the battery ----------------------------------------

// TermPool holds corpus-derived material the battery is built from: identifier
// tokens with approximate frequencies, multi-word phrases, literals containing
// regex metacharacters, and literals containing multibyte (non-ASCII) runes.
// Sampling is deterministic given the seed and the (fixed, read-only) corpus.
type TermPool struct {
	rng       *rand.Rand
	tokenFreq map[string]int
	phrases   []string
	metas     []string
	unicodes  []string

	// phrasesSeen/metasSeen/unicodesSeen are the running counts of candidates
	// offered to the corresponding reservoir, needed by reservoirAdd's
	// Algorithm R (the count is unrecoverable from the slice once it's capped
	// at its max size).
	phrasesSeen, metasSeen, unicodesSeen int
}

const (
	maxDistinctTokens = 300_000
	sampleBytesPerDoc = 128 << 10 // cap per-file scan so giant files don't dominate
	reservoirPhrases  = 4000
	reservoirMetas    = 4000
	reservoirUnicode  = 2000
)

func newTermPool(seed int64) *TermPool {
	return &TermPool{
		rng:       rand.New(rand.NewSource(seed)),
		tokenFreq: make(map[string]int, 1<<16),
	}
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
func isAlphaByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// observe samples tokens/phrases/metas/unicode literals from one file's content.
func (p *TermPool) observe(content []byte) {
	c := content
	if len(c) > sampleBytesPerDoc {
		c = c[:sampleBytesPerDoc]
	}
	// Identifier tokens (ASCII word runs starting with a letter/underscore).
	for i := 0; i < len(c); {
		if !isAlphaByte(c[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(c) && isWordByte(c[j]) {
			j++
		}
		tok := string(c[i:j])
		if n := len(tok); n >= 3 && n <= 40 {
			if _, ok := p.tokenFreq[tok]; ok || len(p.tokenFreq) < maxDistinctTokens {
				p.tokenFreq[tok]++
			}
		}
		i = j
	}
	p.observeLines(c)
}

func (p *TermPool) observeLines(c []byte) {
	start := 0
	for i := 0; i <= len(c); i++ {
		if i == len(c) || c[i] == '\n' {
			line := c[start:i]
			start = i + 1
			p.observePhrase(line)
			p.observeMeta(line)
			p.observeUnicode(line)
		}
	}
}

// observePhrase reservoir-samples a two-word phrase (with the original
// separator preserved) from a line, so phrase literals match real content.
func (p *TermPool) observePhrase(line []byte) {
	// Find first word, the separator run, then the second word.
	i := 0
	for i < len(line) && !isAlphaByte(line[i]) {
		i++
	}
	w1 := i
	for i < len(line) && isWordByte(line[i]) {
		i++
	}
	if i == w1 {
		return
	}
	sep := i
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == sep || i >= len(line) || !isAlphaByte(line[i]) {
		return
	}
	for i < len(line) && isWordByte(line[i]) {
		i++
	}
	phrase := string(line[w1:i])
	if len(phrase) >= 6 && len(phrase) <= 50 {
		p.phrases = reservoirAdd(p.rng, p.phrases, p.phrasesSeen, phrase, reservoirPhrases)
		p.phrasesSeen++
	}
}

// regexMeta is the set of bytes that are regex-special; a literal containing one
// must be matched literally (battery bucket d).
func isMetaByte(c byte) bool {
	switch c {
	case '.', '(', ')', '[', ']', '{', '}', '*', '+', '?', '|', '^', '$', '\\':
		return true
	}
	return false
}

// observeMeta reservoir-samples a short printable-ASCII run containing a regex
// metacharacter, to be searched as a fixed string.
func (p *TermPool) observeMeta(line []byte) {
	for i := 0; i < len(line); i++ {
		if !isMetaByte(line[i]) {
			continue
		}
		// Grow a window of printable, non-space ASCII around i.
		lo := i
		for lo > 0 && isLiteralByte(line[lo-1]) {
			lo--
		}
		hi := i + 1
		for hi < len(line) && isLiteralByte(line[hi]) {
			hi++
		}
		w := line[lo:hi]
		if len(w) >= 3 && len(w) <= 24 {
			p.metas = reservoirAdd(p.rng, p.metas, p.metasSeen, string(w), reservoirMetas)
			p.metasSeen++
		}
		i = hi
	}
}

func isLiteralByte(c byte) bool {
	return c > ' ' && c < 0x7f // printable, non-space ASCII (includes metachars)
}

// observeUnicode reservoir-samples a short literal containing a multibyte rune.
func (p *TermPool) observeUnicode(line []byte) {
	for i := 0; i < len(line); i++ {
		if line[i] < 0x80 {
			continue
		}
		lo := i
		for lo > 0 && (line[lo-1] >= 0x80 || isWordByte(line[lo-1])) {
			lo--
		}
		hi := i
		for hi < len(line) && (line[hi] >= 0x80 || isWordByte(line[hi])) {
			hi++
		}
		w := line[lo:hi]
		if n := len(w); n >= 2 && n <= 30 && hasMultibyte(w) {
			p.unicodes = reservoirAdd(p.rng, p.unicodes, p.unicodesSeen, string(w), reservoirUnicode)
			p.unicodesSeen++
		}
		if hi > i {
			i = hi
		}
	}
}

func hasMultibyte(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

// reservoirAdd implements Algorithm R uniform reservoir sampling: res keeps at
// most max items, and seen is the number of items already observed for this
// reservoir before this call (0 for the first item). After n calls, every
// observed item has had an equal max/n probability of surviving in the final
// reservoir, deterministically given the rng.
func reservoirAdd(rng *rand.Rand, res []string, seen int, item string, max int) []string {
	if seen < max {
		return append(res, item)
	}
	if j := rng.Intn(seen + 1); j < max {
		res[j] = item
	}
	return res
}

// sortedTokens returns tokens sorted by ascending frequency then name, for
// deterministic banding in the battery generator.
func (p *TermPool) sortedTokens() []string {
	toks := make([]string, 0, len(p.tokenFreq))
	for t := range p.tokenFreq {
		toks = append(toks, t)
	}
	sort.Slice(toks, func(i, j int) bool {
		fi, fj := p.tokenFreq[toks[i]], p.tokenFreq[toks[j]]
		if fi != fj {
			return fi < fj
		}
		return toks[i] < toks[j]
	})
	return toks
}
