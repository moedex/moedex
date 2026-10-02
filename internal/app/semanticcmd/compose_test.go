package semanticcmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"moedex/internal/semantic"
)

func composeDigest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func composeFixture(configuration string) *semantic.Artifact {
	snapshot := semantic.SourceSnapshot{Repo: "repo", InputFingerprint: composeDigest("roster"), Commit: strings.Repeat("a", 40), ProjectID: "42"}
	snapshot.ID = snapshot.ComputeID()
	source := semantic.Source{SnapshotID: snapshot.ID, Path: "A.cs", RawSHA256: composeDigest("class A {}"), ByteSize: 10}
	source.ID = source.ComputeID()
	capture := `{"configuration":"` + configuration + `"}`
	ctx := semantic.BuildContext{SnapshotID: snapshot.ID, Project: "A.csproj", Capture: capture, InputFingerprint: composeDigest(capture), Extractor: "roslyn", ExtractorVersion: "2", Status: "complete", SourceIDs: []string{source.ID}}
	ctx.ID = ctx.ComputeID()
	return &semantic.Artifact{Version: semantic.FormatVersion, Snapshots: []semantic.SourceSnapshot{snapshot}, Sources: []semantic.Source{source}, Contexts: []semantic.BuildContext{ctx}}
}
func writeComposeFixture(t *testing.T, dir, name string, a *semantic.Artifact) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := semantic.Write(path, a); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestComposeFilesDeterministicVariantsAndSnapshotSummary(t *testing.T) {
	dir := t.TempDir()
	debug, release := composeFixture("Debug"), composeFixture("Release")
	a := writeComposeFixture(t, dir, "debug.json", debug)
	b := writeComposeFixture(t, dir, "release.json", release)
	first := filepath.Join(dir, "first.json")
	summary, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{a, b, a}, Output: first})
	if err != nil {
		t.Fatal(err)
	}
	ab, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if summary.Inputs != 3 || summary.InputBytes != int64(2*len(ab)+len(bb)) || summary.Contexts != 2 || summary.Sources != 1 || !summary.Complete {
		t.Fatalf("summary: %+v", summary)
	}
	detail := summary.SnapshotDetails[0]
	if len(summary.SnapshotDetails) != 1 || detail.SnapshotID != debug.Snapshots[0].ID || detail.Repo != "repo" || detail.ProjectID != "42" || detail.Commit != strings.Repeat("a", 40) || !reflect.DeepEqual(detail.Projects, []string{"A.csproj"}) || !reflect.DeepEqual(detail.Inputs, []string{a, b}) {
		t.Fatalf("snapshot mapping summary: %+v", detail)
	}
	second := filepath.Join(dir, "second.json")
	if _, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{b, a}, Output: second}); err != nil {
		t.Fatal(err)
	}
	one, _ := os.ReadFile(first)
	two, _ := os.ReadFile(second)
	if !bytes.Equal(one, two) {
		t.Fatal("composition order/duplicate changed artifact bytes")
	}
	got, err := semantic.Read(first, semantic.Limits{})
	if err != nil || !reflect.DeepEqual(got.Sources, debug.Sources) {
		t.Fatal("source identities changed", err)
	}
	if bytes.Contains(one, []byte(dir)) {
		t.Fatal("machine path persisted into artifact")
	}
	if _, err := os.Stat(filepath.Join(dir, "CURRENT")); !os.IsNotExist(err) {
		t.Fatal("composition wrote CURRENT")
	}
}

func TestComposeFilesBoundsFailuresAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := writeComposeFixture(t, dir, "input.json", composeFixture("Debug"))
	raw, _ := os.ReadFile(path)
	incomplete := composeFixture("Broken")
	incomplete.Contexts[0].Status = "incomplete"
	incomplete.Contexts[0].ID = incomplete.Contexts[0].ComputeID()
	bad := writeComposeFixture(t, dir, "incomplete.json", incomplete)
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	many := make([]string, semantic.MaxComposeInputs+1)
	for i := range many {
		many[i] = path
	}
	cases := []ComposeOptions{
		{Inputs: nil}, {Inputs: many}, {Inputs: []string{""}},
		{Inputs: []string{path}, MaxBytes: -1}, {Inputs: []string{path}, MaxBytes: semantic.DefaultMaxBytes + 1},
		{Inputs: []string{path, path}, MaxBytes: int64(2*len(raw) - 1)},
		{Inputs: []string{bad}}, {Inputs: []string{corrupt}}, {Inputs: []string{dir}}, {Inputs: []string{link}},
	}
	for i, opts := range cases {
		opts.Output = filepath.Join(dir, "failed-"+string(rune('a'+i))+".json")
		if _, err := ComposeFiles(context.Background(), opts); err == nil {
			t.Fatalf("case %d succeeded", i)
		}
		if _, err := os.Stat(opts.Output); !os.IsNotExist(err) {
			t.Fatalf("case %d published after failure", i)
		}
	}
	if _, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{path}, Output: path}); !errors.Is(err, os.ErrExist) {
		t.Fatal("overwrite accepted", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, raw) {
		t.Fatal("input overwritten")
	}
	for _, name := range []string{"CURRENT", "current"} {
		if _, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{path}, Output: filepath.Join(dir, name)}); err == nil {
			t.Fatal("CURRENT output accepted")
		}
	}
	if _, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{path}}); err == nil {
		t.Fatal("missing output accepted")
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".semantic-*")); len(tmp) != 0 {
		t.Fatalf("temporary files leaked: %v", tmp)
	}
}

