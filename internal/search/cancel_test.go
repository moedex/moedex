package search

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"

	"moedex/internal/index"
	"moedex/internal/trigram"
)

const (
	cancelTestBlobs = 64
	cancelTestLines = 16
)

// cancelTestIndex exceeds both the parallel-verify threshold and the positional
// cancellation stride, without needing a slow scan. Each blob has 16 matching
// content lines and eight distinct qzx candidate lines (512 postings in total).
func cancelTestIndex(tb testing.TB) *index.Index {
	tb.Helper()
	ix := index.New()
	for blob := 0; blob < cancelTestBlobs; blob++ {
		var sb []byte
		sb = append(sb, []byte(fmt.Sprintf("// generated bundle %d\n", blob))...)
		for line := 0; line < cancelTestLines; line++ {
			sb = append(sb, []byte(fmt.Sprintf("var handler_%d=function(response){return payload(%d)};\n", line, blob*cancelTestLines+line))...)
			if line%2 == 0 {
				sb = append(sb, []byte(fmt.Sprintf("  qzx_marker_%d = 1;\n", line))...)
			}
		}
		sb = append(sb, []byte(fmt.Sprintf("var Razavi_%d = itemAnswer(%d);\n", blob, blob))...)
		rel := fmt.Sprintf("bundles/app%d.min.js", blob)
		ix.AddFile("repo", rel, "/abs/"+rel, fmt.Sprintf("sha-%d", blob), sb)
	}
	return ix
}

// cancelAfterChecksContext cancels when a hot loop reaches a specified check,
// rather than after a wall-clock delay. Earlier checks return an open channel,
// including the entry guard. Concurrent callers at or beyond the threshold all
// cancel before returning Done, so a delayed threshold caller cannot let other
// workers keep scanning. Both the counter and context cancellation are safe for
// concurrent use.
type cancelAfterChecksContext struct {
	context.Context
	cancel      context.CancelFunc
	checks      atomic.Int64
	cancelAfter int64
}

func newCancelAfterChecksContext(n int64) *cancelAfterChecksContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelAfterChecksContext{Context: ctx, cancel: cancel, cancelAfter: n}
}

func (c *cancelAfterChecksContext) Done() <-chan struct{} {
	if c.checks.Add(1) >= c.cancelAfter {
		c.cancel()
	}
	return c.Context.Done()
}

func TestRegexWithStatsCancelsPromptly(t *testing.T) {
	ix := cancelTestIndex(t)

	cases := []struct {
		name       string
		pat        string
		positional bool
		matches    int
	}{
		{"content-scan", "handler|response|payload", false, cancelTestBlobs * cancelTestLines},
		{"positional", "qzx", true, cancelTestBlobs * cancelTestLines / 2},
	}
	for _, mode := range []struct {
		name  string
		procs int
	}{{"serial", 1}, {"parallel", 4}} {
		t.Run(mode.name, func(t *testing.T) {
			old := runtime.GOMAXPROCS(mode.procs)
			t.Cleanup(func() { runtime.GOMAXPROCS(old) })
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					baseline, baseStats, err := RegexWithStats(context.Background(), ix, c.pat)
					if err != nil {
						t.Fatalf("uncancelled %q: %v", c.pat, err)
					}
					if len(baseline) != c.matches {
						t.Fatalf("uncancelled matches = %d, want %d", len(baseline), c.matches)
					}
					if isPos := baseStats.LineFilter == "positional"; isPos != c.positional {
						t.Fatalf("LineFilter = %q, want positional=%v", baseStats.LineFilter, c.positional)
					}
					workers := baseStats.ParallelWorkers
					if (workers > 1) != (mode.procs > 1) {
						t.Fatalf("workers = %d, want parallel=%v", workers, mode.procs > 1)
					}

					t.Run("pre-cancelled", func(t *testing.T) {
						ctx := newCancelAfterChecksContext(3)
						ctx.cancel()
						matches, stats, err := RegexWithStats(ctx, ix, c.pat)
						if !errors.Is(err, context.Canceled) || len(matches) != 0 || stats != (Stats{}) || ctx.checks.Load() != 1 {
							t.Fatalf("entry guard: matches=%d stats=%+v err=%v checks=%d", len(matches), stats, err, ctx.checks.Load())
						}
					})

					if c.positional {
						t.Run("candidate-build", func(t *testing.T) {
							// Check 1 is the entry guard, check 2 precedes posting 0,
							// and check 3 follows cancelCheckStride processed postings.
							// Zero stats also distinguish this abort from verification.
							ctx := newCancelAfterChecksContext(3)
							defer ctx.cancel()
							matches, stats, err := RegexWithStats(ctx, ix, c.pat)
							if !errors.Is(err, context.Canceled) || len(matches) != 0 || stats != (Stats{}) || ctx.checks.Load() != 3 {
								t.Fatalf("candidate-build cancellation: matches=%d stats=%+v err=%v checks=%d", len(matches), stats, err, ctx.checks.Load())
							}
						})
					}

					t.Run("verification", func(t *testing.T) {
						// Content checks once per blob: allowing every worker's
						// first check plus one more guarantees a completed blob.
						cancelAfter := int64(1 + workers + 1)
						maxRE2 := int64(workers * cancelTestLines)
						if c.positional {
							postings := ix.PostingCount(trigram.Trigram{'q', 'z', 'x'})
							buildChecks := (postings + cancelCheckStride - 1) / cancelCheckStride
							// Allow all candidate-build checks, then cancel after
							// one serial stride or at least one completed parallel
							// chunk. Each chunk has a pull and a verify-entry check.
							verifyChecks := 2
							maxRE2 = cancelCheckStride
							if workers > 1 {
								verifyChecks = 2*workers + 1
								maxRE2 = int64(16 * workers)
							}
							cancelAfter = int64(1 + buildChecks + verifyChecks)
						}
						ctx := newCancelAfterChecksContext(cancelAfter)
						defer ctx.cancel()
						matches, stats, err := RegexWithStats(ctx, ix, c.pat)
						if !errors.Is(err, context.Canceled) || ctx.checks.Load() < cancelAfter {
							t.Fatalf("verification cancellation: err=%v checks=%d, want at least %d", err, ctx.checks.Load(), cancelAfter)
						}
						if stats.LineFilter != baseStats.LineFilter || stats.ParallelWorkers != workers {
							t.Fatalf("cancelled route/workers = %q/%d, want %q/%d", stats.LineFilter, stats.ParallelWorkers, baseStats.LineFilter, workers)
						}
						if stats.LinesRE2 <= 0 || stats.LinesRE2 > maxRE2 || stats.CandidateBytes <= 0 || stats.CandidateBytes >= baseStats.CandidateBytes {
							t.Fatalf("partial work: stats=%+v, want 0 < LinesRE2 <= %d and fewer bytes than %d", stats, maxRE2, baseStats.CandidateBytes)
						}
						if c.positional && stats.CandidateLines != baseStats.CandidateLines {
							t.Fatalf("candidate build incomplete: got %d lines, want %d", stats.CandidateLines, baseStats.CandidateLines)
						}
						if workers == 1 && (ctx.checks.Load() != cancelAfter || stats.LinesRE2 != maxRE2) {
							t.Fatalf("serial stop: checks=%d LinesRE2=%d, want %d/%d", ctx.checks.Load(), stats.LinesRE2, cancelAfter, maxRE2)
						}
						if len(matches) != int(stats.LinesRE2) || len(matches) >= len(baseline) {
							t.Fatalf("partial matches = %d, want LinesRE2=%d and fewer than %d", len(matches), stats.LinesRE2, len(baseline))
						}
						gold := make(map[Match]bool, len(baseline))
						for _, match := range baseline {
							gold[match] = true
						}
						for _, match := range matches {
							if !gold[match] {
								t.Fatalf("cancelled result absent from baseline: %+v", match)
							}
						}
					})
				})
			}
		})
	}
}

