// Package blobstore is moedex's global content-addressable store (CAS): the
// storage layer that makes cross-shard dedup and per-blob delta indexing
// first-class, the self-declared biggest win of the 2026 redesign (CHANGE #1).
//
// # Why a CAS
//
// Today (pre-CAS) the unit of storage is a content-byte *shard* that interleaves
// repos (internal/parity/corpus.go), and each shard inlines blob content via
// diskstore.Save. A blob whose byte-identical content appears in two repos that
// land in two different shards is therefore stored TWICE on disk: the in-RAM
// dedup (index.bySHA) is per-shard, so it never spans shards. Freshness is
// shard-level: parity.Rebuild re-ingests every co-resident repo of any changed
// repo (internal/parity/manifest.go), losing cross-shard dedup and re-indexing
// far more than the change.
//
// A CAS keyed by a content hash of the ingested bytes stores each unique blob's
// content exactly ONCE for the whole corpus. The key is blobstore.contentKey: a
// SHA-1 of the exact bytes ingested in git's blob-object form, which for a CLEAN
// file equals git's `ls-files -s` SHA but for a DIRTY file (working tree !=
// committed blob) is the true hash of the working-tree bytes ingest actually
// read — NOT git's stale index SHA. Keying on the bytes-actually-stored makes a
// Put idempotent AND content-true: the second repo that carries byte-identical
// content adds nothing, while a file whose content differs from an already-stored
// blob is never dedup-skipped. That single primitive is global cross-shard dedup.
// A repo's identity becomes its *set of blob keys* (see manifest.go), so
// re-indexing a changed repo is a set-diff that touches only its net-new blobs —
// the per-blob delta — and never its co-resident repos.
//
// # On-disk layout (two files in a directory; all integers little-endian)
//
//	blobs.pack   append-only, one record per unique blob in insertion order:
//	  shaLen     uint32 ; sha     [shaLen]byte
//	  contentLen uint64 ; content [contentLen]byte
//
//	blobs.idx    the SHA -> {packOffset, packLen} directory, written atomically
//	             (temp+rename) only after the pack is fsynced, so a crash never
//	             leaves an index entry pointing past the durable pack:
//	  magic      [8]byte  "MOEBLOB1"
//	  version    uint32   = idxVersion (1)
//	  _reserved  uint32   = 0
//	  numBlobs   uint64
//	  packBytes  uint64   pack size the index was written against (recovery bound)
//	  per blob (in pack order):
//	    shaLen   uint32 ; sha [shaLen]byte
//	    packOff  uint64
//	    packLen  uint64    (== shaLen-record + content-record length in the pack)
//	    contentLen uint64  (the blob's content byte length, for BytesStored)
//
// The SHA is treated as an OPAQUE variable-length string key (never parsed,
// never fixed-width packed), so a future migration to SHA-256 blob hashes needs
// no format change here.
//
// Append-only + idempotent Put means a delta-add is O(net-new blobs) and never
// rewrites the pack. Space reclamation for blobs no longer referenced by any
// repo (compaction/GC) is deliberately deferred (see RefreshCAS); the pack grows
// monotonically, which is correct (never under-approximates) if not minimal.
package blobstore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	packName   = "blobs.pack"
	idxName    = "blobs.idx"
	idxMagic   = "MOEBLOB1"
	idxVersion = 1
	idxHeader  = 32 // magic[8] + version[4] + reserved[4] + numBlobs[8] + packBytes[8]
)

// entry is the in-memory directory record for one unique blob.
type entry struct {
	packOff    int64
	packLen    int64
	contentLen int64
}

// Store is a global content-addressed blob store rooted at a directory. It holds
// every unique blob's content exactly once, keyed by git blob SHA. A Store is
// NOT safe for concurrent writers; BuildCAS/RefreshCAS use it single-threaded.
type Store struct {
	dir     string
	packF   *os.File      // append handle for the pack
	packW   *bufio.Writer // buffered writer over packF
	packPos int64         // current pack size (== next append offset)
	order   []string      // SHAs in pack insertion order (for deterministic export)
	byID    map[string]entry
	dirty   bool // index needs re-persisting on Close
}

