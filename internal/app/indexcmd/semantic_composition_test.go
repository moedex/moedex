package indexcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/app/semanticcmd"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	indexsnapshot "moedex/internal/snapshot"
)

func TestComposedSemanticMultiRepoPublication(t *testing.T) {
	corpus, indexDir := t.TempDir(), filepath.Join(t.TempDir(), "index")
	inputs := []string{}
	workspaces := map[string]string{}
	for _, name := range []string{"repo-one", "repo-two"} {
		sourceRoot := initRepo(t, filepath.Join(corpus, name), map[string]string{"App.csproj": "<Project />\n", "Main.cs": semanticFixtureSource, "Directory.Build.props": "<Project />\n"})
		projection := t.TempDir()
		for _, file := range []string{"App.csproj", "Main.cs", "Directory.Build.props"} {
			b, err := os.ReadFile(filepath.Join(sourceRoot, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projection, file), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		input := semanticSnapshotArtifact(t, projection, name, "", "Main.cs")
		inputs = append(inputs, input)
		a, err := semantic.Read(input, semantic.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		workspaces[a.Snapshots[0].ID] = projection
	}
	out := filepath.Join(t.TempDir(), "composed.semantic")
	if _, err := semanticcmd.ComposeFiles(context.Background(), semanticcmd.ComposeOptions{Inputs: inputs, Output: out}); err != nil {
		t.Fatal(err)
	}
	mapping := filepath.Join(t.TempDir(), "roots.json")
	b, _ := json.Marshal(workspaces)
	if err := os.WriteFile(mapping, b, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--corpus", corpus, "--index-dir", indexDir, "--id", "composed", "--graph=false", "--semantic-artifact", out, "--semantic-workspaces", mapping}
	if err := runSnapshotBuild(args); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := indexsnapshot.Current(indexDir)
	if err != nil || manifest.ID != "composed" {
		t.Fatal("composition did not publish")
	}
	// A mapping omission or a drifted retained input must not replace CURRENT.
	one := map[string]string{}
	for id, path := range workspaces {
		one[id] = path
		break
	}
	b, _ = json.Marshal(one)
	if err := os.WriteFile(mapping, b, 0600); err != nil {
		t.Fatal(err)
	}
	args[5] = "missing"
	if err := runSnapshotBuild(args); err == nil {
		t.Fatal("missing root admitted")
	}
	b, _ = json.Marshal(workspaces)
	if err := os.WriteFile(mapping, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range workspaces {
		if err := os.WriteFile(filepath.Join(path, "Main.cs"), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		break
	}
	args[5] = "drifted"
	if err := runSnapshotBuild(args); err == nil {
		t.Fatal("stale retained source admitted")
	}
	manifest, _, err = indexsnapshot.Current(indexDir)
	if err != nil || manifest.ID != "composed" {
		t.Fatal("failed composition attachment replaced CURRENT")
	}
}

// This optional gate publishes real Debug/Release captures using production
// composition and exhaustive retained-workspace mappings. The output directory
// stays available for servecmd's real leased-MCP test in a separate package.
func TestPublicComposedContractPublication(t *testing.T) {
	captureBase, outBase := os.Getenv("MOEDEX_CONTRACT_CAPTURE_DIR"), os.Getenv("MOEDEX_COMPOSITION_OUTPUT")
	if captureBase == "" || outBase == "" {
		t.Skip("set capture directory and fresh composition output")
	}
	if err := os.Mkdir(outBase, 0700); err != nil {
		t.Fatal(err)
	}
	corpus := filepath.Join(outBase, "corpus")
	if err := os.Mkdir(corpus, 0700); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(captureBase, "fixture")
	files := map[string]string{}
	if err := filepath.WalkDir(sourceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "obj" || d.Name() == "bin" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	initRepo(t, filepath.Join(corpus, "contract-gold"), files)
	inputs := []string{}
	roots := map[string]string{}
	contexts := map[string]string{}
	symbolID := ""
	for _, config := range []string{"Debug", "Release"} {
		retained := filepath.Join(outBase, "retained-"+config)
		if err := os.CopyFS(retained, os.DirFS(sourceRoot)); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(captureBase, config+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		a, err := semanticimport.Import(context.Background(), bytes.NewReader(raw), semanticimport.Options{Repo: "contract-gold", Root: retained})
		if err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range a.Snapshots {
			roots[snapshot.ID] = retained
		}
		for _, c := range a.Contexts {
			contexts[config+":"+c.Project] = c.ID
		}
		for _, s := range a.Symbols {
			if s.Key.Namespace == "contract-gold/Contracts/Contracts.csproj" && s.Key.Descriptor == "T:Shared.Notice" {
				symbolID = s.ID
			}
		}
		input := filepath.Join(outBase, config+".semantic")
		if err := semantic.Write(input, a); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input)
	}
	output := filepath.Join(outBase, "composed.semantic")
	summary, err := semanticcmd.ComposeFiles(context.Background(), semanticcmd.ComposeOptions{Inputs: inputs, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("production composition %+v", summary)
	mapping := filepath.Join(outBase, "roots.json")
	raw, _ := json.Marshal(roots)
	if err := os.WriteFile(mapping, raw, 0600); err != nil {
		t.Fatal(err)
	}
	indexDir := filepath.Join(outBase, "index")
	if err := runSnapshotBuild([]string{"--corpus", corpus, "--index-dir", indexDir, "--id", "composed-real", "--graph=false", "--semantic-artifact", output, "--semantic-workspaces", mapping}); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := indexsnapshot.Current(indexDir)
	if err != nil || manifest.ID != "composed-real" {
		t.Fatal("real composed CURRENT not published")
	}
	if symbolID == "" {
		t.Fatal("missing contract symbol")
	}
	expected := map[string]any{"symbol_id": symbolID, "contexts": contexts, "snapshot_id": manifest.ID, "corpus_fingerprint": manifest.CorpusFingerprint}
	raw, _ = json.Marshal(expected)
	if err := os.WriteFile(filepath.Join(outBase, "expected.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("published actual composed capture at %s with %d alternatives", indexDir, len(contexts))
}
