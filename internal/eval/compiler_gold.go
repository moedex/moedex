package eval

// Compiler gold is deliberately independent of the adapter's descriptor syntax.
// Reviewed fixture source locations establish identity and binding expectations;
// engine output must map its own symbols back to these anchors.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

type CompilerGoldSuite struct {
	Version             int                             `json:"version"`
	LabelSource         string                          `json:"label_source"`
	TargetFramework     string                          `json:"target_framework"`
	Projects            []CompilerGoldProject           `json:"projects"`
	References          []CompilerGoldReference         `json:"references"`
	IdentityGroups      []CompilerGoldIdentityGroup     `json:"identity_groups"`
	ContextDifferences  []CompilerGoldContextDifference `json:"context_differences"`
	GeneratedInputs     []string                        `json:"generated_inputs"`
	ExpectedDiagnostics []CompilerGoldDiagnostic        `json:"expected_diagnostics"`
	Deferred            []string                        `json:"deferred"`
}

type CompilerGoldProject struct {
	ID               string   `json:"id"`
	ProjectFile      string   `json:"project_file"`
	AssemblyName     string   `json:"assembly_name"`
	ExpectedAnalysis string   `json:"expected_analysis"`
	Sources          []string `json:"sources"`
	Defines          []string `json:"defines"`
}

type CompilerGoldAnchor struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type CompilerGoldTarget struct {
	Project string             `json:"project"`
	Anchor  CompilerGoldAnchor `json:"anchor"`
}

type CompilerGoldReference struct {
	ID               string               `json:"id"`
	Project          string               `json:"project"`
	Source           CompilerGoldAnchor   `json:"source"`
	Status           string               `json:"status"`
	Target           *CompilerGoldTarget  `json:"target,omitempty"`
	CandidateTargets []CompilerGoldTarget `json:"candidate_targets,omitempty"`
	ExternalSymbol   string               `json:"external_symbol,omitempty"`
	Coverage         []string             `json:"coverage"`
}

type CompilerGoldIdentityGroup struct {
	ID           string               `json:"id"`
	Relation     string               `json:"relation"`
	Declarations []CompilerGoldTarget `json:"declarations"`
}

type CompilerGoldContextDifference struct {
	Projects     []string `json:"projects"`
	SharedSource string   `json:"shared_source"`
	Reason       string   `json:"reason"`
}

type CompilerGoldDiagnostic struct {
	Project string             `json:"project"`
	Code    string             `json:"code"`
	Anchor  CompilerGoldAnchor `json:"anchor"`
}

// ByteSpan verifies a unique token spelling on the declared 1-based line and
// returns its UTF-8 byte span. This validates source anchors, not C# semantics.
func (a CompilerGoldAnchor) ByteSpan(content []byte) (offset, length int, err error) {
	lines := bytes.Split(content, []byte("\n"))
	if a.Line < 1 || a.Line > len(lines) || a.Text == "" || bytes.Count(lines[a.Line-1], []byte(a.Text)) != 1 {
		return 0, 0, fmt.Errorf("stale or ambiguous compiler anchor %s:%d %q", a.File, a.Line, a.Text)
	}
	for i := 0; i < a.Line-1; i++ {
		offset += len(lines[i]) + 1
	}
	return offset + bytes.Index(lines[a.Line-1], []byte(a.Text)), len([]byte(a.Text)), nil
}