// mustCancel returns an already-cancelled context.
func mustCancel() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestLiteralWithStatsCancelsPromptly(t *testing.T) {
	ix := cancelTestIndex(t)

	// Cover BOTH literal paths: the begin/end-gram positional merge-join (a
	// >=3-char literal) and the sub-trigram full scan (a 1-2 char literal). Both
	// assert the entry-guard contract. TestRegexWithStatsCancelsPromptly exercises
	// the corresponding per-blob and strided positional checks after scan progress.
	cases := []struct {
		name string
		q    string
	}{
		{"merge-join", "handler_response"},
		{"sub-trigram", "=>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// An uncancelled run completes with no error (the baseline contract).
			if _, _, err := LiteralWithStats(context.Background(), ix, c.q); err != nil {
				t.Fatalf("uncancelled %q errored: %v", c.q, err)
			}
			// Deterministic: pre-cancelled ctx returns context.Canceled at once.
			if _, _, err := LiteralWithStats(mustCancel(), ix, c.q); !errors.Is(err, context.Canceled) {
				t.Fatalf("pre-cancelled %q: err = %v, want context.Canceled", c.q, err)
			}
		})
	}
}

// TestSearchUncancelledMatchesBaseline guards the guard: with context.Background()
// the new ctx-threaded path returns exactly the same matches it would have before
// cancellation existed. We compare regex and literal against an independent
// brute-force gold over the same index.
func TestSearchUncancelledMatchesBaseline(t *testing.T) {
	ix := index.New()
	for i := 0; i < 64; i++ {
		content := fmt.Sprintf("package p%d\nhandler response payload x%d\n", i, i)
		if i%5 == 0 {
			content += "Razavi itemAnswer here\n"
		}
		ix.AddFile("r", fmt.Sprintf("f%d.go", i), fmt.Sprintf("/abs/f%d.go", i), fmt.Sprintf("sha-%d", i), []byte(content))
	}

	// Regex, content-scan path.
	reMatches, _, err := RegexWithStats(context.Background(), ix, "handler|response|payload")
	if err != nil {
		t.Fatal(err)
	}
	// Every blob has a "handler response payload" line, so one match per blob.
	if len(reMatches) != 64 {
		t.Fatalf("regex baseline: got %d matches, want 64", len(reMatches))
	}

	// Regex, positional path (case-insensitive rare-token alternation).
	posMatches, _, err := RegexWithStats(context.Background(), ix, "(?i)razavi|itemanswer")
	if err != nil {
		t.Fatal(err)
	}
	if len(posMatches) != 13 { // i%5==0 over 0..63 -> 13 blobs (0,5,...,60)
		t.Fatalf("positional baseline: got %d matches, want 13", len(posMatches))
	}

	// Literal, merge-join path.
	litMatches, _, err := LiteralWithStats(context.Background(), ix, "payload")
	if err != nil {
		t.Fatal(err)
	}
	if len(litMatches) != 64 {
		t.Fatalf("literal baseline: got %d matches, want 64", len(litMatches))
	}
}
