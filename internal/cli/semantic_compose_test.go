package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/app/semanticcmd"
	"moedex/internal/semantic"
)

func cliComposeArtifact(t *testing.T, path string) {
	t.Helper()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	snapshot := semantic.SourceSnapshot{Repo: "repo", InputFingerprint: sum("roster")}
	snapshot.ID = snapshot.ComputeID()
	ctx := semantic.BuildContext{SnapshotID: snapshot.ID, Project: "A.csproj", InputFingerprint: sum("{}"), Capture: "{}", Extractor: "roslyn", ExtractorVersion: "2", Status: "complete"}
	ctx.ID = ctx.ComputeID()
	artifact := &semantic.Artifact{Version: semantic.FormatVersion, Snapshots: []semantic.SourceSnapshot{snapshot}, Contexts: []semantic.BuildContext{ctx}}
	if err := semantic.Write(path, artifact); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticComposeRepeatedInputsAndSummary(t *testing.T) {
	dir := t.TempDir()
	// StringArray preserves a comma within the literal filename.
	input := filepath.Join(dir, "capture,debug.json")
	cliComposeArtifact(t, input)
	output := filepath.Join(dir, "composed.json")
	cmd := newSemanticCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"compose", "--input", input, "--input", input, "--output", output})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var summary semanticcmd.ComposeSummary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("summary %s: %v", out.String(), err)
	}
	if summary.Artifact != output || summary.Inputs != 2 || summary.Contexts != 1 || len(summary.SnapshotDetails) != 1 || len(summary.SnapshotDetails[0].Inputs) != 1 || !strings.HasPrefix(summary.SnapshotDetails[0].SnapshotID, "snapshot:") {
		t.Fatalf("summary=%+v", summary)
	}
	before, _ := os.ReadFile(output)
	again := newSemanticCommand()
	again.SetOut(&bytes.Buffer{})
	again.SetErr(&bytes.Buffer{})
	again.SetArgs([]string{"compose", "--input", input, "--output", output})
	if err := again.Execute(); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing output replaced", err)
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("immutable output changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "CURRENT")); !os.IsNotExist(err) {
		t.Fatal("CURRENT changed")
	}
}

func TestSemanticComposeValidationCancellationAndHelp(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.json")
	cliComposeArtifact(t, input)
	output := filepath.Join(dir, "out.json")
	for _, args := range [][]string{
		{"compose"}, {"compose", "--input", input}, {"compose", "--output", output},
		{"compose", "--input", input, "--output", output, "extra"},
		{"compose", "--input", input, "--output", output, "--max-bytes", "1"},
		{"compose", "--input", input, "--output", output, "--max-bytes", "-1"},
	} {
		cmd := newSemanticCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := newSemanticCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"compose", "--input", input, "--output", output})
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failed command published artifact")
	}
	cmd = newSemanticCommand()
	var help bytes.Buffer
	cmd.SetOut(&help)
	cmd.SetErr(&help)
	cmd.SetArgs([]string{"compose", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--input", "--output", "--max-bytes", "--semantic-workspaces", "snapshot ID", "without overwriting", "does not infer"} {
		if !strings.Contains(help.String(), text) {
			t.Fatalf("help missing %q: %s", text, help.String())
		}
	}
}
