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

	// Fusion selects how the per-arm signals become a result's final Score. The
	// zero value (FusionRRF) is the historical pure-RRF behavior — a byte-identical
	// no-op relative to the pre-reranker code path. FusionLinear re-scores each
	// fused candidate with an installed LinearReranker (SetReranker) over its
	// FeatureVector; it falls back to RRF per candidate when no model is installed,
	// so selecting the mode without a model can never produce all-zero scores. This
	// is the OPTIONAL learned-reranker fusion mode (research/learned-reranker.md);
	// it is post-fusion and recall-preserving (no candidate is dropped), so the
	// ripgrep parity invariant — which lives in internal/parity and internal/search
	// and never passes through rank — is structurally unaffected.
	Fusion Fusion

	// SymbolMinCoverage gates the symbol-name arm: a symbol contributes to a
	// blob's arm score only when it matches at least this fraction of the query's
	// DISTINCT terms. It stops a single coincidental subtoken match from casting a
	// full RRF vote — e.g. query "build deploy stage" matching just "build" in a
	// symbol BuildIndex (coverage 1/3) is gated out, while a symbol whose name IS
	// the query, e.g. "refund" -> func Refund (coverage 1/1), still fires. This
	// was the measured false-boost mode (see eval). A negative value disables the
	// gate (pre-gating behavior).
	//
	// The zero value means the 0.67 default. It was 0.5 until the path arm landed;
	// once the path arm carries the filename signal, the looser 0.5 gate let the
	// symbol arm cast marginal votes the path arm already covers, which cost NDCG in
	// the combined stack. Raising to 0.67 (only near-exact symbol-name matches vote)
	// makes the symbol arm additive-on-recall and ~neutral-on-NDCG alongside path;
	// it is the plateau on the gold (0.67/0.75/1.0 score identically), so a stable
	// choice rather than a fragile peak. See eval's symbol-coverage sweep.
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

	// DenseMinScore gates the dense (embedding cosine) arm: a dense chunk casts its
	// RRF vote only when its cosine similarity is at least this value. The dense arm
	// pulls a fixed candidate pool (top-64 chunks) and, UNGATED, every chunk votes
	// regardless of similarity. Measured on the pooled gold this is CONDITIONALLY
	// good: dense lifts the synonym-gap / agent stratum (where a semantic match is the
	// only signal — corpusGoldAgentNL: +dense NDCG 0.14->0.34) but REGRESSES the
	// answerable gold (where lexical/path/symbol already win and low-cosine dense
	// chunks are noise that displaces correct hits — 0.932->0.923). The threshold
	// keeps the confident semantic matches and drops the noise.
	//
	// Cosine is in [-1,1] and SCALE-DEPENDENT on the embedder, so the default is
	// calibrated for the bundled model (embed/onnx); a different Embedder may want a
	// different value. The zero value means the default (see withDefaults); a NEGATIVE
	// value DISABLES the gate (every dense chunk votes — the pre-gate behavior, used by
	// hermetic fixture tests whose synthetic embedder has a different cosine scale).
	// The gate only matters when the dense arm is active (store+emb set).
	DenseMinScore float64

	// DenseMinQueryTerms gates the dense arm by QUERY LENGTH: the dense arm runs only
	// when the query has at least this many DISTINCT terms. This is the additive
	// mechanism the score gate could not provide. Short keyword queries ("generate
	// csr", "void transaction") are the lexical/symbol/path arms' home turf, where the
	// answer is already found and low-cosine dense chunks are pure noise that displaces
	// it; longer natural-language queries ("migrate a recurring subscription from one
	// processor to another") are where those arms are starved and a semantic match is
	// the only signal. Gating dense to fire only on the long queries makes it PURELY
	// ADDITIVE on the pooled gold — it never touches the (short) answerable queries
	// (no regression) yet keeps the full lift on the (long) synonym-gap stratum. Unlike
	// DenseMinScore this is embedder-agnostic (it never looks at cosine scale).
	//
	// The zero value means the default (see withDefaults); a NEGATIVE value disables
	// the gate (dense runs for every query — the pre-gate behavior, used by hermetic
	// fixture tests whose short synthetic queries exercise the dense plumbing).
	DenseMinQueryTerms int
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
		c.SymbolMinCoverage = 0.67
	}
	if c.PathMinCoverage == 0 {
		c.PathMinCoverage = 0.6
	}
	if c.DenseMinQueryTerms == 0 {
		// Dense fires only on queries with >= 5 distinct terms. Chosen on the pooled
		// gold as the smallest value that is PURELY ADDITIVE: the (short) answerable
		// queries are untouched — identical to the no-dense baseline — while the (long)
		// synonym-gap stratum keeps its full lift (+0.21 NDCG / +0.42 recall). See
		// eval's TestCorpusDenseGateSweep. A negative value disables the gate.
		c.DenseMinQueryTerms = 5
	}
	// DenseMinScore intentionally has NO default bump: the cosine gate is an opt-in,
	// embedder-coupled knob (0 leaves it off; the Rank check requires > 0). The
	// query-length gate above is the embedder-agnostic default.
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
	store *embed.Store    // optional; nil disables the dense arm
	emb   embed.Embedder  // optional; nil disables the dense arm
	syms  *symbol.Index   // optional; nil disables the symbol-name arm
	rr    *LinearReranker // optional; nil keeps pure RRF even when Fusion==FusionLinear
	cfg   Config

	// pathPostings is a small inverted index from a path token to the blobs whose
	// file path(s) contain it, built once in New (skipped when the path arm is
	// disabled). It lets pathArm gather candidates without scanning every blob per
	// query — the scalable analog of the trigram candidate set for the lexical arm.
	//
	// pathInfo mirrors symInfo for the path arm: it holds, per blob, each FileRef's
	// DISTINCT path subtokens (tokenized once here, never per query), so pathArm
	// never re-tokenizes a path on every query.
	pathPostings map[string][]uint64
	pathInfo     map[uint64][]coverageEntry

	// symInfo / symPostings power the symbol-name arm without an every-query
	// full-corpus scan (the same technique as pathPostings). Built once in
	// SetSymbols: symPostings maps a symbol-name subtoken to the blobs defining a
	// symbol whose name carries it (candidate gathering), and symInfo holds, per
	// such blob, each named symbol's DISTINCT name subtokens (tokenized once here,
	// never per query) plus its name line (as a coverageEntry's span). Both nil
	// when no symbol index is set.
	symInfo     map[uint64][]coverageEntry
	symPostings map[string][]uint64

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

