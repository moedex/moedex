package parity

import (
	"bytes"
	"regexp"

	"moedex/internal/index"
)

// matcher reports whether a single line (without its trailing '\n') matches.
type matcher func(line []byte) bool

// goldMatcher builds the independent reference matcher for a query, using the
// exact same Go primitives moedex's verifier uses (bytes.Contains / regexp) but
// WITHOUT any trigram prefiltering. Scanning every line of every blob with this
// is the ground truth for moedex's own semantic universe: if moedex disagrees
// with gold, the trigram reduction or the literal prefilter dropped (or, in
// principle, invented) a match — a real engine bug. If moedex agrees with gold
// but disagrees with ripgrep, the difference is RE2-vs-Rust-regex semantics.
func goldMatcher(q Query) matcher {
	if q.Literal && !q.IgnoreCase {
		pat := []byte(q.Pattern)
		return func(l []byte) bool { return bytes.Contains(l, pat) }
	}
	re := regexp.MustCompile(q.goRegexSource())
	return func(l []byte) bool { return re.Match(l) }
}

// goldInto scans every line of every blob in a shard with m and accumulates
// matches, expanding each matching blob to all its file refs (content dedup).
// li is 0-based here; moedex reports 1-based, so we add 1 — identical to
// search's forEachLine + appendRefs.
func goldInto(a *accum, ix *index.Index, m matcher, ft *FileTable) {
	for id := 0; id < ix.NumBlobs(); id++ {
		b := ix.Blob(uint64(id))
		eachLine(b.Content, func(li int, line []byte) {
			if !m(line) {
				return
			}
			for _, f := range b.Files {
				if fid := ft.IDOf(f.AbsPath); fid >= 0 {
					a.add(fid, li+1)
				}
			}
		})
	}
}

// eachLine reproduces search.forEachLine byte-for-byte: lineIndex is 0-based,
// line excludes the '\n', an empty file yields no lines, and a trailing newline
// does NOT produce a final empty line. Keeping this identical to the search
// package is what makes gold a faithful reference for moedex's line semantics.
func eachLine(content []byte, fn func(li int, line []byte)) {
	if len(content) == 0 {
		return
	}
	li := 0
	for len(content) > 0 {
		i := bytes.IndexByte(content, '\n')
		if i < 0 {
			fn(li, content)
			return
		}
		fn(li, content[:i])
		content = content[i+1:]
		li++
	}
}
