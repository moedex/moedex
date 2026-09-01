package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticCommandTree(t *testing.T) {
	root := NewRoot()
	for _, path := range [][]string{
		{"search"}, {"index", "cas", "export"}, {"index", "snapshot", "inspect"},
		{"graph", "audit"}, {"corpus", "sync"}, {"corpus", "scale"},
		{"serve"}, {"mcp"}, {"nav", "refs"}, {"parity"}, {"doctor"}, {"config"}, {"version"},
	} {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			t.Fatalf("find %v: command=%v err=%v", path, command, err)
		}
	}
}

func TestNormalizeGlobalFlagsAfterDelegatedCommand(t *testing.T) {
	got := normalizeGlobalFlags([]string{
		"index", "build", "-corpus", "/code", "--json", "--config", "/etc/moe.env", "--no-color",
	})
	want := []string{
		"--json", "--config", "/etc/moe.env", "--no-color", "index", "build", "-corpus", "/code",
	}
	if !equalStrings(got, want) {
		t.Fatalf("normalizeGlobalFlags() = %q, want %q", got, want)
	}
}

func TestNormalizeGlobalFlagsHonorsDoubleDash(t *testing.T) {
	got := normalizeGlobalFlags([]string{"search", "--", "--json"})
	want := []string{"search", "--", "--json"}
	if !equalStrings(got, want) {
		t.Fatalf("normalizeGlobalFlags() = %q, want %q", got, want)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestSearchRejectsAmbiguousSource(t *testing.T) {
	root := NewRoot()
	root.SetArgs([]string{"search", "needle", "--index-dir", "snapshots", "--shard-dir", "shards"})
	err := root.Execute()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("error = %#v", err)
	}
}

func TestConfigCommandLoadsExplicitFileAndRedactsJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moe.env")
	if err := os.WriteFile(path, []byte("MOEDEX_SHARD_DIR=/configured\nMOEDEX_AUTH_TOKEN=top-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := NewRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--config", path, "--json", "config", "--diff"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v; stderr=%s", err, stderr.String())
	}
	var envelope struct {
		Schema   int `json:"schema"`
		Settings []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if envelope.Schema != 1 {
		t.Fatalf("schema = %d", envelope.Schema)
	}
	got := map[string]string{}
	for _, setting := range envelope.Settings {
		got[setting.Name] = setting.Value
	}
	if got["MOEDEX_SHARD_DIR"] != "/configured" || got["MOEDEX_AUTH_TOKEN"] != "********" {
		t.Fatalf("settings = %v", got)
	}
}
