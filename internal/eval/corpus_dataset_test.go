package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalCorpusDataset(t *testing.T) {
	d := CorpusDataset{Version: 1, Repos: []CorpusSample{{Repo: "sample", RelDir: "examples/sample", IncludePrefix: "src/", Extension: ".cs", ExcludeContains: []string{"/obj/"}, ExcludeSuffix: []string{".Tests.cs"}}}, Strata: map[string][]GoldQuery{"lexical": {NewBinaryGold("refund", "src/Refund.cs")}}, PooledStrata: []string{"lexical"}, Thresholds: map[string]float64{"minimum": .5}}
	d.Strata["lexical"][0].Relevant["src/Distractor.cs"] = -1
	path := filepath.Join(t.TempDir(), "gold.json")
	write := func(d CorpusDataset) {
		t.Helper()
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(d)
	got, err := LoadCorpusDataset(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Repos[0].keep("src/Refund.cs") || got.Repos[0].keep("src/obj/Generated.cs") || got.Repos[0].keep("src/Refund.Tests.cs") || got.Repos[0].keep("other/Refund.cs") {
		t.Fatal("sample selection did not preserve declared universe")
	}
	t.Setenv("MOEDEX_EVAL_DATASET", path)
	if gold := CorpusGold(); len(gold) != 1 || gold[0].Relevant["src/Refund.cs"] != 1 || gold[0].Relevant["src/Distractor.cs"] != -1 {
		t.Fatalf("pooled labels: %+v", gold)
	}
	if repos := goldCorpusRepos(); len(repos) != 1 || repos[0].RelDir != "examples/sample" {
		t.Fatalf("repo selection: %+v", repos)
	}
	t.Setenv("MOEDEX_EVAL_DATASET", "")
	if _, _, _, ok := BuildGoldCorpusIndex(); ok {
		t.Fatal("unconfigured gates must not index an ambient corpus")
	}
	for _, bad := range []string{"../outside", "/absolute", ".", "a\\b"} {
		d.Repos[0].RelDir = bad
		write(d)
		if _, err := LoadCorpusDataset(path); err == nil {
			t.Errorf("accepted escaping repository path %q", bad)
		}
	}
	d.Repos[0].RelDir = "examples/sample"
	d.PooledStrata = []string{"missing"}
	write(d)
	if _, err := LoadCorpusDataset(path); err == nil {
		t.Fatal("accepted missing pooled labels")
	}
	d.PooledStrata = []string{"lexical"}
	d.Version = 2
	write(d)
	if _, err := LoadCorpusDataset(path); err == nil {
		t.Fatal("accepted unsupported dataset version")
	}
}
