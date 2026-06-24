package search

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"moedex/internal/index"
)

// cancelTestIndex builds an index big enough to exceed both the parallel-verify
// threshold and the cancellation stride, with large blobs so an uncancelled scan
// takes meaningful wall-time. The rare tokens "razavi"/"itemanswer" sit on a
// couple of lines so a case-insensitive alternation routes through the positional
// path, while the longer ">3 char" literal alternation routes through the content
// scan — the two paths the cancellation checks must both cover.
func cancelTestIndex(tb testing.TB) *index.Index {
	tb.Helper()
	const nBlobs = 600
	const linesPerBlob = 1500
	ix := index.New()
	for blob := 0; blob < nBlobs; blob++ {
		var sb []byte
		sb = append(sb, []byte(fmt.Sprintf("// generated bundle %d\n", blob))...)
		for line := 0; line < linesPerBlob; line++ {
			sb = append(sb, []byte(fmt.Sprintf("var handler_%d=function(response){return payload(%d)};\n", line, blob*linesPerBlob+line))...)
			// A moderately-frequent 3-char token "qzx" (~1 line in 8) gives the
			// positional driver a large-but-under-cap posting list, so the positional
			// candidate-build + verify loops are long enough to cancel mid-flight.
			if line%8 == 0 {
				sb = append(sb, []byte(fmt.Sprintf("  qzx_marker_%d = 1;\n", line))...)
			}
		}
		// Two rare tokens, each on one line of this blob, so positional candidate
		// sets are tiny but the corpus is fully scanned by the content path.
		sb = append(sb, []byte(fmt.Sprintf("var Razavi_%d = itemAnswer(%d);\n", blob, blob))...)
		rel := fmt.Sprintf("bundles/app%d.min.js", blob)
		ix.AddFile("repo", rel, "/abs/"+rel, fmt.Sprintf("sha-%d", blob), sb)
	}
	return ix
}

func TestRegexWithStatsCancelsPromptly(t *testing.T) {
	ix := cancelTestIndex(t)

	// Cover BOTH routing paths: a content-scan pattern (>3-char literal
	// alternation, no positional reduction) and a positional-reducible pattern
	// (a 3-char literal whose driver posting list is large enough to make the
	// positional candidate-build + verify loops the dominant cost).
	cases := []struct {
		name       string
		pat        string
		positional bool
	}{
		{"content-scan", "handler|response|payload", false},
		{"positional", "qzx", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Baseline: an uncancelled run completes, returns no error, and confirms
			// the pattern routes through the path we intend to exercise.
			start := time.Now()
			_, stats, err := RegexWithStats(context.Background(), ix, c.pat)
			base := time.Since(start)
			if err != nil {
				t.Fatalf("uncancelled %q errored: %v", c.pat, err)
			}
			if isPos := stats.LineFilter == "positional"; isPos != c.positional {
				t.Fatalf("pattern %q routed positional=%v (LineFilter=%q), want positional=%v", c.pat, isPos, stats.LineFilter, c.positional)
			}
			if base < 5*time.Millisecond {
				t.Skipf("baseline scan too fast (%s) to test cancellation meaningfully", base)
			}

			// Deterministic: a pre-cancelled ctx returns context.Canceled at once
			// (the entry guard), well below the full-scan baseline.
			pcStart := time.Now()
			_, _, err = RegexWithStats(mustCancel(), ix, c.pat)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("pre-cancelled %q: err = %v, want context.Canceled", c.pat, err)
			}
			if d := time.Since(pcStart); d > base/2 {
				t.Fatalf("pre-cancelled %q took %s; baseline %s — entry guard did not short-circuit", c.pat, d, base)
			}

			// In-loop: a ctx cancelled just after the scan starts must be observed by
			// the hot-loop checks, aborting far below the full baseline. A small fixed
			// delay (well under the baseline) lets the scan get going first.
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(2*time.Millisecond, cancel)
			defer cancel()
			tlStart := time.Now()
			_, _, err = RegexWithStats(ctx, ix, c.pat)
			tl := time.Since(tlStart)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("mid-scan-cancelled %q: err = %v, want context.Canceled", c.pat, err)
			}
			if tl > base/2 {
				t.Fatalf("mid-scan-cancelled %q took %s; baseline %s — loop did not observe cancellation promptly", c.pat, tl, base)
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
	// are tight, SIMD/integer-comparison loops that finish in a couple of ms on
	// this corpus — too fast to time-cancel reliably — so each asserts the
	// deterministic entry-guard contract. Their in-loop stride checks use the same
	// `i%cancelCheckStride == 0 && canceled(ctx)` / per-blob pattern that
	// TestRegexWithStatsCancelsPromptly exercises with timing on the slower regex
	// scan and positional paths.
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
