package semanticrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestToolchainViewSelectsOneSDKAndCopiesHost(t *testing.T) {
	o, _ := captureFixture(t, "exit 0\n")
	root := filepath.Dir(o.Dotnet)
	if err := os.MkdirAll(filepath.Join(root, "sdk", "99.0.0"), 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "toolchain")
	host, err := stageToolchain(context.Background(), o.Dotnet, o.SDKPath, dest)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dest, "sdk"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "10.0.100" {
		t.Fatal(entries, err)
	}
	info, err := os.Lstat(host)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("host must be copied", err)
	}
	before, _ := os.ReadFile(o.Dotnet)
	after, _ := os.ReadFile(host)
	if string(before) != string(after) {
		t.Fatal("host bytes changed")
	}
	if err := os.WriteFile(host, []byte("private"), 0700); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(o.Dotnet)
	if string(original) != string(before) {
		t.Fatal("caller host modified")
	}
	sdk, err := filepath.EvalSymlinks(filepath.Join(dest, "sdk", "10.0.100"))
	expected, expectedErr := filepath.EvalSymlinks(o.SDKPath)
	if err != nil || expectedErr != nil || sdk != expected {
		t.Fatal(sdk, err)
	}
}
