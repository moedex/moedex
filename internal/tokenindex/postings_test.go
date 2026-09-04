package tokenindex

import (
	"testing"

	"moedex/internal/index"
)

// threeBlobIndex builds a tiny index whose term statistics are known by hand.
func threeBlobIndex(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()
	ix.AddFile("r", "a.go", "/r/a.go", "sha-a", []byte("alpha beta alpha"))
	ix.AddFile("r", "b.go", "/r/b.go", "sha-b", []byte("beta gamma"))
	ix.AddFile("r", "c.go", "/r/c.go", "sha-c", []byte("gamma"))
	return ix
}

func TestPostingsExposesSortedBlobsAndFrequencies(t *testing.T) {
	ti := Build(threeBlobIndex(t))

	p := ti.Postings("alpha")
	if p.Len() != 1 {
		t.Fatalf("alpha df = %d, want 1", p.Len())
	}
	if p.Blob(0) != 0 || p.TF(0) != 2 {
		t.Fatalf("alpha posting = (blob %d, tf %d), want (0, 2)", p.Blob(0), p.TF(0))
	}

	b := ti.Postings("beta")
	if b.Len() != 2 {
		t.Fatalf("beta df = %d, want 2", b.Len())
	}
	if b.Blob(0) != 0 || b.Blob(1) != 1 {
		t.Fatalf("beta blobs = [%d %d], want ascending [0 1]", b.Blob(0), b.Blob(1))
	}
}

func TestPostingsAbsentTermIsEmptyNotPanic(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	p := ti.Postings("nosuchterm")
	if p.Len() != 0 {
		t.Fatalf("absent term df = %d, want 0", p.Len())
	}
	if p.TFOf(0) != 0 {
		t.Fatalf("absent term TFOf = %d, want 0", p.TFOf(0))
	}
}

func TestTFOfMatchesTermFreq(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	for _, term := range []string{"alpha", "beta", "gamma", "absent"} {
		p := ti.Postings(term)
		for blob := uint64(0); blob < 4; blob++ {
			if got, want := p.TFOf(blob), ti.TermFreq(term, blob); got != want {
				t.Fatalf("TFOf(%q,%d) = %d, TermFreq = %d", term, blob, got, want)
			}
		}
	}
}

func TestTermsAreStoredInLexicographicOrder(t *testing.T) {
	ti := Build(threeBlobIndex(t))
	prev := ""
	for i := 0; i < len(ti.termOff)-1; i++ {
		cur := string(ti.termBytes(i))
		if i > 0 && cur <= prev {
			t.Fatalf("term %d (%q) does not follow %q in ascending order", i, cur, prev)
		}
		prev = cur
	}
}
