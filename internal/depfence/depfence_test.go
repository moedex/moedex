package depfence

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestShellDependencyFence keeps the interactive dependency stack above the
// engine/application boundary. Build-tagged files are parsed too, so optional
// arms cannot bypass the fence.
func TestShellDependencyFence(t *testing.T) {
	root := repositoryRoot(t)
	for _, tree := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				name, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if isShellDependency(name) && !shellMayImport(rel) {
					t.Errorf("%s imports shell-only dependency %s", rel, name)
				}
				if strings.HasPrefix(filepath.ToSlash(rel), "internal/corpus/catalog/") && name == "os/exec" {
					t.Errorf("%s violates the exec-free corpus catalog boundary", rel)
				}
				if !strings.HasSuffix(rel, "_test.go") && violatesGraphSeam(filepath.ToSlash(rel), name) {
					t.Errorf("%s crosses the offline/online graph seam by importing %s", rel, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func violatesGraphSeam(rel, imported string) bool {
	switch {
	case strings.HasPrefix(rel, "internal/graph/build/"):
		return imported == "moedex/internal/graph/serve" || imported == "moedex/internal/serve"
	case strings.HasPrefix(rel, "internal/graph/serve/"):
		return imported == "moedex/internal/graph/build" || imported == "moedex/internal/serve"
	case strings.HasPrefix(rel, "internal/serve/"):
		return imported == "moedex/internal/graph/build" || imported == "moedex/internal/graph/serve"
	default:
		return false
	}
}

func isShellDependency(name string) bool {
	return strings.HasPrefix(name, "charm.land/") ||
		strings.HasPrefix(name, "github.com/charmbracelet/") ||
		name == "github.com/spf13/cobra" || strings.HasPrefix(name, "github.com/spf13/cobra/")
}

func shellMayImport(rel string) bool {
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "internal/cli/") || strings.HasPrefix(rel, "internal/tui/") ||
		strings.HasPrefix(rel, "internal/render/") || strings.HasPrefix(rel, "internal/ui/theme/")
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
