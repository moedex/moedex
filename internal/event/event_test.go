package event

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONSinkWritesStableNDJSONEnvelope(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	ctx := WithSink(context.Background(), NewJSONSink(&output))
	if err := Emit(ctx, Event{Command: "moe index build", Phase: "discover", State: "started"}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(output.String(), "\n"); lines != 1 {
		t.Fatalf("got %d JSON lines: %q", lines, output.String())
	}
	var got Event
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("invalid NDJSON: %v", err)
	}
	if got.Schema != SchemaVersion || got.Command != "moe index build" || got.Time.IsZero() {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestEmitWithoutSinkIsNoop(t *testing.T) {
	t.Parallel()
	if err := Emit(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
}
