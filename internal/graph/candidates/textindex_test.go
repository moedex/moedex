package candidates

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"moedex/internal/index"
	"moedex/internal/symbol"
	"moedex/internal/trigram"
)

func textIndexFixture(t *testing.T, mode string) (*Corpus, []string) {
	t.Helper()
	names := []string{"Add", "Address", "A", "Do", "x$y", "Ünïcode", "\xff\xfe", "A.B", "two words", "NeverPresent"}
	text := strings.Join(names[:len(names)-1], " ; ") + "\n" + strings.Repeat("Add preAdd AddSuffix Address x$y Ünïcode \xff\xfe A.B two words\n", 12)
	random := rand.New(rand.NewSource(128))
	var noise strings.Builder
	for range 200 {
		noise.WriteString([]string{"Add", "A", "Do", "Ünïcode", "\xff\xfe", "x$y", "Other"}[random.Intn(7)])
		noise.WriteByte(' ')
	}
	var idxs []*index.Index
	var shards []symbol.Shard
	for shard := range 3 {
		ix := index.New()
		ix.AddFile("repo", fmt.Sprint(shard), "", "shared-sha", []byte(text))
		ix.AddFile("repo", "noise", "", "noise-sha", []byte(noise.String()))
		symbols := symbol.NewIndex()
		var defs []symbol.Symbol
		for _, name := range names[:len(names)-1] {
			start := strings.Index(text, name)
			defs = append(defs, symbol.Symbol{Name: name, NameStart: start, NameEnd: start + len(name), BodyStart: start, BodyEnd: len(text)})
		}
		symbols.Set(0, defs)
		// Definition and reference share a site; the definition must win. The
		// second reference deliberately has a longer extractor range than text.
		second := strings.Index(text, "\n") + 1
		symbols.SetRefs(0, []symbol.Occurrence{{Name: "Add", Role: symbol.Reference, Start: 0, End: 3}, {Name: "Add", Role: symbol.Reference, Start: second, End: second + 4}})
		switch mode {
		case "content-only":
			ix = index.Restore(ix.Snapshot(), nil)
		case "selective":
			ix, _ = lazyOccurrences(ix, map[trigram.Trigram]bool{{'A', 'd', 'd'}: true})
		}
		idxs = append(idxs, ix)
		shards = append(shards, symbol.Shard{Name: fmt.Sprint(shard), Index: symbols})
	}
	idxs = append(idxs, nil)
	shards = append(shards, symbol.Shard{Name: "nil", Index: symbol.NewIndex()})
	c, err := NewCorpus(symbol.Merge(shards...), idxs...)
	if err != nil {
		t.Fatal(err)
	}
	return c, names
}

func TestTextOccurrenceIndexMatchesOriginalCandidates(t *testing.T) {
	for _, mode := range []string{"eager", "content-only", "selective"} {
		t.Run(mode, func(t *testing.T) {
			base, names := textIndexFixture(t, mode)
			scoped, stats := WithTextOccurrences(base, append(append([]string{}, names...), "Add", ""), 1<<20)
			if !stats.Built || stats.RetainedBytes != stats.RosterBytes+stats.SiteCapacityBytes || stats.BlobsScanned != 6 {
				t.Fatalf("unexpected stats: %+v", stats)
			}
			if base.textOccurrences != nil {
				t.Fatal("mutated original corpus")
			}
			for _, name := range append(names, "Other") {
				if got, want := GenerateCandidates(scoped, name), GenerateCandidates(base, name); !reflect.DeepEqual(got, want) {
					t.Fatalf("%q candidates differ\ngot %+v\nwant %+v", name, got, want)
				}
				if got, want := scoped.occurrences(name), base.occurrences(name); !reflect.DeepEqual(got, want) {
					t.Fatalf("%q occurrence order/classification differ", name)
				}
			}
			// Unsupported punctuation/space names are absent, while an indexed name
			// without any occurrences is represented by a complete empty entry.
			for _, name := range []string{"A.B", "two words"} {
				if scoped.textOccurrences.find(name).name != "" {
					t.Fatalf("unsupported name indexed: %q", name)
				}
			}
			empty := scoped.textOccurrences.find("NeverPresent")
			if empty.name == "" || len(empty.sites) != 0 {
				t.Fatal("complete zero is not represented")
			}
		})
	}
}

func TestTextOccurrenceIndexBudgetFallbackIsAllOrNothing(t *testing.T) {
	base, names := textIndexFixture(t, "eager")
	_, full := WithTextOccurrences(base, names, 1<<20)
	if !full.Built {
		t.Fatal(full)
	}
	for _, budget := range []int64{0, 1, full.RosterBytes, full.RetainedBytes - 1} {
		got, stats := WithTextOccurrences(base, names, budget)
		if got != base || stats.Built || stats.RetainedBytes != 0 || stats.RosterBytes != 0 || stats.SiteCapacityBytes != 0 || stats.FallbackReason == "" {
			t.Fatalf("partial table published with budget %d: %+v", budget, stats)
		}
		for _, name := range names {
			if !reflect.DeepEqual(GenerateCandidates(got, name), GenerateCandidates(base, name)) {
				t.Fatalf("fallback changed %q", name)
			}
		}
	}
	// The exact accounted capacity is sufficient, independent of random hashes.
	scoped, exact := WithTextOccurrences(base, names, full.RetainedBytes)
	if !exact.Built || scoped == base || exact.RetainedBytes > exact.BudgetBytes {
		t.Fatalf("exact budget failed: %+v", exact)
	}
	var roster, sites int64
	roster = int64(cap(scoped.textOccurrences.entries))*int64(unsafe.Sizeof(textOccurrenceEntry{})) + int64(unsafe.Sizeof(textOccurrenceIndex{}))
	for _, entry := range scoped.textOccurrences.entries {
		roster += int64(len(entry.name))
		sites += int64(cap(entry.sites)) * int64(unsafe.Sizeof(siteKey{}))
	}
	if exact.RosterBytes != roster || exact.SiteCapacityBytes != sites {
		t.Fatalf("capacity accounting: %+v versus %d/%d", exact, roster, sites)
	}
}

func TestTextOccurrenceIndexRosterAndDisabledCases(t *testing.T) {
	base, _ := textIndexFixture(t, "eager")
	overflow := make([]string, maxTextOccurrenceNames+1)
	for i := range overflow {
		overflow[i] = "A"
	}
	for _, tc := range []struct {
		corpus *Corpus
		names  []string
		reason string
	}{
		{nil, []string{"Add"}, "nil corpus"},
		{base, []string{"", "A.B"}, "no identifier names"},
		{base, overflow, "name roster limit"},
	} {
		got, stats := WithTextOccurrences(tc.corpus, tc.names, 1<<20)
		if got != tc.corpus || stats.Built || stats.FallbackReason != tc.reason {
			t.Fatalf("unexpected fallback: %+v", stats)
		}
	}
}

func TestTextOccurrenceIndexConcurrentImmutableReaders(t *testing.T) {
	base, names := textIndexFixture(t, "content-only")
	scoped, stats := WithTextOccurrences(base, names, 1<<20)
	if !stats.Built {
		t.Fatal(stats)
	}
	expected := make(map[string][]Edge)
	for _, name := range names {
		expected[name] = GenerateCandidates(base, name)
	}
	// The index owns its roster, rather than retaining the caller's slice.
	names[0] = "Replaced"
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 5 {
				for name, want := range expected {
					if got := GenerateCandidates(scoped, name); !reflect.DeepEqual(got, want) {
						t.Errorf("concurrent result changed for %q", name)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}
