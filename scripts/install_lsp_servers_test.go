package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCFMLDryRunRebuildsWhenWrapperSurvivesWithoutBundle(t *testing.T) {
	toolsDir := t.TempDir()
	out := runCFMLInstallerDryRun(t, toolsDir)

	if !strings.Contains(out, "building CFML server") {
		t.Fatalf("stale wrapper should trigger a rebuild; output:\n%s", out)
	}
	if strings.Contains(out, "cflsp present:") {
		t.Fatalf("stale wrapper was misclassified as present; output:\n%s", out)
	}
}

func TestCFMLDryRunKeepsManagedInstallWithBundle(t *testing.T) {
	toolsDir := t.TempDir()
	server := filepath.Join(toolsDir, "cfc", "cflsp-vscode", "out", "server.js")
	if err := os.MkdirAll(filepath.Dir(server), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server, []byte("// built server fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runCFMLInstallerDryRun(t, toolsDir)
	if !strings.Contains(out, "managed CFML server present:") {
		t.Fatalf("complete managed install should stay idempotent; output:\n%s", out)
	}
	if strings.Contains(out, "building CFML server") {
		t.Fatalf("complete managed install unexpectedly rebuilt; output:\n%s", out)
	}
}

func TestCFMLDryRunSkipsNonCFMLInstallers(t *testing.T) {
	out := runCFMLInstallerDryRun(t, t.TempDir())

	for _, unrelated := range []string{
		"gopls",
		"npm install -g",
		"sql-language-server",
		"dotnet present:",
		"brew install dotnet",
		"dotnet tool install",
		"csharp-ls",
		"rust-analyzer",
		"clangd",
	} {
		if strings.Contains(out, unrelated) {
			t.Fatalf("CFML-only dry-run included unrelated installer output %q:\n%s", unrelated, out)
		}
	}
}

func runCFMLInstallerDryRun(t *testing.T, toolsDir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("installer is a bash script")
	}

	fakeBin := t.TempDir()
	cflsp := filepath.Join(fakeBin, "cflsp")
	if err := os.WriteFile(cflsp, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("install-lsp-servers.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--cfml-only", "--dry-run")
	cmd.Env = replaceEnv(os.Environ(), "PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = replaceEnv(cmd.Env, "MOEDEX_TOOLS_DIR", toolsDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer dry-run: %v\n%s", err, out)
	}
	return string(out)
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
