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
	"io"
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

// ContentStoreAppender is the DELTA-aware writer for the shared content store: it
// APPENDS only net-new blob content to an EXISTING MOECONT1 store, carrying every
// already-present blob's content forward byte-for-byte at its exact prior offset.
// It is the served-side analogue of the CAS pack's append-only Put (blobstore):
// PutContent on an already-present content hash is a dedup no-op (no bytes added),
// and the existing content section is never rewritten — Write streams the prior
// content bytes through verbatim and only the (small) directory is re-emitted.
//
// Because the MOECONT1 directory section lives AFTER the content section, a
// raw-file in-place append is not possible without moving the directory; this
// appender instead writes a fresh file whose content section is [old content bytes
// (copied byte-for-byte) ++ new content bytes], preserving every existing blob's
// absolute offset, so the file the prior store referenced by offset is reproduced
// exactly plus the appended tail. The directory then covers old+new in insertion
// order. This keeps the delta minimal (BytesAppended == sum of net-new content
// lengths) while remaining a single atomic temp+rename write.
//
// NOT safe for concurrent use.
type ContentStoreAppender struct {
	srcPath string                // the existing store being extended (streamed forward)
	srcLen  int64                 // existing content-section byte length (== old pos)
	dir     map[string]ContentRef // sha -> ref over the COMBINED (old+new) store
	order   []string              // SHAs in combined insertion order (old first, then new)
	newSHAs []string              // SHAs appended this round (the net-new content)
	newBuf  [][]byte              // new content slices, parallel to newSHAs
	pos     int64                 // running content offset (next write position)
	added   int64                 // net-new content bytes appended (the delta footprint)
}

// OpenContentStoreAppender opens the existing MOECONT1 store at path for delta
// extension, seeding the appender with its directory (sha -> {offset,len}) and
// insertion order WITHOUT loading the content payload into memory (it is streamed
// from the file at Write time). A subsequent PutContent of a present sha is a
// dedup no-op; a new sha is queued for append. Write(out) then emits a complete
// store (old content carried forward byte-for-byte + new content appended).
//
// The seeding reads only the fixed header and the O(numBlobs) directory section,
// so opening a multi-GB store is cheap; the content section is never mapped here.
func OpenContentStoreAppender(path string) (*ContentStoreAppender, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr := make([]byte, contentHeaderSize)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, fmt.Errorf("diskstore: read content store header: %w", err)
	}
	if string(hdr[0:8]) != contentMagic {
		return nil, fmt.Errorf("diskstore: bad content store magic %q", hdr[0:8])
	}
	if v := binary.LittleEndian.Uint32(hdr[8:12]); v != contentStoreVersion {
		return nil, fmt.Errorf("diskstore: unsupported content store version %d", v)
	}
	numBlobs := binary.LittleEndian.Uint64(hdr[16:24])
	dirOff := binary.LittleEndian.Uint64(hdr[24:32])
	if dirOff < contentHeaderSize {
		return nil, fmt.Errorf("diskstore: corrupt content store directory offset %d", dirOff)
	}

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if dirOff > uint64(fi.Size()) {
		return nil, fmt.Errorf("diskstore: content store directory offset %d past EOF %d", dirOff, fi.Size())
	}

	// Read just the directory section (O(numBlobs)); the content payload stays on
	// disk and is streamed at Write time.
	dirBytes := make([]byte, uint64(fi.Size())-dirOff)
	if _, err := f.ReadAt(dirBytes, int64(dirOff)); err != nil && err != io.EOF {
		return nil, fmt.Errorf("diskstore: read content store directory: %w", err)
	}

	a := &ContentStoreAppender{
		srcPath: path,
		srcLen:  int64(dirOff) - contentHeaderSize,
		dir:     make(map[string]ContentRef, numBlobs),
	}
	r := &reader{b: dirBytes}
	for i := uint64(0); i < numBlobs; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			return nil, fmt.Errorf("diskstore: content dir entry %d sha: %w", i, err)
		}
		off, err := r.u64()
		if err != nil {
			return nil, fmt.Errorf("diskstore: content dir entry %d off: %w", i, err)
		}
		clen, err := r.u64()
		if err != nil {
			return nil, fmt.Errorf("diskstore: content dir entry %d len: %w", i, err)
		}
		// Stored offsets are ABSOLUTE (content base == contentHeaderSize); normalize
		// to a content-relative offset for the combined store's directory, matching
		// the convention ContentStoreWriter uses internally (ref.Offset is relative).
		key := string(sha)
		a.dir[key] = ContentRef{Offset: int64(off) - contentHeaderSize, Len: int64(clen)}
		a.order = append(a.order, key)
	}
	a.pos = a.srcLen
	return a, nil
}

