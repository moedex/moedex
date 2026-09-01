package symbol

import "sort"

// Cross-shard name resolution.
//
// A single *Index is SHARD-LOCAL: its blob IDs are positions within one shard's
// index, and its byName view answers "who defines/references this name" only for
// the blobs of that shard. The served corpus is many shards (see
// internal/graph/serve), so answering the corpus-wide question — "every shard that
// defines or references AccountBillingContactChanged" — needs a merge across
// per-shard indices and a blob reference qualified by the shard it came from.
//
// Corpus is that merge. Two properties drive its shape:
//
//   - MEMORY. The obvious merge (copy every per-shard Ref into one corpus-wide
//     map) costs one map entry per NAME plus one Ref per OCCURRENCE, duplicating
//     the per-shard byName views that already hold those Refs. At corpus scale
//     that is the larger of the two costs by orders of magnitude. So the merged
//     map holds only a name -> [shard IDs] posting list (deduped, ascending) and
//     resolves the actual occurrences on touch from the owning shard's byName.
//     Map keys reuse the shards' existing name strings, so the merge adds no
//     string bytes either. This mirrors how postings stay on disk and decode
//     on touch rather than being copied into one heap-resident structure.
//   - ATTRIBUTION. A ShardRef carries the shard ID, so a caller can recover the
//     repo/path of every hit (the shard's own index knows its blobs' FileRefs)
//     and can answer "which shards" without materializing every occurrence.
//
// Content dedup interacts with this deliberately: identical content indexed in
// two shards yields a definition in BOTH, because the question a cross-shard
// lookup answers is "which shards contain this" — vendored/shared code
// legitimately resolves to every shard that carries it. Callers wanting the
// content-identity view (one node, many consumers) dedup by the blobs' SHA.
//
// A Corpus takes a READ-ONLY view of the indices handed to AddShard: it reads
// their byName views at merge time, so a shard mutated (Set/SetRefs) after being
// added can introduce names the merged posting list does not know about. Build
// each shard's Index fully, then merge.

// Shard names one shard's symbol index for Merge. Name is an opaque shard
// identity (the caller's shard file basename, typically) surfaced back through
// (*Corpus).ShardName for diagnostics and result attribution.
type Shard struct {
	Name  string
	Index *Index
}

// ShardRef is a Ref qualified by the shard that holds it — the unit a
// cross-shard lookup returns. Blob is an ID within THAT shard's index (use
// (*Corpus).ShardIndex to resolve it), and Start/End are byte offsets into that
// blob's content.
type ShardRef struct {
	Shard int // shard ID, as returned by AddShard / index into Merge's input
	Blob  uint64
	Start int
	End   int
	Role  Role
}

// Corpus is a corpus-wide byName lookup merged from per-shard symbol indices.
// It is read-only once built and safe for concurrent lookups.
type Corpus struct {
	shards []shardEntry
	// byName maps a name to the ascending, deduped IDs of the shards whose index
	// holds at least one occurrence (definition or reference) of it. The
	// occurrences themselves stay in the per-shard indices.
	byName map[string][]int32
}

// shardEntry is one merged shard: its opaque name plus the index that owns its
// blobs, symbols, and occurrences.
type shardEntry struct {
	name string
	ix   *Index
}

// NewCorpus returns an empty cross-shard lookup.
func NewCorpus() *Corpus {
	return &Corpus{byName: map[string][]int32{}}
}

// Merge builds a Corpus from shards, in order: shard i in the input gets shard
// ID i, so a caller holding a sorted shard list can map a ShardRef back to its
// own slice by index. A Shard with a nil Index still consumes an ID, keeping
// those positions aligned even when a shard yielded no symbols at all.
func Merge(shards ...Shard) *Corpus {
	c := NewCorpus()
	for _, s := range shards {
		c.AddShard(s.Name, s.Index)
	}
	return c
}

// AddShard merges ix into the corpus-wide lookup under the opaque identity name
// and returns the shard ID assigned to it. IDs are dense and assigned in call
// order starting at 0. A nil ix is recorded as an empty shard (it consumes an ID
// but contributes no names), so shard IDs stay aligned with a caller's shard
// list even where a shard has no symbol index.
func (c *Corpus) AddShard(name string, ix *Index) int {
	if c.byName == nil {
		c.byName = map[string][]int32{}
	}
	id := len(c.shards)
	c.shards = append(c.shards, shardEntry{name: name, ix: ix})
	if ix == nil {
		return id
	}
	sid := int32(id)
	for n := range ix.byName {
		// IDs are assigned monotonically, so appending keeps every posting list
		// ascending and duplicate-free without a sort or a membership check.
		c.byName[n] = append(c.byName[n], sid)
	}
	return id
}

// NumShards reports how many shards were merged (including empty ones).
func (c *Corpus) NumShards() int { return len(c.shards) }

// NumNames reports how many distinct names the corpus-wide lookup covers.
func (c *Corpus) NumNames() int { return len(c.byName) }

