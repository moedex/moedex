package semanticrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPackDependencyBundleDeterministicAndIsolated(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "pkg", "1.0"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z.dll", "a.nuspec", ".nupkg.metadata"} {
		if err := os.WriteFile(filepath.Join(source, "pkg", "1.0", name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := PackDependencyBundle(context.Background(), source, filepath.Join(root, "bundle-a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := PackDependencyBundle(context.Background(), source, filepath.Join(root, "bundle-b"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ManifestSHA256 != b.ManifestSHA256 || a.Files != 3 || a.Bytes == 0 {
		t.Fatalf("summary: %+v %+v", a, b)
	}
	raw, err := os.ReadFile(filepath.Join(a.Path, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	if a.ManifestSHA256 != hex.EncodeToString(h[:]) {
		t.Fatal("digest not exact manifest bytes")
	}
	m, err := ReadDependencyManifest(filepath.Join(a.Path, "manifest.json"))
	if err != nil || len(m.Files) != 3 {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	if err := os.WriteFile(filepath.Join(source, "pkg", "1.0", "z.dll"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(a.Path, "packages", "pkg", "1.0", "z.dll"))
	if err != nil || !bytes.Equal(got, []byte("z.dll")) {
		t.Fatal("bundle aliases input")
	}
	if _, err := PackDependencyBundle(context.Background(), source, a.Path); err == nil {
		t.Fatal("existing destination replaced")
	}
}

func TestPackDependencyBundleRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(source, "output")
	if _, err := PackDependencyBundle(context.Background(), source, inside); err == nil {
		t.Fatal("nested output accepted")
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatal("nested output created")
	}
	if err := os.Symlink("/outside", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "bundle")
	if _, err := PackDependencyBundle(context.Background(), source, output); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failed output retained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PackDependencyBundle(ctx, source, output); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestPackDependencyBundleExecutableTools(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(source, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tool, 0755); err != nil {
		t.Fatal(err)
	}
	packed, err := PackDependencyBundle(context.Background(), source, filepath.Join(root, "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadDependencyManifest(filepath.Join(packed.Path, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || !m.Files[0].Executable {
		t.Fatal("executable metadata omitted")
	}
	staged := filepath.Join(packed.Path, "packages", "tool")
	info, err := os.Stat(staged)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("staged mode: %v %v", info, err)
	}
	if err := os.Chmod(tool, 0600); err != nil {
		t.Fatal(err)
	}
	other, err := PackDependencyBundle(context.Background(), source, filepath.Join(root, "nonexec"))
	if err != nil {
		t.Fatal(err)
	}
	if packed.ManifestSHA256 == other.ManifestSHA256 {
		t.Fatal("executable metadata not hash-bound")
	}
	if _, err := StageDependencyBundle(context.Background(), DependencyBundleOptions{Directory: source, Files: m.Files}, filepath.Join(root, "badstage")); err == nil {
		t.Fatal("source execute-bit change accepted")
	}
	b, err := StageDependencyBundle(context.Background(), DependencyBundleOptions{Directory: filepath.Join(packed.Path, "packages"), Files: m.Files}, filepath.Join(root, "stage"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := os.Chmod(filepath.Join(b.Root, "tool"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.Verify(context.Background()); err == nil {
		t.Fatal("staged execute-bit change accepted")
	}
}

func TestDependencyExplicitByteBudget(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("12345678"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{-1, MaxDependencyBudgetBytes + 1, 7} {
		output := filepath.Join(t.TempDir(), "bundle")
		if _, err := PackDependencyBundleWithLimit(context.Background(), source, output, limit); err == nil {
			t.Fatalf("accepted budget %d", limit)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("failed pack retained output")
		}
	}
	for _, limit := range []int64{0, 8, MaxDependencyBytes + 1, MaxDependencyBudgetBytes} {
		output := filepath.Join(t.TempDir(), "bundle")
		q, err := PackDependencyBundleWithLimit(context.Background(), source, output, limit)
		if err != nil || q.Bytes != 8 {
			t.Fatalf("%+v %v", q, err)
		}
		manifest, err := ReadDependencyManifest(filepath.Join(output, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := StageDependencyBundle(context.Background(), DependencyBundleOptions{Directory: filepath.Join(output, "packages"), Files: manifest.Files, MaxBytes: 7}, filepath.Join(t.TempDir(), "stage")); err == nil {
			t.Fatal("staging ignored budget")
		}
	}
}
