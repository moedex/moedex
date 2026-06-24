package rank

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"

	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/query"
	"moedex/internal/symbol"
	"moedex/internal/tokenindex"
)

// Config tunes ranking. Zero values get sensible defaults via New.
type Config struct {
	K1       float64 // BM25 term-saturation (default 1.2)
	B        float64 // BM25 length-normalization (default 0.75)
	RRFk     float64 // Reciprocal Rank Fusion constant (default 60)
	MaxSpans int     // cap on LineSpans emitted per result (default 8)

	// SymbolMinCoverage gates the symbol-name arm: a symbol contributes to a
	// blob's arm score only when it matches at least this fraction of the query's
	// DISTINCT terms. It stops a single coincidental subtoken match from casting a
	// full RRF vote — e.g. query "build deploy stage" matching just "build" in a
	// symbol BuildIndex (coverage 1/3) is gated out, while a symbol whose name IS
	// the query, e.g. "refund" -> func Refund (coverage 1/1), still fires. This
	// was the measured false-boost mode (see eval). The zero value means the 0.5
	// default; pass a negative value to disable the gate (pre-gating behavior).
	SymbolMinCoverage float64

	// PathMinCoverage gates AND enables the filename/path arm: a blob whose file
	// path tokens cover at least this fraction of the query's DISTINCT terms casts
	// an RRF vote. Unlike the symbol/dense arms it needs no external index — every
	// blob has a path — so it is ON by default (the zero value means the 0.6
	// default). A NEGATIVE value disables the arm entirely (and skips building the
	// path index). This is the zoekt-style signal: a query like "federated server"
	// finds mysql_create_federated_server.sql even when "federated" appears only in
	// the filename and never in the content the lexical arm scores.
	//
	// The default 0.6 is higher than the symbol arm's 0.5 by design and was chosen
	// on the pooled gold as the gate at which the arm is PURELY ADDITIVE — it never
	// demotes a query below its no-path score. Path tokens are short and common, so
	// a loose gate lets a one-token filename distractor (a file literally named for
	// a couple of the query words but not the true definer) outvote the real result
	// via RRF; 0.6 requires both terms of a 2-term query (a majority for longer
	// ones), which gated out exactly the three gold queries that regressed at 0.5
	// while keeping every win (e.g. "federated server"/"administration service"
	// 0.0->1.0). See eval's gold sweep.
	PathMinCoverage float64
}

func (c Config) withDefaults() Config {
	if c.K1 <= 0 {
		c.K1 = 1.2
	}
	if c.B <= 0 {
		c.B = 0.75
	}
	if c.RRFk <= 0 {
		c.RRFk = 60
	}
	if c.MaxSpans <= 0 {
		c.MaxSpans = 8
	}
	if c.SymbolMinCoverage == 0 {
		c.SymbolMinCoverage = 0.5
	}
	if c.PathMinCoverage == 0 {
		c.PathMinCoverage = 0.6
	}
	return c
}

// Ranker scores queries against a corpus by fusing a lexical (BM25) arm with an
// optional dense (embedding cosine) arm via Reciprocal Rank Fusion.
//
// The dense arm is optional: when store or emb is nil, ranking is pure-lexical
// and the binary needs no embedding server. This keeps moedex runnable with zero
// external dependencies while letting the hybrid light up when a local embedding
// server is configured.
type Ranker struct {
	ix    *index.Index
	ti    *tokenindex.TokenIndex
	store *embed.Store   // optional; nil disables the dense arm
	emb   embed.Embedder // optional; nil disables the dense arm
	syms  *symbol.Index  // optional; nil disables the symbol-name arm
	cfg   Config

	// pathPostings is a small inverted index from a path token to the blobs whose
	// file path(s) contain it, built once in New (skipped when the path arm is
	// disabled). It lets pathArm gather candidates without scanning every blob per
	// query — the scalable analog of the trigram candidate set for the lexical arm.
	pathPostings map[string][]uint64

	// tokenCandidates makes the lexical arm generate candidates from the token
	// index (ti.Docs) instead of the trigram index. The corpus ranker sets this:
	// its content-only index carries no positional postings, so the trigram path
	// would yield nothing. The two paths are equivalent for BM25 — every blob with
	// tf>0 for a term is in ti.Docs(term) — so this changes only WHERE candidates
	// come from, not which blobs ultimately score. Default false preserves the
	// single-index trigram path exactly.
	tokenCandidates bool
}

