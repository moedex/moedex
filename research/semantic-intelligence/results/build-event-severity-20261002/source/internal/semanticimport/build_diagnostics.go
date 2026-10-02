package semanticimport

import (
	"encoding/json"
	"fmt"
)

// Worker20 admits workspace warnings only with evidence from the actual native
// build. Preserve that requirement at both import and publication revalidation.
func verifyBuildDiagnostics(raw, version, project string) error {
	if version != "20" {
		return nil
	}
	var capture struct {
		Proof *struct {
			Policy               string `json:"policy"`
			Verified             bool   `json:"verified"`
			Logs                 int    `json:"logs"`
			BuildsStarted        int    `json:"builds_started"`
			BuildsFinished       int    `json:"builds_finished"`
			Failed               bool   `json:"failed_build_or_log"`
			WorkspaceDiagnostics int    `json:"workspace_diagnostics"`
			MatchedWarnings      int    `json:"matched_warnings"`
			Projects             []struct {
				Project  string `json:"project"`
				Started  int    `json:"started"`
				Finished int    `json:"finished"`
			} `json:"projects"`
			Warnings []struct {
				Project string `json:"project"`
			} `json:"warnings"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"build_diagnostics"`
	}
	if err := json.Unmarshal([]byte(raw), &capture); err != nil {
		return err
	}
	p := capture.Proof
	bad := func() error {
		return fmt.Errorf("semantic import: unverified native build diagnostics for %s", project)
	}
	if p == nil || !p.Verified || p.Failed || len(p.Errors) != 0 {
		return bad()
	}
	if p.Policy == "workspace-no-diagnostics-v1" {
		if p.Logs != 0 || p.BuildsStarted != 0 || p.BuildsFinished != 0 || p.WorkspaceDiagnostics != 0 || p.MatchedWarnings != 0 || len(p.Warnings) != 0 || len(p.Projects) != 0 {
			return bad()
		}
		return nil
	}
	if p.Policy != "native-build-events-v1" || p.Logs <= 0 || p.BuildsStarted <= 0 || p.BuildsStarted != p.BuildsFinished || p.WorkspaceDiagnostics != len(p.Warnings) || p.MatchedWarnings != len(p.Warnings) {
		return bad()
	}
	seen := map[string]bool{}
	for _, row := range p.Projects {
		if !validPath(row.Project) || seen[row.Project] || row.Started <= 0 || row.Started != row.Finished {
			return bad()
		}
		seen[row.Project] = true
	}
	if !seen[project] {
		return bad()
	}
	for _, warning := range p.Warnings {
		if !seen[warning.Project] {
			return bad()
		}
	}
	return nil
}
