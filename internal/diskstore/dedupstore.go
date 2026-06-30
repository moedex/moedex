package diskstore

// dedupstore.go implements the DEDUPED served shard format MOEDEX05: a shard that
// carries postings + per-blob CONTENT-HASH REFERENCES + file refs, but does NOT
// inline blob content. Content lives ONCE in the shared content store
// (contentstore.go, blobs.dat) shared by every shard in the served dir, so a blob
// whose repos land in different shards is stored once for the whole served corpus
// — realizing the CAS's cross-shard dedup on the served side.
//
// This is the format the deduped cas-export path writes (blobstore.ExportDedupedShardDir).
// The direct `moedex-index build` path keeps writing MOEDEX03/04 (inlined content)
// unchanged. The loaders here resolve each blob's content from the shared store as
// a zero-copy mmap sub-slice, so served content stays off the Go heap (an
// improvement over MOEDEX03, whose loadBlobs copies content onto the heap).
//
// # On-disk byte layout (all integers little-endian)
//
//	HEADER (fixed 48 bytes — same shape as MOEDEX03)
//	  magic        [8]byte   "MOEDEX05"
//	  version      uint32    = formatVersionV5 (5)
//	  _reserved    uint32    = 0
//	  numBlobs     uint64
//	  numTrigrams  uint64
//	  blobOff      uint64    absolute byte offset of the BLOB SECTION
//	  postOff      uint64    absolute byte offset of the POSTINGS SECTION
//
//	BLOB SECTION (numBlobs records, blob ID == index order) — CONTENT-LESS:
//	  per blob:
//	    shaLen     uint32 ; sha [shaLen]byte    (the content-store key)
//	    numFiles   uint32
//	    per file ref: repoLen u32+bytes, relLen u32+bytes, absLen u32+bytes
//
//	POSTINGS SECTION (numTrigrams records) — identical to MOEDEX03.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"

	"moedex/internal/index"
	"moedex/internal/trigram"
)

const (
	magicDeduped    = "MOEDEX05"
	formatVersionV5 = 5
	headerSizeV5    = 48 // same shape as MOEDEX03
)

// contentRegistrar is the shared-content-store sink SaveDeduped registers each
// blob's content with. Both the full-export writer (*ContentStoreWriter) and the
// delta-export appender (*ContentStoreAppender) satisfy it, so the SAME shard
// serializer drives both the full and the incremental deduped export — the shard
// on-disk format is byte-identical regardless of which sink stored the content.
// PutContent is idempotent on the content hash (a present hash is a no-op), the
// cross-shard / co-resident dedup primitive.
type contentRegistrar interface {
	PutContent(sha string, content []byte) ContentRef
}

// SaveDeduped writes ix to path in the MOEDEX05 (content-less) format and records
// each unique blob's content in cw (the shared content store writer). The shard
// stores only the SHA + file refs per blob; cw stores the content ONCE keyed by
// SHA across all shards. The caller writes cw out once for the whole dir
// (cw.Write) after saving every shard.
//
// CONTRACT: ix.Blob(id).SHA must be a content hash of the blob's content (the CAS
// invariant), so cw's idempotent PutContent dedups correctly across shards.
func SaveDeduped(ix *index.Index, path string, cw *ContentStoreWriter) error {
	return saveDeduped(ix, path, cw)
}

// SaveDedupedAppender is the DELTA-export shard serializer: identical to
// SaveDeduped but registers content with a *ContentStoreAppender (which carries an
// existing store forward and appends only net-new content). The shard bytes are
// byte-identical to what SaveDeduped would write for the same index — only the
// content-store sink differs — so a delta-rewritten shard is indistinguishable on
// disk from a fully-re-exported one.
func SaveDedupedAppender(ix *index.Index, path string, a *ContentStoreAppender) error {
	return saveDeduped(ix, path, a)
}

