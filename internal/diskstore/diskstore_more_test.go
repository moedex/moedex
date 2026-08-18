package diskstore

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/index"
)

// buildSynthetic returns a small but non-trivial index: one multi-byte unicode
// file, a deduped pair, and a repetitive ASCII file. It mirrors the corpus used
// by TestRoundTripSynthetic so the mmap and error tests exercise real postings.
func buildSynthetic(t *testing.T) *index.Index {
	t.Helper()
	ix := index.New()

	unicode := []byte("package main\nfunc café() { 日本語 = \"naïve\" }\nαβγδ trigram\n")
	ix.AddFile("repo", "uni.go", "/abs/uni.go", gitBlobSHA(unicode), unicode)

	dup := []byte("shared content\nline two here\n")
	dupSHA := gitBlobSHA(dup)
	ix.AddFile("repo", "a.txt", "/abs/a.txt", dupSHA, dup)
	ix.AddFile("repo", "b.txt", "/abs/b.txt", dupSHA, dup)

	ix.AddFile("repo", "c.txt", "/abs/c.txt", gitBlobSHA([]byte("abcabcabc")), []byte("abcabcabc"))
	return ix
}

// assertIndexEqual checks that got reproduces want's blobs and every trigram's
// postings. Used for both the in-memory and mmap load paths.
func assertIndexEqual(t *testing.T, got, want *index.Index) {
	t.Helper()
	if got.NumBlobs() != want.NumBlobs() {
		t.Fatalf("NumBlobs = %d, want %d", got.NumBlobs(), want.NumBlobs())
	}
	for id := 0; id < want.NumBlobs(); id++ {
		w := want.Blob(uint64(id))
		h := got.Blob(uint64(id))
		if h.SHA != w.SHA {
			t.Errorf("blob %d SHA = %q, want %q", id, h.SHA, w.SHA)
		}
		if !reflect.DeepEqual(h.Files, w.Files) {
			t.Errorf("blob %d Files = %#v, want %#v", id, h.Files, w.Files)
		}
		if !reflect.DeepEqual(h.Content, w.Content) {
			t.Errorf("blob %d Content mismatch (have len=%d want len=%d)", id, len(h.Content), len(w.Content))
		}
	}
	for _, tg := range want.Trigrams() {
		if !reflect.DeepEqual(got.Postings(tg), want.Postings(tg)) {
			t.Errorf("postings for %q mismatch:\n have=%v\n want=%v", tg, got.Postings(tg), want.Postings(tg))
		}
	}
}

func saveTemp(t *testing.T, ix *index.Index) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.moedex")
	if err := Save(ix, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path
}

