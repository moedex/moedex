package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/embed"
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
				m, _, _ := snap.c.Literal(context.Background(), "marker")
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
	if m, _, _ := snap.c.Literal(context.Background(), "AlphaToken"); len(m) != 1 {
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

// failingEmbedder always errors, simulating a dense embedding backend that is
// transiently down (embed service unreachable, model unavailable, etc.).
type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, []string) ([]embed.Vector, error) {
	return nil, errors.New("embed backend unreachable")
}
func (failingEmbedder) Dim() int { return 8 }

// TestOpenRankOrDegradeFallsBackToLexicalOnDenseFailure pins F-22: a dense-arm
// failure at OpenRank time must degrade to lexical+symbol rather than
// aborting the whole corpus open, exactly like openRankCorpus already does at
// boot -- and the SAME retry must be available to the SIGHUP reload paths,
// which is why it is factored into this standalone helper rather than left
// inlined in openRankCorpus.
func TestOpenRankOrDegradeFallsBackToLexicalOnDenseFailure(t *testing.T) {
	dir := makeShardDir(t, "DenseFallbackToken")
	ctx := context.Background()
	cfg := server.RankConfig{TopK: 5, Emb: failingEmbedder{}}

	rc, effective, err := openRankOrDegrade(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("openRankOrDegrade: %v, want a clean fallback to lexical+symbol", err)
	}
	defer func() { _ = rc.Close() }()

	if effective.Emb != nil {
		t.Fatalf("effective RankConfig still configures a dense arm after falling back")
	}
	if rc.DenseChunks() != 0 {
		t.Fatalf("DenseChunks = %d, want 0 after falling back to lexical+symbol", rc.DenseChunks())
	}
	// cfg is passed by value and must never be mutated: a caller (a SIGHUP
	// reload) that keeps its own cfg across calls needs a fresh dense attempt
	// every time, not a corpus that stays degraded forever after one
	// transient failure (F-22's actual complaint).
	if cfg.Emb == nil {
		t.Fatalf("openRankOrDegrade mutated the caller's cfg; a later reload would never retry dense again")
	}

	// Confirm the corpus still answers queries lexically.
	win, err := rc.SearchContext(ctx, "DenseFallbackToken", 2000, 5)
	if err != nil {
		t.Fatalf("search after fallback: %v", err)
	}
	if len(win.Blocks) == 0 {
		t.Error("expected a context block for DenseFallbackToken from the lexical fallback corpus")
	}
}

// TestOpenRankOrDegradeRetriesDenseFreshOnEachCall complements the above: a
// second SIGHUP-style call reusing the SAME original cfg (dense still
// configured, never mutated by the first call) must independently attempt
// dense and fall back again, proving one failed attempt cannot leave the
// corpus permanently degraded the way calling server.OpenRank directly with
// an already-degraded cfg would.
func TestOpenRankOrDegradeRetriesDenseFreshOnEachCall(t *testing.T) {
	dir := makeShardDir(t, "DenseRetryToken")
	ctx := context.Background()
	cfg := server.RankConfig{TopK: 5, Emb: failingEmbedder{}}

	for i := 0; i < 2; i++ {
		rc, effective, err := openRankOrDegrade(ctx, dir, cfg)
		if err != nil {
			t.Fatalf("call %d: openRankOrDegrade: %v, want a clean fallback every time", i, err)
		}
		if effective.Emb != nil {
			t.Fatalf("call %d: effective cfg still configures a dense arm after fallback", i)
		}
		if cfg.Emb == nil {
			t.Fatalf("call %d: openRankOrDegrade mutated the caller's cfg", i)
		}
		_ = rc.Close()
	}
}