// PutContent records content under sha if not already present (in the existing
// store OR queued this round) and returns its content-relative reference. It is
// idempotent: a present sha returns the existing ref and adds ZERO bytes — the
// delta dedup primitive, identical in contract to ContentStoreWriter.PutContent
// and blobstore.Store.Put. sha must be a content hash of the exact bytes.
//
// INTEGRITY (intentional, not a gap): the dedup-skip of an already-present sha
// trusts that the prior store's bytes under that key are correct; it does NOT
// re-hash them. This is deliberate — re-hashing the carried-forward store on every
// append would be an O(corpus) pass that defeats the whole point of an incremental
// delta. Correctness is instead guaranteed at SERVE time: the serving path opens the
// shared content store with OpenContentStoreVerified(GitBlobSHA1) (default-on; see
// server.openSharedContent / MOEDEX_VERIFY_CONTENT), which re-hashes every entry
// against its key and FAILS THE BOOT on any present-key-but-wrong-bytes corruption.
// So a bit-rotted prior store cannot be silently served — it is caught at the next
// open, whether or not a delta append ran. The appended store is itself a valid
// MOECONT1 file the same verify-at-open covers. (Moving the check earlier is a
// one-line opt-in: open the prior store via OpenContentStoreVerified before
// appending, gated by the same flag — NOT done here by default, to preserve the
// incremental win.)
func (a *ContentStoreAppender) PutContent(sha string, content []byte) ContentRef {
	if ref, ok := a.dir[sha]; ok {
		return ref
	}
	ref := ContentRef{Offset: a.pos, Len: int64(len(content))}
	a.dir[sha] = ref
	a.order = append(a.order, sha)
	a.newSHAs = append(a.newSHAs, sha)
	a.newBuf = append(a.newBuf, append([]byte(nil), content...))
	a.pos += int64(len(content))
	a.added += int64(len(content))
	return ref
}

// Ref returns the stored reference for sha and whether it is present.
func (a *ContentStoreAppender) Ref(sha string) (ContentRef, bool) {
	ref, ok := a.dir[sha]
	return ref, ok
}

// Len returns the number of unique content records in the combined store.
func (a *ContentStoreAppender) Len() int { return len(a.order) }

// BytesStored returns the total content bytes the combined store holds (old+new).
func (a *ContentStoreAppender) BytesStored() int64 { return a.pos }

// BytesAppended returns ONLY the net-new content bytes queued this round — the
// honest delta footprint (the bytes physically appended beyond the prior store).
func (a *ContentStoreAppender) BytesAppended() int64 { return a.added }

// BlobsAppended returns the number of net-new unique blobs queued this round.
func (a *ContentStoreAppender) BlobsAppended() int { return len(a.newSHAs) }

