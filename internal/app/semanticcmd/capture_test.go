package semanticcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validCaptureOptions(t *testing.T) CaptureOptions {
	t.Helper()
	root := t.TempDir()
	return CaptureOptions{
		ManagedRoot: root, Repo: "group/project", Project: "App/App.csproj",
		Framework: "net10.0", Configuration: "Debug", Dotnet: "/missing/dotnet",
		Worker: "/missing/worker.dll", SDKPath: "/missing/sdk",
		Workspace: filepath.Join(root, "workspace"), Output: filepath.Join(root, "artifact.json"),
		RestoreOffline: true, Timeout: DefaultCaptureTimeout,
	}
}

func TestCaptureRejectsMissingConsentAndInvalidDeadlineBeforeExecution(t *testing.T) {
	for _, name := range []string{"offline restore disabled", "zero deadline", "negative deadline"} {
		t.Run(name, func(t *testing.T) {
			opts := validCaptureOptions(t)
			want := "timeout must be positive"
			switch name {
			case "offline restore disabled":
				opts.RestoreOffline = false
				want = "restore-offline must be explicitly enabled"
			case "zero deadline":
				opts.Timeout = 0
			case "negative deadline":
				opts.Timeout = -1
			}
			if _, err := Capture(context.Background(), opts); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected validation error %q, got %v", want, err)
			}
			for _, path := range []string{opts.Workspace, opts.Output} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid capture touched %s: %v", path, err)
				}
			}
		})
	}
}

func TestCaptureRequiresEveryExecutionInput(t *testing.T) {
	cases := []struct {
		name  string
		clear func(*CaptureOptions)
	}{
		{"repo", func(o *CaptureOptions) { o.Repo = " " }},
		{"project", func(o *CaptureOptions) { o.Project = "" }},
		{"framework", func(o *CaptureOptions) { o.Framework = "" }},
		{"dotnet", func(o *CaptureOptions) { o.Dotnet = "" }},
		{"worker", func(o *CaptureOptions) { o.Worker = "" }},
		{"sdk-path", func(o *CaptureOptions) { o.SDKPath = "" }},
		{"workspace", func(o *CaptureOptions) { o.Workspace = "" }},
		{"output", func(o *CaptureOptions) { o.Output = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			opts := validCaptureOptions(t)
			test.clear(&opts)
			if _, err := Capture(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--"+test.name+" is required") {
				t.Fatalf("missing %s accepted or misreported: %v", test.name, err)
			}
		})
	}
}

func TestCaptureCancellationPreservesExistingDestination(t *testing.T) {
	opts := validCaptureOptions(t)
	if err := os.WriteFile(opts.Output, []byte("existing artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	data, err := os.ReadFile(opts.Output)
	if err != nil || string(data) != "existing artifact" {
		t.Fatalf("existing destination changed: %q %v", data, err)
	}
	if _, err := os.Lstat(opts.Workspace); !os.IsNotExist(err) {
		t.Fatalf("cancellation created workspace: %v", err)
	}
}

func TestCaptureSourceSelection(t *testing.T) {
	for _, name := range []string{"neither", "both", "managed_commit", "managed_origin", "missing_commit", "missing_origin", "negative_limit", "excessive_limit", "public"} {
		t.Run(name, func(t *testing.T) {
			o := validCaptureOptions(t)
			switch name {
			case "neither":
				o.ManagedRoot = ""
			case "both":
				o.Checkout = "/checkout"
			case "managed_commit":
				o.Commit = "abc"
			case "managed_origin":
				o.Origin = "https://github.com/dotnet/roslyn"
			case "negative_limit":
				o.MaxProjectionBytes = -1
			case "excessive_limit":
				o.MaxProjectionBytes = 1<<30 + 1
			default:
				o.ManagedRoot = ""
				o.Checkout = "/checkout"
				o.Commit = strings.Repeat("a", 40)
				o.Origin = "https://github.com/dotnet/roslyn"
				if name == "missing_commit" {
					o.Commit = ""
				}
				if name == "missing_origin" {
					o.Origin = ""
				}
			}
			err := validateCaptureOptions(o)
			if (err == nil) != (name == "public") {
				t.Fatalf("selection validation: %v", err)
			}
		})
	}
}
