package semanticrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func toolCacheFixture(t *testing.T) *DependencyBundle {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "staged")
	files := map[string]string{".tools/tool/1.0/net8.0/project.assets.json": `{"version":3,"targets":{},"libraries":{},"project":{}}`, ".tools/tool/1.0/net8.0/tool.nuget.cache": `{"version":2,"success":true,"dgSpecHash":"before","projectFilePath":"/before"}`, "tool/1.0/tool.1.0.nupkg": "archive", "tool/1.0/tool.nuspec": "nuspec", "tool/1.0/bin/tool.dll": "binary", "tool/1.0/unrelated.json": `{"version":3}`}
	var manifest []DependencyFile
	for name, raw := range files {
		p := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		manifest = append(manifest, DependencyFile{Path: name, Size: int64(len(raw)), SHA256: sha([]byte(raw))})
	}
	b, err := StageDependencyBundle(t.Context(), DependencyBundleOptions{Directory: source, Files: manifest}, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}
func TestToolCacheAdoptionIsOptInAndRetainsOriginalInputs(t *testing.T) {
	b := toolCacheFixture(t)
	name := ".tools/tool/1.0/net8.0/tool.nuget.cache"
	raw := []byte(`{"version":2,"success":true,"dgSpecHash":"after","projectFilePath":"/a/different/projection"}`)
	if err := os.WriteFile(filepath.Join(b.Root, filepath.FromSlash(name)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if b.Verify(t.Context()) == nil {
		t.Fatal("ordinary verification accepted changed metadata")
	}
	original := b.files[name]
	observations, err := b.adoptToolCache(t.Context(), "restore")
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || b.files[name] != original || b.stagedFiles[name].SHA256 != sha(raw) {
		t.Fatal("missing observations or erased original")
	}
	if err := b.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Root, filepath.FromSlash(name)), append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if b.Verify(t.Context()) == nil {
		t.Fatal("post-adoption drift accepted")
	}
	if _, err := b.adoptToolCache(t.Context(), "resolve-references"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.canonical, filepath.FromSlash(name)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.adoptToolCache(t.Context(), "restore"); err == nil {
		t.Fatal("canonical mutation accepted")
	}
}
func TestToolCacheRejectsChangesOutsideDeclaredJSON(t *testing.T) {
	for _, target := range []string{"tool/1.0/bin/tool.dll", "tool/1.0/unrelated.json", ".tools/tool/1.0/net8.0/unrelated.json", ".tools/other/1.0/net8.0/project.assets.json"} {
		t.Run(target, func(t *testing.T) {
			b := toolCacheFixture(t)
			p := filepath.Join(b.Root, filepath.FromSlash(target))
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(`{"version":3,"targets":{},"libraries":{},"project":{}}`), 0600); err != nil {
				t.Fatal(err)
			}
			before := b.stagedFiles["tool/1.0/bin/tool.dll"]
			if _, err := b.adoptToolCache(t.Context(), "restore"); err == nil {
				t.Fatal("unrelated modification accepted")
			}
			if b.stagedFiles["tool/1.0/bin/tool.dll"] != before {
				t.Fatal("failed adoption committed")
			}
		})
	}
}
func TestToolCacheRejectsInvalidJSONTypeModeAndBudget(t *testing.T) {
	for _, kind := range []string{"malformed", "array", "wrong-version", "null-success", "oversize", "executable", "symlink", "missing", "byte-budget", "no-package"} {
		t.Run(kind, func(t *testing.T) {
			b := toolCacheFixture(t)
			name := ".tools/tool/1.0/net8.0/tool.nuget.cache"
			p := filepath.Join(b.Root, filepath.FromSlash(name))
			switch kind {
			case "malformed":
				_ = os.WriteFile(p, []byte("{"), 0600)
			case "array":
				_ = os.WriteFile(p, []byte("[]"), 0600)
			case "wrong-version":
				_ = os.WriteFile(p, []byte(`{"version":3}`), 0600)
			case "null-success":
				_ = os.WriteFile(p, []byte(`{"version":2,"success":null,"dgSpecHash":"hash","projectFilePath":"/project"}`), 0600)
			case "oversize":
				_ = os.WriteFile(p, []byte(strings.Repeat(" ", maxToolCacheJSONBytes+1)), 0600)
			case "executable":
				_ = os.Chmod(p, 0700)
			case "symlink":
				_ = os.Remove(p)
				if err := os.Symlink(filepath.Join(b.canonical, filepath.FromSlash(name)), p); err != nil {
					t.Fatal(err)
				}
			case "missing":
				_ = os.Remove(p)
			case "byte-budget":
				b.maxBytes = 1
			case "no-package":
				for name := range b.files {
					if strings.HasPrefix(name, "tool/1.0/") {
						delete(b.files, name)
						_ = os.Remove(filepath.Join(b.canonical, filepath.FromSlash(name)))
					}
				}
			}
			if _, err := b.adoptToolCache(t.Context(), "restore"); err == nil {
				t.Fatal("unsafe cache accepted")
			}
		})
	}
}

func TestCaptureToolCacheFailureReceiptAndSourceIntegrity(t *testing.T) {
	for _, sourceMutation := range []bool{false, true} {
		t.Run(string(rune('a'+map[bool]int{false: 0, true: 1}[sourceMutation])), func(t *testing.T) {
			b := toolCacheFixture(t)
			script := `if [ "$2" = -target:Restore ]; then printf '%s' '{"version":2,"success":true,"dgSpecHash":"after","projectFilePath":"/new"}' > "$NUGET_PACKAGES/.tools/tool/1.0/net8.0/tool.nuget.cache"; exit 0; fi
printf 'reference-preparation-failed' >&2
exit 9
`
			if sourceMutation {
				script = strings.Replace(script, "; exit 0; fi", "; printf changed > A.cs; exit 0; fi", 1)
			}
			o, _ := captureFixture(t, script)
			o.DependencyBundle = filepath.Join(t.TempDir(), "bundle")
			if _, err := PackDependencyBundle(t.Context(), b.canonical, o.DependencyBundle); err != nil {
				t.Fatal(err)
			}
			o.RestoreToolCacheMetadata = true
			_, err := Capture(t.Context(), o)
			if err == nil {
				t.Fatal("failed capture published")
			}
			if sourceMutation && !strings.Contains(err.Error(), "projected input changed") {
				t.Fatal(err)
			}
			raw, readErr := os.ReadFile(o.Output + ".dependency-tool-cache.json")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(raw), `"status": "failed"`) || !strings.Contains(string(raw), `"original_bundle_sha256"`) {
				t.Fatal(string(raw))
			}
			if !sourceMutation && (!strings.Contains(string(raw), `"phase": "restore"`) || !strings.Contains(string(raw), `"phase": "resolve-references"`)) {
				t.Fatal("missing phase observations", string(raw))
			}
			if _, e := os.Lstat(o.Workspace); !os.IsNotExist(e) {
				t.Fatal("failure workspace retained")
			}
			if _, e := os.Lstat(o.Output); !os.IsNotExist(e) {
				t.Fatal("failed artifact exists")
			}
		})
	}
}
