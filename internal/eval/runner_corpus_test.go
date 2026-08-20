package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorpusRootDiscovery(t *testing.T) {
	home := t.TempDir()
	managed := filepath.Join(home, ".moedex-managed")
	if err := os.Mkdir(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MOEDEX_CORPUS_ROOT", "")
	t.Setenv("MOEDEX_CORPUS", "")
	if got, ok := CorpusRoot(); !ok || got != managed {
		t.Fatalf("CorpusRoot = %q, %v; want %q, true", got, ok, managed)
	}
}

func TestCorpusRootEnvironmentPrecedence(t *testing.T) {
	rootOverride := t.TempDir()
	corpusEnv := t.TempDir()
	t.Setenv("MOEDEX_CORPUS_ROOT", rootOverride)
	t.Setenv("MOEDEX_CORPUS", corpusEnv)
	if got, ok := CorpusRoot(); !ok || got != rootOverride {
		t.Fatalf("CorpusRoot = %q, %v; want %q, true", got, ok, rootOverride)
	}
}
