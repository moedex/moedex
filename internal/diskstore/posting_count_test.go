package diskstore

import (
	"sync"
	"testing"

	"moedex/internal/index"
	"moedex/internal/trigram"
)

func countFixture(n int) []byte {
	postings := make([]index.Posting, n)
	for i := range postings {
		postings[i] = index.Posting{Blob: uint64(i / 100), Offset: i % 100}
	}
	return index.EncodePostings(postings)
}

func TestMmapPostingCountCacheConcurrentParity(t *testing.T) {
	raw := map[trigram.Trigram][]byte{
		{'a', 'b', 'c'}: countFixture(12345),
		{'x', 'y', 'z'}: countFixture(513),
		{'s', 'm', 'l'}: countFixture(3),
	}
	p := &mmapProvider{raw: raw}
	keys := []trigram.Trigram{{'a', 'b', 'c'}, {'x', 'y', 'z'}, {'s', 'm', 'l'}, {'n', 'o', 't'}}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				for _, g := range keys {
					if got, want := p.PostingCount(g), len(index.DecodePostings(raw[g])); got != want {
						t.Errorf("%v count %d, want %d", g, got, want)
					}
				}
			}
		})
	}
	wg.Wait()
	if len(p.counts) != 2 {
		t.Fatalf("cache retained %d entries, want only two large lists", len(p.counts))
	}
	for g, count := range p.counts {
		if count != len(index.DecodePostings(raw[g])) {
			t.Fatalf("cached count mismatch for %v", g)
		}
	}
}

func TestMmapPostingCountCacheBounded(t *testing.T) {
	encoded := countFixture(512)
	raw := make(map[trigram.Trigram][]byte, postingCountCacheEntries+5)
	for i := 0; i < postingCountCacheEntries+5; i++ {
		raw[trigram.Trigram{byte(i), byte(i >> 8), byte(i >> 16)}] = encoded
	}
	p := &mmapProvider{raw: raw}
	for g := range raw {
		if got := p.PostingCount(g); got != 512 {
			t.Fatalf("count %d, want 512", got)
		}
	}
	if len(p.counts) != postingCountCacheEntries {
		t.Fatalf("cache size %d, want cap %d", len(p.counts), postingCountCacheEntries)
	}
	// A full cache must still return exact counts for non-admitted grams.
	for g := range raw {
		if got := p.PostingCount(g); got != 512 {
			t.Fatalf("full-cache count %d, want 512", got)
		}
	}
}

func TestSelectiveMmapPostingCountMembershipUnchanged(t *testing.T) {
	kept, omitted := trigram.Trigram{'k', 'e', 'p'}, trigram.Trigram{'o', 'm', 'i'}
	p := &selectiveMmapProvider{mmapProvider: &mmapProvider{raw: map[trigram.Trigram][]byte{kept: countFixture(512)}}, selected: map[trigram.Trigram]struct{}{kept: {}}}
	for range 2 {
		if !p.IndexedGram(kept) || p.IndexedGram(omitted) {
			t.Fatal("selective membership changed")
		}
		if p.PostingCount(kept) != 512 || p.PostingCount(omitted) != 0 {
			t.Fatal("materialized counts changed")
		}
	}
}

func BenchmarkMmapPostingCount(b *testing.B) {
	encoded := countFixture(100000)
	g := trigram.Trigram{'c', 'o', 'm'}
	p := &mmapProvider{raw: map[trigram.Trigram][]byte{g: encoded}}
	b.Run("uncached_walk", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if countEncodedPostings(encoded) != 100000 {
				b.Fatal("bad count")
			}
		}
	})
	b.Run("cached_count", func(b *testing.B) {
		p.PostingCount(g)
		b.ReportAllocs()
		for b.Loop() {
			if p.PostingCount(g) != 100000 {
				b.Fatal("bad count")
			}
		}
	})
}
