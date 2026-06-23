package tokenindex

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Binary format (all multi-byte integers little-endian via binary.Uvarint /
// PutUvarint, i.e. variable-length unsigned for compactness):
//
//	magic    : 4 bytes  "TKI1"
//	numDocs  : uvarint
//	totalLen : uvarint
//	docLen section:
//	    count    : uvarint           (number of doclen entries)
//	    repeated : blobID uvarint, length uvarint
//	postings section:
//	    termCount: uvarint
//	    repeated per term:
//	        termLen : uvarint, term bytes
//	        df      : uvarint          (number of (blob,tf) pairs)
//	        repeated: blobID uvarint, tf uvarint
//
// The format is self-describing and fully round-trippable: Load reconstructs
// every field Save wrote, and numDocs/totalLen are persisted directly so
// AvgDocLen survives a round trip without recomputation.
var magic = []byte("TKI1")

// Save writes ti to path.
func Save(ti *TokenIndex, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := writeIndex(w, ti); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeIndex(w io.Writer, ti *TokenIndex) error {
	if _, err := w.Write(magic); err != nil {
		return err
	}
	var buf [binary.MaxVarintLen64]byte
	putU := func(v uint64) error {
		n := binary.PutUvarint(buf[:], v)
		_, err := w.Write(buf[:n])
		return err
	}

	if err := putU(uint64(ti.numDocs)); err != nil {
		return err
	}
	if err := putU(uint64(ti.totalLen)); err != nil {
		return err
	}

	// docLen section
	if err := putU(uint64(len(ti.docLen))); err != nil {
		return err
	}
	for id, l := range ti.docLen {
		if err := putU(id); err != nil {
			return err
		}
		if err := putU(uint64(l)); err != nil {
			return err
		}
	}

	// postings section
	if err := putU(uint64(len(ti.postings))); err != nil {
		return err
	}
	for term, post := range ti.postings {
		if err := putU(uint64(len(term))); err != nil {
			return err
		}
		if _, err := io.WriteString(w, term); err != nil {
			return err
		}
		if err := putU(uint64(len(post))); err != nil {
			return err
		}
		for id, tf := range post {
			if err := putU(id); err != nil {
				return err
			}
			if err := putU(uint64(tf)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Load reads a TokenIndex previously written by Save.
func Load(path string) (*TokenIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	return readIndex(r)
}

func readIndex(r *bufio.Reader) (*TokenIndex, error) {
	hdr := make([]byte, len(magic))
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("tokenindex: reading magic: %w", err)
	}
	if string(hdr) != string(magic) {
		return nil, fmt.Errorf("tokenindex: bad magic %q", hdr)
	}

	getU := func() (uint64, error) { return binary.ReadUvarint(r) }

	numDocs, err := getU()
	if err != nil {
		return nil, err
	}
	totalLen, err := getU()
	if err != nil {
		return nil, err
	}

	ti := &TokenIndex{
		postings: make(map[string]map[uint64]int),
		docLen:   make(map[uint64]int),
		numDocs:  int(numDocs),
		totalLen: int(totalLen),
	}

	dlCount, err := getU()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < dlCount; i++ {
		id, err := getU()
		if err != nil {
			return nil, err
		}
		l, err := getU()
		if err != nil {
			return nil, err
		}
		ti.docLen[id] = int(l)
	}

	termCount, err := getU()
	if err != nil {
		return nil, err
	}
	for i := uint64(0); i < termCount; i++ {
		tlen, err := getU()
		if err != nil {
			return nil, err
		}
		tb := make([]byte, tlen)
		if _, err := io.ReadFull(r, tb); err != nil {
			return nil, err
		}
		df, err := getU()
		if err != nil {
			return nil, err
		}
		post := make(map[uint64]int, df)
		for j := uint64(0); j < df; j++ {
			id, err := getU()
			if err != nil {
				return nil, err
			}
			tf, err := getU()
			if err != nil {
				return nil, err
			}
			post[id] = int(tf)
		}
		ti.postings[string(tb)] = post
	}
	return ti, nil
}
