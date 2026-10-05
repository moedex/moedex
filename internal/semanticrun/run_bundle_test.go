package semanticrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureBundleFixture(t *testing.T, script string) Options {
	t.Helper()
	o, _ := captureFixture(t, script)
	packages := t.TempDir()
	if err := os.MkdirAll(filepath.Join(packages, "p", "1.0"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packages, "p", "1.0", "data"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	o.DependencyBundle = filepath.Join(t.TempDir(), "bundle")
	if _, err := PackDependencyBundle(context.Background(), packages, o.DependencyBundle); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestCaptureStagesPrivateDependencies(t *testing.T) {
	o := captureBundleFixture(t, "test \"$(cat \"$NUGET_PACKAGES/p/1.0/data\")\" = original || exit 8\nprintf 'verified private dependency' >&2\nexit 9\n")
	_, err := Capture(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "verified private dependency") {
		t.Fatalf("restore did not receive bundle: %v", err)
	}
	for _, path := range []string{o.Workspace, o.Output} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("failed capture retained %s", path)
		}
	}
	got, err := os.ReadFile(filepath.Join(o.DependencyBundle, "packages", "p", "1.0", "data"))
	if err != nil || string(got) != "original" {
		t.Fatalf("caller bundle changed: %q %v", got, err)
	}
}

func TestCaptureRestoreFeedsAreConfined(t *testing.T) {
	for _, bundled := range []bool{false, true} {
		script := `
case "$2" in
  -target:Restore|-target:ResolveReferences) ;;
  *) printf 'unexpected worker' >&2; exit 9 ;;
esac
found=0
for arg do
  case "$arg" in
    -p:RestoreSources=*)
      test "$arg" = "-p:RestoreSources=$EXPECTED_SOURCE" || { printf 'wrong feed' >&2; exit 8; }
      found=1 ;;
  esac
done
test "$found" = 1 || { printf 'missing feed' >&2; exit 8; }
if [ "$2" = -target:ResolveReferences ]; then printf 'local feeds verified' >&2; exit 9; fi
exit 0
`
		// The fixture tools receive the same isolated environment as real capture.
		// Derive the expected source from its private cache, not the host config.
		expected := "$DOTNET_CLI_HOME/../empty-feed"
		if bundled {
			expected = "$NUGET_PACKAGES"
		}
		script = "EXPECTED_SOURCE=\"" + expected + "\"\n" + script
		var o Options
		if bundled {
			o = captureBundleFixture(t, script)
		} else {
			o, _ = captureFixture(t, script)
		}
		// Normalize the textual path used by the fake tool's comparison.
		script = strings.ReplaceAll(script, "$DOTNET_CLI_HOME/../empty-feed", filepath.Join(o.Workspace, "empty-feed"))
		if err := os.WriteFile(o.Dotnet, []byte("#!/bin/sh\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := Capture(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "local feeds verified") {
			t.Fatalf("bundled=%v: %v", bundled, err)
		}
	}
}

func TestCaptureRejectsDependencyMutationBeforeWorker(t *testing.T) {
	for _, target := range []string{"\"$NUGET_PACKAGES/p/1.0/data\"", "\"$DOTNET_CLI_HOME/../dependency-manifest.json\""} {
		t.Run(target, func(t *testing.T) {
			o := captureBundleFixture(t, "if [ \"$2\" != -target:Restore ]; then printf 'WORKER-RAN' >&2; exit 9; fi\nprintf tampered > "+target+"\nexit 0\n")
			_, err := Capture(context.Background(), o)
			if err == nil || strings.Contains(err.Error(), "WORKER-RAN") {
				t.Fatalf("mutation admitted: %v", err)
			}
			if _, err := os.Lstat(o.Output); !os.IsNotExist(err) {
				t.Fatal("mutation published artifact")
			}
		})
	}
}

func TestCaptureRejectsDestinationInsideDependencyBundle(t *testing.T) {
	o := captureBundleFixture(t, "exit 99\n")
	o.Workspace = filepath.Join(o.DependencyBundle, "new-workspace")
	if _, err := Capture(context.Background(), o); err == nil || !strings.Contains(err.Error(), "outside dependency bundle") {
		t.Fatalf("error: %v", err)
	}
	if _, err := os.Lstat(o.Workspace); !os.IsNotExist(err) {
		t.Fatal("created workspace inside bundle")
	}
}

func TestCaptureStandardRestoreIsExplicit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		o, _ := captureFixture(t, "for arg do if [ \"$arg\" = '-p:RestoreUseStaticGraphEvaluation=false' ]; then printf standard-restore >&2; exit 9; fi; done\nprintf project-default-restore >&2\nexit 9\n")
		o.RestoreStandardEvaluation = enabled
		_, err := Capture(context.Background(), o)
		want := "project-default-restore"
		if enabled {
			want = "standard-restore"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("enabled=%v: %v", enabled, err)
		}
	}
}

func TestCapturePinsRestoreSDK(t *testing.T) {
	o, _ := captureFixture(t, "test \"$1\" = \"${MSBuildSDKsPath%/Sdks}/MSBuild.dll\" || { printf wrong-sdk >&2; exit 8; }\ntest \"$2\" = -target:Restore || exit 8\nprintf pinned-restore >&2\nexit 9\n")
	_, err := Capture(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "pinned-restore") {
		t.Fatal(err)
	}
}
func TestCaptureDependencyBudgetAndPropertySeparators(t *testing.T) {
	for _, limit := range []int64{-1, MaxDependencyBudgetBytes + 1, 1} {
		o := captureBundleFixture(t, "printf SHOULD-NOT-RUN >&2; exit 9\n")
		o.MaxDependencyBytes = limit
		_, err := Capture(context.Background(), o)
		if err == nil || strings.Contains(err.Error(), "SHOULD-NOT-RUN") {
			t.Fatal(err)
		}
	}
	o, _ := captureFixture(t, "exit 9\n")
	o.Workspace += ";injected=x"
	if _, err := Capture(context.Background(), o); err == nil || !strings.Contains(err.Error(), "separators") {
		t.Fatal(err)
	}
}