// buildPathIndex populates pathPostings (token -> sorted, deduped blob IDs whose
// file path(s) contain that token) and pathInfo (blob -> each FileRef's distinct
// path subtokens, kept as a coverageEntry per FileRef — never merged across a
// blob's paths — so pathArm can still score a SHA-deduped blob by its BEST
// single path, exactly as a fresh per-query tokenize would). Every entry's span
// is the file head (line 1): a path match points at a file, not a content
// line. Tokens come from tokenindex.Tokenize over each FileRef.RelPath, so they
// split on '/', '.', '_', '-' and camelCase exactly as query terms do. A blob is
// listed once per token in pathPostings even if several of its paths (or path
// segments) contain it. Built once at construction; blobs are visited in
// ascending ID order, so every postings list is already sorted.
func (r *Ranker) buildPathIndex() {
	pp := map[string][]uint64{}
	info := make(map[uint64][]coverageEntry)
	n := r.ix.NumBlobs()
	for id := uint64(0); id < uint64(n); id++ {
		b := r.ix.Blob(id)
		if b == nil {
			continue
		}
		seen := map[string]bool{}
		entries := make([]coverageEntry, 0, len(b.Files))
		for _, f := range b.Files {
			uniq := distinctTokens(tokenindex.Tokenize([]byte(f.RelPath)))
			entries = append(entries, coverageEntry{subtoks: uniq, span: LineSpan{StartLine: 1, EndLine: 1}})
			for _, t := range uniq {
				if !seen[t] {
					seen[t] = true
					pp[t] = append(pp[t], id)
				}
			}
		}
		if len(entries) > 0 {
			info[id] = entries
		}
	}
	r.pathPostings = pp
	r.pathInfo = info
}

// SetSymbols installs (or clears, when s is nil) the optional symbol-name index
// that powers the symbol-name ranking arm. nil leaves ranking exactly as it was
// before this arm existed (lexical + optional dense only). It (re)builds the arm's
// inverted lookup so symbolArm never scans the whole corpus per query.
func (r *Ranker) SetSymbols(s *symbol.Index) {
	r.syms = s
	r.buildSymbolIndex()
}

