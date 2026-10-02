// Package semanticcmd stages and inspects validated offline compiler artifacts.
// It deliberately does not change an active index's CURRENT pointer.
package semanticcmd

import (
	"context"
	"fmt"
	"os"

	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
)

type ImportOptions struct {
	Input  string
	Output string
	semanticimport.Options
}

type Summary struct {
	Format      string `json:"format"`
	Version     int    `json:"version"`
	Complete    bool   `json:"complete"`
	Snapshots   int    `json:"snapshots"`
	Contexts    int    `json:"contexts"`
	Sources     int    `json:"sources"`
	Symbols     int    `json:"symbols"`
	Occurrences int    `json:"occurrences"`
	Bindings    int    `json:"bindings"`
	Diagnostics int    `json:"diagnostics"`
}

func summarize(a *semantic.Artifact) Summary {
	return Summary{semantic.Format, a.Version, a.Complete(), len(a.Snapshots), len(a.Contexts), len(a.Sources), len(a.Symbols), len(a.Occurrences), len(a.Bindings), len(a.Diagnostics)}
}

// ImportFile validates every input and requires complete compiler capture before
// creating Output. Existing files are never replaced, even on successful import.
func ImportFile(ctx context.Context, opts ImportOptions) (Summary, error) {
	if opts.Input == "" || opts.Output == "" {
		return Summary{}, fmt.Errorf("semantic import: input and output are required")
	}
	f, err := os.Open(opts.Input)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	artifact, err := semanticimport.Import(ctx, f, opts.Options)
	if err != nil {
		return Summary{}, err
	}
	if !artifact.Complete() {
		return Summary{}, fmt.Errorf("semantic import: incomplete compiler capture; no artifact staged")
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if err := semantic.Write(opts.Output, artifact); err != nil {
		return Summary{}, err
	}
	return summarize(artifact), nil
}

func Inspect(path string) (Summary, error) {
	artifact, err := semantic.Read(path, semantic.Limits{})
	if err != nil {
		return Summary{}, err
	}
	return summarize(artifact), nil
}
