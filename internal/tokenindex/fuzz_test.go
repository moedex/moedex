package tokenindex

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoadFromNeverPanics drives the TKI2 parser over arbitrary bytes. The
// reader hands mmap'd sections straight to callers as slices, so a bad offset
// that survives validation becomes a segfault inside a query rather than an
// error at load. The contract is total: any input either parses into a usable
// index or returns an error.
func FuzzLoadFromNeverPanics(f *testing.F) {
	ti := Build(nil)
	dir := f.TempDir()
	p := filepath.Join(dir, "seed.tki")
	if err := Save(ti, p); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
			if len(b) > 8 {
				f.Add(b[:len(b)/2])
			}
		}
	}
	f.Add([]byte("TKI2"))
	f.Add(make([]byte, tkiHeaderSize))

	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := loadFrom(b)
		if err != nil {
			return
		}
		// A successfully parsed index must be safe to exercise fully.
		_ = got.NumDocs()
		_ = got.AvgDocLen()
		for blob := uint64(0); blob < 8; blob++ {
			_ = got.DocLen(blob)
		}
		for i := 0; i < len(got.termOff)-1; i++ {
			term := string(got.termBytes(i))
			p := got.Postings(term)
			for j := 0; j < p.Len(); j++ {
				_ = p.Blob(j)
				_ = p.TF(j)
			}
			_ = p.TFOf(0)
		}
	})
}