func TestLoadMmapRoundTrip(t *testing.T) {
	ix := buildSynthetic(t)
	path := saveTemp(t, ix)

	got, closer, err := LoadMmap(path)
	if err != nil {
		t.Fatalf("LoadMmap: %v", err)
	}

	// Trigrams() must come from the provider and cover exactly the saved set.
	if len(got.Trigrams()) != len(ix.Trigrams()) {
		t.Errorf("mmap Trigrams count = %d, want %d", len(got.Trigrams()), len(ix.Trigrams()))
	}
	// This decodes postings out of the mapping (mmapProvider.Postings).
	assertIndexEqual(t, got, ix)

	if err := closer.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Close must be idempotent — a second call should be a no-op, not a double
	// munmap error.
	if err := closer.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestEmptyIndexRoundTrip(t *testing.T) {
	ix := index.New()
	if ix.NumBlobs() != 0 || len(ix.Trigrams()) != 0 {
		t.Fatalf("expected empty index, got %d blobs / %d trigrams", ix.NumBlobs(), len(ix.Trigrams()))
	}
	path := saveTemp(t, ix)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertIndexEqual(t, got, ix)

	gotM, closer, err := LoadMmap(path)
	if err != nil {
		t.Fatalf("LoadMmap: %v", err)
	}
	defer closer.Close()
	assertIndexEqual(t, gotM, ix)
}

func TestParseHeaderErrors(t *testing.T) {
	// A valid header to mutate from.
	valid := func() []byte {
		b := make([]byte, headerSize)
		copy(b[0:8], magic)
		binary.LittleEndian.PutUint32(b[8:12], formatVersion)
		binary.LittleEndian.PutUint64(b[16:24], 0)          // numBlobs
		binary.LittleEndian.PutUint64(b[24:32], 0)          // numTrigrams
		binary.LittleEndian.PutUint64(b[32:40], headerSize) // blobOff
		binary.LittleEndian.PutUint64(b[40:48], headerSize) // postOff
		return b
	}

	tests := []struct {
		name    string
		data    []byte
		wantSub string
	}{
		{"too small", make([]byte, headerSize-1), "too small"},
		{
			name:    "bad magic",
			data:    func() []byte { b := valid(); copy(b[0:8], "NOPEXX00"); return b }(),
			wantSub: "bad magic",
		},
		{
			name:    "bad version",
			data:    func() []byte { b := valid(); binary.LittleEndian.PutUint32(b[8:12], 99); return b }(),
			wantSub: "unsupported version",
		},
		{
			name:    "blobOff past EOF",
			data:    func() []byte { b := valid(); binary.LittleEndian.PutUint64(b[32:40], 1<<20); return b }(),
			wantSub: "corrupt section offsets",
		},
		{
			name: "blobOff after postOff",
			data: func() []byte {
				b := valid()
				binary.LittleEndian.PutUint64(b[32:40], 48) // blobOff
				binary.LittleEndian.PutUint64(b[40:48], 40) // postOff < blobOff
				return b
			}(),
			wantSub: "corrupt section offsets",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHeader(tc.data)
			if err == nil {
				t.Fatalf("parseHeader = nil error, want %q", tc.wantSub)
			}
			if !contains(err.Error(), tc.wantSub) {
				t.Errorf("parseHeader err = %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}

// TestParseHeaderSelectiveOverflow: a MOEDEX04 header whose selCount*trigram.N
// overflows uint64 and wraps around to a small value must still be rejected.
// selCount below is chosen so that, mod 2^64, selCount*3 == 64: the naive check
// `h.selOff + h.selCount*trigram.N > uint64(len(data))` computes 64+64 == 128,
// which is NOT greater than len(data)==128, so it wrongly accepts a selCount of
// ~1.2e19 — a SELECTION section that would run ~3.6e19 bytes past EOF. The
// overflow-safe check must catch this via division, which cannot overflow.
func TestParseHeaderSelectiveOverflow(t *testing.T) {
	const dataLen = 128
	b := make([]byte, dataLen)
	copy(b[0:8], magicSelective)
	binary.LittleEndian.PutUint32(b[8:12], formatVersionV4)
	binary.LittleEndian.PutUint64(b[16:24], 0)                    // numBlobs
	binary.LittleEndian.PutUint64(b[24:32], 0)                    // numTrigrams
	binary.LittleEndian.PutUint64(b[32:40], headerSizeV4)         // blobOff
	binary.LittleEndian.PutUint64(b[40:48], headerSizeV4)         // postOff
	binary.LittleEndian.PutUint64(b[48:56], headerSizeV4)         // selOff
	binary.LittleEndian.PutUint64(b[56:64], 12297829382473034432) // selCount: *3 wraps mod 2^64 to 64

	_, err := parseHeader(b)
	if err == nil {
		t.Fatal("parseHeader accepted a selCount whose *trigram.N overflows uint64 and wraps past the bound check, want corrupt-selection error")
	}
	if !contains(err.Error(), "corrupt selection") {
		t.Errorf("parseHeader err = %q, want substring %q", err, "corrupt selection")
	}
}

// TestParseHeaderRejectsImplausibleBlobCount is the regression for the
// eager-make-from-untrusted-count gap in loadBlobs' caller: numBlobs is an
// attacker-controlled header field that used to be handed straight to
// make([]index.BlobData, n) before a single blob record was read. A numBlobs
// claiming far more blob records than the BLOB SECTION's actual bytes could
// ever encode (each record needs >= minBlobRecordSize bytes) must be rejected
// by parseHeader itself, not discovered only once loadBlobs' bounds-checked
// reads run out of data. The 32-byte blob section below can hold at most two
// 16-byte records, so 5,000,000 is implausible for it while staying far below
// the scale (~4e9, a multi-hundred-GB allocation) the real finding reproduced.
func TestParseHeaderRejectsImplausibleBlobCount(t *testing.T) {
	const blobSectionLen = 32
	const dataLen = headerSize + blobSectionLen
	b := make([]byte, dataLen)
	copy(b[0:8], magic)
	binary.LittleEndian.PutUint32(b[8:12], formatVersion)
	binary.LittleEndian.PutUint64(b[16:24], 5_000_000)                 // numBlobs: implausible for a 32-byte section
	binary.LittleEndian.PutUint64(b[24:32], 0)                         // numTrigrams
	binary.LittleEndian.PutUint64(b[32:40], headerSize)                // blobOff
	binary.LittleEndian.PutUint64(b[40:48], headerSize+blobSectionLen) // postOff: blob section is the whole tail

	_, err := parseHeader(b)
	if err == nil {
		t.Fatal("parseHeader accepted a numBlobs the blob section could not possibly hold")
	}
	if !contains(err.Error(), "blob count") {
		t.Errorf("parseHeader err = %q, want substring %q", err, "blob count")
	}
}

// TestParseHeaderRejectsImplausibleTrigramCount is the loadMmap/walkPostings
// counterpart: numTrigrams used to size a postings map directly. See
// TestParseHeaderRejectsImplausibleBlobCount for the full rationale.
func TestParseHeaderRejectsImplausibleTrigramCount(t *testing.T) {
	const postSectionLen = 22 // fits at most two 11-byte trigram records
	const dataLen = headerSize + postSectionLen
	b := make([]byte, dataLen)
	copy(b[0:8], magic)
	binary.LittleEndian.PutUint32(b[8:12], formatVersion)
	binary.LittleEndian.PutUint64(b[16:24], 0)          // numBlobs
	binary.LittleEndian.PutUint64(b[24:32], 5_000_000)  // numTrigrams: implausible for a 22-byte section
	binary.LittleEndian.PutUint64(b[32:40], headerSize) // blobOff
	binary.LittleEndian.PutUint64(b[40:48], headerSize) // postOff: postings section is the whole tail

	_, err := parseHeader(b)
	if err == nil {
		t.Fatal("parseHeader accepted a numTrigrams the postings section could not possibly hold")
	}
	if !contains(err.Error(), "trigram count") {
		t.Errorf("parseHeader err = %q, want substring %q", err, "trigram count")
	}
}

// TestParseHeaderSelectiveRejectsImplausibleTrigramCount is the MOEDEX04
// (selective) analogue: that branch bounds numTrigrams against selOff-postOff
// rather than len(data)-postOff, a distinct expression that needs its own
// coverage.
func TestParseHeaderSelectiveRejectsImplausibleTrigramCount(t *testing.T) {
	const postSectionLen = 22
	const dataLen = headerSizeV4 + postSectionLen
	b := make([]byte, dataLen)
	copy(b[0:8], magicSelective)
	binary.LittleEndian.PutUint32(b[8:12], formatVersionV4)
	binary.LittleEndian.PutUint64(b[16:24], 0)                           // numBlobs
	binary.LittleEndian.PutUint64(b[24:32], 5_000_000)                   // numTrigrams: implausible for a 22-byte section
	binary.LittleEndian.PutUint64(b[32:40], headerSizeV4)                // blobOff
	binary.LittleEndian.PutUint64(b[40:48], headerSizeV4)                // postOff
	binary.LittleEndian.PutUint64(b[48:56], headerSizeV4+postSectionLen) // selOff
	binary.LittleEndian.PutUint64(b[56:64], 0)                           // selCount

	_, err := parseHeader(b)
	if err == nil {
		t.Fatal("parseHeader accepted a numTrigrams the postings section could not possibly hold (MOEDEX04)")
	}
	if !contains(err.Error(), "trigram count") {
		t.Errorf("parseHeader err = %q, want substring %q", err, "trigram count")
	}
}

func TestLoadNonexistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.moedex")

	if _, err := Load(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load err = %v, want os.ErrNotExist", err)
	}
	if _, _, err := LoadMmap(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadMmap err = %v, want os.ErrNotExist", err)
	}
}

func TestLoadMmapTooSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.moedex")
	if err := os.WriteFile(path, []byte("MOEDEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadMmap(path)
	if err == nil {
		t.Fatal("LoadMmap on tiny file = nil error, want failure")
	}
	if !contains(err.Error(), "too small") {
		t.Errorf("err = %q, want substring %q", err, "too small")
	}
}

func TestLoadTruncatedPostings(t *testing.T) {
	ix := buildSynthetic(t)
	path := saveTemp(t, ix)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Lop off the tail of the postings section: the header still claims the full
	// trigram count, so walkPostings must run off the end and report EOF rather
	// than silently returning a partial index.
	truncated := data[:len(data)-16]
	badPath := filepath.Join(t.TempDir(), "truncated.moedex")
	if err := os.WriteFile(badPath, truncated, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Load(badPath)
	if err == nil {
		t.Fatal("Load of truncated file = nil error, want failure")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Load err = %v, want wrapped io.ErrUnexpectedEOF", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return len(sub) == 0
}
