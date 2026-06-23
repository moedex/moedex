package parity

import (
	"moedex/internal/index"
	"moedex/internal/search"
)

// moedexInto runs a query through moedex's real retrieval path (trigram
// candidate selection + verification) over one shard and accumulates the
// resulting (fileID, line) matches.
func moedexInto(a *accum, ix *index.Index, q Query, ft *FileTable) error {
	ms, err := moedexMatches(ix, q)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if id := ft.IDOf(m.AbsPath); id >= 0 {
			a.add(id, m.Line)
		}
	}
	return nil
}

// moedexMatches routes a battery query to the matching moedex search entry
// point, mirroring exactly how gold and ripgrep are driven:
//   - plain literal      -> search.Literal (byte substring)
//   - everything else    -> search.Regex over the query's Go regexp source
//     (case-insensitive literals become (?i)\Qlit\E; regex/regex-ci as written).
func moedexMatches(ix *index.Index, q Query) ([]search.Match, error) {
	if q.Literal && !q.IgnoreCase {
		return search.Literal(ix, q.Pattern), nil
	}
	return search.Regex(ix, q.goRegexSource())
}