// New builds a Ranker. store and emb may both be nil to disable the dense arm.
// The symbol-name arm is opt-in via SetSymbols and stays disabled here so New's
// signature (and existing callers in mcp/eval/tests) are unchanged.
func New(ix *index.Index, ti *tokenindex.TokenIndex, store *embed.Store, emb embed.Embedder, cfg Config) *Ranker {
	r := &Ranker{ix: ix, ti: ti, store: store, emb: emb, cfg: cfg.withDefaults()}
	if r.cfg.PathMinCoverage >= 0 {
		r.buildPathIndex()
	}
	return r
}

// buildPathIndex populates pathPostings: token -> sorted, deduped blob IDs whose
// file path(s) contain that token. Tokens come from tokenindex.Tokenize over each
// FileRef.RelPath, so they split on '/', '.', '_', '-' and camelCase exactly as
// query terms do. A blob is listed once per token even if several of its paths (or
// path segments) contain it. Built once at construction; blobs are visited in
// ascending ID order, so every postings list is already sorted.
func (r *Ranker) buildPathIndex() {
	pp := map[string][]uint64{}
	n := r.ix.NumBlobs()
	for id := uint64(0); id < uint64(n); id++ {
		b := r.ix.Blob(id)
		if b == nil {
			continue
		}
		seen := map[string]bool{}
		for _, f := range b.Files {
			for _, t := range tokenindex.Tokenize([]byte(f.RelPath)) {
				if !seen[t] {
					seen[t] = true
					pp[t] = append(pp[t], id)
				}
			}
		}
	}
	r.pathPostings = pp
}

// SetSymbols installs (or clears, when s is nil) the optional symbol-name index
// that powers the symbol-name ranking arm. nil leaves ranking exactly as it was
// before this arm existed (lexical + optional dense only).
func (r *Ranker) SetSymbols(s *symbol.Index) { r.syms = s }

// SetDense installs (or clears, when either is nil) the dense arm's embedding
// store and embedder. Symmetric with SetSymbols; lets a caller light up the
// dense arm without reconstructing the ranker.
func (r *Ranker) SetDense(store *embed.Store, emb embed.Embedder) {
	r.store = store
	r.emb = emb
}

// UseTokenCandidates switches lexical candidate generation to the token index
// (see Ranker.tokenCandidates). Required for the corpus ranker, whose index has
// no positional postings.
func (r *Ranker) UseTokenCandidates(v bool) { r.tokenCandidates = v }

