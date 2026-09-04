package embed

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveOverAnOpenMappedStoreKeepsItReadable pins the RefreshEmbeddings
// hazard: a refresh reuses vectors from the CURRENTLY OPEN store while writing
// its replacement to the same path. Save writes a temp sibling and renames, so
// the old mapping stays valid for the whole write -- unlinked but mapped. That
// is load-bearing rather than incidental, so it gets a test.
func TestSaveOverAnOpenMappedStoreKeepsItReadable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e.store")

	orig := storeFromVectors(t, 4, [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}})
	if err := orig.Save(p); err != nil {
		t.Fatal(err)
	}
	open, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()

	// Build a replacement that ALIASES the open mapping, as a refresh does.
	reused := open.vecAt(0)
	next := storeFromVectors(t, 4, [][]float32{reused, {0, 0, 1, 0}})
	if err := next.Save(p); err != nil {
		t.Fatalf("Save over an open store: %v", err)
	}

	// The still-open mapping must remain readable and unchanged.
	if got := open.vecAt(0); got[0] != 1 || got[1] != 0 {
		t.Fatalf("open mapping changed under a rename: %v", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("replacement not in place: %v", err)
	}
	fresh, err := LoadStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.Len() != 2 || fresh.vecAt(1)[2] != 1 {
		t.Fatal("replacement store did not round-trip")
	}
}
