package semanticimport

import (
	"encoding/json"
	"testing"
)

func buildDiagnosticProofFixture() map[string]any {
	return map[string]any{"policy": "native-build-events-v1", "verified": true, "logs": 1, "builds_started": 1, "builds_finished": 1, "workspace_diagnostics": 1, "matched_warnings": 1,
		"projects": []any{map[string]any{"project": "A.csproj", "started": 1, "finished": 1}},
		"warnings": []any{map[string]any{"project": "A.csproj", "message": "warning"}}}
}

func TestBuildDiagnosticAdmission(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"valid":                func(map[string]any) {},
		"unverified":           func(p map[string]any) { p["verified"] = false },
		"unknown policy":       func(p map[string]any) { p["policy"] = "future" },
		"no logs":              func(p map[string]any) { p["logs"] = 0 },
		"failed build":         func(p map[string]any) { p["failed_build_or_log"] = true },
		"logged error":         func(p map[string]any) { p["errors"] = []any{map[string]any{"message": "warning"}} },
		"unfinished build":     func(p map[string]any) { p["builds_finished"] = 0 },
		"unmatched diagnostic": func(p map[string]any) { p["workspace_diagnostics"] = 2 },
		"unmatched warning":    func(p map[string]any) { p["matched_warnings"] = 0 },
		"missing project":      func(p map[string]any) { p["projects"] = []any{} },
		"unfinished project":   func(p map[string]any) { p["projects"].([]any)[0].(map[string]any)["finished"] = 0 },
		"foreign warning":      func(p map[string]any) { p["warnings"].([]any)[0].(map[string]any)["project"] = "Other.csproj" },
	} {
		t.Run(name, func(t *testing.T) {
			proof := buildDiagnosticProofFixture()
			mutate(proof)
			raw, err := json.Marshal(map[string]any{"project": "A.csproj", "build_diagnostics": proof})
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyCapture(record{Project: "A.csproj", ExtractorVersion: "20", CaptureJSON: string(raw)}); (err == nil) != (name == "valid") {
				t.Fatalf("capture admission: %v", err)
			}
		})
	}
	if verifyBuildDiagnostics(`{}`, "20", "A.csproj") == nil {
		t.Fatal("missing proof admitted")
	}
	if err := verifyBuildDiagnostics(`{}`, "19", "A.csproj"); err != nil {
		t.Fatal("legacy capture rejected", err)
	}
}

func TestWorkspaceWithoutLoggerRequiresNoDiagnostics(t *testing.T) {
	for _, field := range []string{"", "workspace_diagnostics", "logs", "builds_started", "builds_finished", "matched_warnings", "warnings", "errors", "projects"} {
		t.Run(field, func(t *testing.T) {
			proof := map[string]any{"policy": "workspace-no-diagnostics-v1", "verified": true}
			if field != "" {
				if field == "warnings" || field == "errors" || field == "projects" {
					proof[field] = []any{map[string]any{"project": "A.csproj"}}
				} else {
					proof[field] = 1
				}
			}
			raw, _ := json.Marshal(map[string]any{"build_diagnostics": proof})
			if err := verifyBuildDiagnostics(string(raw), "20", "A.csproj"); (err == nil) != (field == "") {
				t.Fatalf("legacy policy %q: %v", field, err)
			}
		})
	}
}

func TestNativeWorkspaceWarningAdmission(t *testing.T) {
	for _, kind := range []string{"Warning", "Failure", "", "warning"} {
		proof := buildDiagnosticProofFixture()
		proof["policy"] = "native-build-events-v2"
		proof["workspace_diagnostics"] = 2
		proof["workspace_warnings"] = []any{map[string]any{"kind": kind, "message": "duplicate additional document"}}
		raw, _ := json.Marshal(map[string]any{"build_diagnostics": proof})
		if err := verifyBuildDiagnostics(string(raw), "21", "A.csproj"); (err == nil) != (kind == "Warning") {
			t.Fatalf("kind %q: %v", kind, err)
		}
		if verifyBuildDiagnostics(string(raw), "20", "A.csproj") == nil {
			t.Fatal("worker20 accepted successor policy")
		}
		proof["workspace_diagnostics"] = 1
		raw, _ = json.Marshal(map[string]any{"build_diagnostics": proof})
		if verifyBuildDiagnostics(string(raw), "21", "A.csproj") == nil {
			t.Fatal("warning multiplicity mismatch admitted")
		}
	}
	if verifyBuildDiagnostics(`{}`, "21", "A.csproj") == nil {
		t.Fatal("worker21 missing proof admitted")
	}
}
