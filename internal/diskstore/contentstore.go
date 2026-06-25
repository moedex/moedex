package diskstore

// contentstore.go implements the SHARED CONTENT STORE that backs the deduped
// served shard format (MOEDEX05). It is the served-side analogue of the CAS pack
// (internal/blobstore): each unique blob's content is stored EXACTLY ONCE for the
// whole served corpus, keyed by content hash, and mmap'd so the bytes stay on disk
// (off the Go heap) — preserving the mmap working-set property the served corpus
// depends on.
//
// # Why a separate shared file (not per-shard inlined content)
//
// The legacy served format (MOEDEX03/04) inlines each blob's full content into the
// shard's blob section. A blob whose repos land in different shards is therefore
// stored once per shard that holds a carrying repo — the served corpus loses the
// CAS's cross-shard dedup. MOEDEX05 shards instead carry a per-blob REFERENCE
// (content hash -> {offset,len}) into this one shared store, so the served corpus's
// content footprint drops to ~the CAS StoredBytes (the unique-content size).
//
// # On-disk layout (single file; all integers little-endian)
//
//	HEADER (fixed 32 bytes)
//	  magic       [8]byte  "MOECONT1"
//	  version     uint32   = contentStoreVersion (1)
//	  _reserved   uint32   = 0
//	  numBlobs    uint64   number of unique content records
//	  dirOff      uint64   absolute byte offset of the DIRECTORY section
//
//	CONTENT SECTION (starts at HEADER end; numBlobs records in write order)
//	  per blob: content [contentLen]byte   (raw indexed bytes, no framing)
//
//	DIRECTORY SECTION (starts at dirOff; numBlobs records)
//	  per blob:
//	    shaLen   uint32 ; sha [shaLen]byte
//	    contentOff uint64   absolute byte offset of the content in the CONTENT section
//	    contentLen uint64   content byte length
//
// Content bytes are stored contiguously and unframed so a loader can hand out a
// zero-copy sub-slice of the mmap for any blob. The SHA is an opaque variable-
// length key (SHA-1 today, SHA-256-ready), matching blobstore.
//
// # Integrity (content-addressed self-verification)
//
// Because the store is content-addressed, each directory key IS the expected hash
// of its bytes. OpenContentStoreVerified (used by the serving path) re-hashes every
// entry against its key at open (Verify), so a corrupt/garbled store — a present
// key whose bytes no longer hash to it — FAILS LOUDLY at boot instead of silently
// serving wrong-or-empty content (a parity-violating silent under-approximation).
// It is a one-time O(corpus-bytes) sequential pass over the mmap; an operator with
// a very large corpus can opt out (nil hasher) and trust the store as-is.

import (
	"bufio"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
)

const (
	// ContentStoreName is the shared content store filename written under a deduped
	// served shard dir, alongside the MOEDEX05 *.idx shards.
	ContentStoreName = "blobs.dat"

	contentMagic        = "MOECONT1"
	contentStoreVersion = 1
	contentHeaderSize   = 32 // magic[8] + version[4] + reserved[4] + numBlobs[8] + dirOff[8]
)

// ContentStoreWriter accumulates unique blob content in deterministic order and
// writes the shared content store. It is the export-side builder: PutContent is
// idempotent on the content hash, so a blob referenced by several repos/shards is
// physically written ONCE. NOT safe for concurrent use.
type ContentStoreWriter struct {
	dir     map[string]ContentRef // sha -> ref (offset/len), the dedup directory
	order   []string              // SHAs in insertion order (deterministic)
	content [][]byte              // content slices, parallel to order
	pos     int64                 // running content offset (next write position)
}

// ContentRef locates a blob's content within the shared store: its byte offset
// from the start of the file and its length.
type ContentRef struct {
	Offset int64
	Len    int64
}

// NewContentStoreWriter returns an empty writer.
func NewContentStoreWriter() *ContentStoreWriter {
	return &ContentStoreWriter{dir: map[string]ContentRef{}}
}

// PutContent records content under sha if not already present and returns its
// reference. It is idempotent: a repeat sha returns the existing ref WITHOUT
// re-storing (the cross-shard dedup primitive, mirroring blobstore.Store.Put).
// The CORRECTNESS CONTRACT matches blobstore: sha must be a content hash of the
// exact bytes, so an existing sha provably means identical bytes.
func (w *ContentStoreWriter) PutContent(sha string, content []byte) ContentRef {
	if ref, ok := w.dir[sha]; ok {
		return ref
	}
	ref := ContentRef{Offset: w.pos, Len: int64(len(content))}
	w.dir[sha] = ref
	w.order = append(w.order, sha)
	// Copy so the writer owns the bytes independent of the caller's buffer.
	w.content = append(w.content, append([]byte(nil), content...))
	w.pos += int64(len(content))
	return ref
}

