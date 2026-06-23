package index

import (
	"reflect"
	"testing"

	"moedex/internal/trigram"
)

func tg(s string) trigram.Trigram { return trigram.Trigram{s[0], s[1], s[2]} }

func TestBlobLineOf(t *testing.T) {
	// content: "ab\ncd\ne" -> line starts at byte offsets 0, 3, 6.
	//   bytes: a(0) b(1) \n(2) c(3) d(4) \n(5) e(6)
	b := &Blob{Content: []byte("ab\ncd\ne")}
	b.lineStarts = lineStarts(b.Content)

	cases := []struct {
		name string
		off  int
		want int
	}{
		{"first byte", 0, 1},
		{"mid first line", 1, 1},
		{"newline byte belongs to its line", 2, 1},
		{"start of second line", 3, 2},
		{"mid second line", 4, 2},
		{"last line", 6, 3},
		{"offset past end clamps to last line", 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := b.LineOf(tc.off); got != tc.want {
				t.Errorf("LineOf(%d) = %d, want %d", tc.off, got, tc.want)
			}
		})
	}
}

func TestBlobLineOfNoTrailingNewline(t *testing.T) {
	b := &Blob{Content: []byte("ab")}
	b.lineStarts = lineStarts(b.Content)
	for _, off := range []int{0, 1} {
		if got := b.LineOf(off); got != 1 {
			t.Errorf("LineOf(%d) = %d, want 1", off, got)
		}
	}
}

func TestAddFileDedupBySHA(t *testing.T) {
	ix := New()
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("hello"))
	ix.AddFile("repo", "b.txt", "/abs/b.txt", "sha1", []byte("hello"))

	if ix.NumBlobs() != 1 {
		t.Fatalf("NumBlobs = %d, want 1 (same SHA must dedup)", ix.NumBlobs())
	}
	b := ix.Blob(0)
	wantFiles := []FileRef{
		{Repo: "repo", RelPath: "a.txt", AbsPath: "/abs/a.txt"},
		{Repo: "repo", RelPath: "b.txt", AbsPath: "/abs/b.txt"},
	}
	if !reflect.DeepEqual(b.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", b.Files, wantFiles)
	}
	// Content indexed once: each trigram of "hello" appears exactly once.
	if got := ix.Postings(tg("hel")); !reflect.DeepEqual(got, []Posting{{Blob: 0, Offset: 0}}) {
		t.Errorf("postings for \"hel\" = %v, want single posting", got)
	}
}

func TestAddFileDistinctSHAGetsNewID(t *testing.T) {
	ix := New()
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("foo"))
	ix.AddFile("repo", "b.txt", "/abs/b.txt", "sha2", []byte("bar"))

	if ix.NumBlobs() != 2 {
		t.Fatalf("NumBlobs = %d, want 2", ix.NumBlobs())
	}
	if ix.Blob(0).ID != 0 || ix.Blob(0).SHA != "sha1" {
		t.Errorf("blob 0 = {ID:%d SHA:%q}, want {0 sha1}", ix.Blob(0).ID, ix.Blob(0).SHA)
	}
	if ix.Blob(1).ID != 1 || ix.Blob(1).SHA != "sha2" {
		t.Errorf("blob 1 = {ID:%d SHA:%q}, want {1 sha2}", ix.Blob(1).ID, ix.Blob(1).SHA)
	}
	// Each blob's content is indexed under its own ID.
	if got := ix.Postings(tg("foo")); !reflect.DeepEqual(got, []Posting{{Blob: 0, Offset: 0}}) {
		t.Errorf("postings for \"foo\" = %v, want {Blob:0 Offset:0}", got)
	}
	if got := ix.Postings(tg("bar")); !reflect.DeepEqual(got, []Posting{{Blob: 1, Offset: 0}}) {
		t.Errorf("postings for \"bar\" = %v, want {Blob:1 Offset:0}", got)
	}
}

func TestAddFileOwnsContent(t *testing.T) {
	ix := New()
	buf := []byte("abc")
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", buf)
	// Mutating the caller's buffer must not corrupt the indexed content.
	buf[0] = 'X'
	if got := string(ix.Blob(0).Content); got != "abc" {
		t.Errorf("Blob content = %q, want \"abc\" (index must copy caller buffer)", got)
	}
}

func TestAddFilePostingsSortedAndCorrect(t *testing.T) {
	ix := New()
	// "abca" -> trigrams: "abc"@0, "bca"@1.
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("abca"))
	// Second blob adds more postings; lists must stay sorted by (Blob, Offset).
	ix.AddFile("repo", "b.txt", "/abs/b.txt", "sha2", []byte("abc"))

	if got := ix.Postings(tg("abc")); !reflect.DeepEqual(got, []Posting{{Blob: 0, Offset: 0}, {Blob: 1, Offset: 0}}) {
		t.Errorf("postings for \"abc\" = %v, want [{0 0} {1 0}]", got)
	}
	if got := ix.Postings(tg("bca")); !reflect.DeepEqual(got, []Posting{{Blob: 0, Offset: 1}}) {
		t.Errorf("postings for \"bca\" = %v, want [{0 1}]", got)
	}
}

func TestBlobOutOfRangeReturnsNil(t *testing.T) {
	ix := New()
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("abc"))
	// One blob (id 0) exists. Any id >= NumBlobs is out of range and must
	// return nil rather than panic, so callers can bounds-check by nil-check.
	cases := []uint64{1, 2, 100, ^uint64(0)}
	for _, id := range cases {
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Blob(%d) panicked: %v, want nil", id, r)
				}
			}()
			if got := ix.Blob(id); got != nil {
				t.Errorf("Blob(%d) = %v, want nil", id, got)
			}
		})
	}
	// In-range id still resolves.
	if got := ix.Blob(0); got == nil || got.ID != 0 {
		t.Errorf("Blob(0) = %v, want blob with ID 0", got)
	}
}

func TestPostingsEagerPath(t *testing.T) {
	ix := New()
	ix.AddFile("repo", "a.txt", "/abs/a.txt", "sha1", []byte("abc"))

	if got := ix.Postings(tg("abc")); !reflect.DeepEqual(got, []Posting{{Blob: 0, Offset: 0}}) {
		t.Errorf("present trigram = %v, want [{0 0}]", got)
	}
	if got := ix.Postings(tg("zzz")); got != nil {
		t.Errorf("absent trigram = %v, want nil", got)
	}
}
