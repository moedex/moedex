package graphserve

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"moedex/internal/graph/diskgraph"
)

func TestListSymbolsStableSelectionAndEqualNameOrdering(t *testing.T) {
	s := &graphSnapshot{bySymbol: make(map[string][]diskgraph.Key), nodes: make(map[diskgraph.Key]nodeMetadata)}
	for i := 299; i >= 0; i-- {
		name := fmt.Sprintf("Name%03d", i)
		key := diskgraph.Key{BlobSHA: name}
		s.bySymbol[name] = []diskgraph.Key{key}
		s.nodes[key] = nodeMetadata{Kind: "Method", Symbol: name, Locations: []GraphLocation{{Repo: "repo", Path: name + ".go", Line: 2}}}
	}
	decode := func(query string) ([]symbolEntry, bool) {
		t.Helper()
		r := s.listSymbols("", "repo", query)
		raw, err := json.Marshal(r["structuredContent"])
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Symbols   []symbolEntry `json:"symbols"`
			Truncated bool          `json:"truncated"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result.Symbols, result.Truncated
	}
	for range 20 {
		got, truncated := decode("")
		if !truncated || len(got) != maxListSymbols {
			t.Fatalf("bad cap: %d %v", len(got), truncated)
		}
		for i, entry := range got {
			if entry.Name != fmt.Sprintf("Name%03d", i) {
				t.Fatalf("unstable capped membership at %d: %s", i, entry.Name)
			}
		}
	}
	// Equal displayed names must have a stable location order, irrespective of
	// their content-addressed posting order.
	var want []string
	for i := 9; i >= 0; i-- {
		key := diskgraph.Key{BlobSHA: fmt.Sprint(i)}
		s.bySymbol["Shared"] = append(s.bySymbol["Shared"], key)
		s.nodes[key] = nodeMetadata{Kind: "Method", Symbol: "Shared", Locations: []GraphLocation{{Repo: "repo", Path: fmt.Sprintf("%d.go", i), Line: 2}}}
	}
	for i := range 10 {
		want = append(want, fmt.Sprintf("%d.go", i))
	}
	got, truncated := decode("Shared")
	var paths []string
	for _, entry := range got {
		paths = append(paths, entry.Path)
	}
	if truncated || !reflect.DeepEqual(paths, want) {
		t.Fatalf("equal-name order: %v, truncated=%v", paths, truncated)
	}
}
