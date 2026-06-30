package index

import (
	"reflect"
	"testing"
)

func TestBuildTargetNilSelectorMatchesEagerIndex(t *testing.T) {
	files := []struct {
		rel     string
		sha     string
		content string
	}{
		{"a.txt", "sha-a", "alpha beta gamma"},
		{"b.txt", "sha-b", "beta gamma delta"},
	}

	eager := New()
	target := NewBuildTarget(nil)
	for _, f := range files {
		eager.AddFile("r", f.rel, "/abs/"+f.rel, f.sha, []byte(f.content))
		target.AddFile("r", f.rel, "/abs/"+f.rel, f.sha, []byte(f.content))
	}
	if target.NumBlobs() != eager.NumBlobs() {
		t.Fatalf("NumBlobs=%d want %d", target.NumBlobs(), eager.NumBlobs())
	}

	got := target.Finalize()
	if got.Selective() {
		t.Fatal("nil selector must use the non-selective eager path")
	}
	if len(got.Trigrams()) != len(eager.Trigrams()) {
		t.Fatalf("trigram count=%d want %d", len(got.Trigrams()), len(eager.Trigrams()))
	}
	for _, tg := range eager.Trigrams() {
		if !reflect.DeepEqual(got.Postings(tg), eager.Postings(tg)) {
			t.Fatalf("postings for %q differ: got=%v want=%v", tg.String(), got.Postings(tg), eager.Postings(tg))
		}
	}
}

func TestBuildTargetSelectorUsesSelectiveBuilder(t *testing.T) {
	target := NewBuildTarget(FrequencyThresholdSelector{MaxDocFraction: 0.5})
	target.AddFile("r", "0", "/abs/0", "s0", []byte("xyz aaa"))
	target.AddFile("r", "1", "/abs/1", "s1", []byte("xyz bbb"))
	target.AddFile("r", "2", "/abs/2", "s2", []byte("xyz qrs"))

	if target.NumBlobs() != 3 {
		t.Fatalf("NumBlobs=%d want 3", target.NumBlobs())
	}
	got := target.Finalize()
	if !got.Selective() {
		t.Fatal("non-nil selector must produce a selective index")
	}
	if got.IndexedGram(tg("xyz")) {
		t.Fatal("frequent gram xyz should be dropped by the selector")
	}
	if !got.IndexedGram(tg("qrs")) {
		t.Fatal("rare gram qrs should be kept by the selector")
	}
}