// Ref returns the stored reference for sha and whether it is present.
func (w *ContentStoreWriter) Ref(sha string) (ContentRef, bool) {
	ref, ok := w.dir[sha]
	return ref, ok
}

// Len returns the number of unique content records.
func (w *ContentStoreWriter) Len() int { return len(w.order) }

// BytesStored returns the total content bytes physically stored (sum of unique
// blob content lengths) — the deduped footprint, comparable to
// blobstore.Store.BytesStored.
func (w *ContentStoreWriter) BytesStored() int64 { return w.pos }

// Write serializes the shared content store to path atomically (temp+rename) so a
// crash mid-write never leaves a half-store the shards reference.
func (w *ContentStoreWriter) Write(path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)

	// Header: content section starts right after the fixed header; the directory
	// section starts after all content.
	dirOff := uint64(contentHeaderSize) + uint64(w.pos)
	hdr := make([]byte, contentHeaderSize)
	copy(hdr[0:8], contentMagic)
	binary.LittleEndian.PutUint32(hdr[8:12], contentStoreVersion)
	binary.LittleEndian.PutUint32(hdr[12:16], 0)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(len(w.order)))
	binary.LittleEndian.PutUint64(hdr[24:32], dirOff)
	if _, err := bw.Write(hdr); err != nil {
		f.Close()
		return err
	}

	// CONTENT section.
	for _, c := range w.content {
		if _, err := bw.Write(c); err != nil {
			f.Close()
			return err
		}
	}

	// DIRECTORY section. Offsets are absolute file offsets (content section base is
	// contentHeaderSize), so a loader resolves content with no arithmetic.
	for _, sha := range w.order {
		var rec []byte
		rec = appendU32LenBytes(rec, []byte(sha))
		rec = binary.LittleEndian.AppendUint64(rec, uint64(contentHeaderSize)+uint64(w.dir[sha].Offset))
		rec = binary.LittleEndian.AppendUint64(rec, uint64(w.dir[sha].Len))
		if _, err := bw.Write(rec); err != nil {
			f.Close()
			return err
		}
	}

	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ContentStore is a read-only, mmap'd view of a shared content store. It serves
// each blob's content as a zero-copy sub-slice of the mapping, so content stays on
// disk / mmap'd (off the Go heap). The mapping must stay open as long as any blob
// content sub-slice it returned is in use; Close unmaps it.
type ContentStore struct {
	region *mmapRegion
	bySHA  map[string]ContentRef
}

// OpenContentStore mmaps the shared content store at path. Returns the store and
// an error; the caller must Close it when done. A missing file is reported as an
// error (callers that allow a legacy dir without a shared store check existence
// first).
func OpenContentStore(path string) (*ContentStore, error) {
	region, err := mmapOpenMin(path, contentHeaderSize)
	if err != nil {
		return nil, err
	}
	data := region.data
	if len(data) < contentHeaderSize {
		region.Close()
		return nil, fmt.Errorf("diskstore: content store too small (%d bytes)", len(data))
	}
	if string(data[0:8]) != contentMagic {
		region.Close()
		return nil, fmt.Errorf("diskstore: bad content store magic %q", data[0:8])
	}
	if v := binary.LittleEndian.Uint32(data[8:12]); v != contentStoreVersion {
		region.Close()
		return nil, fmt.Errorf("diskstore: unsupported content store version %d", v)
	}
	numBlobs := binary.LittleEndian.Uint64(data[16:24])
	dirOff := binary.LittleEndian.Uint64(data[24:32])
	if dirOff > uint64(len(data)) {
		region.Close()
		return nil, fmt.Errorf("diskstore: corrupt content store directory offset")
	}

	bySHA := make(map[string]ContentRef, numBlobs)
	r := &reader{b: data[dirOff:]}
	for i := uint64(0); i < numBlobs; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			region.Close()
			return nil, fmt.Errorf("diskstore: content dir entry %d sha: %w", i, err)
		}
		off, err := r.u64()
		if err != nil {
			region.Close()
			return nil, fmt.Errorf("diskstore: content dir entry %d off: %w", i, err)
		}
		clen, err := r.u64()
		if err != nil {
			region.Close()
			return nil, fmt.Errorf("diskstore: content dir entry %d len: %w", i, err)
		}
		if off+clen > uint64(len(data)) {
			region.Close()
			return nil, fmt.Errorf("diskstore: content dir entry %d out of bounds", i)
		}
		bySHA[string(sha)] = ContentRef{Offset: int64(off), Len: int64(clen)}
	}
	return &ContentStore{region: region, bySHA: bySHA}, nil
}

