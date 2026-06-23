package search_test

import (
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
)

// TestMmapParity proves the full persistence path: build an index, Save it,
// reload it with postings left in an mmap, and confirm search over the
// mmap-backed index still matches ripgrep exactly — same queries as the
// in-memory parity test.
func TestMmapParity(t *testing.T) {
	requireRipgrep(t)
	dir := t.TempDir()

	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.cs", "namespace TC.SslApi;\npublic class SslService {\n  public string Token;\n  void get() {}\n}\n")
	write("b.cs", "using System;\nusing System.Text;\nclass Other { get; set; }\n")
	write("unicode.txt", "let prix = café_au_lait;\nΣumма = Δ + ß\nplain ascii line\n")
	dup := "marker line\nZZUNIQUEDUPTOKEN appears here\n"
	write("dup1.cs", dup)
	write("dup2.cs", dup)

	ix, files := indexDir(t, "synthetic", dir)

	path := filepath.Join(t.TempDir(), "index.moedex")
	if err := diskstore.Save(ix, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, closer, err := diskstore.LoadMmap(path)
	if err != nil {
		t.Fatalf("mmap load: %v", err)
	}
	defer closer.Close()

	// runParity asserts the loaded index's literal+regex results equal rg's.
	runParity(t, loaded, files)
}
