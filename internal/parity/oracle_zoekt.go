package parity

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// zoektDiff is the optional competitive differential. Zoekt is itself Go-based
// (its regex engine is Go's RE2, like moedex's verifier), so divergences come
// not from regex semantics but from Zoekt's indexing limits — notably
// -file_limit (default 2MB: larger files unindexed) and -max_trigram_count
// (default 20000 trigrams/doc: large docs truncated) — which make Zoekt MISS
// matches that moedex (no such caps) finds. We compare at FILE granularity
// against gold-derived truth: moedex must never be beaten by Zoekt on recall.
type zoektDiff struct {
	searchBin string
	idxDir    string
	available bool
	notes     []string
}

// zoektBuckets are the battery categories sent to Zoekt. Sub-trigram (<3 chars),
// case-insensitive, and unicode buckets are excluded — Zoekt's smart-case and
// short-pattern handling differ enough to be noise in a soft differential.
var zoektBuckets = map[Bucket]bool{
	BCommonLiteral: true, BRareLiteral: true, BPhraseLiteral: true,
	BMetaLiteral: true, BHighFrequency: true, BRegex: true,
}

// setupZoekt indexes the mirror (exactly F) with zoekt-index and returns a
// searcher. If the binaries are absent, available=false and the whole section is
// skipped (offline-tolerant per AC-E1).
func setupZoekt(mirrorDir, idxDir string, fileLimit int, logf func(string, ...any)) *zoektDiff {
	z := &zoektDiff{idxDir: idxDir}
	indexBin, err := exec.LookPath("zoekt-index")
	if err != nil {
		z.notes = append(z.notes, "zoekt-index not on PATH; differential skipped")
		return z
	}
	z.searchBin, err = exec.LookPath("zoekt")
	if err != nil {
		z.notes = append(z.notes, "zoekt (search) not on PATH; differential skipped")
		return z
	}
	args := []string{"-index", idxDir, "-disable_ctags"}
	if fileLimit > 0 {
		args = append(args, "-file_limit", strconv.Itoa(fileLimit))
		z.notes = append(z.notes, fmt.Sprintf("zoekt-index -file_limit=%d", fileLimit))
	}
	args = append(args, mirrorDir)
	cmd := exec.Command(indexBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		z.notes = append(z.notes, fmt.Sprintf("zoekt-index failed: %v (%s)", err, truncate(stderr.String(), 200)))
		return z
	}
	z.available = true
	if logf != nil {
		logf("zoekt indexed mirror into %s", idxDir)
	}
	return z
}

// query builds a Zoekt content query for a battery query: literals are quoted
// (substring), regex is passed through (Zoekt parses it as RE2), case forced.
func zoektQueryString(q Query) string {
	caseTok := "case:yes"
	if q.IgnoreCase {
		caseTok = "case:no"
	}
	if q.Literal {
		esc := strings.ReplaceAll(q.Pattern, `\`, `\\`)
		esc = strings.ReplaceAll(esc, `"`, `\"`)
		return caseTok + ` "` + esc + `"`
	}
	return caseTok + " " + q.Pattern
}

// fileSet returns the set of fileIDs Zoekt reports as matching, or ok=false if
// the query errored (skipped in the differential).
func (z *zoektDiff) fileSet(q Query, ft *FileTable) (map[int]bool, bool) {
	if !z.available {
		return nil, false
	}
	cmd := exec.Command(z.searchBin, "-index_dir", z.idxDir, "-l", zoektQueryString(q))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	out := map[int]bool{}
	br := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, rerr := br.ReadBytes('\n')
		if t := strings.TrimSpace(string(line)); t != "" {
			if id := mirrorPathToID(t); id >= 0 {
				out[id] = true
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_, _ = io.Copy(io.Discard, br)
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, false // zoekt errored on this query shape; skip it
	}
	return out, true
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
