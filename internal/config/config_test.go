package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePrecedenceAndRedaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moe.env")
	if err := os.WriteFile(path, []byte("MOEDEX_SHARD_DIR=/from-file\nMOEDEX_AUTH_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, warnings, err := DefaultRegistry().Resolve([]string{
		"MOEDEX_SHARD_DIR=/from-env",
		"MOEDEX_AUTH_TOKEN=ambient",
	}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	values := state.Values(true)
	got := map[string]Value{}
	for _, value := range values {
		got[value.Name] = value
	}
	if got["MOEDEX_SHARD_DIR"].Value != "/from-file" || got["MOEDEX_SHARD_DIR"].Source != SourceFile {
		t.Fatalf("shard value = %+v", got["MOEDEX_SHARD_DIR"])
	}
	if got["MOEDEX_AUTH_TOKEN"].Value != "********" {
		t.Fatalf("secret leaked: %+v", got["MOEDEX_AUTH_TOKEN"])
	}
}

func TestUnknownFileSettingFailsWithSuggestion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moe.env")
	if err := os.WriteFile(path, []byte("MOEDEX_MCP_MAX_CONCURENCY=4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := DefaultRegistry().Resolve(nil, path)
	if err == nil || !strings.Contains(err.Error(), "MOEDEX_MCP_MAX_CONCURRENCY") {
		t.Fatalf("error = %v", err)
	}
}

func TestAmbientUnknownWarnsAndInvalidTypedValueFails(t *testing.T) {
	registry := DefaultRegistry()
	_, warnings, err := registry.Resolve([]string{"MOEDEX_MCP_CONCURENCY=4"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "closest match") {
		t.Fatalf("warnings = %v", warnings)
	}
	_, _, err = registry.Resolve([]string{"MOEDEX_MCP_MAX_CONCURRENCY=many"}, "")
	if err == nil {
		t.Fatal("invalid integer accepted")
	}
}

func TestApplyRestoresEnvironment(t *testing.T) {
	t.Setenv("MOEDEX_SHARD_DIR", "/ambient")
	path := filepath.Join(t.TempDir(), "moe.env")
	if err := os.WriteFile(path, []byte("MOEDEX_SHARD_DIR=/file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, _, err := DefaultRegistry().Resolve(os.Environ(), path)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := state.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MOEDEX_SHARD_DIR"); got != "/file" {
		t.Fatalf("applied = %q", got)
	}
	restore()
	if got := os.Getenv("MOEDEX_SHARD_DIR"); got != "/ambient" {
		t.Fatalf("restored = %q", got)
	}
}

func TestWriteJSONUsesVersionedEnvelope(t *testing.T) {
	state, _, err := DefaultRegistry().Resolve(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteJSON(&output, state.Values(false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"schema": 1`) || strings.Contains(output.String(), "ambient") {
		t.Fatalf("output = %s", output.String())
	}
}

func TestRegistryDefaultsMatchRuntimeContracts(t *testing.T) {
	state, _, err := DefaultRegistry().Resolve(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, value := range state.Values(false) {
		values[value.Name] = value.Value
	}
	for name, want := range map[string]string{
		"MOEDEX_GRAPH_LSP_REQUESTS_PER_SECOND": "100",
		"MOEDEX_GRAPH_CLUSTER_MAX_NODES":       "250000",
		"MOEDEX_VERIFY_CONTENT":                "true",
	} {
		if got := values[name]; got != want {
			t.Errorf("%s default = %q, want %q", name, got, want)
		}
	}
}
