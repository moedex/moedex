package eval_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	evaluation "moedex/internal/eval"
)

const compilerFixture = "testdata/semantic-intelligence/compiler-v1"

func TestCompilerGoldSourceAnchors(t *testing.T) {
	suite, err := evaluation.LoadCompilerGoldSuite(os.DirFS(compilerFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Projects) != 6 || len(suite.References) != 19 || len(suite.IdentityGroups) != 5 {
		t.Fatalf("unexpected fixture scope: %d projects, %d references, %d identity groups", len(suite.Projects), len(suite.References), len(suite.IdentityGroups))
	}
	coverage := map[string]bool{}
	for _, reference := range suite.References {
		for _, kind := range reference.Coverage {
			coverage[kind] = true
		}
	}
	for _, kind := range []string{"overload", "generic", "type_arity", "nested", "partial", "interface", "project_reference", "framework_reference", "conditional", "generated", "unresolved", "ambiguous", "linked_source", "project_context"} {
		if !coverage[kind] {
			t.Errorf("missing source-authored coverage %s", kind)
		}
	}
}

func TestCompilerGoldUTF8Anchor(t *testing.T) {
	anchor := evaluation.CompilerGoldAnchor{File: "example.cs", Line: 2, Text: "Pick"}
	offset, length, err := anchor.ByteSpan([]byte("first\nπ😀 Pick()\n"))
	if err != nil || offset != 13 || length != 4 {
		t.Fatalf("byte span=%d/%d err=%v; want UTF8 span 13/4", offset, length, err)
	}
	if _, _, err := anchor.ByteSpan([]byte("first\nPick(); Pick();\n")); err == nil {
		t.Fatal("ambiguous line token accepted")
	}
}

func compilerFixtureFS(t *testing.T) fstest.MapFS {
	t.Helper()
	out := fstest.MapFS{}
	root := os.DirFS(compilerFixture)
	if err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		out[path] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompilerGoldRejectsInvalidLabels(t *testing.T) {
	for name, mutate := range map[string]func(*evaluation.CompilerGoldSuite){
		"version":                 func(s *evaluation.CompilerGoldSuite) { s.Version = 2 },
		"duplicate-project":       func(s *evaluation.CompilerGoldSuite) { s.Projects[1].ID = s.Projects[0].ID },
		"stale-source":            func(s *evaluation.CompilerGoldSuite) { s.References[0].Source.Line = 9999 },
		"wrong-project-source":    func(s *evaluation.CompilerGoldSuite) { s.References[0].Project = "ContextA" },
		"wrong-project-target":    func(s *evaluation.CompilerGoldSuite) { s.References[0].Target.Project = "ContextA" },
		"status":                  func(s *evaluation.CompilerGoldSuite) { s.References[0].Status = "probably" },
		"missing-resolved-target": func(s *evaluation.CompilerGoldSuite) { s.References[0].Target = nil },
		"duplicate-reference":     func(s *evaluation.CompilerGoldSuite) { s.References[1].ID = s.References[0].ID },
		"duplicate-identity-anchor": func(s *evaluation.CompilerGoldSuite) {
			s.IdentityGroups[0].Declarations[1] = s.IdentityGroups[0].Declarations[0]
		},
		"unshared-context":             func(s *evaluation.CompilerGoldSuite) { s.ContextDifferences[0].Projects[1] = "Broken" },
		"unknown-generated-input":      func(s *evaluation.CompilerGoldSuite) { s.GeneratedInputs = []string{"missing.g.cs"} },
		"complete-with-required-error": func(s *evaluation.CompilerGoldSuite) { s.ExpectedDiagnostics[0].Project = "App" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := compilerFixtureFS(t)
			suite, err := evaluation.LoadCompilerGoldSuite(fixture)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&suite)
			data, err := json.Marshal(suite)
			if err != nil {
				t.Fatal(err)
			}
			fixture["gold.json"] = &fstest.MapFile{Data: data}
			if _, err := evaluation.LoadCompilerGoldSuite(fixture); err == nil {
				t.Fatal("invalid compiler gold accepted")
			}
		})
	}
}
