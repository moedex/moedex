package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CorpusDataset keeps repository selection, relevance labels and calibrated
// gates outside the executable. Configure its JSON path with MOEDEX_EVAL_DATASET.
type CorpusDataset struct {
	Version      int                    `json:"version"`
	Repos        []CorpusSample         `json:"repos"`
	Strata       map[string][]GoldQuery `json:"strata"`
	PooledStrata []string               `json:"pooled_strata"`
	Thresholds   map[string]float64     `json:"thresholds"`
}

// CorpusSample is a declarative repository-relative file selection.
type CorpusSample struct {
	Repo            string   `json:"repo"`
	RelDir          string   `json:"rel_dir"`
	IncludePrefix   string   `json:"include_prefix,omitempty"`
	Extension       string   `json:"extension,omitempty"`
	ExcludeContains []string `json:"exclude_contains,omitempty"`
	ExcludePrefix   []string `json:"exclude_prefix,omitempty"`
	ExcludeSuffix   []string `json:"exclude_suffix,omitempty"`
}

func (s CorpusSample) keep(p string) bool {
	if !strings.HasPrefix(p, s.IncludePrefix) || !strings.HasSuffix(p, s.Extension) {
		return false
	}
	for _, v := range s.ExcludeContains {
		if strings.Contains(p, v) {
			return false
		}
	}
	for _, v := range s.ExcludePrefix {
		if strings.HasPrefix(p, v) {
			return false
		}
	}
	for _, v := range s.ExcludeSuffix {
		if strings.HasSuffix(p, v) {
			return false
		}
	}
	return true
}

// LoadCorpusDataset decodes a versioned manifest and rejects paths that escape
// the caller's corpus root. It performs no acquisition or network access.
func LoadCorpusDataset(path string) (CorpusDataset, error) {
	var d CorpusDataset
	f, err := os.Open(path)
	if err != nil {
		return d, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("dataset must contain one JSON value")
	}
	if d.Version != 1 || len(d.Repos) == 0 || len(d.PooledStrata) == 0 {
		return d, fmt.Errorf("unsupported or empty corpus dataset")
	}
	seen := map[string]bool{}
	for _, s := range d.Repos {
		if s.Repo == "" || seen[s.Repo] || s.RelDir == "." || !filepath.IsLocal(s.RelDir) || strings.Contains(s.RelDir, "\\") {
			return d, fmt.Errorf("invalid or duplicate corpus repository %q", s.Repo)
		}
		seen[s.Repo] = true
	}
	pooled := map[string]bool{}
	for _, name := range d.PooledStrata {
		if len(d.Strata[name]) == 0 || pooled[name] {
			return d, fmt.Errorf("invalid or duplicate pooled stratum %q", name)
		}
		pooled[name] = true
	}
	for name, queries := range d.Strata {
		for _, q := range queries {
			if strings.TrimSpace(q.Query) == "" || len(q.Relevant) == 0 {
				return d, fmt.Errorf("empty gold query in %q", name)
			}
			for p := range q.Relevant {
				if !filepath.IsLocal(p) || strings.Contains(p, "\\") {
					return d, fmt.Errorf("invalid relevance label in %q", name)
				}
			}
		}
	}
	for name, v := range d.Thresholds {
		if v < 0 || v > 1 {
			return d, fmt.Errorf("invalid threshold %q", name)
		}
	}
	return d, nil
}
