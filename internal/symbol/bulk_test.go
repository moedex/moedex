package symbol

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/index"
)

func TestBulkBuildAndLoadMatchIncremental(t *testing.T) {
	sources := index.New()
	want := NewIndex()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("file%d.cs", i)
		content := []byte(fmt.Sprintf("public class Type%d {\n public void Shared() { Target(); }\n}\n", i))
		sources.AddFile("repo", name, name, name, content)
	}
	for id := uint64(0); id < uint64(sources.NumBlobs()); id++ {
		syms, refs, err := CSharpExtractor{}.ExtractDefsRefs(sources.Blob(id).Content)
		if err != nil {
			t.Fatal(err)
		}
		want.Set(id, syms)
		want.SetRefs(id, refs)
	}
	for _, got := range []*Index{BuildMulti(sources), Build(sources, CSharpExtractor{})} {
		if got.deferNames {
			t.Fatal("unfinished bulk index published")
		}
		assertSameIndex(t, got, want)
		// References-only blobs must survive the separate union pass on reload.
		got.SetRefs(999, []Occurrence{{Name: "Target", Role: Reference, Start: 2, End: 8}})
		path := filepath.Join(t.TempDir(), "symbols")
		if err := Save(got, path); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		assertSameIndex(t, loaded, got)
		var before, after bytes.Buffer
		if err := writeIndex(&before, got); err != nil {
			t.Fatal(err)
		}
		if err := writeIndex(&after, loaded); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before.Bytes(), after.Bytes()) {
			t.Fatal("bulk reload changed deterministic serialization")
		}
		// Bulk finalization must leave ordinary mutation semantics intact.
		got.Set(0, nil)
		got.SetRefs(0, nil)
		loaded.Set(0, nil)
		loaded.SetRefs(0, nil)
		assertSameIndex(t, loaded, got)
		for _, ref := range got.References("Shared") {
			if ref.Blob == 0 {
				t.Fatal("removed blob remains inverted")
			}
		}
	}
}

func assertSameIndex(t *testing.T, got, want *Index) {
	t.Helper()
	if !reflect.DeepEqual(got.byBlob, want.byBlob) || !reflect.DeepEqual(got.refsByBlob, want.refsByBlob) {
		t.Fatal("per-blob values differ")
	}
	if len(got.byName) != len(want.byName) {
		t.Fatal("name counts differ")
	}
	for name := range want.byName {
		if !reflect.DeepEqual(got.References(name), want.References(name)) || !reflect.DeepEqual(got.Definitions(name), want.Definitions(name)) {
			t.Fatalf("name %s differs", name)
		}
	}
}

func BenchmarkBulkNameConstruction(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			syms := []Symbol{{Name: "Shared", NameStart: 1, NameEnd: 7, BodyEnd: 20}}
			refs := []Occurrence{{Name: "Target", Role: Reference, Start: 10, End: 16}}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ix := NewIndex()
				ix.deferNames = true
				for id := 0; id < n; id++ {
					ix.Set(uint64(id), syms)
					ix.SetRefs(uint64(id), refs)
				}
				ix.rebuildNames()
				if len(ix.byName["Shared"]) != n {
					b.Fatal("missing definitions")
				}
			}
		})
	}
}
