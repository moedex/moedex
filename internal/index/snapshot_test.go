package index

import (
	"reflect"
	"sort"
	"testing"

	"moedex/internal/trigram"
)

func sortedTrigrams(ts []trigram.Trigram) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.String()
	}
	sort.Strings(out)
	return out
}

func TestSnapshotInIDOrder(t *testing.T) {
	ix := New()
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("foo"))
	ix.AddFile("repo", "b.txt", "/abs/b.txt", "sha2", []byte("bar"))

	snap := ix.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len(snap) = %d, want 2", len(snap))
	}
	if snap[0].SHA != "sha1" || string(snap[0].Content) != "foo" {
		t.Errorf("snap[0] = {SHA:%q Content:%q}, want {sha1 foo}", snap[0].SHA, snap[0].Content)
	}
	if snap[1].SHA != "sha2" || string(snap[1].Content) != "bar" {
		t.Errorf("snap[1] = {SHA:%q Content:%q}, want {sha2 bar}", snap[1].SHA, snap[1].Content)
	}
}

func TestSnapshotCopiesAreIndependent(t *testing.T) {
	ix := New()
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("foo"))

	snap := ix.Snapshot()
	// Mutating the snapshot's Content and Files must not touch the index.
	snap[0].Content[0] = 'X'
	snap[0].Files[0].RelPath = "mutated.txt"

	if got := string(ix.Blob(0).Content); got != "foo" {
		t.Errorf("index content = %q, want \"foo\" (snapshot must deep-copy Content)", got)
	}
	if got := ix.Blob(0).Files[0].RelPath; got != "a.txt" {
		t.Errorf("index RelPath = %q, want \"a.txt\" (snapshot must deep-copy Files)", got)
	}
}

func TestTrigramsEagerPath(t *testing.T) {
	ix := New()
	// "abca" -> {"abc", "bca"}.
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("abca"))

	if got := sortedTrigrams(ix.Trigrams()); !reflect.DeepEqual(got, []string{"abc", "bca"}) {
		t.Errorf("Trigrams = %v, want [abc bca]", got)
	}
}

func TestRestoreRebuildsBlobsAndPostings(t *testing.T) {
	blobs := []BlobData{
		{SHA: "sha1", Content: []byte("foo"), Files: []FileRef{{Repo: "r", RelPath: "a.txt", AbsPath: "/a"}}},
		{SHA: "sha2", Content: []byte("bar"), Files: []FileRef{{Repo: "r", RelPath: "b.txt", AbsPath: "/b"}}},
	}
	postings := map[trigram.Trigram][]Posting{
		tg("foo"): {{Blob: 0, Offset: 0}},
		tg("bar"): {{Blob: 1, Offset: 0}},
	}
	ix := Restore(blobs, postings)

	if ix.NumBlobs() != 2 {
		t.Fatalf("NumBlobs = %d, want 2", ix.NumBlobs())
	}
	if ix.Blob(1).SHA != "sha2" || ix.Blob(1).ID != 1 {
		t.Errorf("blob 1 = {ID:%d SHA:%q}, want {1 sha2}", ix.Blob(1).ID, ix.Blob(1).SHA)
	}
	// lineStarts must be rebuilt so LineOf works on restored blobs.
	if got := ix.Blob(0).LineOf(0); got != 1 {
		t.Errorf("restored LineOf(0) = %d, want 1", got)
	}
	if got := ix.Postings(tg("bar")); !reflect.DeepEqual(got, []Posting{{Blob: 1, Offset: 0}}) {
		t.Errorf("Postings(bar) = %v, want [{1 0}]", got)
	}
	// bySHA must be rebuilt: re-adding an existing SHA dedups rather than
	// creating a new blob.
	ix.AddFile("r", "c.txt", "/c", "sha1", []byte("foo"))
	if ix.NumBlobs() != 2 {
		t.Errorf("after re-adding sha1, NumBlobs = %d, want 2 (bySHA not rebuilt)", ix.NumBlobs())
	}
}

// TestRestoreAliasesContentWithoutCopying locks in Restore's documented
// contract: unlike AddFile (which defensively copies) and Snapshot (which
// deep-copies on the way out), Restore takes ownership of the BlobData slices
// passed in and aliases them directly into the index. A caller that mutates
// Content/Files after calling Restore would corrupt the indexed data.
func TestRestoreAliasesContentWithoutCopying(t *testing.T) {
	content := []byte("foo")
	files := []FileRef{{Repo: "r", RelPath: "a.txt", AbsPath: "/a"}}
	blobs := []BlobData{{SHA: "sha1", Content: content, Files: files}}

	ix := Restore(blobs, nil)
	content[0] = 'X'
	files[0].RelPath = "mutated.txt"

	if got := string(ix.Blob(0).Content); got != "Xoo" {
		t.Errorf("index content = %q, want %q (Restore must alias, not copy, Content)", got, "Xoo")
	}
	if got := ix.Blob(0).Files[0].RelPath; got != "mutated.txt" {
		t.Errorf("index RelPath = %q, want %q (Restore must alias, not copy, Files)", got, "mutated.txt")
	}
}

// fakePP is a PostingProvider that serves from an in-memory map, letting us
// assert that a lazily-loaded index delegates to its provider.
type fakePP struct {
	m map[trigram.Trigram][]Posting
}

func (f *fakePP) Postings(t trigram.Trigram) []Posting { return f.m[t] }
func (f *fakePP) Trigrams() []trigram.Trigram {
	out := make([]trigram.Trigram, 0, len(f.m))
	for t := range f.m {
		out = append(out, t)
	}
	return out
}

func TestRestoreLazyDelegatesToProvider(t *testing.T) {
	blobs := []BlobData{{SHA: "sha1", Content: []byte("foo")}}
	pp := &fakePP{m: map[trigram.Trigram][]Posting{
		tg("foo"): {{Blob: 0, Offset: 0}},
	}}
	ix := RestoreLazy(blobs, pp)

	if ix.NumBlobs() != 1 || ix.Blob(0).SHA != "sha1" {
		t.Fatalf("blobs not materialized: NumBlobs=%d", ix.NumBlobs())
	}
	if got := ix.Postings(tg("foo")); !reflect.DeepEqual(got, []Posting{{Blob: 0, Offset: 0}}) {
		t.Errorf("Postings delegated = %v, want [{0 0}]", got)
	}
	if got := sortedTrigrams(ix.Trigrams()); !reflect.DeepEqual(got, []string{"foo"}) {
		t.Errorf("Trigrams delegated = %v, want [foo]", got)
	}
}
