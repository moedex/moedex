// Package server turns a directory of prebuilt moedex shards into a warm,
// long-lived corpus that answers queries with zero cold-start.
//
// A Corpus mmaps every shard once (diskstore.LoadMmap) and holds the mappings
// for its lifetime, so the postings never enter the Go heap and queries pay only
// decode-on-touch. Because search.Match carries absolute/repo/relative paths,
// results from independent shards are globally meaningful and merge by simple
// concatenation — there is no cross-shard blob-ID space to reconcile. This is the
// retrieval spine the daemon and (later) the ranked MCP path compose against.
package server

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/search"
)

// shard pairs a loaded index with the mmap region that backs its postings. The
// region must stay open for as long as the index may be queried.
type shard struct {
	path   string
	ix     *index.Index
	closer io.Closer
}

// Corpus is a warm, read-only view over a set of mmap'd shards. It is safe for
// concurrent use by multiple goroutines: Open and Close mutate it, but the
// query methods only read immutable shard state.
//
// When the shard dir is a DEDUPED export (MOEDEX05 shards + a shared content store
// blobs.dat — see blobstore.ExportDedupedShardDir), Corpus also holds that shared
// content store mmap'd for its lifetime: the shards carry only content-hash
// references, and each blob's content is a zero-copy sub-slice of the one shared
// mapping. A blob whose repos span several shards is mapped ONCE, not once per
// shard, so the served corpus inherits the CAS's cross-shard dedup and content
// stays off the Go heap. A legacy inlined-content dir leaves content nil.
type Corpus struct {
	dir     string
	shards  []shard
	content *diskstore.ContentStore // non-nil only for a deduped (MOEDEX05) dir
}

// Open mmaps every "*.idx" shard under dir (in sorted filename order) and
// returns a queryable Corpus. On any load failure it closes whatever it had
// already mapped and returns the error, so a partial mapping is never leaked.
//
// It transparently handles both shard formats: a legacy inlined-content dir
// (MOEDEX03/04) loads via diskstore.LoadMmap, and a deduped dir (MOEDEX05 + the
// shared blobs.dat content store) loads each shard against the once-opened shared
// store. The format is detected per dir; the external API is unchanged.
func Open(dir string) (*Corpus, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		return nil, fmt.Errorf("server: glob shards: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("server: no *.idx shards under %s", dir)
	}
	sort.Strings(paths)

	c := &Corpus{dir: dir}
	cs, err := openSharedContent(dir, paths)
	if err != nil {
		return nil, err
	}
	c.content = cs
	for _, p := range paths {
		ix, closer, err := openShard(p, cs)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("server: load shard %s: %w", p, err)
		}
		c.shards = append(c.shards, shard{path: p, ix: ix, closer: closer})
	}
	return c, nil
}

// Close unmaps every shard and the shared content store (if any). After Close the
// Corpus must not be queried — querying mapped-out memory crashes. Close is
// idempotent.
//
// Close is NOT concurrency-safe against an in-flight query: it does not itself
// wait for queries already running against this Corpus to finish, so a caller
// that hot-swaps Corpus instances (see cmd/moedex-serve/reload.go's
// corpusSnapshot/rankSnapshot) MUST drain in-flight queries — e.g. via a
// refcounted WaitGroup — before calling Close on the retired instance.
func (c *Corpus) Close() error {
	var first error
	for i := range c.shards {
		if c.shards[i].closer != nil {
			if err := c.shards[i].closer.Close(); err != nil && first == nil {
				first = err
			}
			c.shards[i].closer = nil
		}
	}
	c.shards = nil
	if c.content != nil {
		if err := c.content.Close(); err != nil && first == nil {
			first = err
		}
		c.content = nil
	}
	return first
}

// NumShards reports how many shards are mapped.
func (c *Corpus) NumShards() int { return len(c.shards) }

// NumBlobs sums distinct blobs across all shards.
func (c *Corpus) NumBlobs() int {
	n := 0
	for i := range c.shards {
		n += c.shards[i].ix.NumBlobs()
	}
	return n
}

// Regex runs pattern against every shard and returns the merged, deterministically
// ordered matches. Shards are scanned concurrently; if more than one fails (e.g. a
// malformed pattern), the lowest-indexed shard's error is the one surfaced, not
// necessarily the first to fail in wall-clock time. Per-shard scans that fail
// abort the whole query. A cancelled/expired ctx aborts the per-shard scans
// promptly and surfaces ctx.Err().
func (c *Corpus) Regex(ctx context.Context, pattern string) ([]search.Match, search.Stats, error) {
	return c.fan(ctx, func(ctx context.Context, ix *index.Index) ([]search.Match, search.Stats, error) {
		m, s, err := search.RegexWithStats(ctx, ix, pattern)
		return m, s, err
	})
}

// Literal runs a literal query against every shard and returns merged matches. A
// cancelled/expired ctx aborts the per-shard scans promptly and surfaces
// ctx.Err().
func (c *Corpus) Literal(ctx context.Context, q string) ([]search.Match, search.Stats, error) {
	return c.fan(ctx, func(ctx context.Context, ix *index.Index) ([]search.Match, search.Stats, error) {
		m, s, err := search.LiteralWithStats(ctx, ix, q)
		return m, s, err
	})
}

// fan runs one per-shard query across all shards concurrently (bounded by
// NumCPU), then merges and orders the results. The per-shard verify stage is
// already globally throttled by search's package-level permit pool, so fanning
// shards in parallel cannot oversubscribe the CPU.
func (c *Corpus) fan(ctx context.Context, query func(context.Context, *index.Index) ([]search.Match, search.Stats, error)) ([]search.Match, search.Stats, error) {
	n := len(c.shards)
	perShard := make([][]search.Match, n)
	stats := make([]search.Stats, n)
	errs := make([]error, n)

	limit := runtime.NumCPU()
	if limit < 1 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range c.shards {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			perShard[i], stats[i], errs[i] = query(ctx, c.shards[i].ix)
		}(i)
	}
	wg.Wait()

	// Aggregate the attribution fields we expose. Stats.add is unexported, so we
	// sum the relevant fields directly; QueryAll is true only if every shard
	// fell back to scanning all candidates.
	var agg search.Stats
	agg.QueryAll = n > 0
	var total int
	for i := range errs {
		if errs[i] != nil {
			return nil, search.Stats{}, fmt.Errorf("server: shard %s: %w", c.shards[i].path, errs[i])
		}
		agg.CandidateBlobs += stats[i].CandidateBlobs
		agg.CandidateBytes += stats[i].CandidateBytes
		agg.CandidateLines += stats[i].CandidateLines
		agg.LinesAfterFilter += stats[i].LinesAfterFilter
		agg.LinesRE2 += stats[i].LinesRE2
		agg.QueryAll = agg.QueryAll && stats[i].QueryAll
		total += len(perShard[i])
	}

	merged := make([]search.Match, 0, total)
	for _, m := range perShard {
		merged = append(merged, m...)
	}
	sortMatches(merged)
	return merged, agg, nil
}

// sortMatches imposes a stable, repo/path/line order so results are deterministic
// regardless of shard layout or goroutine scheduling.
func sortMatches(m []search.Match) {
	sort.Slice(m, func(i, j int) bool {
		if m[i].Repo != m[j].Repo {
			return m[i].Repo < m[j].Repo
		}
		if m[i].RelPath != m[j].RelPath {
			return m[i].RelPath < m[j].RelPath
		}
		if m[i].Line != m[j].Line {
			return m[i].Line < m[j].Line
		}
		return m[i].AbsPath < m[j].AbsPath
	})
}