// Write serializes the combined store to outPath atomically (temp+rename). The
// content section is the prior store's content bytes (streamed byte-for-byte from
// the source, preserving every existing blob's offset) followed by the net-new
// content; the directory covers old+new in insertion order. outPath may equal the
// source path (the rename swaps the new file into place after the source is fully
// read into the temp file). A crash mid-write leaves the durable source intact.
func (a *ContentStoreAppender) Write(outPath string) error {
	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)

	dirOff := uint64(contentHeaderSize) + uint64(a.pos)
	hdr := make([]byte, contentHeaderSize)
	copy(hdr[0:8], contentMagic)
	binary.LittleEndian.PutUint32(hdr[8:12], contentStoreVersion)
	binary.LittleEndian.PutUint32(hdr[12:16], 0)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(len(a.order)))
	binary.LittleEndian.PutUint64(hdr[24:32], dirOff)
	if _, err := bw.Write(hdr); err != nil {
		f.Close()
		return err
	}

	// CONTENT section — old content bytes streamed forward byte-for-byte (preserving
	// every prior blob's absolute offset), then the appended net-new content.
	if a.srcLen > 0 {
		src, err := os.Open(a.srcPath)
		if err != nil {
			f.Close()
			return err
		}
		// Copy exactly the prior content section [contentHeaderSize, contentHeaderSize+srcLen).
		if _, err := src.Seek(contentHeaderSize, io.SeekStart); err != nil {
			src.Close()
			f.Close()
			return err
		}
		if _, err := io.CopyN(bw, src, a.srcLen); err != nil {
			src.Close()
			f.Close()
			return fmt.Errorf("diskstore: copy prior content section: %w", err)
		}
		src.Close()
	}
	for _, c := range a.newBuf {
		if _, err := bw.Write(c); err != nil {
			f.Close()
			return err
		}
	}

	// DIRECTORY section — absolute offsets (content base == contentHeaderSize).
	for _, sha := range a.order {
		ref := a.dir[sha]
		var rec []byte
		rec = appendU32LenBytes(rec, []byte(sha))
		rec = binary.LittleEndian.AppendUint64(rec, uint64(contentHeaderSize)+uint64(ref.Offset))
		rec = binary.LittleEndian.AppendUint64(rec, uint64(ref.Len))
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
	return os.Rename(tmp, outPath)
}

// CompactContentStore rewrites the MOECONT1 store at srcPath into outPath keeping
// ONLY the content whose key is in live, dropping every unreferenced (dead) blob,
// and writes the result atomically (temp+rename). It is the in-place CHEAP space
// reclaim for the shared content store: it rewrites the EXISTING store from its own
// live entries rather than re-exporting every shard from the CAS (the expensive
// path). live is the conservative liveness set — the union of content-hash refs of
// every live MOEDEX05 shard (DedupedShardSHAs) — so a key NOT in live is provably
// referenced by no live shard and safe to drop.
//
// SACRED CONSTRAINT: a key present in `live` but ABSENT from the source store is a
// caller bug (a shard references content the store does not hold) and is reported as
// an error rather than silently dropped — dropping it would under-approximate. The
// kept entries preserve their content bytes exactly; only dead bytes are reclaimed.
// outPath may equal srcPath (the rename swaps the new file in after the source is
// fully read). It returns the number of blobs kept and the content bytes kept.
//
// The written store is a valid MOECONT1 file: the same mmap working-set property and
// the same content-integrity verify (OpenContentStoreVerified / GitBlobSHA1) apply,
// because each kept entry's key is copied with its exact bytes — a re-hash still
// matches. Insertion order of the kept entries follows the source's directory order
// (deterministic given the source).
func CompactContentStore(srcPath, outPath string, live map[string]bool) (keptBlobs int, keptBytes int64, err error) {
	cs, err := OpenContentStore(srcPath)
	if err != nil {
		return 0, 0, err
	}
	defer cs.Close()

	// Verify every live key is present in the source BEFORE writing anything, so a
	// caller bug surfaces as a loud error and never as a silent dropped reference.
	for sha := range live {
		if _, ok := cs.bySHA[sha]; !ok {
			return 0, 0, fmt.Errorf("diskstore: compact content store: live key %s not in source store %s (would under-approximate)", sha, srcPath)
		}
	}

	// Rebuild from the source's directory order so the output is deterministic and
	// the carried-forward content keeps a stable layout. We read each kept blob's
	// bytes from the mmap and re-Put them into a fresh writer (which assigns new,
	// gap-free offsets — that is the space reclaim).
	w := NewContentStoreWriter()
	// Iterate in directory insertion order. The mmap directory map is unordered, so
	// re-derive order by walking the on-disk directory section.
	order, err := dedupedStoreOrder(srcPath)
	if err != nil {
		return 0, 0, err
	}
	for _, sha := range order {
		if !live[sha] {
			continue // dead: drop it (the reclaim)
		}
		content, ok := cs.Content(sha)
		if !ok {
			// Cannot happen (we verified above), but never silently drop.
			return 0, 0, fmt.Errorf("diskstore: compact content store: key %s vanished mid-compaction", sha)
		}
		w.PutContent(sha, content)
	}
	if err := w.Write(outPath); err != nil {
		return 0, 0, err
	}
	return w.Len(), w.BytesStored(), nil
}

