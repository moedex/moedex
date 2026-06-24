package parity

import (
	"context"

	"moedex/internal/index"
	"moedex/internal/search"
)

// moedexInto runs a query through moedex's real retrieval path (trigram
// candidate selection + verification) over one shard and accumulates the
// resulting (fileID, line) matches.
func moedexInto(a *accum, ix *index.Index, q Query, ft *FileTable) (QueryAttribution, error) {
	ms, attr, err := moedexMatches(ix, q)
	if err != nil {
		return attr, err
	}
	for _, m := range ms {
		if id := ft.IDOf(m.AbsPath); id >= 0 {
			a.add(id, m.Line)
		}
	}
	return attr, nil
}

// moedexMatches routes a battery query to the matching moedex search entry
// point, mirroring exactly how gold and ripgrep are driven:
//   - plain literal      -> search.Literal (byte substring)
//   - everything else    -> search.Regex over the query's Go regexp source
//     (case-insensitive literals become (?i)\Qlit\E; regex/regex-ci as written).
func moedexMatches(ix *index.Index, q Query) ([]search.Match, QueryAttribution, error) {
	if q.Literal && !q.IgnoreCase {
		ms, stats, err := search.LiteralWithStats(context.Background(), ix, q.Pattern)
		return ms, attributionFromSearchStats(q, stats, ix.NumBlobs()), err
	}
	ms, stats, err := search.RegexWithStats(context.Background(), ix, q.goRegexSource())
	return ms, attributionFromSearchStats(q, stats, ix.NumBlobs()), err
}

func attributionFromSearchStats(q Query, stats search.Stats, totalBlobs int) QueryAttribution {
	candidateKind := stats.LineFilter
	linesRE2Known := false
	if !(q.Literal && !q.IgnoreCase) {
		if stats.QueryAll {
			candidateKind = "regex-all"
		} else {
			candidateKind = "regex-trigram"
		}
		linesRE2Known = true
	}
	return QueryAttribution{
		CandidateBlobs:        int64(stats.CandidateBlobs),
		CandidateBytes:        stats.CandidateBytes,
		CandidateLines:        stats.CandidateLines,
		CandidateKind:         candidateKind,
		AllCandidates:         stats.CandidateBlobs == totalBlobs,
		QueryAll:              stats.QueryAll,
		LineFilterKind:        stats.LineFilter,
		LinesEnteringRE2:      stats.LinesRE2,
		LinesEnteringRE2Known: linesRE2Known,
		VerifyWorkers:         stats.ParallelWorkers,
		observed:              true,
	}
}
