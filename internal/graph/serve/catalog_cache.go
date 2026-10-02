package graphserve

// The catalog sidecar is disposable acceleration, never graph authority. Its
// identity binds the opened graph bytes and ordered loaded symbol inputs. All
// pointer ownership stays with the current SymbolCorpus; only value metadata
// and ordered lookup lists are serialized.
import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"log"
	"os"
	"path/filepath"

	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

// Bump this identity when catalog, location, or path-index semantics change,
// including changes independent of symbol.ExtractorsVersion.
const catalogCacheMagic = "MOECAT01"
const catalogCacheHeader = 72
const catalogCacheMaxBytes = 768 << 20
const catalogCacheMaxStrings = 8 << 20
const catalogCacheMaxStringBytes = 256 << 20
const catalogCacheMaxNodes = 8 << 20
const catalogCacheMaxEntries = 32 << 20

var errCatalogCache = errors.New("invalid or oversized graph catalog cache")

func (s *graphSnapshot) catalogCacheIdentity() ([32]byte, bool) {
	if s.symbols == nil || len(s.symbols.cacheFingerprints) != len(s.symbols.idxs) || s.buildID == "" {
		return [32]byte{}, false
	}
	h := sha256.New()
	h.Write([]byte(catalogCacheMagic))
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(s.buildID)))
	h.Write(n[:])
	h.Write([]byte(s.buildID))
	binary.LittleEndian.PutUint64(n[:], uint64(len(s.symbols.cacheFingerprints)))
	h.Write(n[:])
	for _, fp := range s.symbols.cacheFingerprints {
		h.Write(fp[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, true
}

func (s *graphSnapshot) buildCatalogCached(dir string) bool {
	identity, usable := s.catalogCacheIdentity()
	path := filepath.Join(dir, "graph-catalog.cache")
	if usable && s.readCatalogCache(path, identity) == nil {
		return true
	}
	s.buildCatalog()
	if usable {
		if err := s.writeCatalogCache(path, identity); err != nil {
			log.Printf("server: graph catalog cache write skipped: %v", err)
		}
	}
	return false
}

// Hash the already opened descriptor before decoding and again as it is decoded.
// This detects corruption and in-place modification without trusting path stats.
func (s *graphSnapshot) readCatalogCache(path string, identity [32]byte) error {
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return errCatalogCache
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() < catalogCacheHeader || st.Size() > catalogCacheMaxBytes {
		return errCatalogCache
	}
	var header [catalogCacheHeader]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return err
	}
	if string(header[:8]) != catalogCacheMagic || string(header[8:40]) != string(identity[:]) {
		return errCatalogCache
	}
	size := st.Size() - catalogCacheHeader
	h := sha256.New()
	if _, err = io.Copy(h, io.NewSectionReader(f, catalogCacheHeader, size)); err != nil {
		return err
	}
	if string(h.Sum(nil)) != string(header[40:]) {
		return errCatalogCache
	}
	h.Reset()
	r := &catalogDecoder{r: bufio.NewReaderSize(io.TeeReader(io.NewSectionReader(f, catalogCacheHeader, size), h), 64<<10), remaining: size}
	candidate := &graphSnapshot{nodes: make(map[diskgraph.Key]nodeMetadata), bySymbol: make(map[string][]diskgraph.Key), byPath: make(map[string][]locatedNode)}
	for n := r.count(catalogCacheMaxNodes, 5); n > 0 && r.err == nil; n-- {
		key := r.key()
		if _, exists := candidate.nodes[key]; exists {
			return errCatalogCache
		}
		meta := nodeMetadata{Symbol: r.str(), Kind: r.str()}
		count := r.sliceCount(5)
		if count > 0 {
			meta.Locations = make([]GraphLocation, 0)
			for i := uint64(1); i < count && r.err == nil; i++ {
				meta.Locations = append(meta.Locations, GraphLocation{Repo: r.str(), Path: r.str(), AbsPath: r.str(), Line: r.integer(), BlobSHA: r.str()})
			}
		}
		candidate.nodes[key] = meta
	}
	for n := r.count(catalogCacheMaxEntries, 2); n > 0 && r.err == nil; n-- {
		name := r.str()
		if _, exists := candidate.bySymbol[name]; exists {
			return errCatalogCache
		}
		var keys []diskgraph.Key
		count := r.sliceCount(2)
		if count > 0 {
			keys = make([]diskgraph.Key, 0)
		}
		for ; count > 1 && r.err == nil; count-- {
			key := r.key()
			if _, ok := candidate.nodes[key]; !ok {
				return errCatalogCache
			}
			keys = append(keys, key)
		}
		candidate.bySymbol[name] = keys
	}
	for n := r.count(catalogCacheMaxEntries, 2); n > 0 && r.err == nil; n-- {
		path := r.str()
		if _, exists := candidate.byPath[path]; exists {
			return errCatalogCache
		}
		var nodes []locatedNode
		count := r.sliceCount(3)
		if count > 0 {
			nodes = make([]locatedNode, 0)
		}
		for ; count > 1 && r.err == nil; count-- {
			node := locatedNode{line: r.integer(), key: r.key()}
			if _, ok := candidate.nodes[node.key]; !ok {
				return errCatalogCache
			}
			nodes = append(nodes, node)
		}
		candidate.byPath[path] = nodes
	}
	if r.err != nil {
		return r.err
	}
	if r.remaining != 0 || string(h.Sum(nil)) != string(header[40:]) {
		return errCatalogCache
	}
	if final, err := f.Stat(); err != nil || final.Size() != st.Size() {
		return errCatalogCache
	}
	s.nodes, s.bySymbol, s.byPath = candidate.nodes, candidate.bySymbol, candidate.byPath
	s.blobs = make(map[string][]*index.Blob)
	for shard := 0; shard < s.symbols.NumShards(); shard++ {
		for id := uint64(0); id < uint64(s.symbols.NumShardBlobs(shard)); id++ {
			blob := s.symbols.ShardBlob(shard, id)
			if blob != nil && blob.SHA != "" {
				s.blobs[blob.SHA] = append(s.blobs[blob.SHA], blob)
			}
		}
	}
	return nil
}

type catalogDecoder struct {
	r           *bufio.Reader
	remaining   int64
	strings     []string
	stringBytes int
	entries     uint64
	err         error
}

func (r *catalogDecoder) byte() (byte, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.remaining <= 0 {
		r.err = io.ErrUnexpectedEOF
		return 0, r.err
	}
	b, e := r.r.ReadByte()
	r.err = e
	if e == nil {
		r.remaining--
	}
	return b, e
}
func (r *catalogDecoder) ReadByte() (byte, error) { return r.byte() }
func (r *catalogDecoder) num() uint64 {
	if r.err != nil {
		return 0
	}
	v, e := binary.ReadUvarint(r)
	if e != nil {
		r.err = e
	}
	return v
}
func (r *catalogDecoder) integer() int {
	v := r.num()
	if v > uint64(^uint(0)>>1) {
		r.err = errCatalogCache
		return 0
	}
	return int(v)
}
func (r *catalogDecoder) count(max uint64, minBytes uint64) uint64 {
	n := r.num()
	if n > max || n > uint64(max64(r.remaining, 0))/minBytes || r.entries+n > catalogCacheMaxEntries {
		r.err = errCatalogCache
		return 0
	}
	r.entries += n
	return n
}

// Slice counts reserve zero for nil and encode a nonnil length as length+1.
func (r *catalogDecoder) sliceCount(minBytes uint64) uint64 {
	n := r.num()
	if n > catalogCacheMaxEntries || (n > 0 && n-1 > uint64(max64(r.remaining, 0))/minBytes) || r.entries+n > catalogCacheMaxEntries {
		r.err = errCatalogCache
		return 0
	}
	r.entries += n
	return n
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func (r *catalogDecoder) str() string {
	token := r.num()
	if r.err != nil {
		return ""
	}
	if token&1 == 0 {
		id := token >> 1
		if id >= uint64(len(r.strings)) {
			r.err = errCatalogCache
			return ""
		}
		return r.strings[id]
	}
	n := token >> 1
	if n > 1<<20 || n > uint64(max64(r.remaining, 0)) || len(r.strings) >= catalogCacheMaxStrings || uint64(r.stringBytes)+n > catalogCacheMaxStringBytes {
		r.err = errCatalogCache
		return ""
	}
	b := make([]byte, int(n))
	_, r.err = io.ReadFull(r.r, b)
	if r.err != nil {
		return ""
	}
	r.remaining -= int64(n)
	s := string(b)
	r.strings = append(r.strings, s)
	r.stringBytes += int(n)
	return s
}
func (r *catalogDecoder) key() diskgraph.Key {
	return diskgraph.Key{BlobSHA: r.str(), SymbolOffset: r.num()}
}

type catalogEncoder struct {
	w           *bufio.Writer
	h           hash.Hash
	strings     map[string]uint64
	size        int64
	stringBytes int
	entries     uint64
	err         error
}

func (w *catalogEncoder) bytes(b []byte) {
	if w.err != nil {
		return
	}
	if w.size+int64(len(b)) > catalogCacheMaxBytes-catalogCacheHeader {
		w.err = errCatalogCache
		return
	}
	_, w.err = w.w.Write(b)
	if w.err == nil {
		w.h.Write(b)
		w.size += int64(len(b))
	}
}
func (w *catalogEncoder) num(v uint64) {
	var b [10]byte
	n := binary.PutUvarint(b[:], v)
	w.bytes(b[:n])
}
func (w *catalogEncoder) count(n int) {
	if n < 0 || w.entries+uint64(n) > catalogCacheMaxEntries {
		w.err = errCatalogCache
		return
	}
	w.entries += uint64(n)
	w.num(uint64(n))
}
func (w *catalogEncoder) str(s string) {
	if w.err != nil {
		return
	}
	if id, ok := w.strings[s]; ok {
		w.num(id << 1)
		return
	}
	if len(s) > 1<<20 || len(w.strings) >= catalogCacheMaxStrings || w.stringBytes+len(s) > catalogCacheMaxStringBytes {
		w.err = errCatalogCache
		return
	}
	w.num(uint64(len(s))<<1 | 1)
	w.bytes([]byte(s))
	w.strings[s] = uint64(len(w.strings))
	w.stringBytes += len(s)
}
func (w *catalogEncoder) key(k diskgraph.Key) { w.str(k.BlobSHA); w.num(k.SymbolOffset) }
func (s *graphSnapshot) writeCatalogCache(path string, identity [32]byte) error {
	if len(s.nodes) > catalogCacheMaxNodes {
		return errCatalogCache
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".graph-catalog-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	var header [catalogCacheHeader]byte
	copy(header[:8], catalogCacheMagic)
	copy(header[8:40], identity[:])
	if _, err = f.Write(header[:]); err != nil {
		return err
	}
	w := &catalogEncoder{w: bufio.NewWriterSize(f, 64<<10), h: sha256.New(), strings: make(map[string]uint64)}
	w.count(len(s.nodes))
	for key, meta := range s.nodes {
		w.key(key)
		w.str(meta.Symbol)
		w.str(meta.Kind)
		if meta.Locations == nil {
			w.count(0)
		} else {
			w.count(len(meta.Locations) + 1)
		}
		for _, loc := range meta.Locations {
			if loc.Line < 0 {
				return errCatalogCache
			}
			w.str(loc.Repo)
			w.str(loc.Path)
			w.str(loc.AbsPath)
			w.num(uint64(loc.Line))
			w.str(loc.BlobSHA)
		}
		if w.err != nil {
			return w.err
		}
	}
	w.count(len(s.bySymbol))
	for name, keys := range s.bySymbol {
		w.str(name)
		if keys == nil {
			w.count(0)
		} else {
			w.count(len(keys) + 1)
		}
		for _, key := range keys {
			w.key(key)
		}
		if w.err != nil {
			return w.err
		}
	}
	w.count(len(s.byPath))
	for path, nodes := range s.byPath {
		w.str(path)
		if nodes == nil {
			w.count(0)
		} else {
			w.count(len(nodes) + 1)
		}
		for _, node := range nodes {
			if node.line < 0 {
				return errCatalogCache
			}
			w.num(uint64(node.line))
			w.key(node.key)
		}
		if w.err != nil {
			return w.err
		}
	}
	if w.err != nil {
		return w.err
	}
	if err = w.w.Flush(); err != nil {
		return err
	}
	copy(header[40:], w.h.Sum(nil))
	if _, err = f.WriteAt(header[:], 0); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