// SetDense installs (or clears, when either is nil) the dense arm's embedding
// store and embedder. Symmetric with SetSymbols; lets a caller light up the
// dense arm without reconstructing the ranker.
func (r *Ranker) SetDense(store *embed.Store, emb embed.Embedder) {
	r.store = store
	r.emb = emb
}

// SetReranker installs (or clears, when m is nil) the optional learned reranker
// used by FusionLinear. With no reranker installed, FusionLinear falls back to RRF
// per candidate, so the mode is safe to select before a model is trained. Symmetric
// with SetSymbols/SetDense; lets a caller A/B RRF vs learned on one ranker.
func (r *Ranker) SetReranker(m *LinearReranker) { r.rr = m }

// SetFusion sets the fusion mode in place (without reconstructing the ranker).
func (r *Ranker) SetFusion(f Fusion) { r.cfg.Fusion = f }

// UseTokenCandidates switches lexical candidate generation to the token index
// (see Ranker.tokenCandidates). Required for the corpus ranker, whose index has
// no positional postings.
func (r *Ranker) UseTokenCandidates(v bool) { r.tokenCandidates = v }

// candidate is one fused blob: its FeatureVector (the per-arm signals captured at
// fusion time) plus the salient spans. Both Rank and Features build candidates via
// the SAME fuse() path, so the features a model is TRAINED on are byte-for-byte the
// features it SCORES at inference — no divergence between train and serve.
type candidate struct {
	blob  uint64
	feat  FeatureVector
	spans []LineSpan
}

// fuse runs the four arms and aggregates them into per-blob candidates. It captures
// the RRF score AND every per-arm raw score/rank into each candidate's
// FeatureVector. RRF itself only needs feat.RRFScore; the extra fields are inert
// under FusionRRF and feed the learned reranker under FusionLinear.
func (r *Ranker) fuse(ctx context.Context, q string) ([]candidate, error) {
	terms := tokenindex.Tokenize([]byte(q))

	lex := r.lexicalArm(terms) // sorted desc by BM25
	var dense []armScore       // sorted desc by cosine (nil if no arm / gated out)
	if r.denseAllowedFor(q, terms) {
		var err error
		if dense, err = r.denseArm(ctx, q); err != nil {
			return nil, err
		}
	}
	sym := r.symbolArm(terms) // sorted desc by symbol-name match (nil if no arm)
	path := r.pathArm(terms)  // sorted desc by filename/path match (nil if disabled)

	type agg struct {
		blob  uint64
		feat  FeatureVector
		spans []LineSpan
	}
	byBlob := map[uint64]*agg{}
	order := make([]uint64, 0) // first-seen order, so Rank's result-assembly order is stable
	get := func(blob uint64) *agg {
		a := byBlob[blob]
		if a == nil {
			a = &agg{blob: blob}
			byBlob[blob] = a
			order = append(order, blob)
		}
		return a
	}

	for i, s := range lex {
		a := get(s.blob)
		a.feat.RRFScore += 1.0 / (r.cfg.RRFk + float64(i+1))
		a.feat.BM25 = s.score
		a.feat.LexRank = i + 1
	}
	for i, s := range dense {
		// Dense confidence gate: a too-weak cosine is noise (it displaces correct hits
		// on keyword queries) so it does not vote. A non-positive DenseMinScore leaves
		// the arm ungated. dense is sorted desc by score, so gated chunks are the tail.
		if r.cfg.DenseMinScore > 0 && s.score < r.cfg.DenseMinScore {
			continue
		}
		a := get(s.blob)
		// RRF vote uses i+1 (the chunk's position in the score-sorted arm), exactly as
		// before — the rank feature mirrors that same position so the feature and the
		// RRF contribution agree, and the default RRF path is byte-identical.
		a.feat.RRFScore += 1.0 / (r.cfg.RRFk + float64(i+1))
		if s.score > a.feat.DenseCosine {
			a.feat.DenseCosine = s.score
		}
		if a.feat.DenseRank == AbsentRank {
			a.feat.DenseRank = i + 1
		}
		a.spans = append(a.spans, s.span)
	}
	for i, s := range sym {
		a := get(s.blob)
		a.feat.RRFScore += 1.0 / (r.cfg.RRFk + float64(i+1))
		a.feat.SymbolCoverage = s.score
		a.feat.SymRank = i + 1
		a.spans = append(a.spans, s.span)
	}
	for i, s := range path {
		a := get(s.blob)
		a.feat.RRFScore += 1.0 / (r.cfg.RRFk + float64(i+1))
		a.feat.PathCoverage = s.score
		a.feat.PathRank = i + 1
		a.spans = append(a.spans, s.span)
	}

	out := make([]candidate, 0, len(order))
	for _, blob := range order {
		a := byBlob[blob]
		out = append(out, candidate{blob: blob, feat: a.feat, spans: a.spans})
	}
	return out, nil
}

