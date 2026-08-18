package server

// symbolcorpus.go is the corpus-wide SYMBOL spine: it builds one symbol index per
// shard and merges them into a single cross-shard byName lookup
// (symbol.Corpus), then resolves the resulting shard-qualified refs back to
// repo/path/line.
//
// Why per-shard-then-merge rather than one index over a concatenated corpus (the
// shape RankCorpus/loadUnified uses for ranking)? Because the question this
// serves is per-REPO attribution — "every repo that defines or references
// AccountBillingContactChanged". A concatenated index answers name lookups but
// flattens shard identity into a global blob ID, so recovering which shard (and
// therefore which subset of the corpus) a hit came from means re-deriving shard
// boundaries. Keeping each shard's index intact and merging only the name
// dimension preserves that attribution, and lets a shard be rebuilt or dropped
// without touching its neighbours.
//
// Content lifetime mirrors Corpus/RankCorpus: for a deduped dir every shard's
// blob content is a zero-copy sub-slice of the one shared content-store mmap, so
// that store stays open for the SymbolCorpus' lifetime and Close releases it. A
// legacy inlined-content dir heap-copies content instead (the same profile
// RankCorpus has there), because resolving a hit to its repo/path/line needs the
// blobs' file refs and line starts to stay live.

import (
	"fmt"
	"path/filepath"
	"sort"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

// SymbolSite is one cross-shard symbol occurrence resolved to a human-meaningful
// location: which shard and repo it lives in, the file and 1-based line, and the
// byte range of the name token itself.
//
// A blob's content may appear under several paths (content dedup), in which case
// Repo/RelPath/AbsPath name the FIRST file ref recorded for it; Repos lists every
// repo the content appears in, so a vendored definition reports all its homes.
type SymbolSite struct {
	Name    string
	Shard   string // shard file basename, e.g. "003.idx"
	Repo    string
	RelPath string
	AbsPath string
	SHA     string   // git blob SHA — the content identity across repos
	Repos   []string // every repo this blob's content appears in (sorted, deduped)
	Line    int      // 1-based line of Start
	Start   int      // byte offset of the name token
	End     int      // byte offset just past the name token
	Role    symbol.Role
}

// SymbolCorpus is the corpus-wide symbol lookup over a directory of prebuilt
// shards. It is read-only after Open and safe for concurrent lookups; Close
// releases the shared content mapping and must not race with in-flight lookups.
type SymbolCorpus struct {
	corpus  *symbol.Corpus
	idxs    []*index.Index          // per-shard content indices, by shard ID
	content *diskstore.ContentStore // non-nil only for a deduped (MOEDEX05) dir
}

// globShards globs the "*.idx" shards under dir in sorted filename order — the
// one place the served shard set is enumerated, so every spine (retrieval,
// ranking, symbols) agrees on both the membership and the order that assigns
// shard IDs.
func globShards(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.idx"))
	if err != nil {
		return nil, fmt.Errorf("server: glob shards: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("server: no *.idx shards under %s", dir)
	}
	sort.Strings(paths)
	return paths, nil
}

// OpenSymbols builds the cross-shard symbol lookup for the shard dir: it loads
// each shard's blob CONTENT (no postings — symbol extraction never queries
// trigrams), extracts that shard's symbols with symbol.BuildMulti, and merges
// every per-shard index into one corpus-wide byName lookup. Shard IDs are the
// positions of the sorted "*.idx" set, so they match the order Open and OpenRank
// use.
//
// A shard that yields no symbols still holds its ID, so IDs stay aligned with the
// shard set regardless of content.
func OpenSymbols(dir string) (*SymbolCorpus, error) {
	paths, err := globShards(dir)
	if err != nil {
		return nil, err
	}
	cs, err := openSharedContent(dir, paths)
	if err != nil {
		return nil, err
	}
	// A deduped dir's blob content aliases cs's mmap and the returned SymbolCorpus
	// retains those indices, so cs must outlive it; release it on any error path
	// before ownership is handed over.
	ok := false
	defer func() {
		if !ok && cs != nil {
			cs.Close()
		}
	}()

	sc := &SymbolCorpus{corpus: symbol.NewCorpus(), content: cs}
	for _, p := range paths {
		var (
			bs  []index.BlobData
			err error
		)
		if cs != nil && diskstore.IsDeduped(p) {
			bs, err = diskstore.LoadBlobsDeduped(p, cs)
		} else {
			bs, err = diskstore.LoadBlobs(p)
		}
		if err != nil {
			return nil, fmt.Errorf("server: load blobs %s: %w", p, err)
		}
		ix := index.Restore(bs, nil)
		id := sc.corpus.AddShard(filepath.Base(p), symbol.BuildMulti(ix))
		// AddShard hands out dense IDs in call order, so appending keeps idxs
		// indexable by shard ID.
		if id != len(sc.idxs) {
			return nil, fmt.Errorf("server: shard %s got ID %d, expected %d", p, id, len(sc.idxs))
		}
		sc.idxs = append(sc.idxs, ix)
	}

	ok = true // hand cs ownership to the SymbolCorpus
	return sc, nil
}

// Close releases the shared content-store mmap backing the shards' blob content
// (a no-op for a legacy inlined-content dir). After Close the SymbolCorpus must
// not be used: SymbolSite resolution reads blob content. Close is idempotent.
func (sc *SymbolCorpus) Close() error {
	if sc.content != nil {
		err := sc.content.Close()
		sc.content = nil
		return err
	}
	return nil
}

// Merged exposes the merged cross-shard lookup for callers that want raw
// shard-qualified refs (no path resolution) or the name-enumeration seam. It is
// named Merged, not Corpus, to keep it distinct from the retrieval-spine Corpus
// type in this package.
func (sc *SymbolCorpus) Merged() *symbol.Corpus { return sc.corpus }

// NumShards reports how many shards were merged.
func (sc *SymbolCorpus) NumShards() int { return sc.corpus.NumShards() }

// NumShardBlobs reports how many blobs shard (by ID) holds, for a caller that
// needs to enumerate every blob across every shard (e.g. building an
// online-serving catalog) without reaching into SymbolCorpus's private
// per-shard index storage. 0 for an out-of-range shard.
func (sc *SymbolCorpus) NumShardBlobs(shard int) int {
	if shard < 0 || shard >= len(sc.idxs) {
		return 0
	}
	return sc.idxs[shard].NumBlobs()
}

// ShardBlob returns blob id's record within shard, or nil when the shard or
// blob id is unknown.
func (sc *SymbolCorpus) ShardBlob(shard int, id uint64) *index.Blob {
	if shard < 0 || shard >= len(sc.idxs) {
		return nil
	}
	return sc.idxs[shard].Blob(id)
}

// NumNames reports how many distinct names the corpus-wide lookup covers.
func (sc *SymbolCorpus) NumNames() int { return sc.corpus.NumNames() }

// References returns every occurrence of name across the whole shard set —
// definitions and references — resolved to repo/path/line and ordered by shard,
// blob, then byte offset. nil when no shard knows the name.
func (sc *SymbolCorpus) References(name string) []SymbolSite {
	return sc.sites(name, sc.corpus.References(name))
}

// Definitions returns only the definition occurrences of name across the whole
// shard set, resolved and ordered the same way, or nil when nothing defines it.
func (sc *SymbolCorpus) Definitions(name string) []SymbolSite {
	return sc.sites(name, sc.corpus.Definitions(name))
}

// sites resolves shard-qualified refs to located sites, dropping any ref whose
// blob cannot be resolved (a shard/blob mismatch would be a build bug, and a
// silently wrong path is worse than an omitted one).
func (sc *SymbolCorpus) sites(name string, refs []symbol.ShardRef) []SymbolSite {
	if len(refs) == 0 {
		return nil
	}
	out := make([]SymbolSite, 0, len(refs))
	for _, r := range refs {
		if s, ok := sc.Locate(name, r); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Locate resolves one shard-qualified ref to its repo/path/line, carrying name
// through onto the site (a ShardRef records position and role, not the name it
// was looked up under). ok=false when the shard or blob is unknown.
func (sc *SymbolCorpus) Locate(name string, r symbol.ShardRef) (SymbolSite, bool) {
	if r.Shard < 0 || r.Shard >= len(sc.idxs) {
		return SymbolSite{}, false
	}
	blob := sc.idxs[r.Shard].Blob(r.Blob)
	if blob == nil {
		return SymbolSite{}, false
	}
	site := SymbolSite{
		Name:  name,
		Shard: sc.corpus.ShardName(r.Shard),
		SHA:   blob.SHA,
		Repos: blobRepos(blob),
		Line:  blob.LineOf(r.Start),
		Start: r.Start,
		End:   r.End,
		Role:  r.Role,
	}
	if len(blob.Files) > 0 {
		site.Repo = blob.Files[0].Repo
		site.RelPath = blob.Files[0].RelPath
		site.AbsPath = blob.Files[0].AbsPath
	}
	return site, true
}

// DefiningRepos returns the sorted, deduped repos that DEFINE name anywhere in
// the corpus — the cross-shard question the merge exists to answer. Content
// dedup is honored: a definition in shared content reports every repo that
// carries that content.
func (sc *SymbolCorpus) DefiningRepos(name string) []string {
	return sc.repos(sc.corpus.Definitions(name))
}

// ReferencingRepos returns the sorted, deduped repos that REFERENCE name (a
// non-declaring use) anywhere in the corpus. A repo that both defines and
// references name appears in this list and in DefiningRepos.
func (sc *SymbolCorpus) ReferencingRepos(name string) []string {
	var refs []symbol.ShardRef
	for _, r := range sc.corpus.References(name) {
		if r.Role == symbol.Reference {
			refs = append(refs, r)
		}
	}
	return sc.repos(refs)
}

// repos collects the sorted, deduped repo set behind a list of refs.
func (sc *SymbolCorpus) repos(refs []symbol.ShardRef) []string {
	seen := map[string]bool{}
	for _, r := range refs {
		if r.Shard < 0 || r.Shard >= len(sc.idxs) {
			continue
		}
		blob := sc.idxs[r.Shard].Blob(r.Blob)
		if blob == nil {
			continue
		}
		for _, f := range blob.Files {
			if f.Repo != "" {
				seen[f.Repo] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// blobRepos lists the sorted, deduped repos a blob's content appears in.
func blobRepos(blob *index.Blob) []string {
	if len(blob.Files) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, f := range blob.Files {
		if f.Repo != "" {
			seen[f.Repo] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
