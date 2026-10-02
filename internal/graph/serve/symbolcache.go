package graphserve

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"moedex/internal/index"
	"moedex/internal/symbol"
)

const graphSymbolCacheMagic = "MGSC0001"
const graphSymbolCacheHeader = 8 + 32 + 8 + 32
const graphSymbolCacheMaxBytes int64 = 1 << 30

func graphSymbolCachePath(shardPath string) string { return shardPath + ".graph-symbols" }

// graphSymbolShardFingerprint hashes the loaded, normalized corpus rather than
// trusting stored SHA labels, timestamps, or sizes. Ordered metadata is included
// so the same digest also binds serving catalogs, and in particular captures
// BuildMulti's first-file language selection. Length prefixes prevent ambiguity.
func graphSymbolShardFingerprint(ix *index.Index) [32]byte {
	h := sha256.New()
	h.Write([]byte("moedex-graph-symbol-input-v1"))
	cacheHashUint(h, uint64(symbol.ExtractorsVersion))
	cacheHashUint(h, uint64(ix.NumBlobs()))
	for id := uint64(0); id < uint64(ix.NumBlobs()); id++ {
		blob := ix.Blob(id)
		cacheHashUint(h, id)
		cacheHashString(h, blob.SHA)
		cacheHashUint(h, uint64(len(blob.Content)))
		h.Write(blob.Content)
		cacheHashUint(h, uint64(len(blob.Files)))
		for _, file := range blob.Files {
			cacheHashString(h, file.Repo)
			cacheHashString(h, file.RelPath)
			cacheHashString(h, file.AbsPath)
		}
	}
	return [32]byte(h.Sum(nil))
}

func cacheHashUint(h hash.Hash, n uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], n)
	h.Write(b[:])
}

func cacheHashString(h hash.Hash, s string) {
	cacheHashUint(h, uint64(len(s)))
	io.WriteString(h, s)
}

// readGraphSymbolCache verifies and decodes one opened file. Atomic concurrent
// replacement cannot swap the checked payload for a different decoded payload.
func readGraphSymbolCache(path string, fingerprint [32]byte, source *index.Index) (*symbol.Index, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("graph symbol cache: not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < graphSymbolCacheHeader || info.Size()-graphSymbolCacheHeader > graphSymbolCacheMaxBytes {
		return nil, fmt.Errorf("graph symbol cache: invalid file size or type")
	}
	var header [graphSymbolCacheHeader]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint64(header[40:48])
	if string(header[:8]) != graphSymbolCacheMagic || !bytes.Equal(header[8:40], fingerprint[:]) || length != uint64(info.Size()-graphSymbolCacheHeader) {
		return nil, fmt.Errorf("graph symbol cache: stale or invalid header")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	if !bytes.Equal(h.Sum(nil), header[48:]) {
		return nil, fmt.Errorf("graph symbol cache: payload checksum mismatch")
	}
	if _, err := f.Seek(graphSymbolCacheHeader, io.SeekStart); err != nil {
		return nil, err
	}
	h.Reset()
	syms, err := symbol.Decode(io.TeeReader(io.NewSectionReader(f, graphSymbolCacheHeader, int64(length)), h), int64(length))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(h.Sum(nil), header[48:]) {
		return nil, fmt.Errorf("graph symbol cache: payload changed during decode")
	}
	if err := validateGraphSymbolCache(syms, source); err != nil {
		return nil, err
	}
	return syms, nil
}

func validateGraphSymbolCache(syms *symbol.Index, source *index.Index) error {
	defBlobs, refBlobs := 0, 0
	for id := uint64(0); id < uint64(source.NumBlobs()); id++ {
		blob := source.Blob(id)
		defs, refs := syms.Symbols(id), syms.Refs(id)
		if len(defs) > 0 {
			defBlobs++
		}
		if len(refs) > 0 {
			refBlobs++
		}
		for _, s := range defs {
			if s.NameStart < 0 || s.NameEnd < s.NameStart || s.NameEnd > len(blob.Content) || s.BodyStart < 0 || s.BodyEnd < s.BodyStart || s.BodyEnd > len(blob.Content) {
				return fmt.Errorf("graph symbol cache: definition outside blob")
			}
		}
		for _, r := range refs {
			if r.Start < 0 || r.End < r.Start || r.End > len(blob.Content) {
				return fmt.Errorf("graph symbol cache: reference outside blob")
			}
		}
	}
	if defBlobs != syms.NumBlobs() || refBlobs != syms.NumRefBlobs() {
		return fmt.Errorf("graph symbol cache: unknown or empty blob record")
	}
	return nil
}

// writeGraphSymbolCache publishes a complete envelope atomically. This cache is
// optional: callers retain freshly extracted symbols on all persistence errors.
func writeGraphSymbolCache(path string, fingerprint [32]byte, syms *symbol.Index) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".graph-symbols-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	var header [graphSymbolCacheHeader]byte
	if _, err := f.Write(header[:]); err != nil {
		return err
	}
	h := sha256.New()
	w := bufio.NewWriterSize(&symbolCacheLimitWriter{w: io.MultiWriter(f, h), remaining: graphSymbolCacheMaxBytes}, 64<<10)
	if err := symbol.Encode(w, syms); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	end, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if end-graphSymbolCacheHeader > graphSymbolCacheMaxBytes {
		return fmt.Errorf("graph symbol cache: payload too large")
	}
	copy(header[:8], graphSymbolCacheMagic)
	copy(header[8:40], fingerprint[:])
	binary.LittleEndian.PutUint64(header[40:48], uint64(end-graphSymbolCacheHeader))
	copy(header[48:], h.Sum(nil))
	if _, err := f.WriteAt(header[:], 0); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type symbolCacheLimitWriter struct {
	w         io.Writer
	remaining int64
}

func (w *symbolCacheLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("graph symbol cache: payload too large")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}
