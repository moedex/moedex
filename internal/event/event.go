// Package event defines the progress protocol shared by human and machine CLI
// renderers. Events are append-only NDJSON when --json is active.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

const SchemaVersion = 1

// Event is one stable progress record. Optional counters and fields allow new
// phases to become richer without changing the envelope.
type Event struct {
	Schema  int            `json:"schema"`
	Time    time.Time      `json:"time"`
	Command string         `json:"command"`
	Phase   string         `json:"phase"`
	State   string         `json:"state"`
	Current int64          `json:"current,omitempty"`
	Total   int64          `json:"total,omitempty"`
	Unit    string         `json:"unit,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// Sink accepts progress events. Implementations must be concurrency safe.
type Sink interface {
	Emit(Event) error
}

type sinkKey struct{}

// WithSink installs a progress sink in ctx.
func WithSink(ctx context.Context, sink Sink) context.Context {
	return context.WithValue(ctx, sinkKey{}, sink)
}

// Emit writes an event to the context sink. A context without a sink is a
// deliberate no-op so engine packages do not need renderer conditionals.
func Emit(ctx context.Context, item Event) error {
	sink, _ := ctx.Value(sinkKey{}).(Sink)
	if sink == nil {
		return nil
	}
	if item.Schema == 0 {
		item.Schema = SchemaVersion
	}
	if item.Time.IsZero() {
		item.Time = time.Now().UTC()
	}
	return sink.Emit(item)
}

// JSONSink writes one compact JSON object per line.
type JSONSink struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func NewJSONSink(writer io.Writer) *JSONSink {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return &JSONSink{encoder: encoder}
}

func (s *JSONSink) Emit(item Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder.Encode(item)
}

// TextSink writes concise progress lines to stderr-style streams.
type TextSink struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewTextSink(writer io.Writer) *TextSink { return &TextSink{writer: writer} }

func (s *TextSink) Emit(item Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message := item.Message
	if message == "" {
		message = item.State
	}
	if item.Total > 0 {
		_, err := fmt.Fprintf(s.writer, "> %-10s %d/%d %s %s\n", item.Phase, item.Current, item.Total, item.Unit, message)
		return err
	}
	_, err := fmt.Fprintf(s.writer, "> %-10s %s\n", item.Phase, message)
	return err
}