// Open opens (creating if absent) the CAS rooted at dir, loading the existing
// SHA directory if blobs.idx is present. On open it validates that every
// indexed blob lies within the durable pack (append-only recovery): a pack that
// grew past the last persisted index (a crash between pack-append and
// index-rewrite) is truncated back to the indexed length, so the recovered
// Store is exactly the last durably-indexed state.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, byID: map[string]entry{}}
	if err := s.loadIndex(); err != nil {
		return nil, err
	}

	packPath := filepath.Join(dir, packName)
	fi, err := os.Stat(packPath)
	switch {
	case os.IsNotExist(err):
		// fresh store
	case err != nil:
		return nil, err
	default:
		// Recovery: if the pack is longer than the index accounted for, a prior
		// run appended blob bytes but crashed before rewriting blobs.idx. Truncate
		// the unindexed tail so the pack and index agree (append-only is safe to
		// roll back to the last durable index).
		if fi.Size() > s.packPos {
			if err := os.Truncate(packPath, s.packPos); err != nil {
				return nil, fmt.Errorf("blobstore: recover pack: %w", err)
			}
		} else if fi.Size() < s.packPos {
			return nil, fmt.Errorf("blobstore: pack %d shorter than index expects %d (corrupt)", fi.Size(), s.packPos)
		}
	}

	f, err := os.OpenFile(packPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.packF = f
	s.packW = bufio.NewWriter(f)
	return s, nil
}

