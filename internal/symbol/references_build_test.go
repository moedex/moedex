package symbol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/index"
)

// TestBuildMulti_PopulatesRefs proves BuildMulti emits references for the
// ref-capable languages (.go precise, .cs best-effort) and leaves references
// absent (no panic, empty) for languages without a RefExtractor (.ts/.cfm/.sql)
// in this slice.
func TestBuildMulti_PopulatesRefs(t *testing.T) {
	ix := index.New()
	goSrc := []byte("package p\n\nfunc Helper() {}\n\nfunc Use() { Helper() }\n")
	csSrc := []byte("namespace N\n{\n    public class C\n    {\n        public void Run() { Work(); }\n        private void Work() {}\n    }\n}\n")
	tsSrc := []byte("export class T {\n  m() { this.n(); }\n  n() {}\n}\n")
	cfSrc := []byte("<cffunction name=\"foo\"><cfset bar()></cffunction>\n")
	sqlSrc := []byte("CREATE PROCEDURE p AS SELECT 1;\n")

	ix.AddFile("r", "a.go", "/a.go", "sha-go", goSrc)
	ix.AddFile("r", "b.cs", "/b.cs", "sha-cs", csSrc)
	ix.AddFile("r", "c.ts", "/c.ts", "sha-ts", tsSrc)
	ix.AddFile("r", "d.cfm", "/d.cfm", "sha-cf", cfSrc)
	ix.AddFile("r", "e.sql", "/e.sql", "sha-sql", sqlSrc)

	sym := BuildMulti(ix)

	// Go blob (0): the Helper() call inside Use is a reference.
	goRefs := sym.Refs(0)
	if len(goRefs) == 0 {
		t.Errorf("go blob: expected references, got none")
	}
	if got := sym.References("Helper"); len(got) < 2 { // def + >=1 call
		t.Errorf("References(Helper) = %d, want def + call site", len(got))
	}

	// C# blob (1): the Work() call inside Run is a reference.
	if len(sym.Refs(1)) == 0 {
		t.Errorf("cs blob: expected references, got none")
	}

	// TS/CFML/SQL blobs (2,3,4): no RefExtractor, so no references — but must
	// not panic and must return empty.
	for _, blob := range []uint64{2, 3, 4} {
		if got := sym.Refs(blob); len(got) != 0 {
			t.Errorf("non-ref-capable blob %d: expected no references, got %v", blob, got)
		}
	}
}

// TestLoad_SYM1Backward proves a legacy SYM1 sidecar (symbols only, no refs
// section) loads cleanly with zero references and intact Enclosing behavior.
func TestLoad_SYM1Backward(t *testing.T) {
	// Hand-build a minimal SYM1 payload: magic "SYM1", blobCount=1, blobID=0,
	// symCount=1, one symbol {Name:"Solo", Kind:Func, ns=5,ne=9,bs=0,be=20}.
	var buf bytes.Buffer
	buf.WriteString("SYM1")
	var tmp [binary.MaxVarintLen64]byte
	putU := func(v uint64) {
		n := binary.PutUvarint(tmp[:], v)
		buf.Write(tmp[:n])
	}
	putU(1) // blobCount
	putU(0) // blobID
	putU(1) // symCount
	name := "Solo"
	putU(uint64(len(name)))
	buf.WriteString(name)
	putU(uint64(Func)) // kind
	putU(5)            // nameStart
	putU(9)            // nameEnd
	putU(0)            // bodyStart
	putU(20)           // bodyEnd
	// No refs section: a SYM1 reader stops here.

	ix, err := readIndex(bufio.NewReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		t.Fatalf("readIndex(SYM1): %v", err)
	}
	if ix.NumRefBlobs() != 0 {
		t.Errorf("SYM1 load: NumRefBlobs = %d, want 0", ix.NumRefBlobs())
	}
	// The definition still feeds References (folded from symbols).
	refs := ix.References("Solo")
	if len(refs) != 1 || refs[0].Role != Definition {
		t.Fatalf("SYM1 load: References(Solo) = %+v, want one Definition", refs)
	}
	// Enclosing works against the loaded symbol.
	if s, ok := ix.Enclosing(0, 7); !ok || s.Name != "Solo" {
		t.Errorf("SYM1 load: Enclosing(0,7) = (%+v,%v), want Solo", s, ok)
	}
}

func TestLoad_BadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.sym")
	if err := os.WriteFile(path, []byte("NOPE\x00\x00"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error loading a file with bad magic, got nil")
	}
}
