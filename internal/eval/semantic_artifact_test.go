package eval

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

	"moedex/internal/app/semanticcmd"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
)

// These authored wire records test import/storage contracts, not compiler recall.
// The separate environment-gated test consumes actual MSBuild worker output.
func semanticArtifactWire(t *testing.T, bom bool) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	shared := []byte("// π😀\nclass Caller { string Run() => Target.Resolve(); }\n")
	if bom {
		shared = append([]byte{0xef, 0xbb, 0xbf}, shared...)
	}
	write := func(path string, data []byte) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Shared/LinkedCall.cs", shared)
	var out bytes.Buffer
	emit := func(record map[string]any) {
		record["schema"] = semanticimport.WorkerSchema
		if err := json.NewEncoder(&out).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	for _, project := range []string{"ContextA", "ContextB"} {
		projectPath := project + "/" + project + ".csproj"
		write(projectPath, []byte("<Project Sdk=\"Microsoft.NET.Sdk\" />"))
		targetPath := project + "/Target.cs"
		target := []byte("class Target { public static string Resolve() => \"" + project + "\"; }\n")
		write(targetPath, target)
		sources := []map[string]any{
			{"path": "Shared/LinkedCall.cs", "sha256": semanticTestSHA(shared), "byte_size": len(shared)},
			{"path": targetPath, "sha256": semanticTestSHA(target), "byte_size": len(target)},
		}
		capture, err := json.Marshal(map[string]any{"project": projectPath, "sources": sources})
		if err != nil {
			t.Fatal(err)
		}
		buildContext := semanticTestSHA(capture)
		base := func(kind string) map[string]any {
			return map[string]any{"record_type": kind, "repo": "fixture", "project": projectPath,
				"build_context": buildContext, "extractor": "authored-wire-test", "extractor_version": "1",
				"compiler_version": "test", "compilation_status": "complete"}
		}
		header := base("project")
		header["capture_json"], header["sources"] = string(capture), sources
		emit(header)
		symbol := map[string]any{"language": "csharp", "namespace_kind": "project", "namespace": "fixture/" + projectPath,
			"descriptor": "M:Target.Resolve", "descriptor_kind": "documentation_comment_id"}
		for _, occurrence := range []struct {
			kind, path string
			content    []byte
		}{
			{"declaration", targetPath, target}, {"reference", "Shared/LinkedCall.cs", shared},
		} {
			record := base(occurrence.kind)
			record["source_path"], record["source_sha256"] = occurrence.path, semanticTestSHA(occurrence.content)
			record["span"] = map[string]int{"byte_offset": bytes.Index(occurrence.content, []byte("Resolve")), "byte_length": len("Resolve")}
			record["source_text"], record["binding_status"] = "Resolve", "resolved"
			record["reference_kind"], record["binding_method"], record["symbol"] = "invocation", "compiler", symbol
			if occurrence.kind == "declaration" {
				record["reference_kind"] = "declaration"
			}
			emit(record)
		}
		summary := base("summary")
		summary["declarations"], summary["references"], summary["errors"] = 1, 1, 0
		emit(summary)
	}
	emit(map[string]any{"record_type": "stream_summary", "projects": 2, "declarations": 2, "references": 2, "compilation_status": "complete"})
	return root, out.Bytes()
}

func semanticTestSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestSemanticArtifactSharedBytesRetainDifferentBindings(t *testing.T) {
	root, wire := semanticArtifactWire(t, false)
	artifact, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Complete() {
		t.Fatal("complete input imported as incomplete")
	}
	path := filepath.Join(t.TempDir(), "semantic.artifact")
	if err := semantic.Write(path, artifact); err != nil {
		t.Fatal(err)
	}
	loaded, err := semantic.Read(path, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticSharedBindings(t, loaded, root, "Shared/LinkedCall.cs")
	for _, snapshot := range loaded.Snapshots {
		if snapshot.Repo != "fixture" || snapshot.Commit != "" || snapshot.ProjectID != "" {
			t.Fatalf("legacy source provenance invented: %+v", snapshot)
		}
	}
}

func assertSemanticSharedBindings(t *testing.T, artifact *semantic.Artifact, root, sharedPath string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sharedPath)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte("Resolve")) != 1 {
		t.Fatal("shared source requires one Resolve anchor")
	}
	anchor := uint64(bytes.Index(raw, []byte("Resolve")))
	sources := map[string]semantic.Source{}
	for _, source := range artifact.Sources {
		sources[source.ID] = source
	}
	bindings := map[string]semantic.Binding{}
	for _, binding := range artifact.Bindings {
		bindings[binding.OccurrenceID] = binding
	}
	targets := map[string]string{}
	for _, occurrence := range artifact.Occurrences {
		source := sources[occurrence.SourceID]
		if occurrence.Role != "declaration" || !strings.HasSuffix(source.Path, "/Target.cs") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(source.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if occurrence.Offset+occurrence.Length > uint64(len(content)) {
			t.Fatal("declaration outside source")
		}
		if string(content[occurrence.Offset:occurrence.Offset+occurrence.Length]) == "Resolve" {
			targets[occurrence.ContextID] = bindings[occurrence.ID].SymbolID
		}
	}
	contexts, symbols, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, occurrence := range artifact.Occurrences {
		source := sources[occurrence.SourceID]
		if source.Path != sharedPath || occurrence.Role != "reference" || occurrence.Offset != anchor || occurrence.Length != uint64(len("Resolve")) {
			continue
		}
		binding := bindings[occurrence.ID]
		if binding.Status != "resolved" || binding.SymbolID == "" {
			t.Fatalf("missing shared binding: %+v", binding)
		}
		if want := targets[occurrence.ContextID]; want == "" || binding.SymbolID != want {
			t.Fatalf("shared call bound outside its project context: got %q want %q", binding.SymbolID, want)
		}
		contexts[occurrence.ContextID], symbols[binding.SymbolID], hashes[source.RawSHA256] = true, true, true
	}
	if len(contexts) != 2 || len(symbols) != 2 || len(hashes) != 1 {
		t.Fatalf("shared source contexts=%d targets=%d raw hashes=%d; want 2,2,1", len(contexts), len(symbols), len(hashes))
	}
}

func TestSemanticArtifactRawBOMProvenance(t *testing.T) {
	root, wire := semanticArtifactWire(t, true)
	artifact, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "Shared", "LinkedCall.cs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range artifact.Sources {
		if source.Path != "Shared/LinkedCall.cs" {
			continue
		}
		if source.RawSHA256 != semanticTestSHA(raw) || source.RawSHA256 == semanticTestSHA(raw[3:]) {
			t.Fatal("raw source SHA confused with BOM-normalized content")
		}
		for _, occurrence := range artifact.Occurrences {
			if occurrence.SourceID == source.ID && string(raw[occurrence.Offset:occurrence.Offset+occurrence.Length]) != "Resolve" {
				t.Fatal("compiler evidence lost exact raw-byte coordinates")
			}
		}
	}
	// Normalizing disk bytes while retaining the old worker hash must fail closed.
	if err := os.WriteFile(filepath.Join(root, "Shared", "LinkedCall.cs"), raw[3:], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root}); err == nil {
		t.Fatal("accepted normalized bytes against raw compiler evidence")
	}
}

func TestSemanticArtifactImportedCorruptionRejected(t *testing.T) {
	root, wire := semanticArtifactWire(t, false)
	artifact, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "semantic.artifact")
	if err := semantic.Write(path, artifact); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 16 {
		t.Fatal("unexpected tiny artifact")
	}
	flipped := append([]byte(nil), data...)
	flipped[len(flipped)/2] ^= 1
	for name, invalid := range map[string][]byte{"truncated": data[:len(data)-1], "bit_flip": flipped} {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "bad.artifact")
			if err := os.WriteFile(bad, invalid, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := semantic.Read(bad, semantic.Limits{}); err == nil {
				t.Fatal("corrupted imported artifact accepted")
			}
		})
	}
}

