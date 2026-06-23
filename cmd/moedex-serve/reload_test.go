package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/index"
	"moedex/internal/server"
)

// makeShardDir writes a one-shard servable dir whose single file contains token.
func makeShardDir(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	ix := index.New()
	content := []byte(fmt.Sprintf("package x\n\n// %s marker\nfunc F() {}\n", token))
	ix.AddFile("repo", "x.go", filepath.Join(dir, "x.go"), "sha-"+token, content)
	if err := diskstore.Save(ix, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatalf("save shard: %v", err)
	}
	return dir
}

// TestCorpusHolderHotSwap is the safety test for the mmap'd retrieval reload:
// many goroutines query continuously while the corpus is swapped out from under
// them and the old generation is retired (unmapped). With correct refcounting no
// query ever touches an unmapped shard. Run under -race to catch swap/acquire
// ordering bugs and use-after-close.
func TestCorpusHolderHotSwap(t *testing.T) {
	dirA := makeShardDir(t, "AlphaToken")
	dirB := makeShardDir(t, "BetaToken")

	ca, err := server.Open(dirA)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	h := newCorpusHolder(ca)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	var queries int64
	var qmu sync.Mutex
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := h.acquire()
				// "marker" appears in every generation, so a healthy snapshot
				// always returns exactly one match; 0 would mean we read a
				// half-swapped or unmapped corpus.
				m, _ := snap.c.Literal("marker")
				if len(m) != 1 {
					t.Errorf("query saw %d matches, want 1 (corrupt/unmapped snapshot)", len(m))
				}
				snap.release()
				qmu.Lock()
				queries++
				qmu.Unlock()
			}
		}()
	}

	// Hammer swaps while readers run. Alternate dirs; retire each old generation
	// in the background exactly as production does.
	for i := 0; i < 30; i++ {
		dir := dirA
		if i%2 == 0 {
			dir = dirB
		}
		nc, err := server.Open(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		old := h.swap(nc)
		go old.retire()
		time.Sleep(time.Millisecond)
	}

	close(stop)
	readers.Wait()

	// The live generation must still answer, and the last swap was dirA.
	snap := h.acquire()
	if m, _ := snap.c.Literal("AlphaToken"); len(m) != 1 {
		t.Errorf("final corpus: AlphaToken matches = %d, want 1", len(m))
	}
	snap.release()
	snap.retire() // refcount now 0: drain returns immediately, then close cleanly
	if queries == 0 {
		t.Fatal("no queries ran; test did not exercise the swap")
	}
	t.Logf("served %d queries across 30 hot swaps", queries)
}

// TestRankHolderSwap covers the ranked (-mcp) reload path: SearchContext serves
// through the holder and a swap redirects subsequent searches to the new ranked
// corpus.
func TestRankHolderSwap(t *testing.T) {
	dirA := makeShardDir(t, "AlphaRank")
	dirB := makeShardDir(t, "BetaRank")
	ctx := context.Background()

	rcA, err := server.OpenRank(ctx, dirA, server.RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("openrank A: %v", err)
	}
	h := newRankHolder(rcA)

	win, err := h.SearchContext(ctx, "AlphaRank", 2000, 5)
	if err != nil {
		t.Fatalf("search A: %v", err)
	}
	if len(win.Blocks) == 0 {
		t.Error("expected a context block for AlphaRank before swap")
	}

	rcB, err := server.OpenRank(ctx, dirB, server.RankConfig{TopK: 5})
	if err != nil {
		t.Fatalf("openrank B: %v", err)
	}
	old := h.swap(rcB)
	go old.retire()

	win, err = h.SearchContext(ctx, "BetaRank", 2000, 5)
	if err != nil {
		t.Fatalf("search B: %v", err)
	}
	if len(win.Blocks) == 0 {
		t.Error("expected a context block for BetaRank after swap")
	}
}
