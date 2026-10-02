package symbol

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Frozen independently from the accelerated regexes and prefix classifiers.
var legacyCSTypeDecl = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|sealed|abstract|partial|unsafe|new|readonly)\s+)*(class|interface|struct|enum|record)(?:\s+(?:class|struct))?\s+([A-Za-z_][A-Za-z0-9_]*)`)
var legacyCSMethodDecl = regexp.MustCompile(`(?m)^[ \t]*(?:(?:public|private|protected|internal|static|virtual|override|abstract|sealed|async|partial|extern|unsafe|new|readonly)\s+)*[A-Za-z_][A-Za-z0-9_<>,.\[\] ?]*?\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^>]*>)?\s*\(`)

func checkCSDeclarations(t *testing.T, content []byte) {
	t.Helper()
	for _, method := range []bool{false, true} {
		original := legacyCSTypeDecl
		if method {
			original = legacyCSMethodDecl
		}
		want := original.FindAllSubmatchIndex(content, -1)
		got := csDeclarationMatches(content, method)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("method=%v input=%q\ngot %v\nwant %v", method, content, got, want)
		}
	}
}

func TestCSDeclarationMatchesPathological(t *testing.T) {
	cases := []string{
		"", "public class A {}\nvoid M() {}", "public\nclass\nA\nvoid\nM\n(\n",
		"public\r\nstatic\fvoid M\r\n<T\n>\f(\n", "public\vclass A\nvoid\vM(\n",
		"void M<\"(\"\nclass Ghost\n>(\nvoid Real(\n", "public record\nstruct R\n(\n",
		"A<B<C>> M(\nA<B C(\nA<B<C\nvoid Z(\n", "void M<\xff\x00(){}\n>(\n",
		"class A\nclass B\nclass C", "public\npublic\nFoo(\nBar Baz(\n",
		"/*\npublic\nclass Hidden\n*/\nclass Real {}\n", "\"\"\"\nvoid Ghost<\n>(\n\"\"\"\nvoid Real(\n",
		strings.Repeat("int Foo<\n", 1000), strings.Repeat("public\n", 1000),
		"class " + strings.Repeat("X", 5000) + "\nvoid M(\n",
	}
	for _, input := range cases {
		checkCSDeclarations(t, []byte(input))
	}
	for b := 0; b < 256; b++ {
		checkCSDeclarations(t, []byte("public"+string(byte(b))+"class A\nvoid M<T"+string(byte(b))+">(\n"))
	}
}

func TestCSDeclarationMatchesRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(70137))
	tokens := []string{"public", "private", "static", "new", "record", "class", "struct", "void", "M", "Name", "A<B<C>>", "Foo<", ">(\n", "(", "<", ">", "?", "[", "]", "=", ".", ",", ":", ";", "_", "\n", "\r\n", "\t", "\f", "\v", " ", "  ", "\x00", "\xff", "\"", "/*", "//", "}"}
	for trial := 0; trial < 2000; trial++ {
		var input strings.Builder
		for n := rng.Intn(150); n > 0; n-- {
			input.WriteString(tokens[rng.Intn(len(tokens))])
		}
		checkCSDeclarations(t, []byte(input.String()))
	}
}

func TestCSDeclarationMatchesFallback(t *testing.T) {
	for _, input := range []string{strings.Repeat("int Foo<\n", 1000), strings.Repeat("public\n", 1000)} {
		_, fallback := csDeclarationMatchesBounded([]byte(input), true)
		if !fallback {
			t.Fatal("long ambiguous prefix must fall back")
		}
		checkCSDeclarations(t, []byte(input))
	}
	// Each short candidate can reach the same closing angle; total repeated
	// scans exceed the work budget before reaching the per-window limit.
	input := strings.Repeat("int Foo<\n", 200) + ">;\n"
	_, fallback := csDeclarationMatchesBounded([]byte(input), true)
	if !fallback {
		t.Fatal("repeated short windows must exhaust work budget")
	}
	checkCSDeclarations(t, []byte(input))
}

func csDeclarationBenchmarkInput(b *testing.B) []byte {
	b.Helper()
	if file := os.Getenv("MOEDEX_CS_DECL_BENCH_FILE"); file != "" {
		content, err := os.ReadFile(file)
		if err != nil {
			b.Fatal(err)
		}
		return content
	}
	return bytes.Repeat([]byte("public class Example<T> {\n    private readonly List<string> _items = new();\n    public async Task<string> ReadAsync(int index) {\n        var item = _items[index];\n        return await Transform(item);\n    }\n}\n"), 100)
}

func BenchmarkCSDeclarationMatches(b *testing.B) {
	content := csDeclarationBenchmarkInput(b)
	for _, legacy := range []bool{true, false} {
		name := "bounded"
		if legacy {
			name = "legacy"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(content)))
			for b.Loop() {
				if legacy {
					legacyCSTypeDecl.FindAllSubmatchIndex(content, -1)
					legacyCSMethodDecl.FindAllSubmatchIndex(content, -1)
				} else {
					csDeclarationMatches(content, false)
					csDeclarationMatches(content, true)
				}
			}
		})
	}
}

func TestCSDeclarationMatchesCorpus(t *testing.T) {
	root := os.Getenv("MOEDEX_CS_DECL_CORPUS")
	if root == "" {
		t.Skip("set MOEDEX_CS_DECL_CORPUS for independent declaration match parity")
	}
	files, fallbacks := 0, 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".cs" {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		checkCSDeclarations(t, content)
		files++
		for _, method := range []bool{false, true} {
			_, fallback := csDeclarationMatchesBounded(content, method)
			if fallback {
				fallbacks++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("files=%d fallback scans=%d/%d", files, fallbacks, files*2)
}