// Rank scores query and returns up to topK results, best fused score first.
func (r *Ranker) Rank(ctx context.Context, q string, topK int) ([]RankedResult, error) {
	terms := tokenindex.Tokenize([]byte(q))

	lex := r.lexicalArm(terms)       // sorted desc by BM25
	dense, err := r.denseArm(ctx, q) // sorted desc by cosine (nil if no arm)
	if err != nil {
		return nil, err
	}
	sym := r.symbolArm(terms) // sorted desc by symbol-name match (nil if no arm)
	path := r.pathArm(terms)  // sorted desc by filename/path match (nil if disabled)

	// Reciprocal Rank Fusion: each arm contributes 1/(k+rank) at a blob's rank.
	type agg struct {
		blob    uint64
		rrf     float64
		lexical float64
		dense   float64
		hasLex  bool
	}
	byBlob := map[uint64]*agg{}
	get := func(blob uint64) *agg {
		a := byBlob[blob]
		if a == nil {
			a = &agg{blob: blob}
			byBlob[blob] = a
		}
		return a
	}
	for i, s := range lex {
		a := get(s.blob)
		a.rrf += 1.0 / (r.cfg.RRFk + float64(i+1))
		a.lexical = s.score
		a.hasLex = true
	}
	denseSpan := map[uint64][]LineSpan{}
	for i, s := range dense {
		a := get(s.blob)
		a.rrf += 1.0 / (r.cfg.RRFk + float64(i+1))
		if s.score > a.dense {
			a.dense = s.score
		}
		denseSpan[s.blob] = append(denseSpan[s.blob], s.span)
	}
	// Third RRF arm: a blob whose symbol NAME matches the query is a strong
	// signal. It folds into the same per-blob rrf aggregate as lexical/dense
	// (1/(k+rank)); the symbol's name line seeds context assembly. There is no
	// new RankedResult field — the contribution lives entirely in Score.
	symSpan := map[uint64][]LineSpan{}
	for i, s := range sym {
		a := get(s.blob)
		a.rrf += 1.0 / (r.cfg.RRFk + float64(i+1))
		symSpan[s.blob] = append(symSpan[s.blob], s.span)
	}
	// Fourth RRF arm: a blob whose file PATH matches the query (the zoekt-style
	// filename signal). Folds into the same per-blob rrf aggregate; its span is the
	// file head so a path-only hit still carries context.
	pathSpan := map[uint64][]LineSpan{}
	for i, s := range path {
		a := get(s.blob)
		a.rrf += 1.0 / (r.cfg.RRFk + float64(i+1))
		pathSpan[s.blob] = append(pathSpan[s.blob], s.span)
	}

	results := make([]RankedResult, 0, len(byBlob))
	for blob, a := range byBlob {
		b := r.ix.Blob(blob)
		spans := r.lexicalSpans(b, terms)
		spans = append(spans, denseSpan[blob]...)
		spans = append(spans, symSpan[blob]...)
		spans = append(spans, pathSpan[blob]...)
		results = append(results, RankedResult{
			Blob:      blob,
			Files:     b.Files,
			Score:     a.rrf,
			Lexical:   a.lexical,
			Dense:     a.dense,
			LineSpans: mergeSpans(spans, r.cfg.MaxSpans),
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Blob < results[j].Blob // deterministic tie-break
	})
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

type lexScore struct {
	blob  uint64
	score float64
}

// lexicalArm generates candidate blobs via the trigram query path (one literal
// query per query term, unioned) and BM25-scores each candidate. Returns the
// candidates sorted by descending BM25 score.
func (r *Ranker) lexicalArm(terms []string) []lexScore {
	cand := r.candidateBlobs(terms)
	if len(cand) == 0 {
		return nil
	}
	n := float64(r.ti.NumDocs())
	avgdl := r.ti.AvgDocLen()
	out := make([]lexScore, 0, len(cand))
	for _, blob := range cand {
		score := 0.0
		dl := float64(r.ti.DocLen(blob))
		for _, t := range terms {
			tf := float64(r.ti.TermFreq(t, blob))
			if tf == 0 {
				continue
			}
			df := float64(r.ti.DocFreq(t))
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			denom := tf + r.cfg.K1*(1-r.cfg.B+r.cfg.B*dl/avgdl)
			score += idf * (tf * (r.cfg.K1 + 1)) / denom
		}
		if score > 0 {
			out = append(out, lexScore{blob: blob, score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].blob < out[j].blob
	})
	return out
}

// candidateBlobs unions the trigram candidate sets of each query term (terms
// with at least one trigram). Terms too short to yield a trigram are skipped for
// candidate generation but still participate in BM25 scoring. If no term yields
// a usable trigram query, every blob is a candidate (sound fallback).
func (r *Ranker) candidateBlobs(terms []string) []uint64 {
	if r.tokenCandidates {
		return r.tokenCandidateBlobs(terms)
	}
	seen := map[uint64]bool{}
	used := false
	for _, t := range terms {
		if len(t) < 3 {
			continue
		}
		q, err := query.FromRegexp(regexp.QuoteMeta(t))
		if err != nil {
			continue
		}
		used = true
		for _, b := range q.Eval(r.ix) {
			seen[b] = true
		}
	}
	if !used {
		all := make([]uint64, r.ix.NumBlobs())
		for i := range all {
			all[i] = uint64(i)
		}
		return all
	}
	out := make([]uint64, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// tokenCandidateBlobs unions ti.Docs over the query terms — the blobs that
// actually contain each term. This is the exact BM25 candidate set and needs no
// trigram index. A term too short or absent simply contributes nothing; if no
// term matches any document, the result is empty (BM25 would score nothing
// anyway), so there is no all-blobs fallback to do.
func (r *Ranker) tokenCandidateBlobs(terms []string) []uint64 {
	seen := map[uint64]bool{}
	for _, t := range terms {
		for _, b := range r.ti.Docs(t) {
			seen[b] = true
		}
	}
	out := make([]uint64, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

type denseScore struct {
	blob  uint64
	score float64
	span  LineSpan
}

// denseArm runs cosine search over chunk embeddings, keeping the best-scoring
// chunk per blob. Returns nil when no dense arm is configured.
func (r *Ranker) denseArm(ctx context.Context, q string) ([]denseScore, error) {
	if r.store == nil || r.emb == nil {
		return nil, nil
	}
	hits, err := r.store.Search(ctx, r.emb, q, 64)
	if err != nil {
		return nil, err
	}
	best := map[uint64]denseScore{}
	order := []uint64{}
	for _, h := range hits {
		s := float64(h.Score)
		if cur, ok := best[h.Chunk.Blob]; !ok || s > cur.score {
			if !ok {
				order = append(order, h.Chunk.Blob)
			}
			best[h.Chunk.Blob] = denseScore{
				blob:  h.Chunk.Blob,
				score: s,
				span:  LineSpan{StartLine: h.Chunk.StartLine, EndLine: h.Chunk.EndLine},
			}
		}
	}
	out := make([]denseScore, 0, len(best))
	for _, b := range order {
		out = append(out, best[b])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].blob < out[j].blob
	})
	return out, nil
}

type symScore struct {
	blob  uint64
	score float64
	span  LineSpan
}

// symbolArm ranks blobs by how strongly their defined symbol NAMES match the
// query. It is disabled (returns nil) when no symbol index is installed.
//
// Matching rule: each symbol's Name is tokenized with tokenindex.Tokenize (the
// SAME tokenizer the query terms came from, so "RefundOrder" -> {refund, order,
// refundorder} matches a query term "refund"). A symbol contributes a hit for
// every DISTINCT query term that appears among its name subtokens. A blob's arm
// score is the sum of those hits across all QUALIFYING symbols, so a blob that
// defines a symbol named like the query outranks one that merely mentions the word.
//
// Coverage gate: a symbol qualifies only when it matches at least
// cfg.SymbolMinCoverage of the query's distinct terms. This suppresses the
// measured false-boost mode where one common term coincidentally matched a
// symbol subtoken (query "build deploy stage" hitting BuildIndex on "build",
// coverage 1/3) and cast a full RRF vote for an irrelevant blob, while a symbol
// whose name covers the query intent ("refund" -> func Refund, coverage 1/1)
// still fires. See Config.SymbolMinCoverage.
//
// The contributed LineSpan is the name line of the best-matching symbol (its
// NameStart mapped to a 1-based line via Blob.LineOf), so the definition line
// feeds context assembly.
func (r *Ranker) symbolArm(terms []string) []symScore {
	if r.syms == nil || len(terms) == 0 {
		return nil
	}
	want := make(map[string]bool, len(terms))
	for _, t := range terms {
		if t != "" {
			want[t] = true
		}
	}
	if len(want) == 0 {
		return nil
	}

	// A symbol must match at least this many distinct query terms to qualify.
	// ceil(coverage * nTerms), floored at 1 so a positive threshold always
	// requires a real match.
	nTerms := len(want)
	minHits := int(math.Ceil(r.cfg.SymbolMinCoverage * float64(nTerms)))
	if minHits < 1 {
		minHits = 1
	}

	out := make([]symScore, 0)
	for blob := uint64(0); blob < uint64(r.ix.NumBlobs()); blob++ {
		syms := r.syms.Symbols(blob)
		if len(syms) == 0 {
			continue
		}
		var total float64
		bestHits := -1
		var bestSym symbol.Symbol
		for _, s := range syms {
			if s.Name == "" {
				continue // unnamed (func literals) cannot match a name
			}
			subtoks := tokenindex.Tokenize([]byte(s.Name))
			seen := map[string]bool{}
			hits := 0
			for _, st := range subtoks {
				if want[st] && !seen[st] {
					seen[st] = true
					hits++
				}
			}
			if hits < minHits {
				continue // coverage gate: too weak a match to vote
			}
			total += float64(hits)
			if hits > bestHits {
				bestHits = hits
				bestSym = s
			}
		}
		if total == 0 {
			continue
		}
		b := r.ix.Blob(blob)
		line := b.LineOf(bestSym.NameStart)
		out = append(out, symScore{
			blob:  blob,
			score: total,
			span:  LineSpan{StartLine: line, EndLine: line},
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].blob < out[j].blob
	})
	return out
}

type pathScore struct {
	blob  uint64
	score float64
	span  LineSpan
}

// pathArm ranks blobs by how strongly their file PATH matches the query — the
// zoekt-style filename signal. Disabled (returns nil) when PathMinCoverage is
// negative or the path index was not built.
//
// Matching mirrors the symbol arm: each candidate blob's path is tokenized with
// the same tokenindex.Tokenize as the query, and a blob scores by the number of
// DISTINCT query terms its best path covers. Candidates come from pathPostings
// (the blobs sharing at least one path token with the query), so a query never
// scans the whole corpus. The coverage gate (cfg.PathMinCoverage of the distinct
// query terms) keeps a single common token like "service" or "src" from voting
// for every path that contains it.
//
// A multi-file blob (content dedup) is scored by its BEST-matching path. The
// contributed LineSpan is the file head (line 1): a path match points at a file,
// not a content line, and seeding line 1 gives the context assembler something to
// show for a blob the lexical arm never surfaced (the whole point — "federated"
// lives only in the filename).
func (r *Ranker) pathArm(terms []string) []pathScore {
	if r.cfg.PathMinCoverage < 0 || r.pathPostings == nil || len(terms) == 0 {
		return nil
	}
	want := make(map[string]bool, len(terms))
	for _, t := range terms {
		if t != "" {
			want[t] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	minHits := int(math.Ceil(r.cfg.PathMinCoverage * float64(len(want))))
	if minHits < 1 {
		minHits = 1
	}

	// Candidate blobs: any blob whose path carries at least one query term.
	cand := map[uint64]bool{}
	for t := range want {
		for _, b := range r.pathPostings[t] {
			cand[b] = true
		}
	}

	out := make([]pathScore, 0, len(cand))
	for blob := range cand {
		b := r.ix.Blob(blob)
		if b == nil {
			continue
		}
		bestHits := 0
		for _, f := range b.Files {
			seen := map[string]bool{}
			hits := 0
			for _, t := range tokenindex.Tokenize([]byte(f.RelPath)) {
				if want[t] && !seen[t] {
					seen[t] = true
					hits++
				}
			}
			if hits > bestHits {
				bestHits = hits
			}
		}
		if bestHits < minHits {
			continue // coverage gate: too weak a path match to vote
		}
		out = append(out, pathScore{
			blob:  blob,
			score: float64(bestHits),
			span:  LineSpan{StartLine: 1, EndLine: 1},
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].blob < out[j].blob
	})
	return out
}

// lexicalSpans finds lines containing any query term and groups consecutive
// matching lines into spans (1-based inclusive). Matching is case-insensitive
// substring against the canonical (already lowercased) terms.
func (r *Ranker) lexicalSpans(b *index.Blob, terms []string) []LineSpan {
	if len(terms) == 0 || len(b.Content) == 0 {
		return nil
	}
	lines := strings.Split(string(b.Content), "\n")
	var spans []LineSpan
	for i, line := range lines {
		low := strings.ToLower(line)
		hit := false
		for _, t := range terms {
			if t != "" && strings.Contains(low, t) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		ln := i + 1
		if n := len(spans); n > 0 && spans[n-1].EndLine >= ln-1 {
			spans[n-1].EndLine = ln
		} else {
			spans = append(spans, LineSpan{StartLine: ln, EndLine: ln})
		}
	}
	return spans
}

// mergeSpans sorts, merges overlapping/adjacent spans, and caps the count.
func mergeSpans(spans []LineSpan, max int) []LineSpan {
	if len(spans) == 0 {
		return nil
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartLine != spans[j].StartLine {
			return spans[i].StartLine < spans[j].StartLine
		}
		return spans[i].EndLine < spans[j].EndLine
	})
	out := []LineSpan{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s.StartLine <= last.EndLine+1 {
			if s.EndLine > last.EndLine {
				last.EndLine = s.EndLine
			}
			continue
		}
		out = append(out, s)
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}