// score turns a candidate's features into its final Score per the active fusion
// mode. Under FusionRRF (the default) it is exactly the RRF aggregate; under
// FusionLinear with a valid model it is the learned re-score; FusionLinear with no
// valid model falls back to RRF.
func (r *Ranker) score(feat FeatureVector) float64 {
	if r.cfg.Fusion == FusionLinear && r.rr.Valid() {
		return r.rr.Score(feat, r.cfg.RRFk)
	}
	return feat.RRFScore
}

// Rank scores query and returns up to topK results, best fused score first.
func (r *Ranker) Rank(ctx context.Context, q string, topK int) ([]RankedResult, error) {
	terms := tokenindex.Tokenize([]byte(q))
	cands, err := r.fuse(ctx, q)
	if err != nil {
		return nil, err
	}

	results := make([]RankedResult, 0, len(cands))
	for _, c := range cands {
		b := r.ix.Blob(c.blob)
		results = append(results, RankedResult{
			Blob:    c.blob,
			Files:   b.Files,
			Score:   r.score(c.feat),
			Lexical: c.feat.BM25,
			Dense:   c.feat.DenseCosine,
			// Arm-contributed spans (symbol name line, dense chunk) only; the
			// expensive lexical spans are added below, AFTER truncation.
			LineSpans: c.spans,
		})
	}
	sortByScoreThenBlob(results, func(r RankedResult) float64 { return r.Score }, func(r RankedResult) uint64 { return r.Blob })
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	// Lexical spans scan a blob's FULL content (lowercase + substring match), so
	// computing them per candidate is O(candidates) whole-blob scans — the dominant
	// cost of a query, and wasted for every blob the topK cut drops. Defer it to the
	// survivors here: identical output (the sort keys are Score/Blob, never spans),
	// O(topK) scans instead of O(candidates). With topK unset (<=0) the loop still
	// covers every result, matching the prior behavior exactly.
	for i := range results {
		b := r.ix.Blob(results[i].Blob)
		spans := r.lexicalSpans(b, terms)
		spans = append(spans, results[i].LineSpans...)
		results[i].LineSpans = mergeSpans(spans, r.cfg.MaxSpans)
	}
	return results, nil
}

// Features runs the four arms for q and returns the per-candidate FeatureVector
// keyed by blob, WITHOUT applying any fusion mode. It is the training/analysis
// entry point: the eval extracts these to build labeled rows, guaranteeing the
// trained model sees exactly the features Rank scores. Ordering follows fuse()'s
// first-seen order.
func (r *Ranker) Features(ctx context.Context, q string) ([]BlobFeatures, error) {
	cands, err := r.fuse(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]BlobFeatures, 0, len(cands))
	for _, c := range cands {
		out = append(out, BlobFeatures{Blob: c.blob, Files: r.ix.Blob(c.blob).Files, Feat: c.feat})
	}
	return out, nil
}

