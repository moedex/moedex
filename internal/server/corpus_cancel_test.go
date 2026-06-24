package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestCorpusRegexPropagatesCancellation builds a multi-shard corpus with large
// blobs, then proves a pre-cancelled context makes the fanned-out per-shard scans
// abort and surface context.Canceled through the first-non-nil-error aggregation.
func TestCorpusRegexPropagatesCancellation(t *testing.T) {
	dir := t.TempDir()
	// A few shards, each with large blobs so an uncancelled scan is non-trivial and
	// the cancellation path is the one under test.
	for s := 0; s < 3; s++ {
		files := map[string]string{}
		for b := 0; b < 40; b++ {
			var sb strings.Builder
			for line := 0; line < 1500; line++ {
				fmt.Fprintf(&sb, "var handler_%d=function(response){return payload(%d)};\n", line, b)
			}
			files[fmt.Sprintf("s%d_app%d.js", s, b)] = sb.String()
		}
		buildShard(t, dir, fmt.Sprintf("shard-%04d.idx", s), files)
	}

	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Sanity: an uncancelled query succeeds.
	if _, _, err := c.Regex(context.Background(), "handler|response|payload"); err != nil {
		t.Fatalf("uncancelled Regex errored: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Regex(ctx, "handler|response|payload"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Regex: err = %v, want context.Canceled", err)
	}

	// Literal cancellation propagates the same way.
	if _, _, err := c.Literal(ctx, "handler"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Literal: err = %v, want context.Canceled", err)
	}
}
