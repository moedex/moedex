package semanticcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"moedex/internal/semantic"
)

type ComposeOptions struct {
	Inputs []string
	Output string
	// MaxBytes bounds aggregate input envelopes and the composed artifact.
	// Zero uses the artifact format's 64 MiB default; larger limits are rejected.
	MaxBytes int64
}

type ComposeSummary struct {
	Summary
	Artifact        string            `json:"artifact"`
	Inputs          int               `json:"inputs"`
	InputBytes      int64             `json:"input_bytes"`
	SnapshotDetails []ComposeSnapshot `json:"snapshot_details"`
}

type ComposeSnapshot struct {
	SnapshotID string   `json:"snapshot_id"`
	Repo       string   `json:"repo"`
	ProjectID  string   `json:"project_id,omitempty"`
	Commit     string   `json:"commit,omitempty"`
	Projects   []string `json:"projects"`
	Inputs     []string `json:"inputs"`
}

// ComposeFiles stages one independently owned artifact from complete captures.
// It preserves recorded source/context identities and neither revalidates live
// source projections nor asserts dependency/runtime compatibility. Publication
// through the existing explicit semantic-artifact path remains a separate step.
func ComposeFiles(ctx context.Context, opts ComposeOptions) (ComposeSummary, error) {
	if err := ctx.Err(); err != nil {
		return ComposeSummary{}, err
	}
	if len(opts.Inputs) == 0 || len(opts.Inputs) > semantic.MaxComposeInputs {
		return ComposeSummary{}, fmt.Errorf("semantic compose: require 1..%d input artifacts", semantic.MaxComposeInputs)
	}
	if strings.TrimSpace(opts.Output) == "" {
		return ComposeSummary{}, fmt.Errorf("semantic compose: output is required")
	}
	if strings.EqualFold(filepath.Base(filepath.Clean(opts.Output)), "CURRENT") {
		return ComposeSummary{}, fmt.Errorf("semantic compose: output cannot be CURRENT")
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = semantic.DefaultMaxBytes
	}
	if maxBytes < 1 || maxBytes > semantic.DefaultMaxBytes {
		return ComposeSummary{}, fmt.Errorf("semantic compose: max-bytes must be 1..%d (0 uses default)", semantic.DefaultMaxBytes)
	}
	for _, path := range opts.Inputs {
		if strings.TrimSpace(path) == "" {
			return ComposeSummary{}, fmt.Errorf("semantic compose: input paths must not be empty")
		}
	}
	if _, err := os.Lstat(opts.Output); err == nil {
		return ComposeSummary{}, fmt.Errorf("semantic compose: output already exists: %w", os.ErrExist)
	} else if !os.IsNotExist(err) {
		return ComposeSummary{}, err
	}
	artifacts := make([]*semantic.Artifact, 0, len(opts.Inputs))
	var total, expanded int64
	for _, path := range opts.Inputs {
		if err := ctx.Err(); err != nil {
			return ComposeSummary{}, err
		}
		remaining := maxBytes - total
		if remaining <= 0 || expanded >= maxBytes {
			return ComposeSummary{}, fmt.Errorf("semantic compose: aggregate inputs exceed byte limit")
		}
		artifact, n, err := readComposeInput(ctx, path, remaining, maxBytes-expanded)
		if err != nil {
			return ComposeSummary{}, fmt.Errorf("semantic compose: input %q: %w", path, err)
		}
		total += n
		// Retain the pre-compression aggregate memory budget before reading another input.
		raw, err := json.Marshal(artifact)
		if err != nil {
			return ComposeSummary{}, err
		}
		expanded += int64(len(raw))
		if expanded > maxBytes {
			return ComposeSummary{}, fmt.Errorf("semantic compose: expanded inputs exceed byte limit")
		}
		if !artifact.Complete() {
			return ComposeSummary{}, fmt.Errorf("semantic compose: input %q is incomplete; no artifact staged", path)
		}
		artifacts = append(artifacts, artifact)
	}
	composed, err := semantic.Compose(ctx, artifacts, semantic.Limits{MaxBytes: maxBytes})
	if err != nil {
		return ComposeSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return ComposeSummary{}, err
	}
	if err := semantic.Write(opts.Output, composed); err != nil {
		return ComposeSummary{}, err
	}
	projectsBySnapshot := map[string]map[string]bool{}
	inputsBySnapshot := map[string]map[string]bool{}
	for _, context := range composed.Contexts {
		if projectsBySnapshot[context.SnapshotID] == nil {
			projectsBySnapshot[context.SnapshotID] = map[string]bool{}
		}
		projectsBySnapshot[context.SnapshotID][context.Project] = true
	}
	for i, artifact := range artifacts {
		for _, snapshot := range artifact.Snapshots {
			if inputsBySnapshot[snapshot.ID] == nil {
				inputsBySnapshot[snapshot.ID] = map[string]bool{}
			}
			inputsBySnapshot[snapshot.ID][opts.Inputs[i]] = true
		}
	}
	details := make([]ComposeSnapshot, 0, len(composed.Snapshots))
	for _, snapshot := range composed.Snapshots {
		detail := ComposeSnapshot{SnapshotID: snapshot.ID, Repo: snapshot.Repo, ProjectID: snapshot.ProjectID, Commit: snapshot.Commit, Projects: []string{}, Inputs: []string{}}
		for project := range projectsBySnapshot[snapshot.ID] {
			detail.Projects = append(detail.Projects, project)
		}
		for input := range inputsBySnapshot[snapshot.ID] {
			detail.Inputs = append(detail.Inputs, input)
		}
		sort.Strings(detail.Projects)
		sort.Strings(detail.Inputs)
		details = append(details, detail)
	}
	return ComposeSummary{Summary: summarize(composed), Artifact: opts.Output, Inputs: len(opts.Inputs), InputBytes: total, SnapshotDetails: details}, nil
}

func readComposeInput(ctx context.Context, path string, remaining, expandedRemaining int64) (*semantic.Artifact, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("input must be a regular artifact file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > remaining {
		return nil, 0, fmt.Errorf("aggregate inputs exceed byte limit or input is not regular")
	}
	reader := &composeInputReader{ctx: ctx, reader: f}
	artifact, err := semantic.ReadFrom(reader, semantic.Limits{MaxBytes: remaining, MaxPayloadBytes: expandedRemaining})
	return artifact, reader.bytes, err
}

// Count actual bytes supplied to the bounded decoder, not a possibly stale
// stat size. Cancellation also interrupts the reader between file reads.
type composeInputReader struct {
	ctx    context.Context
	reader io.Reader
	bytes  int64
}

func (r *composeInputReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}
