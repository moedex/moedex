package indexcmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
	server "moedex/internal/serve"
	indexsnapshot "moedex/internal/snapshot"
)

const semanticFixtureSource = "\xef\xbb\xbf// π😀\nclass FixtureTarget {}\n"

func semanticSnapshotFixture(t *testing.T) (corpus, repo, indexDir string) {
	t.Helper()
	corpus = t.TempDir()
	repo = initRepo(t, filepath.Join(corpus, "repo"), map[string]string{
		"App.csproj":            "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>\n",
		"Main.cs":               semanticFixtureSource,
		"Directory.Build.props": "<Project><PropertyGroup><DefineConstants>ORIGINAL</DefineConstants></PropertyGroup></Project>\n",
	})
	return corpus, repo, filepath.Join(t.TempDir(), "index")
}

func semanticSnapshotSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Authored worker-shaped records establish attachment invariants. Actual compiler
// extraction is tested separately by internal/eval's MSBuild integration gate.
func semanticSnapshotArtifact(t *testing.T, root, repo, commit, sourcePath string, extraInputs ...string) string {
	t.Helper()
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	source := read(sourcePath)
	generatedPath := ".generated/fixture/Generated.g.cs"
	generated := []byte("class GeneratedTarget {}\n")
	sources := []map[string]any{
		{"path": sourcePath, "sha256": semanticSnapshotSHA(source), "byte_size": len(source), "generated": false},
		{"path": generatedPath, "sha256": semanticSnapshotSHA(generated), "byte_size": len(generated), "generated": true},
	}
	fileInput := func(path string) map[string]any {
		data := read(path)
		return map[string]any{"path": path, "scope": "source", "sha256": semanticSnapshotSHA(data), "byte_size": len(data)}
	}
	imports := []any{fileInput("Directory.Build.props")}
	for _, path := range extraInputs {
		imports = append(imports, fileInput(path))
	}
	capture, err := json.Marshal(map[string]any{
		"project": "App.csproj", "repo": repo, "extractor": "msbuild-roslyn", "extractor_version": "1", "compiler_version": "5.0.0.0",
		"project_file": fileInput("App.csproj"), "imports": imports, "sources": sources, "project_references": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	buildContext := semanticSnapshotSHA(capture)
	var wire bytes.Buffer
	emit := func(row map[string]any) {
		row["schema"] = semanticimport.WorkerSchema
		if err := json.NewEncoder(&wire).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	base := func(kind string) map[string]any {
		return map[string]any{"record_type": kind, "repo": repo, "project": "App.csproj", "build_context": buildContext,
			"extractor": "msbuild-roslyn", "extractor_version": "1", "compiler_version": "5.0.0.0", "compilation_status": "complete"}
	}
	// Embedding belongs only to the transport roster, not the capture digest.
	transportSources := []map[string]any{sources[0], {"path": generatedPath, "sha256": semanticSnapshotSHA(generated), "byte_size": len(generated), "generated": true, "content_base64": base64.StdEncoding.EncodeToString(generated)}}
	header := base("project")
	header["capture_json"], header["sources"] = string(capture), transportSources
	emit(header)
	for _, item := range []struct {
		path, name string
		content    []byte
	}{{sourcePath, "FixtureTarget", source}, {generatedPath, "GeneratedTarget", generated}} {
		row := base("declaration")
		row["source_path"], row["source_sha256"], row["source_text"] = item.path, semanticSnapshotSHA(item.content), item.name
		row["span"] = map[string]int{"byte_offset": bytes.Index(item.content, []byte(item.name)), "byte_length": len(item.name)}
		row["binding_status"], row["binding_method"], row["reference_kind"] = "resolved", "roslyn-semantic-model", "declaration"
		row["symbol"] = map[string]any{"language": "csharp", "namespace_kind": "project", "namespace": repo + "/App.csproj", "descriptor": "T:" + item.name, "descriptor_kind": "documentation_comment_id"}
		emit(row)
	}
	summary := base("summary")
	summary["declarations"], summary["references"], summary["errors"] = 2, 0, 0
	emit(summary)
	emit(map[string]any{"record_type": "stream_summary", "projects": 1, "declarations": 2, "references": 0, "compilation_status": "complete"})
	artifact, err := semanticimport.Import(context.Background(), &wire, semanticimport.Options{Repo: repo, Root: root, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "input.artifact")
	if err := semantic.Write(output, artifact); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestSnapshotBuildRetainedSemanticWorkspace(t *testing.T) {
	corpus, repo, indexDir := semanticSnapshotFixture(t)
	workspace := t.TempDir()
	for _, name := range []string{"App.csproj", "Main.cs", "Directory.Build.props"} {
		b, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(workspace, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(workspace, "obj"), 0700); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(workspace, "obj", "project.assets.json")
	if err := os.WriteFile(assets, []byte("derived restore inputs"), 0600); err != nil {
		t.Fatal(err)
	}
	input := semanticSnapshotArtifact(t, workspace, "repo", "", "Main.cs", "obj/project.assets.json")
	args := []string{"-corpus", corpus, "-index-dir", indexDir, "-id", "projection", "-semantic-artifact", input}
	if err := runSnapshotBuild(args); err == nil {
		t.Fatal("missing derived input accepted without projection")
	}
	if err := runSnapshotBuild(append(args, "-semantic-workspace", workspace)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assets, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	args[5] = "mutated"
	if err := runSnapshotBuild(append(args, "-semantic-workspace", workspace)); err == nil {
		t.Fatal("mutated projection accepted")
	}
	m, _, err := indexsnapshot.Current(indexDir)
	if err != nil || m.ID != "projection" {
		t.Fatalf("failed attachment changed CURRENT: %+v %v", m, err)
	}
}

func assertSemanticSnapshotLexical(t *testing.T, root string, m *indexsnapshot.Manifest) {
	t.Helper()
	c, err := server.Open(filepath.Join(root, filepath.FromSlash(m.ServingRoot)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	matches, _, err := c.Literal(context.Background(), "FixtureTarget")
	if err != nil || len(matches) == 0 {
		t.Fatalf("lexical source unavailable: matches=%d err=%v", len(matches), err)
	}
}

func TestSnapshotBuildSemanticWithoutHeuristicGraph(t *testing.T) {
	corpus, repo, indexDir := semanticSnapshotFixture(t)
	input := semanticSnapshotArtifact(t, repo, "repo", "", "Main.cs")
	if err := runSnapshotBuild([]string{"--corpus", corpus, "--index-dir", indexDir, "--id", "compiler-only", "--graph=false", "--semantic-artifact", input}); err != nil {
		t.Fatal(err)
	}
	m, root, err := indexsnapshot.Current(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range m.Capabilities {
		if strings.Contains(capability, "graph") {
			t.Fatalf("advertises absent graph: %s", capability)
		}
	}
	for _, component := range m.Components {
		for _, artifact := range component.Artifacts {
			if strings.Contains(filepath.Base(artifact.Path), "graph") || strings.Contains(filepath.Base(artifact.Path), "cluster") {
				t.Fatalf("graph artifact created: %s", artifact.Path)
			}
		}
	}
	for _, required := range []string{"corpus-tokens.tki", "corpus-symbols.sym"} {
		if _, err := os.Stat(filepath.Join(root, m.ServingRoot, required)); err != nil {
			t.Fatal(err)
		}
	}
	audit, ok := m.Components["semantic"]
	if !ok || len(audit.Artifacts) != 1 {
		t.Fatal("compiler audit missing")
	}
	if _, ok := m.Components[indexsnapshot.SemanticIndexComponent]; !ok {
		t.Fatal("compiler compact index missing")
	}
	reader, err := semanticindex.Open(filepath.Join(root, indexsnapshot.SemanticIndexPath), semanticindex.Expected{ArtifactSHA256: audit.Artifacts[0].SHA256, CorpusFingerprint: m.CorpusFingerprint}, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result, err := reader.Bindings(context.Background(), semanticindex.Position{Repo: "repo", Path: "Main.cs", Offset: uint64(bytes.Index([]byte(semanticFixtureSource), []byte("FixtureTarget")))}, 10)
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("compiler lookup unavailable: %+v %v", result, err)
	}
	assertSemanticSnapshotLexical(t, root, m)
}

func TestSnapshotBuildSemanticAttachmentAndExplicitDrop(t *testing.T) {
	corpus, repo, indexDir := semanticSnapshotFixture(t)
	input := semanticSnapshotArtifact(t, repo, "repo", "", "Main.cs")
	if err := runSnapshotBuild([]string{"-corpus", corpus, "-index-dir", indexDir, "-id", "with-semantic", "-semantic-artifact", input}); err != nil {
		t.Fatal(err)
	}
	m, root, err := indexsnapshot.Current(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	component, exists := m.Components["semantic"]
	if !exists || component.InputFingerprint != m.CorpusFingerprint || component.Metadata["coverage"] != "explicit-project-subset" || component.Metadata["usage"] != "offline-only" {
		t.Fatalf("semantic attachment metadata: %+v", component)
	}
	if len(component.Artifacts) != 1 || component.Artifacts[0].Path != "semantic/artifact.json" {
		t.Fatalf("semantic artifact ownership: %+v", component.Artifacts)
	}
	compact, exists := m.Components[indexsnapshot.SemanticIndexComponent]
	if !exists || compact.Metadata["source_artifact_sha256"] != component.Artifacts[0].SHA256 || compact.InputFingerprint != m.CorpusFingerprint {
		t.Fatalf("compact index is not bound to audit/corpus: %+v", compact)
	}
	reader, err := semanticindex.Open(filepath.Join(root, indexsnapshot.SemanticIndexPath), semanticindex.Expected{ArtifactSHA256: component.Artifacts[0].SHA256, CorpusFingerprint: m.CorpusFingerprint}, semanticindex.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	attachedPath := filepath.Join(root, "semantic", "artifact.json")
	attached, err := semantic.Read(attachedPath, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	foundRaw, foundGenerated := false, false
	for _, source := range attached.Sources {
		if source.Path == "Main.cs" {
			foundRaw = source.RawSHA256 == semanticSnapshotSHA([]byte(semanticFixtureSource))
		}
		if strings.HasPrefix(source.Path, ".generated/") {
			foundGenerated = source.Generated && len(source.Content) != 0
		}
	}
	if !foundRaw || !foundGenerated {
		t.Fatalf("raw BOM/generated evidence lost: raw=%v generated=%v", foundRaw, foundGenerated)
	}
	if err := indexsnapshot.ValidateFiles(root, m); err != nil {
		t.Fatal(err)
	}
	assertSemanticSnapshotLexical(t, root, m)
	// The generation must own its copy, regardless of later importer-file mutation.
	if err := os.WriteFile(input, []byte("replaced external staging input"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := semantic.Read(attachedPath, semantic.Limits{}); err != nil {
		t.Fatalf("snapshot aliases external input: %v", err)
	}
	if err := indexsnapshot.ValidateFiles(root, m); err != nil {
		t.Fatal(err)
	}
	if err := runSnapshotBuild([]string{"-corpus", corpus, "-index-dir", indexDir, "-id", "lexical-followup"}); err != nil {
		t.Fatal(err)
	}
	next, nextRoot, err := indexsnapshot.Current(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := next.Components["semantic"]; exists {
		t.Fatal("new generation inherited semantic artifact without opt-in")
	}
	if _, exists := next.Components[indexsnapshot.SemanticIndexComponent]; exists {
		t.Fatal("new generation inherited compact semantic index")
	}
	if _, err := os.Stat(filepath.Join(nextRoot, "semantic", "artifact.json")); !os.IsNotExist(err) {
		t.Fatalf("semantic bytes carried forward: %v", err)
	}
	assertSemanticSnapshotLexical(t, nextRoot, next)
	if _, err := semantic.Read(attachedPath, semantic.Limits{}); err != nil {
		t.Fatalf("followup damaged prior immutable artifact: %v", err)
	}
}

func TestSnapshotBuildRejectsSemanticMismatchPreservingCurrent(t *testing.T) {
	for _, scenario := range []string{"source", "configuration", "repository", "commit", "missing_occurrence", "privacy", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			corpus, repo, indexDir := semanticSnapshotFixture(t)
			if err := runSnapshotBuild([]string{"-corpus", corpus, "-index-dir", indexDir, "-id", "prior"}); err != nil {
				t.Fatal(err)
			}
			label, commit, path := "repo", "", "Main.cs"
			if scenario == "repository" {
				label = "different-repo"
			}
			if scenario == "commit" {
				commit = strings.Repeat("a", 40)
			}
			if scenario == "missing_occurrence" {
				path = "Untracked.cs"
				writeFile(t, filepath.Join(repo, path), semanticFixtureSource)
			}
			input := semanticSnapshotArtifact(t, repo, label, commit, path)
			switch scenario {
			case "source":
				writeFile(t, filepath.Join(repo, "Main.cs"), semanticFixtureSource+"// edited after capture\n")
			case "configuration":
				writeFile(t, filepath.Join(repo, "Directory.Build.props"), "<Project><PropertyGroup><DefineConstants>CHANGED</DefineConstants></PropertyGroup></Project>\n")
			case "privacy":
				writeFile(t, filepath.Join(repo, ".ai-privacy.yml"), "global_privacy_level: 3\nprivacy_levels:\n  - path: /restricted/\n    privacy_level: 1\n")
			case "corrupt":
				if err := os.WriteFile(input, []byte("invalid semantic artifact"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := runSnapshotBuild([]string{"-corpus", corpus, "-index-dir", indexDir, "-id", "rejected", "-semantic-artifact", input}); err == nil {
				t.Fatal("mismatched semantic attachment published")
			}
			m, root, err := indexsnapshot.Current(indexDir)
			if err != nil || m.ID != "prior" {
				t.Fatalf("failed attachment changed CURRENT: %+v err=%v", m, err)
			}
			if _, err := os.Stat(indexsnapshot.SnapshotPath(indexDir, "rejected")); !os.IsNotExist(err) {
				t.Fatalf("failed generation became visible: %v", err)
			}
			if err := indexsnapshot.ValidateFiles(root, m); err != nil {
				t.Fatal(err)
			}
			assertSemanticSnapshotLexical(t, root, m)
		})
	}
}
