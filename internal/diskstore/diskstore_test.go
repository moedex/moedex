package diskstore

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/index"
	"moedex/internal/ingest"
)

// gitBlobSHA computes the git blob SHA-1 of content ("blob <len>\0" + content),
// so synthetic blobs are keyed exactly as ingest would key them.
func gitBlobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// assertRoundTrip saves ix, loads it back, and asserts the loaded index is
// identical: blob count, per-blob SHA/Files/Runes, and every trigram's postings.
func assertRoundTrip(t *testing.T, ix *index.Index) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "index.moedex")
	if err := Save(ix, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.NumBlobs() != ix.NumBlobs() {
		t.Fatalf("NumBlobs = %d, want %d", got.NumBlobs(), ix.NumBlobs())
	}

	for id := 0; id < ix.NumBlobs(); id++ {
		want := ix.Blob(uint64(id))
		have := got.Blob(uint64(id))
		if have.SHA != want.SHA {
			t.Errorf("blob %d SHA = %q, want %q", id, have.SHA, want.SHA)
		}
		if !reflect.DeepEqual(have.Files, want.Files) {
			t.Errorf("blob %d Files = %#v, want %#v", id, have.Files, want.Files)
		}
		if !reflect.DeepEqual(have.Content, want.Content) {
			t.Errorf("blob %d Content mismatch (len have=%d want=%d)", id, len(have.Content), len(want.Content))
		}
	}

	for _, tg := range ix.Trigrams() {
		if !reflect.DeepEqual(got.Postings(tg), ix.Postings(tg)) {
			t.Errorf("postings for %q mismatch:\n have=%v\n want=%v", tg, got.Postings(tg), ix.Postings(tg))
		}
	}
}

func TestRoundTripSynthetic(t *testing.T) {
	ix := index.New()

	// A multi-byte unicode line plus ASCII, to exercise rune offsets.
	unicode := []byte("package main\nfunc café() { 日本語 = \"naïve\" }\nαβγδ trigram\n")
	ix.AddFile("repo", "uni.go", "/abs/uni.go", gitBlobSHA(unicode), unicode)

	// Two byte-identical files: must dedup to one blob with two FileRefs.
	dup := []byte("shared content\nline two here\n")
	dupSHA := gitBlobSHA(dup)
	ix.AddFile("repo", "a.txt", "/abs/a.txt", dupSHA, dup)
	ix.AddFile("repo", "b.txt", "/abs/b.txt", dupSHA, dup)

	// A third distinct file for good measure.
	ix.AddFile("repo", "c.txt", "/abs/c.txt", gitBlobSHA([]byte("abcabcabc")), []byte("abcabcabc"))

	if ix.NumBlobs() != 3 {
		t.Fatalf("expected 3 blobs after dedup, got %d", ix.NumBlobs())
	}
	// Confirm the dedup blob actually carries two refs (so the round trip is
	// meaningfully testing the multi-FileRef path).
	dupBlob := ix.Blob(1)
	if len(dupBlob.Files) != 2 {
		t.Fatalf("expected dedup blob to have 2 files, got %d", len(dupBlob.Files))
	}

	assertRoundTrip(t, ix)
}

func TestRoundTripConfiguredCorpus(t *testing.T) {
	dir := os.Getenv("MOEDEX_EVAL_CORPUS")
	if dir == "" {
		t.Skip("set MOEDEX_EVAL_CORPUS to a local repository")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("configured repo not present at %s: %v", dir, err)
	}

	files, err := ingest.Repo(filepath.Base(dir), dir)
	if err != nil {
		t.Skipf("ingest.Repo failed (not a git repo?): %v", err)
	}
	if len(files) == 0 {
		t.Skip("configured repo has no indexable files")
	}

	ix := index.New()
	for _, f := range files {
		ix.AddFile(f.Repo, f.RelPath, f.AbsPath, f.SHA, f.Content)
	}
	t.Logf("indexed %d files into %d blobs, %d trigrams", len(files), ix.NumBlobs(), len(ix.Trigrams()))

	assertRoundTrip(t, ix)
}