func saveDeduped(ix *index.Index, path string, cw contentRegistrar) error {
	blobs := ix.Snapshot()
	trigrams := ix.Trigrams()

	// Register each blob's content in the shared store (dedup across shards) and
	// serialize the content-less blob section.
	blobBuf := make([]byte, 0, 1<<16)
	for _, b := range blobs {
		cw.PutContent(b.SHA, b.Content)
		blobBuf = appendDedupedBlob(blobBuf, b)
	}

	blobOff := uint64(headerSizeV5)
	postOff := blobOff + uint64(len(blobBuf))

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	hdr := make([]byte, headerSizeV5)
	copy(hdr[0:8], magicDeduped)
	binary.LittleEndian.PutUint32(hdr[8:12], formatVersionV5)
	binary.LittleEndian.PutUint32(hdr[12:16], 0)
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(len(blobs)))
	binary.LittleEndian.PutUint64(hdr[24:32], uint64(len(trigrams)))
	binary.LittleEndian.PutUint64(hdr[32:40], blobOff)
	binary.LittleEndian.PutUint64(hdr[40:48], postOff)
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write(blobBuf); err != nil {
		return err
	}

	if err := writePostings(w, ix, trigrams); err != nil {
		return err
	}
	return w.Flush()
}

// appendDedupedBlob serializes one blob WITHOUT its content: sha + file refs only.
func appendDedupedBlob(buf []byte, b index.BlobData) []byte {
	buf = appendU32LenBytes(buf, []byte(b.SHA))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(b.Files)))
	for _, fr := range b.Files {
		buf = appendU32LenBytes(buf, []byte(fr.Repo))
		buf = appendU32LenBytes(buf, []byte(fr.RelPath))
		buf = appendU32LenBytes(buf, []byte(fr.AbsPath))
	}
	return buf
}

// IsDeduped reports whether the shard at path is a MOEDEX05 (deduped, content-less)
// shard, by reading only its magic. A read/size error reports false (callers fall
// back to the legacy inlined-content loaders). This is the dispatch primitive
// server.Open/OpenRank use to decide whether a shared content store is required.
func IsDeduped(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var m [8]byte
	if _, err := f.ReadAt(m[:], 0); err != nil {
		return false
	}
	return string(m[:]) == magicDeduped
}

// dedupedHeader is the parsed MOEDEX05 header.
type dedupedHeader struct {
	numBlobs    uint64
	numTrigrams uint64
	blobOff     uint64
	postOff     uint64
}

func parseDedupedHeader(data []byte) (dedupedHeader, error) {
	if len(data) < headerSizeV5 {
		return dedupedHeader{}, fmt.Errorf("diskstore: deduped file too small (%d bytes)", len(data))
	}
	if string(data[0:8]) != magicDeduped {
		return dedupedHeader{}, fmt.Errorf("diskstore: bad deduped magic %q", data[0:8])
	}
	if v := binary.LittleEndian.Uint32(data[8:12]); v != formatVersionV5 {
		return dedupedHeader{}, fmt.Errorf("diskstore: unsupported deduped version %d", v)
	}
	h := dedupedHeader{
		numBlobs:    binary.LittleEndian.Uint64(data[16:24]),
		numTrigrams: binary.LittleEndian.Uint64(data[24:32]),
		blobOff:     binary.LittleEndian.Uint64(data[32:40]),
		postOff:     binary.LittleEndian.Uint64(data[40:48]),
	}
	if h.blobOff > uint64(len(data)) || h.postOff > uint64(len(data)) || h.blobOff > h.postOff {
		return dedupedHeader{}, fmt.Errorf("diskstore: corrupt deduped section offsets")
	}
	return h, nil
}