func LoadCompilerGoldSuite(root fs.FS) (CompilerGoldSuite, error) {
	var suite CompilerGoldSuite
	data, err := fs.ReadFile(root, "gold.json")
	if err != nil {
		return suite, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&suite); err != nil {
		return suite, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return suite, fmt.Errorf("compiler gold must contain one JSON object")
	}
	if suite.Version != 1 || strings.TrimSpace(suite.LabelSource) == "" || suite.TargetFramework == "" || len(suite.Projects) == 0 || len(suite.References) == 0 {
		return suite, fmt.Errorf("compiler gold needs version 1, provenance, framework, projects and references")
	}
	projects := map[string]CompilerGoldProject{}
	sources := map[string]map[string]bool{}
	content := map[string][]byte{}
	for _, project := range suite.Projects {
		if project.ID == "" || projects[project.ID].ID != "" || project.AssemblyName == "" || !fs.ValidPath(project.ProjectFile) || len(project.Sources) == 0 {
			return suite, fmt.Errorf("invalid/duplicate compiler project %q", project.ID)
		}
		if project.ExpectedAnalysis != "complete" && project.ExpectedAnalysis != "incomplete" {
			return suite, fmt.Errorf("project %s has invalid analysis expectation", project.ID)
		}
		if _, err := fs.ReadFile(root, project.ProjectFile); err != nil {
			return suite, err
		}
		projects[project.ID] = project
		sources[project.ID] = map[string]bool{}
		for _, file := range project.Sources {
			if !fs.ValidPath(file) || sources[project.ID][file] {
				return suite, fmt.Errorf("invalid/duplicate source %q in %s", file, project.ID)
			}
			data, err := fs.ReadFile(root, file)
			if err != nil {
				return suite, err
			}
			content[file] = data
			sources[project.ID][file] = true
		}
	}
	checkAnchor := func(project string, a CompilerGoldAnchor) error {
		if !sources[project][a.File] {
			return fmt.Errorf("anchor %s is outside project %q", a.File, project)
		}
		_, _, err := a.ByteSpan(content[a.File])
		return err
	}
	ids := map[string]bool{}
	for _, ref := range suite.References {
		if ref.ID == "" || ids[ref.ID] || len(ref.Coverage) == 0 {
			return suite, fmt.Errorf("invalid/duplicate reference %q", ref.ID)
		}
		ids[ref.ID] = true
		if err := checkAnchor(ref.Project, ref.Source); err != nil {
			return suite, err
		}
		switch ref.Status {
		case "resolved":
			if ref.Target == nil || ref.ExternalSymbol != "" || len(ref.CandidateTargets) > 0 {
				return suite, fmt.Errorf("resolved reference %s needs one source target", ref.ID)
			}
		case "external":
			if ref.ExternalSymbol == "" || ref.Target != nil || len(ref.CandidateTargets) > 0 {
				return suite, fmt.Errorf("external reference %s needs only external symbol", ref.ID)
			}
		case "ambiguous":
			if len(ref.CandidateTargets) < 2 || ref.Target != nil || ref.ExternalSymbol != "" {
				return suite, fmt.Errorf("ambiguous reference %s needs candidate targets", ref.ID)
			}
		case "unresolved", "inactive":
			if ref.Target != nil || ref.ExternalSymbol != "" || len(ref.CandidateTargets) > 0 {
				return suite, fmt.Errorf("nonresolved reference %s cannot have target", ref.ID)
			}
		default:
			return suite, fmt.Errorf("unknown binding status %q", ref.Status)
		}
		targets := append([]CompilerGoldTarget{}, ref.CandidateTargets...)
		if ref.Target != nil {
			targets = append(targets, *ref.Target)
		}
		seen := map[CompilerGoldTarget]bool{}
		for _, target := range targets {
			if seen[target] {
				return suite, fmt.Errorf("duplicate candidate target in %s", ref.ID)
			}
			seen[target] = true
			if err := checkAnchor(target.Project, target.Anchor); err != nil {
				return suite, err
			}
		}
	}
	groups := map[string]bool{}
	for _, group := range suite.IdentityGroups {
		if group.ID == "" || groups[group.ID] || len(group.Declarations) < 2 || (group.Relation != "same_symbol" && group.Relation != "different_symbols") {
			return suite, fmt.Errorf("invalid identity group %q", group.ID)
		}
		groups[group.ID] = true
		seen := map[CompilerGoldTarget]bool{}
		for _, decl := range group.Declarations {
			if seen[decl] {
				return suite, fmt.Errorf("duplicate identity anchor in %s", group.ID)
			}
			seen[decl] = true
			if err := checkAnchor(decl.Project, decl.Anchor); err != nil {
				return suite, err
			}
		}
	}
	for _, difference := range suite.ContextDifferences {
		if len(difference.Projects) < 2 || difference.Reason == "" {
			return suite, fmt.Errorf("context difference needs projects and reason")
		}
		seen := map[string]bool{}
		for _, project := range difference.Projects {
			if seen[project] || !sources[project][difference.SharedSource] {
				return suite, fmt.Errorf("invalid shared source context %s/%s", project, difference.SharedSource)
			}
			seen[project] = true
		}
	}
	for _, file := range suite.GeneratedInputs {
		if _, ok := content[file]; !ok {
			return suite, fmt.Errorf("unknown generated source %s", file)
		}
	}
	for _, diag := range suite.ExpectedDiagnostics {
		if projects[diag.Project].ExpectedAnalysis != "incomplete" || !strings.HasPrefix(diag.Code, "CS") {
			return suite, fmt.Errorf("invalid diagnostic expectation %s", diag.Code)
		}
		if err := checkAnchor(diag.Project, diag.Anchor); err != nil {
			return suite, err
		}
	}
	return suite, nil
}
