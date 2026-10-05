package eval

import (
	"fmt"
	"os"
)

// configuredCorpusDataset is used by opt-in corpus gates. Invalid explicit
// configuration fails the gate rather than silently becoming an absent corpus.
func configuredCorpusDataset() CorpusDataset {
	path := os.Getenv("MOEDEX_EVAL_DATASET")
	if path == "" {
		return CorpusDataset{}
	}
	d, err := LoadCorpusDataset(path)
	if err != nil {
		panic(fmt.Sprintf("load MOEDEX_EVAL_DATASET: %v", err))
	}
	return d
}

// CorpusGold returns the externally configured pooled relevance labels.
func CorpusGold() []GoldQuery {
	d := configuredCorpusDataset()
	var gold []GoldQuery
	for _, name := range d.PooledStrata {
		gold = append(gold, d.Strata[name]...)
	}
	return gold
}

func corpusGoldAgentNL() []GoldQuery { return configuredCorpusDataset().Strata["agent_nl"] }
func SeedCorpusGold() []GoldQuery    { return configuredCorpusDataset().Strata["seed"] }

func corpusThreshold(name string) float64 {
	v, ok := configuredCorpusDataset().Thresholds[name]
	if !ok {
		panic(fmt.Sprintf("external corpus dataset lacks threshold %q", name))
	}
	return v
}
