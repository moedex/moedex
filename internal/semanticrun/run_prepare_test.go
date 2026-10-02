package semanticrun

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCapturePreparesReferencesBeforeWorker(t *testing.T) {
	o, _ := captureFixture(t, `
case "$2" in
-target:Restore) printf restored > "$DOTNET_CLI_HOME/stage"; exit 0 ;;
-target:ResolveReferences)
 test "$(cat "$DOTNET_CLI_HOME/stage")" = restored || exit 8
 test "$1" = "${MSBuildSDKsPath%/Sdks}/MSBuild.dll" || exit 8
 for required in '-p:TargetFramework=net10.0' '-p:Configuration=Release' '-p:BuildProjectReferences=true' '-p:UseSharedCompilation=false' '-maxcpucount:1'; do
  found=false; for arg do if [ "$arg" = "$required" ]; then found=true; fi; done
  test "$found" = true || exit 8
 done
 printf prepared > "$DOTNET_CLI_HOME/stage"; exit 0 ;;
esac
test "$(cat "$DOTNET_CLI_HOME/stage")" = prepared || exit 8
printf worker-after-preparation >&2; exit 9
`)
	o.Configuration = "Release"
	_, err := Capture(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "worker-after-preparation") {
		t.Fatalf("phase order: %v", err)
	}
}

func TestCaptureRejectsPreparationFailureOrMutation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"build-error", "printf analyzer-build-failed >&2; exit 7", "project reference preparation"},
		{"source", "printf changed > A.cs; exit 0", "projected input changed"},
		{"dependency", "printf changed > \"$NUGET_PACKAGES/p/1.0/data\"; exit 0", "dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "if [ \"$2\" = -target:Restore ]; then exit 0; fi\nif [ \"$2\" = -target:ResolveReferences ]; then " + tc.body + "; fi\nprintf WORKER-RAN >&2; exit 9\n"
			o := captureBundleFixture(t, script)
			_, err := Capture(context.Background(), o)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "WORKER-RAN") {
				t.Fatalf("preparation accepted: %v", err)
			}
			for _, p := range []string{o.Output, o.Workspace} {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Fatalf("failed preparation retained %s", p)
				}
			}
		})
	}
}

func TestCaptureRejectsInvalidWorkerBudgetBeforeExecution(t *testing.T) {
	for _, limit := range []int64{-1, (256 << 20) + 1} {
		o, _ := captureFixture(t, "printf SHOULD-NOT-RUN >&2; exit 9")
		o.MaxWorkerBytes = limit
		_, err := Capture(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "worker byte limit") || strings.Contains(err.Error(), "SHOULD-NOT-RUN") {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if _, err := os.Lstat(o.Workspace); !os.IsNotExist(err) {
			t.Fatal("invalid limit created workspace")
		}
	}
}