// ShardName returns the opaque identity of shard, or "" for an out-of-range ID.
func (c *Corpus) ShardName(shard int) string {
	if shard < 0 || shard >= len(c.shards) {
		return ""
	}
	return c.shards[shard].name
}

// ShardIndex returns the symbol index backing shard, or nil for an out-of-range
// ID (or a shard added with a nil index). It is how a caller resolves a
// ShardRef's Blob: the ID is meaningful only against this index.
func (c *Corpus) ShardIndex(shard int) *Index {
	if shard < 0 || shard >= len(c.shards) {
		return nil
	}
	return c.shards[shard].ix
}

// References returns every occurrence of name across ALL merged shards — both
// definitions (Role=Definition) and references (Role=Reference) — sorted by
// shard, then blob, then byte offset, with definitions before references at the
// same offset. It returns nil for a name no shard knows. The result is a fresh
// copy.
//
// Like (*Index).References this is SYNTACTIC and NAME-BASED, and merging widens
// that: names now collide across the whole corpus, not just within one shard.
func (c *Corpus) References(name string) []ShardRef {
	return c.collect(name, false)
}

// Definitions returns only the definition occurrences of name across all merged
// shards, sorted the same way, or nil when no shard defines it. The result is a
// fresh copy.
func (c *Corpus) Definitions(name string) []ShardRef {
	return c.collect(name, true)
}

// collect resolves name against every shard in its posting list, optionally
// keeping definitions only, and orders the merged result.
func (c *Corpus) collect(name string, defsOnly bool) []ShardRef {
	ids := c.byName[name]
	if len(ids) == 0 {
		return nil
	}
	var out []ShardRef
	for _, sid := range ids {
		ix := c.shards[sid].ix
		if ix == nil {
			continue // shard mutated to empty after merge; nothing to resolve
		}
		for _, r := range ix.byName[name] {
			if defsOnly && r.Role != Definition {
				continue
			}
			out = append(out, ShardRef{
				Shard: int(sid),
				Blob:  r.Blob,
				Start: r.Start,
				End:   r.End,
				Role:  r.Role,
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	sortShardRefs(out)
	return out
}

// sortShardRefs orders ShardRefs by shard, then blob, then byte offset, then
// role (Definition before Reference) — the cross-shard analogue of sortRefs.
func sortShardRefs(refs []ShardRef) {
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Shard != refs[j].Shard {
			return refs[i].Shard < refs[j].Shard
		}
		if refs[i].Blob != refs[j].Blob {
			return refs[i].Blob < refs[j].Blob
		}
		if refs[i].Start != refs[j].Start {
			return refs[i].Start < refs[j].Start
		}
		return refs[i].Role < refs[j].Role
	})
}

// DefiningShards returns the ascending IDs of the shards that DEFINE name, or
// nil when none do. It answers "every repo that defines this symbol" without
// materializing the occurrences, touching only the shards on name's posting
// list.
func (c *Corpus) DefiningShards(name string) []int {
	return c.shardsWithRole(name, Definition)
}

// ReferencingShards returns the ascending IDs of the shards that REFERENCE name
// (a non-declaring use), or nil when none do. A shard that both defines and
// references name appears in this list and in DefiningShards.
func (c *Corpus) ReferencingShards(name string) []int {
	return c.shardsWithRole(name, Reference)
}

// shardsWithRole lists the shards holding at least one occurrence of name in the
// given role. The posting list is already ascending and deduped, so the result
// is too.
func (c *Corpus) shardsWithRole(name string, role Role) []int {
	var out []int
	for _, sid := range c.byName[name] {
		ix := c.shards[sid].ix
		if ix == nil {
			continue
		}
		for _, r := range ix.byName[name] {
			if r.Role == role {
				out = append(out, int(sid))
				break
			}
		}
	}
	return out
}

// EachName calls fn for every distinct name in the corpus-wide lookup, stopping
// early if fn returns false. Iteration order is unspecified (it follows Go's map
// order and so varies between runs); callers needing determinism must sort.
// This is the enumeration seam for passes that fan out over every known name —
// e.g. generating edge candidates per definition — without materializing a
// corpus-sized slice of names.
func (c *Corpus) EachName(fn func(name string) bool) {
	for n := range c.byName {
		if !fn(n) {
			return
		}
	}
}

// Symbols returns the symbols of blob within shard (sorted by BodyStart), or nil
// for an unknown shard/blob.
func (c *Corpus) Symbols(shard int, blob uint64) []Symbol {
	ix := c.ShardIndex(shard)
	if ix == nil {
		return nil
	}
	return ix.Symbols(blob)
}

// Enclosing returns the innermost symbol of shard's blob covering byte offset
// off, and true; a zero Symbol and false when the shard is unknown or nothing
// covers off. It is the cross-shard form of (*Index).Enclosing, used to widen a
// ShardRef into the definition block that contains it.
func (c *Corpus) Enclosing(shard int, blob uint64, off int) (Symbol, bool) {
	ix := c.ShardIndex(shard)
	if ix == nil {
		return Symbol{}, false
	}
	return ix.Enclosing(blob, off)
}