func TestSemanticArtifactConfigurationChangeSeparatesContext(t *testing.T) {
	root, wire := semanticArtifactWire(t, false)
	before, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var changed bytes.Buffer
	var fingerprint string
	for _, line := range bytes.Split(bytes.TrimSpace(wire), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["project"] == "ContextA/ContextA.csproj" {
			if record["record_type"] == "project" {
				var capture map[string]any
				if err := json.Unmarshal([]byte(record["capture_json"].(string)), &capture); err != nil {
					t.Fatal(err)
				}
				capture["defines"] = []string{"FAST"}
				data, err := json.Marshal(capture)
				if err != nil {
					t.Fatal(err)
				}
				fingerprint = semanticTestSHA(data)
				record["capture_json"] = string(data)
			}
			record["build_context"] = fingerprint
		}
		if err := json.NewEncoder(&changed).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	after, err := semanticimport.Import(context.Background(), &changed, semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	find := func(a *semantic.Artifact) semantic.BuildContext {
		for _, c := range a.Contexts {
			if c.Project == "ContextA/ContextA.csproj" {
				return c
			}
		}
		t.Fatal("missing context")
		return semantic.BuildContext{}
	}
	left, right := find(before), find(after)
	if left.ID == right.ID || left.InputFingerprint == right.InputFingerprint {
		t.Fatal("configuration-only change reused context identity")
	}
	for _, source := range before.Sources {
		found := false
		for _, other := range after.Sources {
			if source.Path == other.Path && source.RawSHA256 == other.RawSHA256 {
				found = true
			}
		}
		if !found {
			t.Fatalf("configuration test unexpectedly changed source %s", source.Path)
		}
	}
}

func TestSemanticArtifactActualMSBuildWorker(t *testing.T) {
	input, root := os.Getenv("MOEDEX_SEMANTIC_WORKER_OUTPUT"), os.Getenv("MOEDEX_SEMANTIC_WORKER_ROOT")
	if input == "" && root == "" {
		t.Skip("set MOEDEX_SEMANTIC_WORKER_OUTPUT and MOEDEX_SEMANTIC_WORKER_ROOT for actual compiler integration")
	}
	if input == "" || root == "" {
		t.Fatal("both worker output and root are required")
	}
	file, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	artifact, err := semanticimport.Import(context.Background(), file, semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Complete() {
		t.Fatal("actual shared-context fixture must produce a complete compiler artifact")
	}
	path := filepath.Join(t.TempDir(), "semantic.artifact")
	if err := semantic.Write(path, artifact); err != nil {
		t.Fatal(err)
	}
	loaded, err := semantic.Read(path, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticSharedBindings(t, loaded, root, "Shared/LinkedCall.cs")
}

func TestSemanticArtifactActualGeneratedSource(t *testing.T) {
	input, root := os.Getenv("MOEDEX_SEMANTIC_GENERATED_OUTPUT"), os.Getenv("MOEDEX_SEMANTIC_WORKER_ROOT")
	if input == "" {
		t.Skip("set MOEDEX_SEMANTIC_GENERATED_OUTPUT for actual source-generator integration")
	}
	if root == "" {
		t.Fatal("MOEDEX_SEMANTIC_WORKER_ROOT is required")
	}
	wire, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	// Read raw producer bytes and spellings independently of the importer so the
	// round-trip cannot pass by merely agreeing with its own serialization.
	generated := map[string][]byte{}
	type evidenceKey struct {
		path           string
		offset, length uint64
	}
	evidence := map[evidenceKey]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(wire), []byte("\n")) {
		var row struct {
			Type    string `json:"record_type"`
			Sources []struct {
				Path      string `json:"path"`
				Generated bool   `json:"generated"`
				Content   string `json:"content_base64"`
			} `json:"sources"`
			Path string `json:"source_path"`
			Text string `json:"source_text"`
			Span struct {
				Offset uint64 `json:"byte_offset"`
				Length uint64 `json:"byte_length"`
			} `json:"span"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		for _, source := range row.Sources {
			if !source.Generated {
				continue
			}
			content, err := base64.StdEncoding.DecodeString(source.Content)
			if err != nil {
				t.Fatal(err)
			}
			generated[source.Path] = content
		}
		if row.Type == "declaration" || row.Type == "reference" {
			key := evidenceKey{row.Path, row.Span.Offset, row.Span.Length}
			if old, ok := evidence[key]; ok && old != row.Text {
				t.Fatal("contradictory raw occurrence spellings")
			}
			evidence[key] = row.Text
		}
	}
	artifact, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Complete() {
		t.Fatal("successful generator fixture imported as incomplete")
	}
	path := filepath.Join(t.TempDir(), "generated.artifact")
	if err := semantic.Write(path, artifact); err != nil {
		t.Fatal(err)
	}
	loaded, err := semantic.Read(path, semantic.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]semantic.Source{}
	virtualSources, virtualOccurrences := 0, 0
	for _, source := range loaded.Sources {
		sources[source.ID] = source
		if !source.Generated {
			continue
		}
		want, exists := generated[source.Path]
		if !exists || !bytes.Equal(source.Content, want) || source.RawSHA256 != semanticTestSHA(want) || source.ByteSize != uint64(len(want)) {
			t.Fatalf("generated bytes/provenance changed for %s", source.Path)
		}
		if strings.HasPrefix(source.Path, ".generated/") {
			virtualSources++
		}
	}
	for _, occurrence := range loaded.Occurrences {
		source := sources[occurrence.SourceID]
		if !source.Generated {
			continue
		}
		key := evidenceKey{source.Path, occurrence.Offset, occurrence.Length}
		want, exists := evidence[key]
		if !exists || occurrence.Offset > uint64(len(source.Content)) || occurrence.Length > uint64(len(source.Content))-occurrence.Offset {
			t.Fatalf("missing/out-of-bounds generated evidence: %+v", occurrence)
		}
		if string(source.Content[occurrence.Offset:occurrence.Offset+occurrence.Length]) != want {
			t.Fatalf("generated occurrence spelling changed for %s", source.Path)
		}
		if strings.HasPrefix(source.Path, ".generated/") {
			virtualOccurrences++
		}
	}
	if virtualSources == 0 || virtualOccurrences == 0 {
		t.Fatalf("actual source generator absent: virtual sources=%d occurrences=%d", virtualSources, virtualOccurrences)
	}
}

func TestSemanticArtifactActualGeneratorFailureRefusesStage(t *testing.T) {
	input, root := os.Getenv("MOEDEX_SEMANTIC_THROWING_OUTPUT"), os.Getenv("MOEDEX_SEMANTIC_WORKER_ROOT")
	if input == "" {
		t.Skip("set MOEDEX_SEMANTIC_THROWING_OUTPUT for actual generator-failure integration")
	}
	if root == "" {
		t.Fatal("MOEDEX_SEMANTIC_WORKER_ROOT is required")
	}
	wire, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := semanticimport.Import(context.Background(), bytes.NewReader(wire), semanticimport.Options{Repo: "fixture", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Complete() {
		t.Fatal("throwing generator capture marked complete")
	}
	foundFailure := false
	for _, c := range artifact.Contexts {
		for _, issue := range c.Issues {
			if issue == "generator_failure" {
				foundFailure = true
			}
		}
	}
	if !foundFailure {
		t.Fatal("generator failure provenance missing")
	}
	output := filepath.Join(t.TempDir(), "must-not-exist.artifact")
	_, err = semanticcmd.ImportFile(context.Background(), semanticcmd.ImportOptions{Input: input, Output: output, Options: semanticimport.Options{Repo: "fixture", Root: root}})
	if err == nil {
		t.Fatal("application staged failed-generator capture")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed generator created an output artifact: %v", err)
	}
}

func semanticWireRecords(t *testing.T, wire []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(wire), []byte("\n")) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func encodeSemanticWire(t *testing.T, rows []map[string]any) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, row := range rows {
		if err := json.NewEncoder(&wire).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	return wire.Bytes()
}

func TestSemanticArtifactRejectsInconsistentProvenance(t *testing.T) {
	root, wire := semanticArtifactWire(t, false)
	for _, field := range []string{"repo", "extractor", "extractor_version", "compiler_version", "compilation_status"} {
		t.Run("fact_"+field, func(t *testing.T) {
			rows := semanticWireRecords(t, wire)
			for _, row := range rows {
				if row["record_type"] == "reference" {
					row[field] = "inconsistent"
					break
				}
			}
			if _, err := semanticimport.Import(context.Background(), bytes.NewReader(encodeSemanticWire(t, rows)), semanticimport.Options{Repo: "fixture", Root: root}); err == nil {
				t.Fatal("accepted contradictory fact provenance")
			}
		})
	}
	t.Run("diagnostic_context", func(t *testing.T) {
		rows := semanticWireRecords(t, wire)
		diagnostic := map[string]any{"schema": semanticimport.WorkerSchema, "record_type": "diagnostic", "project": "ContextA/ContextA.csproj", "build_context": semanticTestSHA([]byte("different context")), "code": "CS0001", "severity": "warning"}
		rows = append(rows[:len(rows)-1], diagnostic, rows[len(rows)-1])
		if _, err := semanticimport.Import(context.Background(), bytes.NewReader(encodeSemanticWire(t, rows)), semanticimport.Options{Repo: "fixture", Root: root}); err == nil {
			t.Fatal("accepted diagnostic from another build context")
		}
	})
	t.Run("capture_generated", func(t *testing.T) {
		rows := semanticWireRecords(t, wire)
		var newContext string
		for _, row := range rows {
			if row["project"] != "ContextA/ContextA.csproj" {
				continue
			}
			if row["record_type"] == "project" {
				var capture map[string]any
				if err := json.Unmarshal([]byte(row["capture_json"].(string)), &capture); err != nil {
					t.Fatal(err)
				}
				capture["sources"].([]any)[0].(map[string]any)["generated"] = true
				data, err := json.Marshal(capture)
				if err != nil {
					t.Fatal(err)
				}
				newContext = semanticTestSHA(data)
				row["capture_json"] = string(data)
			}
			row["build_context"] = newContext
		}
		if _, err := semanticimport.Import(context.Background(), bytes.NewReader(encodeSemanticWire(t, rows)), semanticimport.Options{Repo: "fixture", Root: root}); err == nil {
			t.Fatal("accepted generated flag contradicting captured roster")
		}
	})
}

func TestSemanticArtifactImportFilePreservesExistingOnFailure(t *testing.T) {
	root, wire := semanticArtifactWire(t, false)
	dir := t.TempDir()
	input, output := filepath.Join(dir, "input.jsonl"), filepath.Join(dir, "semantic.artifact")
	if err := os.WriteFile(input, wire, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := semanticcmd.ImportOptions{Input: input, Output: output, Options: semanticimport.Options{Repo: "fixture", Root: root}}
	if _, err := semanticcmd.ImportFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	rows := semanticWireRecords(t, wire)
	for _, row := range rows {
		row["compilation_status"] = "incomplete"
		if row["record_type"] == "project" {
			row["issues"] = []string{"deliberately incomplete test capture"}
		}
	}
	incomplete := encodeSemanticWire(t, rows)
	parsed, err := semanticimport.Import(context.Background(), bytes.NewReader(incomplete), opts.Options)
	if err != nil || parsed.Complete() {
		t.Fatalf("control must be valid incomplete capture: complete=%v err=%v", parsed != nil && parsed.Complete(), err)
	}
	for name, data := range map[string][]byte{"incomplete": incomplete, "truncated": wire[:len(wire)/2], "complete_no_overwrite": wire} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(input, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := semanticcmd.ImportFile(context.Background(), opts); err == nil {
				t.Fatal("failed/replacement input replaced staged artifact")
			}
			got, err := os.ReadFile(output)
			if err != nil || !bytes.Equal(got, prior) {
				t.Fatalf("existing artifact changed: %v", err)
			}
			if _, err := semantic.Read(output, semantic.Limits{}); err != nil {
				t.Fatalf("existing artifact no longer readable: %v", err)
			}
		})
	}
	if err := os.WriteFile(input, incomplete, 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Output = filepath.Join(dir, "never-created.artifact")
	if _, err := semanticcmd.ImportFile(context.Background(), opts); err == nil {
		t.Fatal("incomplete first import unexpectedly succeeded")
	}
	if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
		t.Fatalf("incomplete import created output: %v", err)
	}
}
