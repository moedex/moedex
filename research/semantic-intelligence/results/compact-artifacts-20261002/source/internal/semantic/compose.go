package semantic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const MaxComposeInputs = 32

// Compose deterministically unions complete compiler captures without publishing
// anything. Limits bounds both the aggregate canonical legacy input envelopes (including
// repeated inputs) and the resulting envelope; it is not a process heap ceiling.
// Every returned record owns its slices. Inputs must not be mutated concurrently.
// Same-ID records must have identical canonical JSON, including audit fields that
// are deliberately excluded from identity calculation. Dependency and binding
// consistency is checked again across the complete union.
func Compose(ctx context.Context, inputs []*Artifact, limits Limits) (*Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) > MaxComposeInputs {
		return nil, fmt.Errorf("semantic: compose requires 1..%d inputs", MaxComposeInputs)
	}
	limit := limits.MaxBytes
	if limit == 0 {
		limit = DefaultMaxBytes
	}
	if limit < 1 || limit > DefaultMaxBytes {
		return nil, fmt.Errorf("semantic: invalid compose byte limit")
	}
	snapshots := composeSet[SourceSnapshot]{}
	contexts := composeSet[BuildContext]{}
	sources := composeSet[Source]{}
	symbols := composeSet[Symbol]{}
	occurrences := composeSet[Occurrence]{}
	bindings := composeSet[Binding]{}
	diagnostics := composeSet[Diagnostic]{}
	var aggregate int64
	for input, a := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if a == nil || a.Version != FormatVersion {
			return nil, fmt.Errorf("semantic: compose input %d has unsupported or missing version", input+1)
		}
		if !a.Complete() {
			return nil, fmt.Errorf("semantic: compose input %d is incomplete", input+1)
		}
		size := int64(len(`{"version":`) + len(strconv.Itoa(a.Version)) + 1)
		add := func(name string, n int64, err error) error {
			if err != nil {
				return err
			}
			size += int64(len(name)+4) + n
			if size > limit {
				return fmt.Errorf("semantic: compose exceeds byte limit")
			}
			return nil
		}
		families := []func() error{
			func() error {
				n, e := snapshots.add(ctx, a.Snapshots, func(r SourceSnapshot) string { return r.ID }, limit)
				return add("snapshots", n, e)
			},
			func() error {
				n, e := contexts.add(ctx, a.Contexts, func(r BuildContext) string { return r.ID }, limit)
				return add("contexts", n, e)
			},
			func() error {
				n, e := sources.add(ctx, a.Sources, func(r Source) string { return r.ID }, limit)
				return add("sources", n, e)
			},
			func() error {
				n, e := symbols.add(ctx, a.Symbols, func(r Symbol) string { return r.ID }, limit)
				return add("symbols", n, e)
			},
			func() error {
				n, e := occurrences.add(ctx, a.Occurrences, func(r Occurrence) string { return r.ID }, limit)
				return add("occurrences", n, e)
			},
			func() error {
				n, e := bindings.add(ctx, a.Bindings, func(r Binding) string { return r.ID }, limit)
				return add("bindings", n, e)
			},
		}
		if len(a.Diagnostics) > 0 {
			families = append(families, func() error {
				n, e := diagnostics.add(ctx, a.Diagnostics, func(r Diagnostic) string { return r.ID }, limit)
				return add("diagnostics", n, e)
			})
		}
		for _, family := range families {
			if err := family(); err != nil {
				return nil, fmt.Errorf("semantic: compose input %d: %w", input+1, err)
			}
		}
		aggregate += composeEnvelopeSize(size)
		if aggregate > limit {
			return nil, fmt.Errorf("semantic: compose aggregate input exceeds %d-byte limit", limit)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("semantic: compose input %d: %w", input+1, err)
		}
	}
	out := &Artifact{Version: FormatVersion}
	var err error
	if out.Snapshots, err = snapshots.finish(ctx); err != nil {
		return nil, err
	}
	if out.Contexts, err = contexts.finish(ctx); err != nil {
		return nil, err
	}
	if out.Sources, err = sources.finish(ctx); err != nil {
		return nil, err
	}
	if out.Symbols, err = symbols.finish(ctx); err != nil {
		return nil, err
	}
	if out.Occurrences, err = occurrences.finish(ctx); err != nil {
		return nil, err
	}
	if out.Bindings, err = bindings.finish(ctx); err != nil {
		return nil, err
	}
	if out.Diagnostics, err = diagnostics.finish(ctx); err != nil {
		return nil, err
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("semantic: contradictory composition: %w", err)
	}
	if !out.Complete() {
		return nil, fmt.Errorf("semantic: composed artifact is incomplete")
	}
	// The union cannot exceed the sum of its bounded input payloads, but verify
	// its canonical legacy envelope size too. Compression does not increase
	// composition budgets, even when Write emits a smaller compact envelope.
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if composeEnvelopeSize(int64(len(raw))) > limit {
		return nil, fmt.Errorf("semantic: composed artifact exceeds byte limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func composeEnvelopeSize(payload int64) int64 {
	header, _ := json.Marshal(envelope{Format: Format, Version: FormatVersion, SHA256: strings.Repeat("0", 64), Payload: []byte{}})
	return int64(len(header)) + int64(base64.StdEncoding.EncodedLen(int(payload)))
}

type composeSet[T any] struct{ records map[string][]byte }

func (s *composeSet[T]) add(ctx context.Context, rows []T, id func(T) string, limit int64) (int64, error) {
	if rows == nil {
		return 4, nil
	} // JSON null, distinct from an empty nonnil input array.
	size := int64(2)
	if s.records == nil {
		s.records = make(map[string][]byte)
	}
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return 0, err
		}
		size += int64(len(raw))
		if i > 0 {
			size++
		}
		if size > limit {
			return 0, fmt.Errorf("record family exceeds byte limit")
		}
		key := id(row)
		if prior, ok := s.records[key]; ok {
			if !bytes.Equal(prior, raw) {
				return 0, fmt.Errorf("contradictory record %s", key)
			}
		} else {
			s.records[key] = raw
		}
	}
	return size, nil
}
func (s *composeSet[T]) finish(ctx context.Context) ([]T, error) {
	if len(s.records) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var row T
		if err := json.Unmarshal(s.records[id], &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}