// RRFk exposes the configured RRF constant so callers extracting Features can flatten
// the per-arm rank features on the same scale the ranker uses (FeatureVector.Slice).
func (r *Ranker) RRFk() float64 { return r.cfg.RRFk }

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
	// Dedup terms: Tokenize only dedups within a single word-run, so a repeated
	// query term (e.g. "refund refund") must not contribute its BM25 term twice.
	seen := make(map[string]bool, len(terms))
	distinct := make([]string, 0, len(terms))
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			distinct = append(distinct, t)
		}
	}
	n := float64(r.ti.NumDocs())
	avgdl := r.ti.AvgDocLen()
	// idf depends only on the term and the corpus, not on the candidate blob,
	// so it is computed once per distinct term.
	//
	// Each term's posting list is ALSO resolved once, here. Calling
	// ti.TermFreq(t, blob) per (blob, term) pair would re-run the term binary
	// search over the whole dictionary every time; with 17.6M terms that is
	// ~24 string comparisons per pair. Instead each term keeps a cursor into
	// its own ascending posting list, and because cand is ascending too, the
	// whole nested loop is one merge walk: O(len(cand) + sum of df).
	idf := make([]float64, len(distinct))
	lists := make([]tokenindex.PostingList, len(distinct))
	cursor := make([]int, len(distinct))
	for i, t := range distinct {
		lists[i] = r.ti.Postings(t)
		df := float64(lists[i].Len())
		idf[i] = math.Log(1 + (n-df+0.5)/(df+0.5))
	}

	out := make([]lexScore, 0, len(cand))
	for _, blob := range cand {
		score := 0.0
		dl := float64(r.ti.DocLen(blob))
		denomBase := r.cfg.K1 * (1 - r.cfg.B + r.cfg.B*dl/avgdl)
		for i := range distinct {
			p := lists[i]
			// Advance this term's cursor to the first posting >= blob.
			c := cursor[i]
			for c < p.Len() && p.Blob(c) < blob {
				c++
			}
			cursor[i] = c
			if c >= p.Len() || p.Blob(c) != blob {
				continue // tf == 0
			}
			tf := float64(p.TF(c))
			denom := tf + denomBase
			score += idf[i] * (tf * (r.cfg.K1 + 1)) / denom
		}
		if score > 0 {
			out = append(out, lexScore{blob: blob, score: score})
		}
	}
	sortByScoreThenBlob(out, func(s lexScore) float64 { return s.score }, func(s lexScore) uint64 { return s.blob })
	return out
}