// loadIndex reads blobs.idx into memory. A missing index is a fresh store.
func (s *Store) loadIndex() error {
	data, err := os.ReadFile(filepath.Join(s.dir, idxName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) < idxHeader {
		return fmt.Errorf("blobstore: index too small (%d bytes)", len(data))
	}
	if string(data[0:8]) != idxMagic {
		return fmt.Errorf("blobstore: bad index magic %q", data[0:8])
	}
	if v := binary.LittleEndian.Uint32(data[8:12]); v != idxVersion {
		return fmt.Errorf("blobstore: unsupported index version %d", v)
	}
	numBlobs := binary.LittleEndian.Uint64(data[16:24])
	packBytes := binary.LittleEndian.Uint64(data[24:32])

	r := &reader{b: data, pos: idxHeader}
	for i := uint64(0); i < numBlobs; i++ {
		sha, err := r.lenBytes()
		if err != nil {
			return fmt.Errorf("blobstore: index entry %d sha: %w", i, err)
		}
		packOff, err := r.u64()
		if err != nil {
			return fmt.Errorf("blobstore: index entry %d off: %w", i, err)
		}
		packLen, err := r.u64()
		if err != nil {
			return fmt.Errorf("blobstore: index entry %d len: %w", i, err)
		}
		contentLen, err := r.u64()
		if err != nil {
			return fmt.Errorf("blobstore: index entry %d clen: %w", i, err)
		}
		key := string(sha)
		s.byID[key] = entry{packOff: int64(packOff), packLen: int64(packLen), contentLen: int64(contentLen)}
		s.order = append(s.order, key)
	}
	s.packPos = int64(packBytes)
	return nil
}

// Has reports whether sha is already stored.
func (s *Store) Has(sha string) bool {
	_, ok := s.byID[sha]
	return ok
}

// Put stores content under sha if not already present and reports whether it was
// newly added. It is idempotent: a Put of a SHA already present is a no-op that
// returns added=false WITHOUT re-reading or comparing content — this is the
// global cross-shard dedup primitive. CORRECTNESS CONTRACT: sha must be a content
// hash of content (a hash of the exact bytes being stored), so that an existing
// SHA provably means identical bytes and the dedup skip is content-true. The CAS
// builders satisfy this via blobstore.contentKey (hash of the ingested bytes),
// NOT git's index SHA — see build.go contentKey for why the distinction matters
// for dirty files.
func (s *Store) Put(sha string, content []byte) (added bool, err error) {
	if _, ok := s.byID[sha]; ok {
		return false, nil
	}
	off := s.packPos
	rec := appendU32LenBytes(nil, []byte(sha))
	rec = binary.LittleEndian.AppendUint64(rec, uint64(len(content)))
	rec = append(rec, content...)
	if _, err := s.packW.Write(rec); err != nil {
		return false, err
	}
	s.packPos += int64(len(rec))
	s.byID[sha] = entry{packOff: off, packLen: int64(len(rec)), contentLen: int64(len(content))}
	s.order = append(s.order, sha)
	s.dirty = true
	return true, nil
}

// Get returns the content stored under sha, read from the pack. It returns an
// error if the SHA is absent.
func (s *Store) Get(sha string) ([]byte, error) {
	e, ok := s.byID[sha]
	if !ok {
		return nil, fmt.Errorf("blobstore: sha %s not found", sha)
	}
	// Flush any buffered appends so a Get after Put sees them on disk.
	if err := s.packW.Flush(); err != nil {
		return nil, err
	}
	buf := make([]byte, e.packLen)
	if _, err := s.packF.ReadAt(buf, e.packOff); err != nil && err != io.EOF {
		// packF is opened write-only for appends; reopen read-only for ReadAt.
		return s.getViaReopen(e)
	}
	return decodeRecord(buf)
}

// getViaReopen reads a blob record by reopening the pack read-only. (The append
// handle is write-only, so ReadAt on it can fail on some platforms.)
func (s *Store) getViaReopen(e entry) ([]byte, error) {
	f, err := os.Open(filepath.Join(s.dir, packName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, e.packLen)
	if _, err := f.ReadAt(buf, e.packOff); err != nil {
		return nil, err
	}
	return decodeRecord(buf)
}

// decodeRecord parses one pack record (shaLen+sha, contentLen+content) and
// returns the content.
func decodeRecord(rec []byte) ([]byte, error) {
	r := &reader{b: rec}
	if _, err := r.lenBytes(); err != nil { // sha (skip)
		return nil, fmt.Errorf("blobstore: decode sha: %w", err)
	}
	clen, err := r.u64()
	if err != nil {
		return nil, fmt.Errorf("blobstore: decode content len: %w", err)
	}
	content, err := r.bytes(int(clen))
	if err != nil {
		return nil, fmt.Errorf("blobstore: decode content: %w", err)
	}
	return append([]byte(nil), content...), nil
}

// Len returns the number of unique blobs stored.
func (s *Store) Len() int { return len(s.byID) }

// BytesStored returns the total CONTENT bytes physically stored across all unique
// blobs (excludes per-record SHA + length framing). It is strictly the sum of
// unique blob content lengths — the dedup win is that this is < the raw bytes the
// corpus would inline per-shard when the same content recurs across repos.
func (s *Store) BytesStored() int64 {
	var n int64
	for _, e := range s.byID {
		n += e.contentLen
	}
	return n
}

// SHAs returns the stored blob SHAs in pack insertion order (deterministic).
func (s *Store) SHAs() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// Flush persists the pack buffer and rewrites the index atomically. It is called
// by Close and may be called by builders at checkpoints.
func (s *Store) Flush() error {
	if err := s.packW.Flush(); err != nil {
		return err
	}
	if err := s.packF.Sync(); err != nil {
		return err
	}
	if !s.dirty {
		return nil
	}
	if err := s.writeIndex(); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// writeIndex serializes the SHA directory to blobs.idx atomically (temp+rename),
// mirroring parity.WriteManifest. Called only after the pack is fsynced so the
// index never references bytes that aren't durable.
func (s *Store) writeIndex() error {
	buf := make([]byte, idxHeader)
	copy(buf[0:8], idxMagic)
	binary.LittleEndian.PutUint32(buf[8:12], idxVersion)
	binary.LittleEndian.PutUint32(buf[12:16], 0)
	binary.LittleEndian.PutUint64(buf[16:24], uint64(len(s.order)))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(s.packPos))
	for _, sha := range s.order {
		e := s.byID[sha]
		buf = appendU32LenBytes(buf, []byte(sha))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(e.packOff))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(e.packLen))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(e.contentLen))
	}
	tmp := filepath.Join(s.dir, idxName+".tmp")
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, idxName))
}

// Close flushes and persists the store, then releases the pack handle.
func (s *Store) Close() error {
	if s.packF == nil {
		return nil
	}
	err := s.Flush()
	if cerr := s.packF.Close(); err == nil {
		err = cerr
	}
	s.packF = nil
	s.packW = nil
	return err
}

// --- little-endian helpers (same codec primitives as internal/diskstore) ---

func appendU32LenBytes(buf, p []byte) []byte {
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(p)))
	return append(buf, p...)
}

// reader is a tiny bounds-checked little-endian cursor (mirrors diskstore.reader).
type reader struct {
	b   []byte
	pos int
}

func (r *reader) need(n int) error {
	if n < 0 || r.pos+n > len(r.b) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (r *reader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(r.b[r.pos:])
	r.pos += 8
	return v, nil
}

func (r *reader) bytes(n int) ([]byte, error) {
	if err := r.need(n); err != nil {
		return nil, err
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *reader) lenBytes() ([]byte, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	return r.bytes(int(n))
}
