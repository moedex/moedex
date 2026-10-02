package verify

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"moedex/internal/graph/candidates"
	"moedex/internal/index"
	"moedex/internal/symbol"
)

func TestRegionCacheReuseEvictionAndBudget(t *testing.T) {
	cache := NewRegionCache(16, 2)
	a := &index.Blob{Content: []byte("/* a */")}
	b := &index.Blob{Content: []byte("\"b\"")}
	c := &index.Blob{Content: []byte("// c\n")}
	for _, step := range []struct {
		blob *index.Blob
		lang language
	}{{a, langCSharp}, {b, langGo}, {a, langCSharp}, {c, langGeneral}, {b, langGo}, {b, langGo}} {
		if got, want := cache.regions(step.blob, step.lang), classifyRegions(step.blob.Content, step.lang); !reflect.DeepEqual(got, want) {
			t.Fatal("cached regions changed classification")
		}
		stats := cache.Stats()
		if stats.Bytes > 16 || stats.Entries > 2 {
			t.Fatalf("budget exceeded: %+v", stats)
		}
	}
	stats := cache.Stats()
	if stats.Hits != 2 || stats.Misses != 4 || stats.Evictions != 2 {
		t.Fatalf("reuse/eviction accounting: %+v", stats)
	}
	large := &index.Blob{Content: []byte(strings.Repeat("x", 17))}
	cache.regions(large, langGeneral)
	cache.regions(large, langGeneral)
	after := cache.Stats()
	if after.Bytes != stats.Bytes || after.Entries != stats.Entries || after.Misses != stats.Misses+2 {
		t.Fatalf("oversized mask was retained: %+v", after)
	}
	// A second language on the same blob must have a distinct mask key.
	cache.regions(b, langCSharp)
	if cache.Stats().Misses != after.Misses+1 {
		t.Fatal("language key was ignored")
	}
	byteLimited := NewRegionCache(8, 20)
	byteLimited.regions(a, langGo)
	byteLimited.regions(b, langGo)
	if stats := byteLimited.Stats(); stats.Entries != 1 || stats.Bytes != 3 || stats.Evictions != 1 {
		t.Fatalf("byte budget did not evict below the entry limit: %+v", stats)
	}
	disabled := NewRegionCache(0, 0)
	disabled.regions(a, langGo)
	if stats := disabled.Stats(); stats.Bytes != 0 || stats.Entries != 0 || stats.Misses != 1 {
		t.Fatalf("disabled cache retained a mask: %+v", stats)
	}
}

func TestCachedSessionMatchesVerifyAcrossEvictions(t *testing.T) {
	sources := []fixture{
		{"r", "a.cs", "using ProcessOrder;\nclass A { void Call() { ProcessOrder(); var s = \"ProcessOrder\"; } }"},
		{"r", "b.go", "package b\nimport \"ProcessOrder\"\nfunc Caller() { ProcessOrder(); /* ProcessOrder */ }"},
		{"r", "c.txt", "ProcessOrder and ProcessOrder()\n\"ProcessOrder\""},
		{"r", "broken.cs", "class C { void Method() { ProcessOrder(); /* unterminated ProcessOrder"},
	}
	var inputs [][]candidates.Edge
	for _, source := range sources {
		inputs = append(inputs, buildCandidates(t, source))
	}
	// Mixed file-language context is immutable before either verification path.
	blob := inputs[0][0].EvidenceBlob()
	blob.Files = append(blob.Files, blob.Files[0])
	blob.Files[len(blob.Files)-1].RelPath = "also.go"
	cache := NewRegionCache(256, 2)
	session := NewSession(cache)
	for pass := 0; pass < 3; pass++ {
		for _, input := range inputs {
			want := Verify(input)
			if got := session.Verify(input); !reflect.DeepEqual(got, want) {
				t.Fatalf("cached output changed on pass%d: got%#v want%#v", pass, got, want)
			}
			// Immediate replay proves reuse as well as eviction from later fixtures.
			if got := session.Verify(input); !reflect.DeepEqual(got, want) {
				t.Fatal("replayed cached output changed")
			}
		}
	}
	stats := cache.Stats()
	if stats.Hits == 0 || stats.Evictions == 0 || stats.Bytes > 256 || stats.Entries > 2 {
		t.Fatalf("cache not exercised: %+v", stats)
	}
}

func TestRegionCacheConcurrentSessions(t *testing.T) {
	input := buildCandidates(t, fixture{"r", "a.cs", "class A { void Caller() { ProcessOrder(); } }\n" + strings.Repeat("// padding\n", 20000)})
	want := Verify(input)
	cache := NewRegionCache(1<<20, 8)
	const workers = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan bool, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := NewSession(cache)
			for range 3 {
				if !reflect.DeepEqual(s.Verify(input), want) {
					failures <- true
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	if len(failures) > 0 {
		t.Fatal("concurrent verification changed output")
	}
	stats := cache.Stats()
	if stats.Misses != 1 || stats.Hits+stats.Waits != workers*3-1 || stats.Entries != 1 {
		t.Fatalf("fills were not reused/coalesced: %+v", stats)
	}
}

func TestSessionPatternBudgetPreservesAnswers(t *testing.T) {
	var source strings.Builder
	source.WriteString("package demo\nfunc Caller() {\n")
	for i := 0; i < maxSessionPatterns+10; i++ {
		fmt.Fprintf(&source, " Name%d()\n", i)
	}
	source.WriteString("}\n")
	for i := 0; i < maxSessionPatterns+10; i++ {
		fmt.Fprintf(&source, "func Name%d() {}\n", i)
	}
	ix := index.New()
	ix.AddFile("r", "source.go", "", "sha", []byte(source.String()))
	corpus, err := candidates.NewCorpus(symbol.Merge(symbol.Shard{Name: "s", Index: symbol.BuildMulti(ix)}), ix)
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession(NewRegionCache(1<<20, 8))
	for i := 0; i < maxSessionPatterns+10; i++ {
		input := candidates.PrepareName(corpus, fmt.Sprintf("Name%d", i)).SourceCandidates()
		if got, want := session.Verify(input), Verify(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("pattern eviction changed Name%d", i)
		}
		if len(session.patterns) > maxSessionPatterns {
			t.Fatalf("unbounded patterns: %d", len(session.patterns))
		}
	}
}

func BenchmarkRegionCacheAcrossNames(b *testing.B) {
	blob := &index.Blob{Content: []byte("class Fixture {\n" + strings.Repeat("void Method() { Target(); }\n", 10000) + "}\n")}
	for _, tc := range []struct {
		name  string
		cache *RegionCache
	}{{"uncached", nil}, {"shared", NewRegionCache(1<<20, 8)}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for range 10 {
					tc.cache.regions(blob, langCSharp)
				}
			}
		})
	}
}
