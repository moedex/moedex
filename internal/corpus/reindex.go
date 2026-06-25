package corpus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// casManifestName mirrors blobstore.BlobManifestName — the file that marks an
// initialized content-addressable store. moedex-corpus checks for it (rather than
// importing the engine's blobstore) to choose cas-build vs cas-refresh, so the
// tool stays decoupled from the engine internals it only ever drives through the
// moedex-index binary.
const casManifestName = "blobmanifest.json"

// ReindexOptions configures the per-blob-delta reindex chain that turns freshly
// synced repos into served shards.
type ReindexOptions struct {
	Root       string   // corpus root passed to cas-build / cas-refresh
	CASDir     string   // persistent content-addressable store
	ShardDir   string   // served (deduped) shard dir consumed by moedex-serve
	ShardBytes int64    // target bytes/shard for the export; 0 = moedex-index default
	IndexBin   string   // moedex-index binary to drive (default "moedex-index")
	Reload     []string // argv run after a successful export (e.g. ["systemctl","reload","moedex-serve"]); empty = skip
}

// StepResult is the outcome of one reindex step (the driven tool's own summary).
type StepResult struct {
	Name   string
	Output string
}

// ReindexReport summarizes a reindex run.
type ReindexReport struct {
	Built    bool // CAS built from scratch (true) vs delta-refreshed (false)
	Steps    []StepResult
	Reloaded bool
}

// Reindex runs the per-blob-delta path that you chose for freshness:
//
//	(cas-build if the CAS is new, else cas-refresh) -> cas-export -deduped -> reload
//
// Each step shells out to the moedex-index binary, so the retrieval engine is
// never linked into moedex-corpus. cas-export with -deduped is delta-aware over an
// existing served dir (appends only net-new content, rewrites only changed
// shards). Reindex returns after the first failing step.
func Reindex(ctx context.Context, r Runner, opts ReindexOptions) (ReindexReport, error) {
	if opts.Root == "" || opts.CASDir == "" || opts.ShardDir == "" {
		return ReindexReport{}, fmt.Errorf("reindex needs a corpus root, -cas-dir and -shard-dir")
	}
	bin := opts.IndexBin
	if bin == "" {
		bin = "moedex-index"
	}
	if _, err := r.LookPath(bin); err != nil {
		return ReindexReport{}, fmt.Errorf("%s not found on PATH — build it with `go build -o moedex-index ./cmd/moedex-index`: %w", bin, err)
	}

	var rep ReindexReport

	// 1. Build the CAS the first time; thereafter delta-refresh it.
	rep.Built = !casExists(opts.CASDir)
	var (
		step StepResult
		err  error
	)
	if rep.Built {
		step, err = runIndex(ctx, r, bin, "cas-build", "-corpus", opts.Root, "-cas-dir", opts.CASDir)
	} else {
		step, err = runIndex(ctx, r, bin, "cas-refresh", "-cas-dir", opts.CASDir, "-corpus", opts.Root)
	}
	rep.Steps = append(rep.Steps, step)
	if err != nil {
		return rep, err
	}

	// 2. Export the deduped served shard dir (delta-aware over an existing dir).
	exportArgs := []string{"cas-export", "-cas-dir", opts.CASDir, "-shard-dir", opts.ShardDir, "-deduped"}
	if opts.ShardBytes > 0 {
		exportArgs = append(exportArgs, "-shard-bytes", fmt.Sprintf("%d", opts.ShardBytes))
	}
	step, err = runIndex(ctx, r, bin, exportArgs...)
	rep.Steps = append(rep.Steps, step)
	if err != nil {
		return rep, err
	}

	// 3. Reload the daemon onto the fresh shards, if a reload command was given.
	if len(opts.Reload) > 0 {
		res, err := r.Run(ctx, opts.Reload[0], opts.Reload[1:]...)
		if err != nil {
			return rep, fmt.Errorf("reload (%s): %w", strings.Join(opts.Reload, " "), err)
		}
		if !res.Ok() {
			return rep, fmt.Errorf("reload (%s) exited %d: %s", strings.Join(opts.Reload, " "), res.Code, lastLine(res.Stderr))
		}
		rep.Reloaded = true
	}
	return rep, nil
}

// runIndex runs one moedex-index subcommand and captures its stdout summary.
func runIndex(ctx context.Context, r Runner, bin string, args ...string) (StepResult, error) {
	name := args[0]
	res, err := r.Run(ctx, bin, args...)
	out := strings.TrimSpace(string(res.Stdout))
	if err != nil {
		return StepResult{Name: name, Output: out}, fmt.Errorf("%s %s: %w", bin, name, err)
	}
	if !res.Ok() {
		detail := strings.TrimSpace(string(res.Stderr))
		if detail == "" {
			detail = out
		}
		return StepResult{Name: name, Output: out}, fmt.Errorf("%s %s exited %d: %s", bin, name, res.Code, lastLine([]byte(detail)))
	}
	return StepResult{Name: name, Output: out}, nil
}

// casExists reports whether casDir holds an initialized CAS (its blob manifest).
func casExists(casDir string) bool {
	_, err := os.Stat(filepath.Join(casDir, casManifestName))
	return err == nil
}