func TestComposeFilesRejectsConflictingSourceAndHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	original := composeFixture("Debug")
	other := composeFixture("Release")
	other.Sources[0].RawSHA256 = composeDigest("class B {}")
	other.Sources[0].ID = other.Sources[0].ComputeID()
	other.Contexts[0].SourceIDs = []string{other.Sources[0].ID}
	other.Contexts[0].ID = other.Contexts[0].ComputeID()
	a := writeComposeFixture(t, dir, "one.json", original)
	b := writeComposeFixture(t, dir, "two.json", other)
	output := filepath.Join(dir, "composed.json")
	if _, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{a, b}, Output: output}); err == nil {
		t.Fatal("conflicting same-snapshot source admitted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ComposeFiles(canceled, ComposeOptions{Inputs: []string{a}, Output: output}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failure published artifact")
	}
	ctx, stop := context.WithCancel(context.Background())
	reader := &composeInputReader{ctx: ctx, reader: strings.NewReader("abcdef")}
	buf := make([]byte, 2)
	if n, err := reader.Read(buf); n != 2 || err != nil || reader.bytes != 2 {
		t.Fatal("actual read count", n, err)
	}
	stop()
	if n, err := reader.Read(buf); n != 0 || !errors.Is(err, context.Canceled) || reader.bytes != 2 {
		t.Fatal("read cancellation", n, err)
	}
}

func TestComposeFilesConcurrentExclusivePublication(t *testing.T) {
	dir := t.TempDir()
	input := writeComposeFixture(t, dir, "input.json", composeFixture("Debug"))
	opts := ComposeOptions{Inputs: []string{input}, Output: filepath.Join(dir, "out.json")}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, err := ComposeFiles(context.Background(), opts); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("published %d competing artifacts", successes)
	}
	if _, err := semantic.Read(opts.Output, semantic.Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestComposeReaderCountsActualEnvelopeBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeComposeFixture(t, dir, "input.json", composeFixture("Debug"))
	raw, _ := os.ReadFile(path)
	padded := append(append([]byte{}, raw...), []byte(" \n\t")...)
	reader := &composeInputReader{ctx: context.Background(), reader: bytes.NewReader(padded)}
	if _, err := semantic.ReadFrom(reader, semantic.Limits{MaxBytes: int64(len(padded))}); err != nil {
		t.Fatal(err)
	}
	if reader.bytes != int64(len(padded)) {
		t.Fatalf("charged %d expected %d", reader.bytes, len(padded))
	}
	if n, err := reader.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatal(n, err)
	}
}

func TestComposeCompressedInputsRetainExpandedBudget(t *testing.T) {
	dir := t.TempDir()
	input := writeComposeFixture(t, dir, "compact.json", composeFixture(strings.Repeat("x", 70<<10)))
	// Both compressed files fit easily, but the second expanded payload cannot.
	raw, err := os.ReadFile(input)
	if err != nil || len(raw) >= 32<<10 {
		t.Fatal("fixture not compressed", err)
	}
	_, err = ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{input, input}, Output: filepath.Join(dir, "bad"), MaxBytes: 120 << 10})
	if err == nil || !strings.Contains(err.Error(), "expanded byte limit") {
		t.Fatalf("expanded aggregate accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad")); !os.IsNotExist(err) {
		t.Fatal("published over-budget composition")
	}
	if _, err := ComposeFiles(context.Background(), ComposeOptions{Inputs: []string{input}, Output: filepath.Join(dir, "good"), MaxBytes: 120 << 10}); err != nil {
		t.Fatal(err)
	}
}
