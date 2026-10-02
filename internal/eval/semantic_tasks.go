package eval

// Semantic task labels are source-authored facts, separate from any engine's
// node IDs, confidence scale, or tool spelling. Adapters supply those mappings.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

type SemanticSuite struct {
	Version     int            `json:"version"`
	LabelSource string         `json:"label_source"`
	Sources     []string       `json:"sources"`
	Tasks       []SemanticTask `json:"tasks"`
}

type SemanticTask struct {
	ID        string         `json:"id"`
	Question  string         `json:"question"`
	Operation string         `json:"operation"`
	Symbol    string         `json:"symbol"`
	Required  []SemanticFact `json:"required"`
	Forbidden []SemanticFact `json:"forbidden"`
}

type SemanticFact struct {
	Source   string            `json:"source"`
	Relation string            `json:"relation"`
	Target   string            `json:"target"`
	Evidence *SemanticEvidence `json:"evidence,omitempty"`
}

type SemanticEvidence struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// LoadSemanticSuite validates the versioned task schema and evidence against
// the actual fixture bytes. It rejects unknown fields and stale evidence.
func LoadSemanticSuite(root fs.FS) (SemanticSuite, error) {
	var suite SemanticSuite
	data, err := fs.ReadFile(root, "tasks.json")
	if err != nil {
		return suite, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&suite); err != nil {
		return suite, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return suite, fmt.Errorf("task suite must contain one JSON object")
	}
	if suite.Version != 1 || strings.TrimSpace(suite.LabelSource) == "" || len(suite.Tasks) == 0 || len(suite.Sources) == 0 {
		return suite, fmt.Errorf("task suite needs version 1, label provenance, sources, and tasks")
	}
	sources := map[string][]byte{}
	for _, name := range suite.Sources {
		if !fs.ValidPath(name) || !strings.Contains(name, "/") {
			return suite, fmt.Errorf("invalid repository-relative source %q", name)
		}
		if _, ok := sources[name]; ok {
			return suite, fmt.Errorf("duplicate source %q", name)
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return suite, err
		}
		sources[name] = data
	}
	ids := map[string]bool{}
	for _, task := range suite.Tasks {
		if task.ID == "" || ids[task.ID] || strings.TrimSpace(task.Question) == "" || task.Symbol == "" || len(task.Required) == 0 || len(task.Forbidden) == 0 {
			return suite, fmt.Errorf("task %q needs a unique ID, question, symbol, positive facts and distractors", task.ID)
		}
		ids[task.ID] = true
		if task.Operation != "hierarchy" && task.Operation != "dependencies" {
			return suite, fmt.Errorf("task %q: unknown operation %q", task.ID, task.Operation)
		}
		seen := map[string]bool{}
		for group, facts := range [][]SemanticFact{task.Required, task.Forbidden} {
			for _, fact := range facts {
				key := fact.Source + "|" + fact.Relation + "|" + fact.Target
				if seen[key] {
					return suite, fmt.Errorf("task %q has duplicate or contradictory fact %s", task.ID, key)
				}
				seen[key] = true
				if fact.Relation != "extends" && fact.Relation != "implements" && fact.Relation != "injects" {
					return suite, fmt.Errorf("unknown relation %q", fact.Relation)
				}
				for _, endpoint := range []string{fact.Source, fact.Target} {
					path, symbol, ok := strings.Cut(endpoint, "#")
					if !ok || symbol == "" || !bytes.Contains(sources[path], []byte(symbol)) {
						return suite, fmt.Errorf("invalid labeled endpoint %q", endpoint)
					}
				}
				if group == 0 && fact.Evidence == nil {
					return suite, fmt.Errorf("required fact %s lacks evidence", key)
				}
				if evidence := fact.Evidence; evidence != nil {
					lines := bytes.Split(sources[evidence.File], []byte("\n"))
					if evidence.Line < 1 || evidence.Line > len(lines) || evidence.Text == "" || bytes.Count(lines[evidence.Line-1], []byte(evidence.Text)) != 1 {
						return suite, fmt.Errorf("fact %s has stale or ambiguous evidence", key)
					}
				}
			}
		}
	}
	return suite, nil
}
