package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	in := "" +
		"# a comment\n" +
		"MOEDEX_SHARD_DIR=/var/lib/moedex/shards\n" +
		"\n" +
		"  MOEDEX_AUTH_TOKEN = \"s3cr3t\"  \n" +
		"export MOEDEX_EMBED='onnx'\n" +
		"MOEDEX_HTTP_ADDR=0.0.0.0:8080\n" +
		"MOEDEX_NOTE=a=b=c\n"
	got, err := parseEnvFile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	want := map[string]string{
		"MOEDEX_SHARD_DIR":  "/var/lib/moedex/shards",
		"MOEDEX_AUTH_TOKEN": "s3cr3t",      // quotes stripped, surrounding space trimmed
		"MOEDEX_EMBED":      "onnx",        // export prefix + single quotes
		"MOEDEX_HTTP_ADDR":  "0.0.0.0:8080",
		"MOEDEX_NOTE":       "a=b=c",       // only the first '=' splits
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseEnvFileErrors(t *testing.T) {
	for _, bad := range []string{"NOTKEYVALUE\n", "=novalue\n", "1BAD=x\n", "BAD KEY=x\n"} {
		if _, err := parseEnvFile(strings.NewReader(bad)); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestExtractConfigPath(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-config", "/a"}, "/a"},
		{[]string{"--config", "/b"}, "/b"},
		{[]string{"-config=/c"}, "/c"},
		{[]string{"--config=/d"}, "/d"},
		{[]string{"-http", ":8080"}, ""},
		{[]string{"-config"}, ""}, // flag with no following value
		{[]string{"-x", "-config", "/e", "-y"}, "/e"},
	}
	for _, c := range cases {
		if got := extractConfigPath(c.args); got != c.want {
			t.Errorf("extractConfigPath(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestLoadEnvFileOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.env")
	if err := os.WriteFile(p, []byte("MOEDEX_TEST_OVERRIDE=fromfile\nMOEDEX_TEST_NEW=newval\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// t.Setenv registers cleanup so loadEnvFile's os.Setenv is restored after.
	t.Setenv("MOEDEX_TEST_OVERRIDE", "fromenv")
	t.Setenv("MOEDEX_TEST_NEW", "")

	if err := loadEnvFile(p); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv("MOEDEX_TEST_OVERRIDE"); got != "fromfile" {
		t.Errorf("file must override env: MOEDEX_TEST_OVERRIDE = %q, want fromfile", got)
	}
	if got := os.Getenv("MOEDEX_TEST_NEW"); got != "newval" {
		t.Errorf("file must set new key: MOEDEX_TEST_NEW = %q, want newval", got)
	}
	if err := loadEnvFile(filepath.Join(dir, "does-not-exist.env")); err == nil {
		t.Error("expected error for missing config file")
	}
}