// loadDedupedBlobs reads the content-less blob section and resolves each blob's
// content from the shared content store cs (zero-copy mmap sub-slice). The
// returned BlobData[i] is the blob with shard-local ID i, with Content aliasing
// the content-store mmap — valid only while cs is open. Every referenced SHA MUST
// be present in cs (the export writes cs from the same blob set); a missing SHA is
// a corrupt/mismatched dir and is reported as an error rather than silently
// dropping content (which would under-approximate — a SACRED parity violation).
func loadDedupedBlobs(sec []byte, n int, cs *ContentStore) ([]index.BlobData, error) {
	r := &reader{b: sec}
	blobs := make([]index.BlobData, n)
	for i := 0; i < n; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			return nil, fmt.Errorf("diskstore: deduped blob %d sha: %w", i, err)
		}
		numFiles, err := r.u32()
		if err != nil {
			return nil, fmt.Errorf("diskstore: deduped blob %d numFiles: %w", i, err)
		}
		if err := r.checkCount(numFiles, minFileRefSize); err != nil {
			return nil, fmt.Errorf("diskstore: deduped blob %d numFiles: %w", i, err)
		}
		files := make([]index.FileRef, numFiles)
		for j := range files {
			repo, err := r.lenBytes()
			if err != nil {
				return nil, fmt.Errorf("diskstore: deduped blob %d file %d repo: %w", i, j, err)
			}
			rel, err := r.lenBytes()
			if err != nil {
				return nil, fmt.Errorf("diskstore: deduped blob %d file %d rel: %w", i, j, err)
			}
			abs, err := r.lenBytes()
			if err != nil {
				return nil, fmt.Errorf("diskstore: deduped blob %d file %d abs: %w", i, j, err)
			}
			files[j] = index.FileRef{Repo: string(repo), RelPath: string(rel), AbsPath: string(abs)}
		}
		shaKey := string(sha)
		content, ok := cs.Content(shaKey)
		if !ok {
			return nil, fmt.Errorf("diskstore: deduped blob %d sha %s not in shared content store", i, shaKey)
		}
		// Content aliases the content-store mmap (zero-copy). It stays valid while
		// the ContentStore is open — server.Corpus/RankCorpus hold the store for the
		// shards' lifetime, so the search/rank layers see stable bytes off the heap.
		blobs[i] = index.BlobData{
			SHA:     shaKey,
			Content: content,
			Files:   files,
		}
	}
	return blobs, nil
}

// LoadMmapDeduped memory-maps a MOEDEX05 shard at path and returns an index whose
// posting lists are decoded on demand from the mapping (like LoadMmap) and whose
// blob content is served as zero-copy sub-slices of cs (the shared content store).
// Both the shard's posting mapping AND cs stay mmap'd, so neither postings nor
// content enter the Go heap. Call the returned Closer when done with the shard
// (it unmaps the shard's own mapping; cs is owned and closed by the caller).
func LoadMmapDeduped(path string, cs *ContentStore) (*index.Index, *mmapRegion, error) {
	region, err := mmapOpen(path)
	if err != nil {
		return nil, nil, err
	}
	data := region.data
	hdr, err := parseDedupedHeader(data)
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	blobs, err := loadDedupedBlobs(data[hdr.blobOff:hdr.postOff], int(hdr.numBlobs), cs)
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	raw := make(map[trigram.Trigram][]byte, hdr.numTrigrams)
	err = walkPostings(data[hdr.postOff:], int(hdr.numTrigrams), func(t trigram.Trigram, enc []byte) {
		raw[t] = enc
	})
	if err != nil {
		region.Close()
		return nil, nil, err
	}
	ix := index.RestoreLazy(blobs, &mmapProvider{raw: raw})
	return ix, region, nil
}

// LoadBlobsDeduped reads only the content-less blob section of a MOEDEX05 shard,
// resolving content from cs (zero-copy mmap sub-slices), and skips the postings
// section entirely — the deduped analogue of LoadBlobs for the corpus ranker.
// result[i] is the blob with shard-local ID i. As with LoadMmapDeduped, content
// aliases the content-store mmap and is valid only while cs is open.
func LoadBlobsDeduped(path string, cs *ContentStore) ([]index.BlobData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hdr, err := parseDedupedHeader(data)
	if err != nil {
		return nil, err
	}
	return loadDedupedBlobs(data[hdr.blobOff:hdr.postOff], int(hdr.numBlobs), cs)
}
