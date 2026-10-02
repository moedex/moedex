package semanticrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func dependencyFixture(t *testing.T) (DependencyBundleOptions, string) {
	t.Helper()
	source := t.TempDir()
	name := "example/1.0/lib/net10.0/example.dll"
	if e := os.MkdirAll(filepath.Join(source, filepath.Dir(name)), 0700); e != nil {
		t.Fatal(e)
	}
	data := []byte("offline package bytes\x00")
	if e := os.WriteFile(filepath.Join(source, name), data, 0600); e != nil {
		t.Fatal(e)
	}
	return DependencyBundleOptions{Directory: source, Files: []DependencyFile{{Path: name, Size: int64(len(data)), SHA256: sha(data)}}}, filepath.Join(t.TempDir(), "packages")
}

func TestDependencyBundleOwnedCopy(t *testing.T) {
	o, dest := dependencyFixture(t)
	b, e := StageDependencyBundle(context.Background(), o, dest)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e := b.Verify(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := b.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(dest); !os.IsNotExist(e) {
		t.Fatalf("staged directory survives: %v", e)
	}
	if _, e := os.Stat(filepath.Join(o.Directory, o.Files[0].Path)); e != nil {
		t.Fatal("caller bundle removed:", e)
	}
	if e := b.Verify(context.Background()); e == nil {
		t.Fatal("closed bundle accepted")
	}
}

func TestDependencyBundleRejectsInvalidInputs(t *testing.T) {
	for _, name := range []string{"missing", "unlisted", "symlink", "directory-symlink", "traversal", "duplicate", "hash", "size", "bytes", "files", "inside", "existing"} {
		t.Run(name, func(t *testing.T) {
			o, dest := dependencyFixture(t)
			file := filepath.Join(o.Directory, o.Files[0].Path)
			switch name {
			case "missing":
				if e := os.Remove(file); e != nil {
					t.Fatal(e)
				}
			case "unlisted":
				if e := os.WriteFile(filepath.Join(o.Directory, "extra"), nil, 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink(file, filepath.Join(o.Directory, "link")); e != nil {
					t.Fatal(e)
				}
			case "directory-symlink":
				if e := os.Symlink(t.TempDir(), filepath.Join(o.Directory, "link")); e != nil {
					t.Fatal(e)
				}
			case "traversal":
				o.Files[0].Path = "../escape"
			case "duplicate":
				o.Files = append(o.Files, o.Files[0])
			case "hash":
				o.Files[0].SHA256 = sha([]byte("wrong"))
			case "size":
				o.Files[0].Size++
			case "bytes":
				o.MaxBytes = 1
			case "files":
				o.MaxFiles = -1
			case "inside":
				dest = filepath.Join(o.Directory, "nested")
			case "existing":
				if e := os.Mkdir(dest, 0700); e != nil {
					t.Fatal(e)
				}
			}
			if b, e := StageDependencyBundle(context.Background(), o, dest); e == nil {
				b.Close()
				t.Fatal("invalid bundle accepted")
			}
			_, e := os.Stat(dest)
			if name == "existing" {
				if e != nil {
					t.Fatal("caller directory removed", e)
				}
			} else if !os.IsNotExist(e) {
				t.Fatalf("failed staging leaked directory: %v", e)
			}
		})
	}
}

func TestDependencyBundleDetectsCanonicalAndStagedTamper(t *testing.T) {
	for _, where := range []string{"canonical", "staged", "added", "missing"} {
		t.Run(where, func(t *testing.T) {
			o, dest := dependencyFixture(t)
			b, e := StageDependencyBundle(context.Background(), o, dest)
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			root := dest
			if where == "canonical" {
				root = o.Directory
			}
			path := filepath.Join(root, o.Files[0].Path)
			switch where {
			case "added":
				e = os.WriteFile(filepath.Join(root, "restore-added"), nil, 0600)
			case "missing":
				e = os.Remove(path)
			default:
				e = os.WriteFile(path, make([]byte, o.Files[0].Size), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = b.Verify(context.Background()); e == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
}

func TestDependencyManifestStrictDecode(t *testing.T) {
	for _, contents := range []string{`{"version":1,"files":[]}`, `{"version":2,"files":[]}`, `{"version":1,"files":[],"typo":true}`, `{"version":1,"files":[]} {}`, `{`} {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if e := os.WriteFile(path, []byte(contents), 0600); e != nil {
			t.Fatal(e)
		}
		_, e := ReadDependencyManifest(path)
		if (e == nil) != (contents == `{"version":1,"files":[]}`) {
			t.Fatalf("decode %s: %v", contents, e)
		}
	}
}

func TestDependencyBundleCancellation(t *testing.T) {
	o, dest := dependencyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := StageDependencyBundle(ctx, o, dest); e != context.Canceled {
		t.Fatalf("canceled staging: %v", e)
	}
	if _, e := os.Stat(dest); !os.IsNotExist(e) {
		t.Fatal("canceled staging created destination")
	}
}
