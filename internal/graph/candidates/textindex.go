package candidates

import (
	"hash/maphash"
	"strings"
	"unsafe"
)

// TextOccurrenceStats accounts for the optional, graph-scoped identifier scan.
// RetainedBytes counts lookup-array capacity, copied name bytes, and site-slice
// capacities. It excludes allocator rounding and transient copies during growth;
// this is a payload budget, not a process heap limit. All retained byte fields
// are zero on fallback; scan counters describe work attempted before fallback.
type TextOccurrenceStats struct {
	Built             bool
	RequestedNames    int
	IndexedNames      int
	BlobsScanned      int
	ContentBytes      uint64
	Occurrences       uint64
	RetainedBytes     int64
	RosterBytes       int64
	SiteCapacityBytes int64
	BudgetBytes       int64
	FallbackReason    string
}

const maxTextOccurrenceNames = 262144

type textOccurrenceEntry struct {
	name  string
	sites []siteKey
}

type textOccurrenceIndex struct {
	seed    maphash.Seed
	entries []textOccurrenceEntry
}

// find returns the matching entry or the empty insertion slot. The fixed table
// is at most half full, so probing always terminates. The seed and table become
// immutable before publication, making lookups safe for concurrent readers.
func (x *textOccurrenceIndex) find(name string) *textOccurrenceEntry {
	slot := maphash.String(x.seed, name) & uint64(len(x.entries)-1)
	for {
		entry := &x.entries[slot]
		if entry.name == "" || entry.name == name {
			return entry
		}
		slot = (slot + 1) & uint64(len(x.entries)-1)
	}
}

func (x *textOccurrenceIndex) findBytes(name []byte) *textOccurrenceEntry {
	slot := maphash.Bytes(x.seed, name) & uint64(len(x.entries)-1)
	for {
		entry := &x.entries[slot]
		if entry.name == "" || entry.name == string(name) {
			return entry
		}
		slot = (slot + 1) & uint64(len(x.entries)-1)
	}
}

func identifierName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		if !identByte(name[i]) {
			return false
		}
	}
	return true
}

// WithTextOccurrences scans content once for the requested identifier names and
// returns a new immutable Corpus view. Its symbol arm, byte-boundary rules and
// candidate ordering are unchanged. Names containing non-identifier bytes and
// names outside the requested roster continue through the original path.
//
// The index is published only after every shard has been scanned. Any budget or
// roster overflow discards the entire new index and returns c, never a partial
// answer. The index borrows corpus identities but owns names and occurrence
// positions. Release the returned view with its corpus; do not retain it after
// closing the underlying indices. The input Corpus is never mutated.
func WithTextOccurrences(c *Corpus, names []string, maxBytes int64) (*Corpus, TextOccurrenceStats) {
	stats := TextOccurrenceStats{RequestedNames: len(names), BudgetBytes: max(0, maxBytes)}
	fallback := func(reason string) (*Corpus, TextOccurrenceStats) {
		stats.Built = false
		stats.RetainedBytes = 0
		stats.RosterBytes = 0
		stats.SiteCapacityBytes = 0
		stats.FallbackReason = reason
		return c, stats
	}
	if c == nil {
		return fallback("nil corpus")
	}
	if maxBytes <= 0 {
		return fallback("disabled budget")
	}
	// Count before allocating. Duplicate roster entries may overestimate the
	// required table, which only changes whether this optimization is admitted.
	supported := 0
	for _, name := range names {
		if identifierName(name) {
			supported++
			if supported > maxTextOccurrenceNames {
				return fallback("name roster limit")
			}
		}
	}
	if supported == 0 {
		return fallback("no identifier names")
	}
	slots := 2
	for slots < supported*2 {
		slots *= 2
	}
	rosterBytes := int64(slots)*int64(unsafe.Sizeof(textOccurrenceEntry{})) + int64(unsafe.Sizeof(textOccurrenceIndex{}))
	if rosterBytes > maxBytes {
		return fallback("byte budget")
	}
	x := &textOccurrenceIndex{seed: maphash.MakeSeed(), entries: make([]textOccurrenceEntry, slots)}
	stats.RosterBytes = rosterBytes
	stats.RetainedBytes = rosterBytes
	for _, name := range names {
		if !identifierName(name) {
			continue
		}
		entry := x.find(name)
		if entry.name != "" {
			continue
		}
		if int64(len(name)) > maxBytes-stats.RetainedBytes {
			return fallback("byte budget")
		}
		entry.name = strings.Clone(name)
		stats.IndexedNames++
		stats.RosterBytes += int64(len(name))
		stats.RetainedBytes += int64(len(name))
	}
	siteBytes := int64(unsafe.Sizeof(siteKey{}))
	for shard, ix := range c.idxs {
		if ix == nil {
			continue
		}
		for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
			blob := ix.Blob(id)
			if blob == nil {
				continue
			}
			stats.BlobsScanned++
			stats.ContentBytes += uint64(len(blob.Content))
			complete := eachIdentifierPosition(blob.Content, func(start, end int) bool {
				entry := x.findBytes(blob.Content[start:end])
				if entry.name == "" {
					return true
				}
				if len(entry.sites) == cap(entry.sites) {
					next := max(4, cap(entry.sites)*2)
					extra := int64(next-cap(entry.sites)) * siteBytes
					if extra > maxBytes-stats.RetainedBytes {
						return false
					}
					sites := make([]siteKey, len(entry.sites), next)
					copy(sites, entry.sites)
					entry.sites = sites
					stats.SiteCapacityBytes += extra
					stats.RetainedBytes += extra
				}
				entry.sites = append(entry.sites, siteKey{shard: shard, blob: id, start: start})
				stats.Occurrences++
				return true
			})
			if !complete {
				return fallback("byte budget")
			}
		}
	}
	result := *c
	result.textOccurrences = x
	stats.Built = true
	return &result, stats
}
