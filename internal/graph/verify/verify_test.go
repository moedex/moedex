package verify

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/graph/candidates"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

type fixture struct {
	repo string
	path string
	src  string
}

const definitionSource = `package target

func ProcessOrder() {}
`

func buildCandidates(t *testing.T, source fixture) []candidates.Edge {
	t.Helper()
	fixtures := []fixture{
		{repo: "target", path: "target.go", src: definitionSource},
		source,
	}
	shards := make([]symbol.Shard, 0, len(fixtures))
	idxs := make([]*index.Index, 0, len(fixtures))
	for i, fixture := range fixtures {
		ix := index.New()
		ix.AddFile(fixture.repo, fixture.path, "/"+fixture.repo+"/"+fixture.path, fmt.Sprintf("sha-%d", i), []byte(fixture.src))
		idxs = append(idxs, ix)
		shards = append(shards, symbol.Shard{
			Name:  fmt.Sprintf("%03d.idx", i),
			Index: symbol.BuildMulti(ix),
		})
	}
	corpus, err := candidates.NewCorpus(symbol.Merge(shards...), idxs...)
	if err != nil {
		t.Fatalf("NewCorpus: %v", err)
	}
	var sourceEdges []candidates.Edge
	for _, edge := range candidates.GenerateCandidates(corpus, "ProcessOrder") {
		if edge.Source.Shard == 1 {
			sourceEdges = append(sourceEdges, edge)
		}
	}
	if len(sourceEdges) == 0 {
		t.Fatal("fixture generated no candidates from the source shard")
	}
	return sourceEdges
}

func edgeLine(edge candidates.Edge) string {
	blob := edge.EvidenceBlob()
	if blob == nil {
		return ""
	}
	_, line := blob.LineAt(edge.Source.Start)
	return strings.TrimSpace(string(line))
}

func assertScores(t *testing.T, input []candidates.Edge, want map[string]struct {
	tier Tier
	kind ReferenceKind
}) {
	t.Helper()
	got := Verify(input)
	if len(got) != len(input) {
		t.Fatalf("Verify returned %d edges for %d candidates: candidates must never be dropped", len(got), len(input))
	}
	seen := map[string]bool{}
	for i, edge := range got {
		if !reflect.DeepEqual(edge.Edge, input[i]) {
			t.Errorf("edge %d changed candidate identity:\n got %+v\nwant %+v", i, edge.Edge, input[i])
		}
		line := edgeLine(input[i])
		expect, ok := want[line]
		if !ok {
			t.Errorf("unexpected candidate on %q: %+v", line, edge)
			continue
		}
		seen[line] = true
		if edge.Tier != expect.tier || edge.Kind != expect.kind {
			t.Errorf("%q scored (%s, %s), want (%s, %s)", line, edge.Tier, edge.Kind, expect.tier, expect.kind)
		}
		wantConfidence := CandidateConfidence
		if expect.tier == Pattern {
			wantConfidence = PatternConfidence
		}
		if edge.Confidence != wantConfidence {
			t.Errorf("%q confidence = %v, want %v", line, edge.Confidence, wantConfidence)
		}
	}
	for line := range want {
		if !seen[line] {
			t.Errorf("fixture did not produce candidate on %q", line)
		}
	}
}

func TestVerifyGoPatternsAndLiteralRejection(t *testing.T) {
	input := buildCandidates(t, fixture{
		repo: "go-caller",
		path: "caller.go",
		src: `package caller

import orders "example.com/ProcessOrder"

func Run() {
	orders.ProcessOrder()
	_ = ProcessOrder{}
	// ProcessOrder()
	_ = "ProcessOrder()"
}
`,
	})

	assertScores(t, input, map[string]struct {
		tier Tier
		kind ReferenceKind
	}{
		`import orders "example.com/ProcessOrder"`: {Pattern, Import},
		`orders.ProcessOrder()`:                    {Pattern, Call},
		`_ = ProcessOrder{}`:                       {Pattern, TypeReference},
		`// ProcessOrder()`:                        {Candidate, Unverified},
		`_ = "ProcessOrder()"`:                     {Candidate, Unverified},
	})
}

func TestVerifyCSharpPatternsAndLiteralRejection(t *testing.T) {
	input := buildCandidates(t, fixture{
		repo: "cs-caller",
		path: "Caller.cs",
		src: `using Domain.ProcessOrder;

internal sealed class Caller
{
    public void Run()
    {
        ProcessOrder();
        ProcessOrder value = null;
        // ProcessOrder();
        var text = "ProcessOrder()";
    }
}
`,
	})

	assertScores(t, input, map[string]struct {
		tier Tier
		kind ReferenceKind
	}{
		`using Domain.ProcessOrder;`:   {Pattern, Import},
		`ProcessOrder();`:              {Pattern, Call},
		`ProcessOrder value = null;`:   {Pattern, TypeReference},
		`// ProcessOrder();`:           {Candidate, Unverified},
		`var text = "ProcessOrder()";`: {Candidate, Unverified},
	})
}

func TestVerifyGeneralIdentifierPatterns(t *testing.T) {
	input := buildCandidates(t, fixture{
		repo: "python-caller",
		path: "caller.py",
		src: `def run():
    ProcessOrder()
    callback = ProcessOrder
    # ProcessOrder()
    text = "ProcessOrder()"
`,
	})

	assertScores(t, input, map[string]struct {
		tier Tier
		kind ReferenceKind
	}{
		`ProcessOrder()`:          {Pattern, Call},
		`callback = ProcessOrder`: {Pattern, IdentifierMatch},
		`# ProcessOrder()`:        {Candidate, Unverified},
		`text = "ProcessOrder()"`: {Candidate, Unverified},
	})
}

func TestVerifyRetainsUnresolvableCandidates(t *testing.T) {
	input := []candidates.Edge{{
		Name:   "ProcessOrder",
		Source: candidates.Site{Start: 0, End: len("ProcessOrder")},
		Type:   candidates.TextOccurrence,
	}}
	got := Verify(input)
	if len(got) != 1 {
		t.Fatalf("Verify returned %d edges, want the unresolvable candidate retained", len(got))
	}
	if got[0].Tier != Candidate || got[0].Confidence != CandidateConfidence || got[0].Kind != Unverified {
		t.Errorf("unresolvable candidate scored %+v, want Candidate/%v/Unverified", got[0], CandidateConfidence)
	}
}
