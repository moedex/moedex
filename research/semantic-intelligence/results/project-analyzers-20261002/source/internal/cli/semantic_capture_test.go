package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func captureCLIArgs() []string {
	return []string{"capture", "--managed-root", "/missing/managed", "--repo", "group/project", "--project", "App/App.csproj", "--framework", "net10.0", "--dotnet", "/missing/dotnet", "--worker", "/missing/worker.dll", "--sdk-path", "/missing/sdk", "--workspace", "/missing/new-workspace", "--output", "/missing/new-artifact"}
}

func TestSemanticCaptureStandardRestoreOptIn(t *testing.T) {
	for _, value := range []string{"false", "true"} {
		cmd := newSemanticCommand()
		capture, _, err := cmd.Find([]string{"capture"})
		if err != nil {
			t.Fatal(err)
		}
		flag := capture.Flags().Lookup("restore-standard-evaluation")
		if flag == nil || flag.DefValue != "false" || !strings.Contains(flag.Usage, "does not edit source configuration") {
			t.Fatalf("missing explicit operational restore option: %v", flag)
		}
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append(captureCLIArgs(), "--restore-offline", "--restore-standard-evaluation="+value))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("option parsing: %v", err)
		}
		if flag.Value.String() != value {
			t.Fatalf("option not parsed: %s", flag.Value.String())
		}
	}
}

func TestSemanticCaptureRequiresExplicitOfflineRestore(t *testing.T) {
	for _, flag := range []string{"", "--restore-offline=false"} {
		command := newSemanticCommand()
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		args := captureCLIArgs()
		if flag != "" {
			args = append(args, flag)
		}
		command.SetArgs(args)
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "restore-offline") {
			t.Fatalf("offline restore guard = %v", err)
		}
	}
}

func TestSemanticCaptureRejectsNonpositiveDeadline(t *testing.T) {
	command := newSemanticCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs(append(captureCLIArgs(), "--restore-offline", "--timeout=0"))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "timeout must be positive") {
		t.Fatalf("deadline guard = %v", err)
	}
}

func TestSemanticCaptureForwardsCancellation(t *testing.T) {
	command := newSemanticCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs(append(captureCLIArgs(), "--restore-offline"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := command.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestSemanticCapturePublicSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"missing_origin", []string{"--checkout", "/missing/checkout", "--commit", strings.Repeat("a", 40)}, "requires --commit and --origin"},
		{"mutual_exclusion", []string{"--checkout", "/missing/checkout", "--managed-root", "/missing/managed"}, "exactly one"},
		{"projection_limit", []string{"--checkout", "/missing/checkout", "--commit", strings.Repeat("a", 40), "--origin", "https://github.com/dotnet/roslyn", "--max-projection-bytes=-1"}, "max-projection-bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := captureCLIArgs()
			args = append(args[:1], args[3:]...)
			args = append(args, "--restore-offline")
			args = append(args, tc.extra...)
			cmd := newSemanticCommand()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("selection: %v", err)
			}
		})
	}
}

func TestSemanticCaptureWorkerBudget(t *testing.T) {
	command := newSemanticCommand()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs(append(captureCLIArgs(), "--restore-offline", "--max-worker-bytes=268435457"))
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "worker byte limit") {
		t.Fatalf("oversize budget: %v", err)
	}
	capture, _, err := command.Find([]string{"capture"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := capture.Flags().Lookup("max-worker-bytes"); flag == nil || flag.DefValue != "0" {
		t.Fatalf("default budget: %v", flag)
	}
}