// candidateBlobs unions the trigram candidate sets of each query term (terms
// with at least one trigram). Terms too short to yield a trigram are skipped for
// candidate generation but still participate in BM25 scoring. If no term yields
// a usable trigram query (e.g. every term is < 3 bytes), it falls back to the
// exact token-index candidate set (tokenCandidateBlobs) instead of every blob in
// the corpus: that fallback is still sound for BM25 (a blob with tf=0 for every
// term scores 0 and is dropped anyway) but bounded by the blobs that actually
// contain a term, not the corpus size — a crafted all-short-token query on the
// trigram path (single-repo moedex-mcp) can no longer force an O(NumBlobs) scan.
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
		return r.tokenCandidateBlobs(terms)
	}
	out := make([]uint64, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// tokenCandidateBlobs unions the term postings over the query terms — the blobs
// that actually contain each term. This is the exact BM25 candidate set and
// needs no trigram index. A term too short or absent simply contributes
// nothing; if no term matches any document, the result is empty (BM25 would
// score nothing anyway), so there is no all-blobs fallback to do.
//
// This drops the per-term []uint64 allocation and sort that ti.Docs performed;
// the ascending output contract is unchanged.
func (r *Ranker) tokenCandidateBlobs(terms []string) []uint64 {
	seen := map[uint64]bool{}
	for _, t := range terms {
		p := r.ti.Postings(t)
		for i := 0; i < p.Len(); i++ {
			seen[p.Blob(i)] = true
		}
	}
	out := make([]uint64, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// denseAllowedFor reports whether the dense arm should run for this query given the
// query-length gate (Config.DenseMinQueryTerms). Short keyword queries are the
// lexical/symbol/path arms' home turf, where dense only adds noise; the dense arm is
// reserved for longer natural-language queries where a semantic match is the only
// signal. A non-positive DenseMinQueryTerms disables the gate (dense always runs).
func (r *Ranker) denseAllowedFor(q string, terms []string) bool {
	if r.cfg.DenseMinQueryTerms <= 0 {
		return true // gate fully disabled (used by eval/measurement)
	}
	// Skip dense for a single-token identifier lookup (e.g. "DropCatchCheckout"): the
	// camelCase tokenizer expands one identifier into many subtokens, which would
	// otherwise trip the term-count gate below and pay for a full embedding scan that
	// the lexical/symbol arms already serve precisely. Dense earns its cost on
	// multi-word natural-language queries, which always have >= 2 whitespace words.
	if len(strings.Fields(q)) < 2 {
		return false
	}
	seen := make(map[string]bool, len(terms))
	for _, t := range terms {
		if t != "" {
			seen[t] = true
		}
	}
	return len(seen) >= r.cfg.DenseMinQueryTerms
}

// denseArm runs cosine search over chunk embeddings, keeping the best-scoring
// chunk per blob. Returns nil when no dense arm is configured.
func (r *Ranker) denseArm(ctx context.Context, q string) ([]armScore, error) {
	if r.store == nil || r.emb == nil {
		return nil, nil
	}
	hits, err := r.store.Search(ctx, r.emb, q, 64)
	if err != nil {
		return nil, err
	}
	best := map[uint64]armScore{}
	order := []uint64{}
	for _, h := range hits {
		// Unlike the lexical/symbol/path arms, a dense chunk's blob ID comes from
		// r.store (an external structure, e.g. a sidecar built against a different
		// or larger index) rather than being derived from r.ix, so it is not
		// structurally guaranteed to name one of r.ix's blobs. Drop anything r.ix
		// can't resolve here so every candidate this arm contributes is safe for
		// fuse/Rank/Features to dereference via r.ix.Blob, the same invariant the
		// other three arms already provide by construction.
		if r.ix.Blob(h.Chunk.Blob) == nil {
			continue
		}
		s := float64(h.Score)
		if cur, ok := best[h.Chunk.Blob]; !ok || s > cur.score {
			if !ok {
				order = append(order, h.Chunk.Blob)
			}
			best[h.Chunk.Blob] = armScore{
				blob:  h.Chunk.Blob,
				score: s,
				span:  LineSpan{StartLine: h.Chunk.StartLine, EndLine: h.Chunk.EndLine},
			}
		}
	}
	out := make([]armScore, 0, len(best))
	for _, b := range order {
		out = append(out, best[b])
	}
	sortByScoreThenBlob(out, func(a armScore) float64 { return a.score }, func(a armScore) uint64 { return a.blob })
	return out, nil
}

// buildSymbolIndex precomputes the symbol-name arm's lookup from the installed
// symbol index. It runs ONE corpus pass (in SetSymbols, at load/reload), the same
// work the old per-query scan did every query: tokenize each symbol name, record
// its distinct subtokens + name line (as a coverageEntry) in symInfo, and union
// the subtoken -> blobs postings in symPostings. With nil syms it clears both
// (arm disabled).
func (r *Ranker) buildSymbolIndex() {
	if r.syms == nil {
		r.symInfo, r.symPostings = nil, nil
		return
	}
	info := map[uint64][]coverageEntry{}
	postSets := map[string]map[uint64]struct{}{}
	n := r.ix.NumBlobs()
	for blob := uint64(0); blob < uint64(n); blob++ {
		syms := r.syms.Symbols(blob)
		if len(syms) == 0 {
			continue
		}
		b := r.ix.Blob(blob)
		if b == nil {
			continue
		}
		var entries []coverageEntry
		for _, s := range syms {
			if s.Name == "" {
				continue // unnamed (func literals) cannot match a name
			}
			uniq := distinctTokens(tokenindex.Tokenize([]byte(s.Name)))
			if len(uniq) == 0 {
				continue
			}
			line := b.LineOf(s.NameStart)
			entries = append(entries, coverageEntry{subtoks: uniq, span: LineSpan{StartLine: line, EndLine: line}})
			for _, t := range uniq {
				set := postSets[t]
				if set == nil {
					set = map[uint64]struct{}{}
					postSets[t] = set
				}
				set[blob] = struct{}{}
			}
		}
		if len(entries) > 0 {
			info[blob] = entries
		}
	}
	posts := make(map[string][]uint64, len(postSets))
	for t, set := range postSets {
		ids := make([]uint64, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		posts[t] = ids
	}
	r.symInfo = info
	r.symPostings = posts
}

// distinctTokens returns toks de-duplicated, preserving first-seen order, in a
// fresh slice (never aliases the input). Symbol-name subtoken sets are tiny, so
// the linear-probe dedup is cheaper than a map for the common short cases.
func distinctTokens(toks []string) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		dup := false
		for _, o := range out {
			if o == t {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out
}

// symbolArm ranks blobs by how strongly their defined symbol NAMES match the
// query. It is disabled (returns nil) when no symbol index is installed.
//
// Matching rule: each symbol's Name is tokenized with tokenindex.Tokenize (the
// SAME tokenizer the query terms came from, so "RefundOrder" -> {refund, order,
// refundorder} matches a query term "refund"). A symbol contributes a hit for
// every DISTINCT query term that appears among its name subtokens. A blob's arm
// score is the sum of those hits across all QUALIFYING symbols (sumQualifying),
// so a blob that defines a symbol named like the query outranks one that merely
// mentions the word.
//
// Coverage gate: a symbol qualifies only when it matches at least
// cfg.SymbolMinCoverage of the query's distinct terms. This suppresses the
// measured false-boost mode where one common term coincidentally matched a
// symbol subtoken (query "build deploy stage" hitting BuildIndex on "build",
// coverage 1/3) and cast a full RRF vote for an irrelevant blob, while a symbol
// whose name covers the query intent ("refund" -> func Refund, coverage 1/1)
// still fires. See Config.SymbolMinCoverage.
//
// The contributed LineSpan is the name line of the best-matching symbol, so the
// definition line feeds context assembly.
//
// Candidates come from symPostings (the blobs that define a symbol whose name
// carries at least one query term), so a query scores only those blobs instead of
// scanning the whole corpus — and the per-symbol subtokens are read from symInfo
// (precomputed in buildSymbolIndex), so no symbol name is re-tokenized per query.
// Because every qualifying symbol matches >= minHits >= 1 query terms, its blob is
// always in the candidate set, so the result is identical to a full scan.
//
// The candidate-gather + coverage-gate + sort boilerplate is shared with pathArm
// via coverageArm; only the aggregation policy (sumQualifying) differs.
func (r *Ranker) symbolArm(terms []string) []armScore {
	if r.syms == nil {
		return nil
	}
	return coverageArm(terms, r.cfg.SymbolMinCoverage, r.symPostings, r.symInfo, sumQualifying)
}

// pathArm ranks blobs by how strongly their file PATH matches the query — the
// zoekt-style filename signal. Disabled (returns nil) when PathMinCoverage is
// negative or the path index was not built.
//
// Matching mirrors the symbol arm: each candidate blob's path subtokens are read
// from pathInfo (precomputed once in buildPathIndex, never re-tokenized per
// query), and a blob scores by the number of DISTINCT query terms its BEST
// single path covers, never summing across a multi-path blob's paths
// (bestSingle). Candidates come from pathPostings (the blobs sharing at least
// one path token with the query), so a query never scans the whole corpus. The
// coverage gate (cfg.PathMinCoverage of the distinct query terms) keeps a
// single common token like "service" or "src" from voting for every path that
// contains it. The contributed LineSpan is the file head (line 1): a path match
// points at a file, not a content line, and seeding line 1 gives the context
// assembler something to show for a blob the lexical arm never surfaced (the
// whole point — "federated" lives only in the filename).
//
// The candidate-gather + coverage-gate + sort boilerplate is shared with
// symbolArm via coverageArm; only the aggregation policy (bestSingle) differs.
func (r *Ranker) pathArm(terms []string) []armScore {
	if r.cfg.PathMinCoverage < 0 {
		return nil
	}
	return coverageArm(terms, r.cfg.PathMinCoverage, r.pathPostings, r.pathInfo, bestSingle)
}

// lexicalSpans finds lines containing any query term and groups consecutive
// matching lines into spans (1-based inclusive). Matching is case-insensitive
// substring against the canonical (already lowercased) terms.
// maxLexicalSpanBytes bounds per-blob lexical span scanning (see lexicalSpans).
// 4 MiB is far above any real source file; blobs larger than this are generated
// data files (e.g. multi-million-line domain lists).
const maxLexicalSpanBytes = 4 << 20

func (r *Ranker) lexicalSpans(b *index.Blob, terms []string) []LineSpan {
	if len(terms) == 0 || len(b.Content) == 0 {
		return nil
	}
	// Skip span highlighting on pathologically large blobs: splitting + lowercasing
	// megabytes of generated data costs ~seconds for negligible value. The blob still
	// ranks and can contribute arm spans; the real fix is keeping such files out of
	// the corpus (hygiene).
	if len(b.Content) > maxLexicalSpanBytes {
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
		if hit {
			spans = append(spans, LineSpan{StartLine: i + 1, EndLine: i + 1})
		}
	}
	return MergeLineRanges(spans, lineSpanBounds, combineLineSpanEnd)
}

// mergeSpans merges overlapping/adjacent spans (via the shared MergeLineRanges
// adjacency rule) and caps the count.
func mergeSpans(spans []LineSpan, max int) []LineSpan {
	out := MergeLineRanges(spans, lineSpanBounds, combineLineSpanEnd)
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}
