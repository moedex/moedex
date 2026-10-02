package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"moedex/internal/semanticindex"
	"strings"
	"testing"
)

type evidencePathReaderFixture struct {
	compilerReaderFixture
	result semanticindex.EvidencePathResult
	err    error
	onCall func()
}

func (r *evidencePathReaderFixture) EvidencePath(context.Context, string, []string, int) (semanticindex.EvidencePathResult, error) {
	if r.onCall != nil {
		r.onCall()
	}
	return r.result, r.err
}
func evidencePathFixture() (*evidencePathReaderFixture, json.RawMessage) {
	r := defaultSelectionMCPFixture()
	r.Snapshot.ID = r.Snapshot.ComputeID()
	r.Source.SnapshotID = r.Snapshot.ID
	r.Source.ID = r.Source.ComputeID()
	r.Context.SnapshotID = r.Snapshot.ID
	r.Context.ID = "context:" + strings.Repeat("a", 64)
	r.Occurrence.ContextID = r.Context.ID
	r.Occurrence.SourceID = r.Source.ID
	r.Occurrence.ID = r.Occurrence.ComputeID()
	r.Binding.OccurrenceID = r.Occurrence.ID
	args, _ := json.Marshal(map[string]any{"symbol_id": r.Symbol.ID, "context_ids": []string{r.Context.ID}})
	return &evidencePathReaderFixture{result: semanticindex.EvidencePathResult{Supported: true, Records: []semanticindex.Result{r}, RowsVisited: 1}}, args
}
func TestEvidencePathMCPLeaseNormalizationAndSchema(t *testing.T) {
	r, args := evidencePathFixture()
	p := &compilerProviderFixture{reader: r}
	tool := CompilerTools(p)[7]
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	validateFixture(t, tool.Specification().OutputSchema, out["structuredContent"])
	q := out["structuredContent"].(CompilerEvidencePathResult)
	if q.Status != "ok" || len(q.Symbols) != 4 || len(q.Records) != 1 || p.acquired != 1 || p.released != 1 {
		t.Fatalf("%+v %+v", q, p)
	}
	r.onCall = func() { r.result.Records = nil }
	out, err = tool.Call(context.Background(), args)
	if err != nil || out["structuredContent"].(CompilerEvidencePathResult).Status != "no_recorded_evidence" {
		t.Fatal(out, err)
	}
}
func TestEvidencePathMCPInvalidCancellationAndMalformed(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"symbol_id":"x"}`, `{"limit":null}`} {
		p := &compilerProviderFixture{}
		out, err := CompilerTools(p)[7].Call(context.Background(), json.RawMessage(raw))
		if err != nil || out["structuredContent"].(CompilerEvidencePathResult).Status != "invalid_arguments" || p.acquired != 0 {
			t.Fatal(out, err)
		}
	}
	for _, change := range []func(*evidencePathReaderFixture){func(r *evidencePathReaderFixture) { r.result.Records[0].Source.RawSHA256 = "bad" }, func(r *evidencePathReaderFixture) { r.result.Records[0].Context.ID = "wrong" }, func(r *evidencePathReaderFixture) { r.result.RowsVisited = 10001 }, func(r *evidencePathReaderFixture) { r.result.Records = append(r.result.Records, r.result.Records[0]) }, func(r *evidencePathReaderFixture) { r.err = errors.New("failure") }} {
		r, args := evidencePathFixture()
		change(r)
		p := &compilerProviderFixture{reader: r}
		if _, err := CompilerTools(p)[7].Call(context.Background(), args); err == nil || p.released != 1 {
			t.Fatal("malformed reader admitted", err)
		}
	}
	r, args := evidencePathFixture()
	ctx, cancel := context.WithCancel(context.Background())
	r.onCall = cancel
	p := &compilerProviderFixture{reader: r}
	if _, err := CompilerTools(p)[7].Call(ctx, args); err != context.Canceled || p.released != 1 {
		t.Fatal(err)
	}
}
func TestEvidencePathMCPSerializedCapTrimsWholeRecords(t *testing.T) {
	r, args := evidencePathFixture()
	record := r.result.Records[0]
	record.Binding.ImplementationFacts = nil
	record.ImplementationSymbols = nil
	symbol := *record.Symbol
	symbol.Key.Descriptor = "T:" + strings.Repeat("\\\"", 40000)
	symbol.ID = symbol.ComputeID()
	record.Symbol = &symbol
	record.Binding.SymbolID = symbol.ID
	r.result.Records = []semanticindex.Result{record}
	args, _ = json.Marshal(map[string]any{"symbol_id": symbol.ID, "context_ids": []string{record.Context.ID}})
	p := &compilerProviderFixture{reader: r}
	out, err := CompilerTools(p)[7].Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(out)
	q := out["structuredContent"].(CompilerEvidencePathResult)
	if len(wire) > EvidencePathResponseBytes || !q.Truncated || len(q.Records) != 0 || len(q.Symbols) != 0 {
		t.Fatalf("bytes%d %+v", len(wire), q)
	}
}
