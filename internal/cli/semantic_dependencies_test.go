package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticDependenciesPack(t *testing.T) {
	root := t.TempDir()
	packages := filepath.Join(root, "packages")
	if err := os.Mkdir(packages, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packages, "p.nuspec"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "bundle")
	cmd := newSemanticCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"dependencies", "pack", "--packages", packages, "--output", output})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Path  string `json:"path"`
		SHA   string `json:"manifest_sha256"`
		Files int    `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.SHA) != 64 || got.Files != 1 {
		t.Fatalf("summary %s: %v", out.String(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd = newSemanticCommand()
	cmd.SetArgs([]string{"dependencies", "pack", "--packages", packages, "--output", filepath.Join(root, "cancelled")})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSemanticDependenciesPackRequiredFlags(t *testing.T) {
	cmd := newSemanticCommand()
	cmd.SetArgs([]string{"dependencies", "pack"})
	if cmd.Execute() == nil {
		t.Fatal("missing inputs accepted")
	}
}
