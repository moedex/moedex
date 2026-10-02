package verify

import (
	"container/list"
	"sync"

	"moedex/internal/index"
)

// RegionCacheStats describes retained mask payload and lookup work. Bytes does
// not include bounded entry bookkeeping or classifications currently in flight.
type RegionCacheStats struct {
	Hits       uint64
	Misses     uint64
	Waits      uint64
	Evictions  uint64
	Entries    int
	Bytes      int64
	MaxBytes   int64
	MaxEntries int
}

type regionEntry struct {
	key  regionKey
	mask []region
}
type regionFlight struct {
	ready chan struct{}
	mask  []region
}

// RegionCache retains immutable lexical masks for one immutable corpus lifetime.
// The byte and entry limits bound retained data; masks exceeding the byte budget
// are computed without retention. Callers must not mutate blobs while using it.
// The cache is concurrency-safe and coalesces concurrent fills for the same key.
// It deliberately has no process-global instance: release it with its corpus.
type RegionCache struct {
	mu                             sync.Mutex
	maxBytes                       int64
	maxEntries                     int
	bytes                          int64
	entries                        map[regionKey]*list.Element
	order                          list.List
	inflight                       map[regionKey]*regionFlight
	hits, misses, waits, evictions uint64
}

func NewRegionCache(maxBytes int64, maxEntries int) *RegionCache {
	return &RegionCache{maxBytes: max(0, maxBytes), maxEntries: max(0, maxEntries), entries: make(map[regionKey]*list.Element), inflight: make(map[regionKey]*regionFlight)}
}

func (c *RegionCache) Stats() RegionCacheStats {
	if c == nil {
		return RegionCacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return RegionCacheStats{Hits: c.hits, Misses: c.misses, Waits: c.waits, Evictions: c.evictions, Entries: len(c.entries), Bytes: c.bytes, MaxBytes: c.maxBytes, MaxEntries: c.maxEntries}
}

func (c *RegionCache) regions(blob *index.Blob, lang language) []region {
	if c == nil {
		return classifyRegions(blob.Content, lang)
	}
	key := regionKey{blob: blob, lang: lang}
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil {
		c.hits++
		c.order.MoveToFront(entry)
		mask := entry.Value.(regionEntry).mask
		c.mu.Unlock()
		return mask
	}
	if pending := c.inflight[key]; pending != nil {
		c.waits++
		c.mu.Unlock()
		<-pending.ready
		return pending.mask
	}
	c.misses++
	// Bound fill bookkeeping too. A bypass only affects work, never answers.
	if c.maxEntries == 0 || int64(len(blob.Content)) > c.maxBytes || len(c.inflight) >= c.maxEntries {
		c.mu.Unlock()
		return classifyRegions(blob.Content, lang)
	}
	pending := &regionFlight{ready: make(chan struct{})}
	c.inflight[key] = pending
	c.mu.Unlock()
	mask := classifyRegions(blob.Content, lang)
	c.mu.Lock()
	for len(c.entries) >= c.maxEntries || c.bytes+int64(len(mask)) > c.maxBytes {
		oldest := c.order.Back()
		entry := oldest.Value.(regionEntry)
		delete(c.entries, entry.key)
		c.bytes -= int64(len(entry.mask))
		c.order.Remove(oldest)
		c.evictions++
	}
	c.entries[key] = c.order.PushFront(regionEntry{key: key, mask: mask})
	c.bytes += int64(len(mask))
	pending.mask = mask
	delete(c.inflight, key)
	close(pending.ready)
	c.mu.Unlock()
	return mask
}
