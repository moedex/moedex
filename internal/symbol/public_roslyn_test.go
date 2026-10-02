package symbol

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// These anchors were read from pinned Roslyn source before evaluating the
// corrected extractor. The test program embedded in SemanticErrorTests is data,
// while the generator's Main is an actual declaration in the indexed file.
func TestPinnedRoslynLiteralDefinitions(t *testing.T) {
	root := os.Getenv("MOEDEX_ROSLYN_SOURCE")
	if root == "" {
		t.Skip("set MOEDEX_ROSLYN_SOURCE to the pinned public checkout")
	}
	for _, tc := range []struct {
		path, hash string
		offset     int
		want       bool
	}{
		{"src/Tools/CompilerGeneratorTools/Source/CSharpErrorFactsGenerator/Program.cs", "f797971c625c212d0cedd7ace6be8d5772842f4fa2dba0e0e61513fe916237c4", 471, true},
		{"src/Compilers/CSharp/Test/Semantic/Semantics/SemanticErrorTests.cs", "4c26cfaf459c6baba75982d94960644e12d557c8c16223a93cc52511a185a517", 1101, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tc.path)))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(content)) != tc.hash {
				t.Fatal("source differs from Roslyn commit 36d26c5466e4d25940657ccb8d5b9557ccaf7be1")
			}
			if string(content[tc.offset:tc.offset+4]) != "Main" {
				t.Fatal("source anchor drift")
			}
			defs, err := (CSharpExtractor{}).Extract(content)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, def := range defs {
				if def.NameStart == tc.offset && def.Name == "Main" {
					found = true
				}
			}
			if found != tc.want {
				t.Fatalf("declaration at raw byte %d = %v, want %v", tc.offset, found, tc.want)
			}
		})
	}
}
