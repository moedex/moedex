package semanticindex

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

// FuzzOpen exercises both integrity rejection and the table parser behind it.
// The mutation input is capped; every iteration replaces a single temp file.
func FuzzOpen(f *testing.F) {
	path := filepath.Join(f.TempDir(), "seed")
	if e := Build(path, fixture(4), provenance()); e != nil {
		f.Fatal(e)
	}
	valid, e := os.ReadFile(path)
	if e != nil {
		f.Fatal(e)
	}
	f.Add(valid, false)
	f.Add(valid, true)
	// Keep a valid header and checksum while corrupting a foreign key; random
	// checksum failures alone would never reach these dependent table checks.
	deep := append([]byte(nil), valid...)
	off := le.Uint64(deep[120+facts*16:])
	le.PutUint64(deep[off+16:], ^uint64(0))
	h := sha256.Sum256(deep[headerSize:])
	copy(deep[24:], h[:])
	f.Add(deep, true)
	f.Add([]byte("MDXSEM01"), false)
	f.Fuzz(func(t *testing.T, data []byte, rehash bool) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		body := append([]byte(nil), data...)
		if rehash && len(body) >= headerSize {
			h := sha256.Sum256(body[headerSize:])
			copy(body[24:], h[:])
		}
		p := filepath.Join(t.TempDir(), "candidate")
		if e := os.WriteFile(p, body, 0600); e != nil {
			t.Fatal(e)
		}
		x, e := Open(p, provenance(), Limits{MaxBytes: 64 << 10})
		if e == nil {
			if e := x.Close(); e != nil {
				t.Fatal(e)
			}
		}
	})
}
