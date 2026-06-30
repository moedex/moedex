package parity

// This file is a test-only cross-check of candidate attribution, exercised by
// TestLatencyAttributionSynthetic and TestLiteralCandidateBlobsSelectiveGate in
// parity_test.go. It independently recomputes the begin/end-gram candidate set
// straight from index postings for profiling-attribution comparison purposes
// only — it does not affect match retrieval. The live attribution path a real
// parity run uses is attributionFromSearchStats in oracle_moedex.go, which
// derives counts from search.Stats instead of recomputing them here.

import (
	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/trigram"
)

type blobStats struct {
	bytes int64
	lines int64
}

type shardStats struct {
	blobs      []blobStats
	totalBytes int64
	totalLines int64
}

func newShardStats(ix *index.Index) shardStats {
	stats := shardStats{blobs: make([]blobStats, ix.NumBlobs())}
	for id := 0; id < ix.NumBlobs(); id++ {
		b := ix.Blob(uint64(id))
		if b == nil {
			continue
		}
		s := blobStats{bytes: int64(len(b.Content)), lines: countLines(b.Content)}
		stats.blobs[id] = s
		stats.totalBytes += s.bytes
		stats.totalLines += s.lines
	}
	return stats
}

func (s shardStats) totals(ids []uint64) (blobs, bytes, lines int64) {
	for _, id := range ids {
		if id >= uint64(len(s.blobs)) {
			continue
		}
		bs := s.blobs[id]
		blobs++
		bytes += bs.bytes
		lines += bs.lines
	}
	return blobs, bytes, lines
}

func countLines(content []byte) int64 {
	var n int64
	eachLine(content, func(int, []byte) { n++ })
	return n
}

func attributionForQuery(ix *index.Index, stats shardStats, q Query) QueryAttribution {
	if q.Literal && !q.IgnoreCase {
		return literalAttribution(ix, stats, q.Pattern)
	}
	return regexAttribution(ix, stats, q.goRegexSource())
}

func literalAttribution(ix *index.Index, stats shardStats, pattern string) QueryAttribution {
	if len(pattern) < trigram.N {
		return QueryAttribution{
			CandidateBlobs:        int64(ix.NumBlobs()),
			CandidateBytes:        stats.totalBytes,
			CandidateLines:        stats.totalLines,
			CandidateKind:         "literal-all",
			AllCandidates:         true,
			QueryAll:              false,
			LineFilterKind:        "literal",
			LinesEnteringRE2:      0,
			LinesEnteringRE2Known: true,
			observed:              true,
		}
	}

	ids, all := literalCandidateBlobs(ix, []byte(pattern))
	if all {
		// On a selective index, a deselected begin/end gram has UNKNOWN postings:
		// the search path force-scans every blob (IndexedGram widening). Attribute
		// that here so candidate counts are honest rather than under-counted.
		return QueryAttribution{
			CandidateBlobs:        int64(ix.NumBlobs()),
			CandidateBytes:        stats.totalBytes,
			CandidateLines:        stats.totalLines,
			CandidateKind:         "literal-all",
			AllCandidates:         true,
			QueryAll:              false,
			LineFilterKind:        "literal",
			LinesEnteringRE2:      0,
			LinesEnteringRE2Known: true,
			observed:              true,
		}
	}
	blobs, bytes, lines := stats.totals(ids)
	return QueryAttribution{
		CandidateBlobs:        blobs,
		CandidateBytes:        bytes,
		CandidateLines:        lines,
		CandidateKind:         "literal-positional",
		AllCandidates:         int(blobs) == ix.NumBlobs(),
		QueryAll:              false,
		LineFilterKind:        "literal",
		LinesEnteringRE2:      0,
		LinesEnteringRE2Known: true,
		observed:              true,
	}
}

// literalCandidateBlobs computes the begin/end-gram candidate-blob set for the
// parity harness's profiling ATTRIBUTION only (CandidateBlobs/Bytes/Lines), not
// for match retrieval — actual matches come from the search package, which is
// selective-index aware.
//
// It returns (ids, all). On the default all-trigram build every gram is indexed,
// so all is always false and ids is the exact begin/end candidate set. On a
// selective build, if either the begin or end gram is DESELECTED (IndexedGram
// false) its postings are UNKNOWN — reading them directly would under-count, so
// we report all=true (every blob is a candidate, matching the search path's
// force-scan widening). This keeps attribution honest under selective indexing;
// it is still not a match-correctness path.
func literalCandidateBlobs(ix *index.Index, qb []byte) (ids []uint64, all bool) {
	begin := trigram.Trigram{qb[0], qb[1], qb[2]}
	end := trigram.Trigram{qb[len(qb)-3], qb[len(qb)-2], qb[len(qb)-1]}
	if ix.Selective() && (!ix.IndexedGram(begin) || !ix.IndexedGram(end)) {
		return nil, true
	}
	off := len(qb) - trigram.N

	begins := ix.Postings(begin)
	ends := ix.Postings(end)
	j := 0
	var out []uint64
	for _, p := range begins {
		want := p.Offset + off
		for j < len(ends) && (ends[j].Blob < p.Blob || (ends[j].Blob == p.Blob && ends[j].Offset < want)) {
			j++
		}
		if j < len(ends) && ends[j].Blob == p.Blob && ends[j].Offset == want {
			if len(out) == 0 || out[len(out)-1] != p.Blob {
				out = append(out, p.Blob)
			}
		}
	}
	return out, false
}

func regexAttribution(ix *index.Index, stats shardStats, pattern string) QueryAttribution {
	q, err := query.FromRegexp(pattern)
	if err != nil {
		return QueryAttribution{CandidateKind: "regex-error", observed: true}
	}
	ids := q.Eval(ix)
	queryAll := q.String() == "ALL"
	blobs, bytes, lines := stats.totals(ids)
	kind := "regex-trigram"
	if queryAll {
		kind = "regex-all"
	}
	return QueryAttribution{
		CandidateBlobs: blobs,
		CandidateBytes: bytes,
		CandidateLines: lines,
		CandidateKind:  kind,
		AllCandidates:  int(blobs) == ix.NumBlobs(),
		QueryAll:       queryAll,
		observed:       true,
	}
}
