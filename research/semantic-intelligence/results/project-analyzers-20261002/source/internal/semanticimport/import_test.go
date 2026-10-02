package semanticimport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moedex/internal/semantic"
)

func TestImportRejectsMissingTerminalAndUnknownSchemas(t *testing.T) {
	for _, stream := range []string{"", `{"schema":"other","record_type":"stream_summary"}`, `{"schema":"moedex.semantic-worker.v1","record_type":"invented"}`} {
		if _, err := Import(context.Background(), strings.NewReader(stream), Options{Repo: "r", Root: t.TempDir()}); err == nil {
			t.Fatalf("accepted %s", stream)
		}
	}
}

func TestImportPreservesPublicOriginWithoutProjectID(t *testing.T) {
	capture := `{"project":"A.csproj","sources":[]}`
	ctxID := digest([]byte(capture))
	rows := []record{
		{Schema: WorkerSchema, Type: "project", Repo: "roslyn", Project: "A.csproj", Context: ctxID, CaptureJSON: capture, Status: "complete", Extractor: "roslyn", ExtractorVersion: "1", CompilerVersion: "1"},
		{Schema: WorkerSchema, Type: "summary", Project: "A.csproj", Context: ctxID, Status: "complete"},
		{Schema: WorkerSchema, Type: "stream_summary", Projects: 1, Status: "complete"},
	}
	var stream strings.Builder
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		stream.Write(b)
		stream.WriteByte('\n')
	}
	opts := Options{Repo: "roslyn", Root: t.TempDir(), Commit: strings.Repeat("a", 40), Origin: "https://github.com/dotnet/roslyn.git"}
	opts.DependencyBundleSHA256 = strings.Repeat("b", 64)
	a, err := Import(context.Background(), strings.NewReader(stream.String()), opts)
	if err != nil {
		t.Fatal(err)
	}
	bounded := opts
	bounded.MaxBytes = 128
	if _, err := Import(context.Background(), strings.NewReader(stream.String()), bounded); err == nil {
		t.Fatal("small default wire budget admitted stream")
	}
	bounded.MaxWireBytes = int64(stream.Len())
	if _, err := Import(context.Background(), strings.NewReader(stream.String()), bounded); err != nil {
		t.Fatalf("explicit wire budget with unchanged input budget: %v", err)
	}
	bounded.MaxWireBytes--
	if _, err := Import(context.Background(), strings.NewReader(stream.String()), bounded); err == nil {
		t.Fatal("wire boundary crossing admitted")
	}
	s := a.Snapshots[0]
	if s.Origin != opts.Origin || s.Commit != opts.Commit || s.ProjectID != "" {
		t.Fatalf("provenance: %+v", s)
	}
	if a.Contexts[0].DependencyBundleSHA256 != opts.DependencyBundleSHA256 {
		t.Fatal("bundle evidence omitted")
	}
	p := filepath.Join(t.TempDir(), "artifact.json")
	if err := semantic.Write(p, a); err != nil {
		t.Fatal(err)
	}
	got, err := semantic.Read(p, semantic.Limits{})
	if err != nil || got.Snapshots[0] != s {
		t.Fatalf("origin roundtrip: %v", err)
	}
	if got.Contexts[0].DependencyBundleSHA256 != opts.DependencyBundleSHA256 {
		t.Fatal("bundle evidence lost in persistence")
	}
	withBundle := a.Contexts[0].ID
	opts.DependencyBundleSHA256 = ""
	without, err := Import(context.Background(), strings.NewReader(stream.String()), opts)
	if err != nil {
		t.Fatal(err)
	}
	if without.Contexts[0].ID == withBundle {
		t.Fatal("bundle not bound to context identity")
	}
	opts.DependencyBundleSHA256 = "invalid"
	if _, err := Import(context.Background(), strings.NewReader(stream.String()), opts); err == nil {
		t.Fatal("invalid bundle digest admitted")
	}
	opts.DependencyBundleSHA256 = ""
	opts.Origin = "https://user:secret@github.com/dotnet/roslyn"
	if _, err := Import(context.Background(), strings.NewReader(stream.String()), opts); err == nil {
		t.Fatal("credential origin admitted")
	}
}

func TestImportInputLimitAndCancellation(t *testing.T) {
	root := t.TempDir()
	if _, err := Import(context.Background(), strings.NewReader(strings.Repeat("x", 128)), Options{Repo: "r", Root: root, MaxBytes: 64}); err == nil {
		t.Fatal("oversized input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Import(ctx, strings.NewReader(`{"schema":"moedex.semantic-worker.v1","record_type":"stream_summary"}`), Options{Repo: "r", Root: root}); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestSourceResolverConfinesSymlinksAndRejectsMetadataMismatch(t *testing.T) {
	rootPath := t.TempDir()
	outside := filepath.Join(t.TempDir(), "source.cs")
	raw := []byte("class Secret {}")
	if err := os.WriteFile(outside, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "link.cs")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	record := sourceRecord{Path: "link.cs", SHA: digest(raw), ByteSize: uint64(len(raw))}
	if _, err := readSource(root, record, 1024); err == nil {
		t.Fatal("source symlink escaped projection")
	}
	if err := os.WriteFile(filepath.Join(rootPath, "local.cs"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	record.Path = "local.cs"
	record.SHA = strings.Repeat("0", 64)
	if _, err := readSource(root, record, 1024); err == nil {
		t.Fatal("wrong hash accepted")
	}
	record.SHA = digest(raw)
	if _, err := readSource(root, record, int64(len(raw)-1)); err == nil {
		t.Fatal("raw byte limit ignored")
	}
}

func TestCaptureRejectsConfigurationDrift(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("<Project />")
	if err := os.WriteFile(filepath.Join(dir, "project.csproj"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	capture := fmt.Sprintf(`{"project_file":{"scope":"source","path":"project.csproj","sha256":"%s","byte_size":%d}}`, digest(raw), len(raw))
	var size int64
	if err := verifyInputs(context.Background(), root, capture, 1024, map[string]string{}, &size); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.csproj"), []byte("<Changed />"), 0600); err != nil {
		t.Fatal(err)
	}
	size = 0
	if err := verifyInputs(context.Background(), root, capture, 1024, map[string]string{}, &size); err == nil {
		t.Fatal("configuration drift accepted")
	}
}

func TestWireBudgetDoesNotEnlargeInputBudget(t *testing.T) {
	for _, limit := range []int64{-1, MaximumWireBytes + 1} {
		_, err := Import(context.Background(), strings.NewReader(""), Options{Repo: "r", Root: t.TempDir(), MaxWireBytes: limit})
		if err == nil || !strings.Contains(err.Error(), "wire byte limit") {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	_, err := Import(context.Background(), strings.NewReader(""), Options{Repo: "r", Root: t.TempDir(), MaxBytes: DefaultMaxBytes + 1, MaxWireBytes: MaximumWireBytes})
	if err == nil || !strings.Contains(err.Error(), "invalid byte limit") {
		t.Fatalf("input budget enlarged: %v", err)
	}
}
