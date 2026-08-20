package corpus

import (
	"path/filepath"
	"testing"
)

func TestResolveRootDefaultsToManagedCorpus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOEDEX_CORPUS", "")
	got, err := ResolveRoot("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".moedex-managed")
	if got != want {
		t.Fatalf("ResolveRoot default = %q, want %q", got, want)
	}
}

func TestResolveRootPrecedence(t *testing.T) {
	t.Setenv("MOEDEX_CORPUS", "/env/corpus")
	got, err := ResolveRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/env/corpus" {
		t.Fatalf("environment root = %q", got)
	}
	got, err = ResolveRoot("/explicit/corpus")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/explicit/corpus" {
		t.Fatalf("explicit root = %q", got)
	}
}
