package eval

import (
	"context"

	"moedex/internal/rank"
)

// This file is the eval-side glue for the learned reranker: it extracts labeled
// feature rows from a gold set (via the Runner's Features entry point), trains a
// LinearReranker, and — critically for honesty on a small gold set — runs
// leave-fold-out CROSS-VALIDATION so the A/B numbers are OUT-OF-FOLD (a model never
// scores the queries it trained on). All of it is pure Go/stdlib (rank.Train is a
// deterministic SGD); no Python, no model files, no new dependency.

// goldRow pairs a candidate's features with its label and source query index.
type goldRow struct {
	groupQuery int // index into the gold slice (the query this candidate came from)
	feat       rank.FeatureVector
	label      float64 // 1 if any of the candidate's files is gold-relevant (grade>=1), else 0
}

// extractGoldRows runs every gold query through the runner's arms and labels each
// returned candidate: a candidate is positive (1) when ANY file it backs is graded
// >= 1 in that query's gold, negative (0) otherwise. topK bounds the candidate pool
// per query (mirrors Evaluate's topK so train and eval see the same pool). The
// runner must be configured with the SAME arms (symbols/dense/path) the A/B will
// evaluate, so the features match.
func extractGoldRows(ctx context.Context, run *Runner, gold []GoldQuery, topK int) ([]goldRow, error) {
	var rows []goldRow
	for qi, g := range gold {
		feats, err := run.Features(ctx, g.Query)
		if err != nil {
			return nil, err
		}
		// Features() returns candidates in arm-discovery order, not score order. We do
		// not need them truncated to topK for TRAINING (more negatives is fine and the
		// pool is small), but we cap to keep the row count bounded and comparable to the
		// eval pool; rank by RRF score so the cap keeps the most plausible candidates.
		rank.SortBlobFeaturesByRRF(feats)
		if topK > 0 && len(feats) > topK {
			feats = feats[:topK]
		}
		for _, bf := range feats {
			label := 0.0
			for _, f := range bf.Files {
				if g.Relevant[f.RelPath] >= 1 {
					label = 1
					break
				}
			}
			rows = append(rows, goldRow{groupQuery: qi, feat: bf.Feat, label: label})
		}
	}
	return rows, nil
}

// toTrainExamples flattens goldRows into rank.TrainExamples (feature slice +
// label + group) using the ranker's RRFk for the rank-reciprocal features.
func toTrainExamples(rows []goldRow, rrfK float64) []rank.TrainExample {
	out := make([]rank.TrainExample, 0, len(rows))
	for _, r := range rows {
		out = append(out, rank.TrainExample{
			X:     r.feat.Slice(rrfK),
			Y:     r.label,
			Group: r.groupQuery,
		})
	}
	return out
}

// crossValModels trains one LinearReranker per fold on the rows NOT in that fold,
// and returns, for each gold-query index, the model trained WITHOUT that query
// (its out-of-fold model). This is the leakage-free protocol: query qi is scored by
// a model that never saw qi's rows. k is the fold count; folds are split by query
// group so a query's rows never straddle the train/test boundary.
//
// Returns modelForQuery[qi] = the out-of-fold model to use when evaluating query qi.
func crossValModels(rows []goldRow, rrfK float64, k int, cfg rank.TrainConfig) map[int]*rank.LinearReranker {
	ex := toTrainExamples(rows, rrfK)
	folds := rank.GroupKFold(ex, k)

	// For each fold, train on everything OUTSIDE the fold, then assign that model to
	// every query whose rows are IN the fold.
	modelForQuery := map[int]*rank.LinearReranker{}
	for _, testIdx := range folds {
		inFold := make(map[int]bool, len(testIdx))
		for _, i := range testIdx {
			inFold[i] = true
		}
		// Training set = rows not in this fold.
		var train []rank.TrainExample
		for i, e := range ex {
			if !inFold[i] {
				train = append(train, e)
			}
		}
		model, _ := rank.Train(train, cfg)
		// Assign to the queries that live in this fold.
		for _, i := range testIdx {
			modelForQuery[rows[i].groupQuery] = model
		}
	}
	return modelForQuery
}

// evaluateCrossVal runs an OUT-OF-FOLD A/B: each gold query is scored by the
// learned model trained without it (from crossValModels), at cutoff k. It mutates
// the runner's reranker per query (installing the query's out-of-fold model) and
// restores RRF afterward. Returns the learned-fusion Report. The runner must already
// be in FusionLinear mode for the learned scores to take effect; queries with no
// out-of-fold model (e.g. a degenerate fold) fall back to whatever the runner's
// current model is.
func evaluateCrossVal(ctx context.Context, run *Runner, gold []GoldQuery, modelForQuery map[int]*rank.LinearReranker, k, topK int) (Report, error) {
	rep := Report{K: k}
	for qi, g := range gold {
		if m := modelForQuery[qi]; m != nil {
			run.SetReranker(m)
		}
		ranked, err := run.rankedRelPaths(ctx, g.Query, topK)
		if err != nil {
			return Report{}, err
		}
		rep.Queries = append(rep.Queries, QueryReport{
			Query:       g.Query,
			NumRelevant: numRelevant(g.Relevant),
			Ranked:      ranked,
			RecallAtK:   RecallAtK(ranked, g.Relevant, k),
			PrecAtK:     PrecisionAtK(ranked, g.Relevant, k),
			MRR:         MRR(ranked, g.Relevant),
			NDCGAtK:     NDCGAtK(ranked, g.Relevant, k),
			UDCGAtK:     UDCGAtK(ranked, g.Relevant, k),
		})
	}
	rep.finalizeMeans()
	return rep, nil
}
