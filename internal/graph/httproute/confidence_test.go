package httproute_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/httproute"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

const routeCaller = `public class Client {
 public void Run() {
  http.GetAsync("/api/items/42");
  http.PostAsync("/api/items/42", body);
 }
}`

func routeHandler(method, name string) string {
	return fmt.Sprintf("package service\nfunc Register() { mux.HandleFunc(%q, %s) }\nfunc %s() {}\n", method+" /api/items/{id}", name, name)
}

func routeConfidenceCorpus(t *testing.T, files []fixtureFile, split bool) *httproute.Corpus {
	t.Helper()
	indexes := []*index.Index{index.New()}
	for i, file := range files {
		if split && i > 0 {
			indexes = append(indexes, index.New())
		}
		ix := indexes[len(indexes)-1]
		data := []byte(file.content)
		ix.AddFile(file.repo, file.relPath, "/"+file.repo+"/"+file.relPath, diskstore.GitBlobSHA1(data), data)
	}
	shards := make([]httproute.Shard, len(indexes))
	for i, ix := range indexes {
		shards[i] = httproute.Shard{Name: fmt.Sprintf("shard-%d", i), Index: ix, Symbols: symbol.BuildMulti(ix)}
	}
	return httproute.NewCorpus(shards...)
}

// Route equality is source evidence, never proof of deployed service routing.
// GET collisions must not contaminate an independent POST at the same path,
// even when the two call sites share one enclosing method.
func TestRouteConfidenceCompetingServicesAndVerbs(t *testing.T) {
	files := []fixtureFile{
		{"client", "Client.cs", routeCaller},
		{"orders", "routes.go", routeHandler("GET", "ReadOrder")},
		{"decoy", "routes.go", routeHandler("GET", "ReadDecoy")},
		{"writer", "routes.go", routeHandler("POST", "WriteOrder")},
	}
	corpus := routeConfidenceCorpus(t, files, true)
	result := httproute.Build(corpus)
	if len(result.Edges) != 3 {
		t.Fatalf("got %d edges, want two GET candidates and one POST pattern: %+v", len(result.Edges), result.Edges)
	}
	for _, edge := range result.Edges {
		want := graph.Candidate
		if edge.Call.Method == httproute.MethodPost {
			want = graph.Pattern
		}
		if edge.Confidence != want {
			t.Errorf("%s: confidence %s, want %s", renderEdge(edge), edge.Confidence, want)
		}
		content := corpus.Blob(edge.Call).Content
		if got := string(content[edge.Call.Start:edge.Call.End]); got != `"/api/items/42"` {
			t.Errorf("call evidence changed: %q", got)
		}
	}
	// The matcher promises site ordering, independent of input enumeration.
	reversed := result.Endpoints
	reversed.Calls = slices.Clone(reversed.Calls)
	reversed.Handlers = slices.Clone(reversed.Handlers)
	slices.Reverse(reversed.Calls)
	slices.Reverse(reversed.Handlers)
	if got := httproute.MatchEndpoints(reversed); !reflect.DeepEqual(got, result.Edges) {
		t.Fatalf("input ordering changed matching: %+v / %+v", got, result.Edges)
	}
}

func TestRouteConfidenceDistinctHandlersInOneRepository(t *testing.T) {
	files := []fixtureFile{
		{"client", "Client.cs", routeCaller},
		{"orders", "one.go", routeHandler("GET", "ReadOne")},
		{"orders", "two.go", routeHandler("GET", "ReadTwo")},
	}
	result := httproute.Build(routeConfidenceCorpus(t, files, false))
	if len(result.Edges) != 2 {
		t.Fatalf("edges = %+v", result.Edges)
	}
	for _, edge := range result.Edges {
		if edge.Confidence != graph.Candidate {
			t.Errorf("distinct handlers promoted: %+v", edge)
		}
	}
}

func TestRouteConfidenceDuplicateDeclarationIsOneDestination(t *testing.T) {
	files := []fixtureFile{
		{"client", "Client.cs", routeCaller},
		{"orders", "routes.go", "package service\nfunc Register() {\n mux.HandleFunc(\"GET /api/items/{id}\", Read)\n mux.HandleFunc(\"GET /api/items/{item}\", Read)\n}\nfunc Read() {}\n"},
	}
	result := httproute.Build(routeConfidenceCorpus(t, files, false))
	if len(result.Edges) != 2 {
		t.Fatalf("edges = %+v", result.Edges)
	}
	for _, edge := range result.Edges {
		if edge.Confidence != graph.Pattern {
			t.Errorf("one handler's duplicate declarations treated as competing destinations: %+v", edge)
		}
	}
}

func TestRouteConfidenceSharedContentContexts(t *testing.T) {
	for _, shared := range []string{"caller", "handler"} {
		for _, sameRepo := range []bool{false, true} {
			for _, split := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/same-repo=%v/split=%v", shared, sameRepo, split), func(t *testing.T) {
					files := []fixtureFile{{"client", "Client.cs", routeCaller}, {"orders", "routes.go", routeHandler("GET", "ReadOrder")}}
					duplicate := files[0]
					if shared == "handler" {
						duplicate = files[1]
					}
					if !sameRepo {
						duplicate.repo = "copy"
					}
					duplicate.relPath = "copy/" + duplicate.relPath
					files = append(files, duplicate)
					result := httproute.Build(routeConfidenceCorpus(t, files, split))
					if len(result.Edges) == 0 {
						t.Fatal("missing diagnostic route matches")
					}
					for _, edge := range result.Edges {
						if edge.Confidence != graph.Candidate {
							t.Errorf("shared content promoted: %+v", edge)
						}
					}
				})
			}
		}
	}
}

func TestRouteConfidenceExactStillOnlyPattern(t *testing.T) {
	files := []fixtureFile{{"client", "Client.cs", strings.ReplaceAll(routeCaller, "/api/items/42", "/api/items/{id}")}, {"orders", "routes.go", routeHandler("GET", "ReadOrder")}}
	result := httproute.Build(routeConfidenceCorpus(t, files, false))
	if len(result.Edges) != 1 || result.Edges[0].Quality != httproute.Exact || result.Edges[0].Confidence != graph.Pattern {
		t.Fatalf("exact route implies no service binding: %+v", result.Edges)
	}
}
