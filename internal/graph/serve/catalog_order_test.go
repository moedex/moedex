package graphserve

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"moedex/internal/graph/diskgraph"
)

func TestCatalogKeyOrderingMatchesPublicIDs(t *testing.T) {
	var keys []diskgraph.Key
	for _, sha := range []string{"", "a", "a0", "a:", "b", strings.Repeat("a", 40), strings.Repeat("b", 40)} {
		for _, offset := range []uint64{0, 1, 2, 9, 10, 11, 20, 99, 100, 1000, math.MaxUint64} {
			keys = append(keys, diskgraph.Key{BlobSHA: sha, SymbolOffset: offset})
		}
	}
	for _, a := range keys {
		for _, b := range keys {
			if got, want := lessGraphKeyID(a, b), keyID(a) < keyID(b); got != want {
				t.Fatalf("ordering %q < %q = %t, want %t", keyID(a), keyID(b), got, want)
			}
		}
	}
}

func TestCatalogSingletonLocationsRetainOwnership(t *testing.T) {
	loc := GraphLocation{Repo: "r", Path: "a.cs", AbsPath: "/r/a.cs", Line: 2, BlobSHA: "sha"}
	for _, left := range []bool{false, true} {
		input := []GraphLocation{loc}
		var got []GraphLocation
		if left {
			got = mergeLocations(input, nil)
		} else {
			got = mergeLocations(nil, input)
		}
		if !reflect.DeepEqual(got, input) {
			t.Fatalf("singleton changed: %+v", got)
		}
		got[0].Line++
		if input[0] != loc {
			t.Fatal("catalog location aliases caller-owned input")
		}
	}
}

func TestCatalogPathIndexMatchesUncachedOracle(t *testing.T) {
	s := &graphSnapshot{nodes: make(map[diskgraph.Key]nodeMetadata), byPath: make(map[string][]locatedNode)}
	files := []GraphLocation{
		{Repo: "r", Path: "src/a.cs", AbsPath: "/r/src/a.cs"},
		{Repo: "r", Path: "src/a.cs", AbsPath: "/alternate/src/a.cs"},
		{Repo: "other", Path: "src/a.cs", AbsPath: "/r/src/a.cs"},
		{Path: "./src/../src/b.cs"},
		{Repo: "r", Path: `src\c.cs`, AbsPath: `C:\r\src\c.cs`},
		{},
	}
	want := make(map[string][]locatedNode)
	for i := 0; i < 1200; i++ {
		key := diskgraph.Key{BlobSHA: fmt.Sprintf("%040x", i%37), SymbolOffset: uint64(i)}
		loc := files[i%len(files)]
		loc.Line = i%9 + 1
		s.nodes[key] = nodeMetadata{Locations: []GraphLocation{loc}}
		for _, spelling := range indexedPathSpellings(loc) {
			want[spelling] = append(want[spelling], locatedNode{line: loc.Line, key: key})
		}
	}
	for path, nodes := range want {
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].line != nodes[j].line {
				return nodes[i].line < nodes[j].line
			}
			return keyID(nodes[i].key) < keyID(nodes[j].key)
		})
		want[path] = nodes
	}
	s.buildPathIndex()
	if !reflect.DeepEqual(s.byPath, want) {
		t.Fatal("cached catalog path index differs from original ordering and spellings")
	}
}