// ContentStoreSHAs returns every content-hash key physically present in the MOECONT1
// store at path, in on-disk directory (insertion) order, reading only the header +
// directory section (O(numBlobs), no content payload). It is the public liveness/
// audit probe — the full set of keys the store holds, of which compaction keeps only
// the referenced subset.
func ContentStoreSHAs(path string) ([]string, error) {
	return dedupedStoreOrder(path)
}

// dedupedStoreOrder returns the content keys of the MOECONT1 store at path in their
// on-disk directory (insertion) order, reading only the header + directory section
// (O(numBlobs), no content payload). It is the order-preserving companion to the
// unordered bySHA map OpenContentStore builds.
func dedupedStoreOrder(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < contentHeaderSize || string(data[0:8]) != contentMagic {
		return nil, fmt.Errorf("diskstore: bad content store magic in %s", path)
	}
	numBlobs := binary.LittleEndian.Uint64(data[16:24])
	dirOff := binary.LittleEndian.Uint64(data[24:32])
	if dirOff > uint64(len(data)) {
		return nil, fmt.Errorf("diskstore: corrupt content store directory offset")
	}
	out := make([]string, 0, numBlobs)
	r := &reader{b: data[dirOff:]}
	for i := uint64(0); i < numBlobs; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			return nil, fmt.Errorf("diskstore: content dir entry %d sha: %w", i, err)
		}
		if _, err := r.u64(); err != nil { // contentOff (skip)
			return nil, err
		}
		if _, err := r.u64(); err != nil { // contentLen (skip)
			return nil, err
		}
		out = append(out, string(sha))
	}
	return out, nil
}

// DedupedShardSHAs reads ONLY the content-less blob section of a MOEDEX05 shard at
// path and returns the set of content-hash keys it references — WITHOUT a shared
// content store and WITHOUT touching the postings or content payload. It is the
// liveness probe for served-store compaction: the union over all live shards is the
// conservative set of content that must be kept. A non-MOEDEX05 / unreadable shard
// is reported as an error (the caller must not treat "couldn't read a shard" as
// "that shard references nothing" — that would under-approximate liveness).
func DedupedShardSHAs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hdr, err := parseDedupedHeader(data)
	if err != nil {
		return nil, err
	}
	sec := data[hdr.blobOff:hdr.postOff]
	r := &reader{b: sec}
	out := make([]string, 0, hdr.numBlobs)
	for i := uint64(0); i < hdr.numBlobs; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			return nil, fmt.Errorf("diskstore: deduped shard %s blob %d sha: %w", path, i, err)
		}
		numFiles, err := r.u32()
		if err != nil {
			return nil, fmt.Errorf("diskstore: deduped shard %s blob %d numFiles: %w", path, i, err)
		}
		// Skip the file refs (repo, rel, abs) — we only need the SHA.
		for j := uint32(0); j < numFiles; j++ {
			if _, err := r.lenBytes(); err != nil { // repo
				return nil, fmt.Errorf("diskstore: deduped shard %s blob %d file %d repo: %w", path, i, j, err)
			}
			if _, err := r.lenBytes(); err != nil { // rel
				return nil, fmt.Errorf("diskstore: deduped shard %s blob %d file %d rel: %w", path, i, j, err)
			}
			if _, err := r.lenBytes(); err != nil { // abs
				return nil, fmt.Errorf("diskstore: deduped shard %s blob %d file %d abs: %w", path, i, j, err)
			}
		}
		out = append(out, string(sha))
	}
	return out, nil
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