// ContentStoreDirDigest returns a SHA-256 of the store's DIRECTORY SECTION — the
// list of (content-hash key, offset, length) records — hex-encoded, or "" if the
// store cannot be read (best-effort; callers degrade to header-only identity).
//
// This is the CONTENT-TRUE identity of the store at O(numBlobs) cost, NOT
// O(total content bytes): because each directory key IS the content hash of its
// blob, the set of keys uniquely identifies the entire content set. Any
// added/removed/changed/reordered blob changes a key (or the record set), so two
// stores with identical byte size AND identical MOECONT1 header (same numBlobs/
// dirOff) but DIFFERENT content necessarily differ here — which is exactly the
// header-only collision this closes. It deliberately hashes the directory bytes
// (keys + offsets + lengths) and never the content payload, so it stays cheap on
// boot.
func ContentStoreDirDigest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) < contentHeaderSize || string(data[0:8]) != contentMagic {
		return ""
	}
	dirOff := binary.LittleEndian.Uint64(data[24:32])
	if dirOff > uint64(len(data)) {
		return ""
	}
	sum := sha256.Sum256(data[dirOff:])
	return hex.EncodeToString(sum[:])
}

// Content returns the zero-copy content sub-slice for sha, or ok=false if absent.
// The returned slice aliases the mmap and is valid only until Close; callers that
// need to retain it past Close must copy.
func (cs *ContentStore) Content(sha string) (content []byte, ok bool) {
	ref, ok := cs.bySHA[sha]
	if !ok {
		return nil, false
	}
	return cs.region.data[ref.Offset : ref.Offset+ref.Len], true
}

// GitBlobSHA1 is the canonical content-store key hasher: the SHA-1 of content in
// git's blob-object form ("blob <len>\0" + content), hex-encoded. It is BYTE-
// IDENTICAL to blobstore.contentKey (the scheme the deduped export keys blobs by),
// so passing it to Verify re-derives each entry's expected key from its stored
// bytes. It lives here, not in blobstore, so the serving path (which loads the
// store but does not import blobstore) can verify without a layering inversion; a
// future SHA-256 migration adds a sibling hasher and the format needs no change
// (keys are opaque strings).
func GitBlobSHA1(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// Verify re-hashes every stored blob's content with hasher and asserts it equals
// the directory key, failing LOUDLY on the first mismatch. Because the store is
// content-addressed, the key IS the expected hash, so a present-key-but-wrong-bytes
// entry (bit-rot, a truncated/garbled blobs.dat, an offset/length corruption that
// still parsed) is corruption that — absent this check — would be served as
// wrong-or-empty content: a SILENT under-approximation, the one thing parity
// forbids. This is a one-time O(corpus-bytes) hash pass at load (acceptable startup
// cost; the bytes are mmap'd, so it is a sequential read, not extra RAM). Pass
// GitBlobSHA1 for the current key scheme. A nil hasher is a no-op (verification
// disabled) so a caller can explicitly opt out for a huge corpus.
func (cs *ContentStore) Verify(hasher func([]byte) string) error {
	if hasher == nil {
		return nil
	}
	for sha, ref := range cs.bySHA {
		got := hasher(cs.region.data[ref.Offset : ref.Offset+ref.Len])
		if got != sha {
			return fmt.Errorf("diskstore: content store corruption: blob keyed %s hashes to %s (%d bytes) — wrong-or-corrupt content, refusing to serve", sha, got, ref.Len)
		}
	}
	return nil
}

// OpenContentStoreVerified opens the store and verifies every entry's content
// against its key with hasher in one call (the default serving path), so a corrupt
// shared store fails the boot rather than silently serving bad content. On a
// verification failure it closes the mapping and returns the error. Pass a nil
// hasher to skip verification (explicit opt-out for very large corpora).
func OpenContentStoreVerified(path string, hasher func([]byte) string) (*ContentStore, error) {
	cs, err := OpenContentStore(path)
	if err != nil {
		return nil, err
	}
	if err := cs.Verify(hasher); err != nil {
		cs.Close()
		return nil, err
	}
	return cs, nil
}

// Len returns the number of unique blobs in the store.
func (cs *ContentStore) Len() int { return len(cs.bySHA) }

// BytesStored sums the content bytes the store physically holds.
func (cs *ContentStore) BytesStored() int64 {
	var n int64
	for _, ref := range cs.bySHA {
		n += ref.Len
	}
	return n
}

// Close unmaps the store. After Close, content sub-slices it returned are invalid.
func (cs *ContentStore) Close() error {
	if cs.region == nil {
		return nil
	}
	err := cs.region.Close()
	cs.region = nil
	return err
}
