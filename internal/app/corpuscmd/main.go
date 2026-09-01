// Command moedex-corpus is the setup-and-freshness operator for moedex's live
// corpus. On a fresh machine it checks that the glab CLI is installed and
// authenticated to TurnCommerce's internal GitLab ONLY, then (in later phases)
// clones every project the operator's access allows into the corpus tree the
// engine indexes, and keeps that mirror fresh.
//
// It is deliberately separate from the retrieval engine and the daemon: those
// are pure-Go and zero-dependency, while corpus acquisition shells out to glab,
// git, and moedex-index over a network. All that lives in internal/corpus,
// behind a Runner seam, so the engine's posture is untouched.
//
// Subcommands:
//
//	moedex-corpus doctor              # preflight: glab? authed to tcdevops only? git? projected repo count
//	moedex-corpus init                # create a Moedex-owned submodule superproject
//	moedex-corpus clone               # shallow-clone the curated corpus, many repos at once
//	moedex-corpus groups --from-disk  # regenerate the group allowlist from an existing mirror
//
// Meet Moe, moedex's eight-tentacled mascot — the corpus wrangler who reaches out
// with many tentacles at once to clone the whole corpus in parallel.
package corpuscmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"moedex/internal/corpus"
	"moedex/internal/version"
)

func Main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "init":
		err = runInit(os.Args[2:])
	case "clone":
		err = runClone(os.Args[2:])
	case "sync":
		err = runSync(os.Args[2:])
	case "groups":
		err = runGroups(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println(version.Line("moedex-corpus", false))
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "moedex-corpus: unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "moedex-corpus: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `moedex-corpus — set up and keep fresh the moedex live corpus.

Usage:
  moedex-corpus doctor [-corpus DIR] [-groups FILE] [-timeout DUR] [-no-banner]
      Diagnose local managed-corpus integrity plus separate glab authentication,
      GitLab/VPN reachability, and Git clone/fetch transport checks.

  moedex-corpus init [-corpus DIR] [-groups FILE] [-concurrency N] [-timeout DUR] [-no-banner]
      Create a new Moedex-owned Git superproject at an empty/new destination,
      add curated projects as stable-ID submodules, and commit the first lock.

  moedex-corpus clone [-corpus DIR] [-groups FILE] [-concurrency N] [-timeout DUR] [-dry-run] [-no-banner]
      Shallow-clone (--depth 1) every curated project into the corpus tree, many
      at once. Idempotent: existing repos are skipped (freshen them with sync).
      Without -groups, the built-in curated allowlist is used.

  moedex-corpus sync [-corpus DIR] [-groups FILE] [-concurrency N] [-timeout DUR] [-prune] [-dry-run] [-no-banner]
      Managed roots reconcile stable project IDs and commit a locked submodule
      snapshot. Unmanaged roots retain the independent-clone compatibility path.
      With -prune, remove projects absent after a complete enumeration.

  moedex-corpus groups --from-disk [-corpus DIR]
      Print the group allowlist derived from the top-level dirs of an existing
      mirror (one group per line, to stdout) — commit it as the default list.

Every subcommand above stops cleanly on SIGINT/SIGTERM and gives up after
-timeout (a generous per-subcommand default; 0 disables it) so a hung glab/git
call fails loudly instead of blocking an unattended run forever.

The tool only ever talks to `+corpus.DefaultHost+`.
`)
}

// Default -timeout values, one per subcommand. doctor only makes a handful of
// quick API/reachability probes; init/clone/sync fan out over the whole
// curated corpus (many repos, real network transfer) so they get a much more
// generous bound. All are overridable with -timeout; 0 disables the deadline
// entirely (signal handling still applies).
const (
	defaultDoctorTimeout = 2 * time.Minute
	defaultInitTimeout   = 2 * time.Hour
	defaultCloneTimeout  = 2 * time.Hour
	defaultSyncTimeout   = 45 * time.Minute
)

// signalContext returns a context that is canceled on SIGINT/SIGTERM — so an
// operator kill, `systemctl stop`, or a reboot's shutdown signal lands as an
// ordinary, catchable cancellation that every glab/git subprocess call
// (Runner.Run/RunEnv -> exec.CommandContext) observes and unwinds from
// cleanly, rather than the process being torn down mid-operation with no
// chance to report what happened. When timeout > 0 the context is
// additionally bounded by it, so a stalled network call cannot block the
// process forever even absent any signal — the concrete gap this closes for
// an unattended, hourly systemd-timer job. Callers must defer the returned
// stop func.
func signalContext(timeout time.Duration) (context.Context, func()) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if timeout <= 0 {
		return ctx, stop
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	return ctx, func() {
		cancel()
		stop()
	}
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "new managed corpus root (default: $MOEDEX_CORPUS or ~/"+corpus.DefaultCorpusDirName+")")
	groupsPath := fs.String("groups", "", "group allowlist file (default: built-in curated list)")
	concurrency := fs.Int("concurrency", corpus.DefaultConcurrency(), "max parallel Git operations (Moe's tentacles)")
	timeout := fs.Duration("timeout", defaultInitTimeout, "give up the whole run after this long (0 disables the deadline); also stops cleanly on SIGINT/SIGTERM")
	noBanner := fs.Bool("no-banner", false, "suppress the Moe banner")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*noBanner {
		fmt.Fprint(os.Stderr, moeBanner)
	}

	root, err := corpus.ResolveRoot(*corpusDir)
	if err != nil {
		return err
	}
	groups, err := resolveGroups(*groupsPath)
	if err != nil {
		return err
	}
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Groups: groups, Concurrency: *concurrency}
	ctx, stop := signalContext(*timeout)
	defer stop()
	r := corpus.ExecRunner{}
	if rep := corpus.Doctor(ctx, r, cfg); !rep.OK() {
		printReport(rep)
		return fmt.Errorf("setup not ready — fix the item(s) marked %s above, then re-run", markFail)
	}
	projects, err := corpus.Enumerate(ctx, r, cfg)
	if err != nil {
		return fmt.Errorf("enumerate projects: %w", err)
	}
	result, err := corpus.InitManaged(ctx, r, cfg, projects)
	if err != nil {
		return err
	}
	fmt.Printf("initialized managed corpus at %s with %d locked project(s) (snapshot %s)\n",
		result.Root, len(result.Lock.Projects), shortObjectID(result.SuperprojectCommit))
	return nil
}

// moeBanner is Moe, the eight-tentacled corpus wrangler. Printed to stderr so it
// never corrupts a piped/redirected stdout (e.g. `groups` output).
const moeBanner = `
        .-∩-.
     .-'     '-.
    (  -     -  )      m o e  ·  moedex corpus wrangler
     |    ‿    |       host: ` + corpus.DefaultHost + `
      '-.___.-'
      /  | |  \        "many tentacles, one clean mirror."
    ~~  ~  ~  ~~
`

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "corpus root (default: $MOEDEX_CORPUS or ~/"+corpus.DefaultCorpusDirName+")")
	groupsPath := fs.String("groups", "", "group allowlist file (default: built-in curated list)")
	timeout := fs.Duration("timeout", defaultDoctorTimeout, "give up the whole run after this long (0 disables the deadline); also stops cleanly on SIGINT/SIGTERM")
	noBanner := fs.Bool("no-banner", false, "suppress the Moe banner")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*noBanner {
		fmt.Fprint(os.Stderr, moeBanner)
	}

	root, err := corpus.ResolveRoot(*corpusDir)
	if err != nil {
		return err
	}
	groups, err := resolveGroups(*groupsPath)
	if err != nil {
		return err
	}
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Groups: groups, Concurrency: corpus.DefaultConcurrency()}

	ctx, stop := signalContext(*timeout)
	defer stop()
	rep := corpus.Doctor(ctx, corpus.ExecRunner{}, cfg)
	printReport(rep)
	if !rep.OK() {
		return fmt.Errorf("preflight failed — fix the item(s) marked %s above, then re-run `moedex-corpus doctor`", markFail)
	}
	return nil
}

const (
	markOK   = "✓"
	markFail = "✗"
	markSkip = "–"
)

// printReport renders a doctor Report to stdout: a line per check, the fix hint
// under any failure, then the host/root and projected clone count.
func printReport(rep corpus.Report) {
	for _, c := range rep.Checks {
		mark := markSkip
		switch c.Status {
		case corpus.StatusOK:
			mark = markOK
		case corpus.StatusFail:
			mark = markFail
		}
		fmt.Printf("  %s  %-32s %s\n", mark, c.Name, c.Detail)
		if c.Status == corpus.StatusFail && c.Fix != "" {
			fmt.Printf("        ↳ fix: %s\n", c.Fix)
		}
	}
	fmt.Println()
	fmt.Printf("  host:   %s\n", rep.Host)
	fmt.Printf("  corpus: %s\n", rep.Root)
	if rep.ProjectedKnown {
		fmt.Printf("  repos:  %d project(s) in scope — Moe is ready to clone them.\n", rep.Projected)
	}
	fmt.Println()
	if rep.OK() {
		fmt.Println("  all clear — Moe has everything he needs.")
	}
}

// resolveGroups returns the allowlist to curate by: the file at path when given,
// else the built-in curated default. Shared by doctor and clone so both scope to
// the same set. An explicit but EMPTY file is rejected — a clone with no
// allowlist would fan out across every visible project, which is never intended.
func resolveGroups(path string) ([]string, error) {
	if path == "" {
		return corpus.DefaultGroups(), nil
	}
	groups, err := corpus.LoadGroups(path)
	if err != nil {
		return nil, fmt.Errorf("load groups %s: %w", path, err)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("group allowlist %s is empty — refusing to clone every visible project; add groups or omit -groups for the built-in default", path)
	}
	return groups, nil
}

// reindexFlags are the shared -reindex options registered on both clone and sync.
type reindexFlags struct {
	enabled    *bool
	casDir     *string
	shardDir   *string
	shardBytes *int64
	indexBin   *string
	reload     *string
}

func addReindexFlags(fs *flag.FlagSet) reindexFlags {
	return reindexFlags{
		enabled:    fs.Bool("reindex", false, "after the git step, rebuild served shards (cas-build/refresh -> cas-export -deduped) and reload"),
		casDir:     fs.String("cas-dir", "", "content-addressable store dir (required with -reindex)"),
		shardDir:   fs.String("shard-dir", "", "served deduped shard dir for moedex-serve (required with -reindex)"),
		shardBytes: fs.Int64("shard-bytes", 0, "target bytes per exported shard (0 = moedex-index default)"),
		indexBin:   fs.String("index-bin", "moedex", "unified moedex binary to drive"),
		reload:     fs.String("reload", "", "command to reload the daemon after export, e.g. 'systemctl reload moedex-serve' (empty = skip)"),
	}
}

// validateReindexFlags checks the -reindex flag triple is well-formed. Call it
// immediately after fs.Parse in runClone/runSync so a misconfigured -reindex
// fails fast, before a (potentially long) clone/sync run, rather than only
// being caught by maybeReindex afterward.
func validateReindexFlags(rf reindexFlags) error {
	if !*rf.enabled {
		return nil
	}
	if *rf.casDir == "" || *rf.shardDir == "" {
		return fmt.Errorf("-reindex requires -cas-dir and -shard-dir")
	}
	return nil
}

// maybeReindex runs the per-blob-delta reindex chain when -reindex was given.
// Shared by clone (first run → cas-build) and sync (steady state → cas-refresh).
func maybeReindex(ctx context.Context, r corpus.Runner, cfg corpus.Config, rf reindexFlags) error {
	if err := validateReindexFlags(rf); err != nil {
		return err
	}
	if !*rf.enabled {
		return nil
	}
	opts := corpus.ReindexOptions{
		Root:       cfg.Root,
		CASDir:     *rf.casDir,
		ShardDir:   *rf.shardDir,
		ShardBytes: *rf.shardBytes,
		IndexBin:   *rf.indexBin,
	}
	if strings.TrimSpace(*rf.reload) != "" {
		opts.Reload = strings.Fields(*rf.reload)
	}

	fmt.Println()
	fmt.Println("Moe is re-indexing the corpus (per-blob delta)...")
	rep, err := corpus.Reindex(ctx, r, opts)
	for _, s := range rep.Steps {
		fmt.Printf("  • %s\n", s.Name)
		for _, ln := range strings.Split(s.Output, "\n") {
			if strings.TrimSpace(ln) != "" {
				fmt.Printf("      %s\n", ln)
			}
		}
	}
	if err != nil {
		return err
	}
	if rep.Reloaded {
		fmt.Println("  ↻ daemon reloaded — fresh shards are live.")
	} else {
		fmt.Println("  (shards updated; reload the daemon to serve them — pass -reload)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// clone
// ---------------------------------------------------------------------------

func runClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "corpus root (default: $MOEDEX_CORPUS or ~/"+corpus.DefaultCorpusDirName+")")
	groupsPath := fs.String("groups", "", "group allowlist file (default: built-in curated list)")
	concurrency := fs.Int("concurrency", corpus.DefaultConcurrency(), "max parallel clones (Moe's tentacles)")
	dryRun := fs.Bool("dry-run", false, "list what would be cloned/skipped without running git")
	timeout := fs.Duration("timeout", defaultCloneTimeout, "give up the whole run after this long (0 disables the deadline); also stops cleanly on SIGINT/SIGTERM")
	noBanner := fs.Bool("no-banner", false, "suppress the Moe banner")
	rf := addReindexFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateReindexFlags(rf); err != nil {
		return err
	}
	if !*noBanner {
		fmt.Fprint(os.Stderr, moeBanner)
	}

	root, err := corpus.ResolveRoot(*corpusDir)
	if err != nil {
		return err
	}
	groups, err := resolveGroups(*groupsPath)
	if err != nil {
		return err
	}
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Groups: groups, Concurrency: *concurrency}

	ctx, stop := signalContext(*timeout)
	defer stop()
	r := corpus.ExecRunner{}

	// Fail fast on a broken setup — the same checks as `doctor`.
	if rep := corpus.Doctor(ctx, r, cfg); !rep.OK() {
		printReport(rep)
		return fmt.Errorf("setup not ready — fix the item(s) marked %s above, then re-run", markFail)
	}

	projects, err := corpus.Enumerate(ctx, r, cfg)
	if err != nil {
		return fmt.Errorf("enumerate projects: %w", err)
	}
	fmt.Printf("Moe found %d curated project(s) for %s.\n", len(projects), root)

	if *dryRun {
		var nNew, nHave int
		for _, p := range projects {
			if cfg.HasClone(p) {
				nHave++
			} else {
				nNew++
			}
		}
		fmt.Printf("dry-run: would clone %d new, skip %d already present (git not run).\n", nNew, nHave)
		return nil
	}

	total := len(projects)
	tentacles := displayTentacles(cfg.Concurrency, total)
	fmt.Printf("Moe reaches out with %d tentacle(s)...\n", tentacles)

	var mu sync.Mutex
	var done int
	progress := func(res corpus.CloneResult) {
		mu.Lock()
		defer mu.Unlock()
		done++
		fmt.Print(progressLine(done, total, res))
	}

	report := corpus.CloneProjects(ctx, r, cfg, projects, progress)
	printCloneSummary(report)
	if err := maybeReindex(ctx, r, cfg, rf); err != nil {
		return err
	}
	if report.Failed > 0 {
		return fmt.Errorf("%d repo(s) failed to clone (see %s above) — re-run to retry; already-cloned repos are skipped", report.Failed, markFail)
	}
	return nil
}

// displayTentacles mirrors the worker-count clamp CloneProjects applies
// internally (corpus.Config.Concurrency, floored at 1 and capped at total), so
// the printed "N tentacle(s)" line always matches the actual parallelism used
// — including for a -concurrency of 0 or negative, which CloneProjects clamps
// to 1 worker but this line previously printed verbatim.
func displayTentacles(concurrency, total int) int {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > total && total > 0 {
		concurrency = total
	}
	return concurrency
}

// progressLine formats one live-progress line for a finished clone result. The
// default case exists so a future CloneOutcome this switch wasn't updated for
// still produces a visible line instead of silently vanishing from live output.
func progressLine(done, total int, res corpus.CloneResult) string {
	switch res.Outcome {
	case corpus.Cloned:
		return fmt.Sprintf("  [%d/%d] 🦑 cloned   %s\n", done, total, res.Project.PathWithNamespace)
	case corpus.Skipped:
		return fmt.Sprintf("  [%d/%d]  · present  %s\n", done, total, res.Project.PathWithNamespace)
	case corpus.Empty:
		return fmt.Sprintf("  [%d/%d]  · empty    %s (no commits — skipped)\n", done, total, res.Project.PathWithNamespace)
	case corpus.Failed:
		return fmt.Sprintf("  [%d/%d] %s FAILED   %s — %s\n", done, total, markFail, res.Project.PathWithNamespace, failDetail(res))
	default:
		return fmt.Sprintf("  [%d/%d] %s UNKNOWN  %s (outcome=%d)\n", done, total, markFail, res.Project.PathWithNamespace, res.Outcome)
	}
}

// printCloneSummary prints the end-of-run tally and lists any failures.
func printCloneSummary(rep corpus.CloneReport) {
	fmt.Println()
	fmt.Printf("Moe is done: %d cloned, %d already present, %d empty (skipped), %d failed.\n", rep.Cloned, rep.Skipped, rep.Empty, rep.Failed)
	if fails := rep.Failures(); len(fails) > 0 {
		fmt.Println("failed repos (carried over for the next run):")
		for _, f := range fails {
			fmt.Printf("  %s %s — %s\n", markFail, f.Project.PathWithNamespace, failDetail(f))
		}
	}
}

// failDetail picks the most useful one-line reason for a failed clone.
func failDetail(res corpus.CloneResult) string {
	if res.Detail != "" {
		return res.Detail
	}
	if res.Err != nil {
		return res.Err.Error()
	}
	return "unknown error"
}

// ---------------------------------------------------------------------------
// sync
// ---------------------------------------------------------------------------

func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "corpus root (default: $MOEDEX_CORPUS or ~/"+corpus.DefaultCorpusDirName+")")
	groupsPath := fs.String("groups", "", "group allowlist file (default: built-in curated list)")
	concurrency := fs.Int("concurrency", corpus.DefaultConcurrency(), "max parallel git operations (Moe's tentacles)")
	prune := fs.Bool("prune", false, "remove local repos that are gone from the server (default: keep + report)")
	dryRun := fs.Bool("dry-run", false, "show the plan (clone/update/missing) without running git")
	timeout := fs.Duration("timeout", defaultSyncTimeout, "give up the whole run after this long (0 disables the deadline); also stops cleanly on SIGINT/SIGTERM")
	noBanner := fs.Bool("no-banner", false, "suppress the Moe banner")
	rf := addReindexFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateReindexFlags(rf); err != nil {
		return err
	}
	if !*noBanner {
		fmt.Fprint(os.Stderr, moeBanner)
	}

	root, err := corpus.ResolveRoot(*corpusDir)
	if err != nil {
		return err
	}
	groups, err := resolveGroups(*groupsPath)
	if err != nil {
		return err
	}
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Groups: groups, Concurrency: *concurrency}

	ctx, stop := signalContext(*timeout)
	defer stop()
	r := corpus.ExecRunner{}

	if rep := corpus.Doctor(ctx, r, cfg); !rep.OK() {
		printReport(rep)
		return fmt.Errorf("setup not ready — fix the item(s) marked %s above, then re-run", markFail)
	}

	projects, err := corpus.Enumerate(ctx, r, cfg)
	if err != nil {
		return fmt.Errorf("enumerate projects: %w", err)
	}

	managed, err := corpus.IsManagedRoot(root)
	if err != nil {
		return err
	}
	if managed {
		return runManagedSync(ctx, r, cfg, projects, *prune, *dryRun, rf)
	}

	if *dryRun {
		plan, err := corpus.PlanSync(cfg, projects)
		if err != nil {
			return err
		}
		fmt.Printf("Moe found %d curated project(s) for %s.\n", len(projects), root)
		missingAction := "kept + reported"
		if *prune {
			missingAction = "PRUNED"
		}
		fmt.Printf("dry-run: would clone %d new, re-check %d existing, and %d missing would be %s (git not run).\n",
			len(plan.ToClone), len(plan.ToUpdate), len(plan.Missing), missingAction)
		return nil
	}

	total := len(projects)
	fmt.Printf("Moe is syncing %d curated project(s) for %s...\n", total, root)

	var mu sync.Mutex
	var done int
	progress := func(res corpus.SyncResult) {
		mu.Lock()
		defer mu.Unlock()
		done++
		switch res.Outcome {
		case corpus.SyncCloned:
			fmt.Printf("  🦑 cloned   %s\n", res.Path)
		case corpus.SyncUpdated:
			fmt.Printf("  ↑ updated  %s\n", res.Path)
		case corpus.SyncPruned:
			fmt.Printf("  🗑 pruned   %s\n", res.Path)
		case corpus.SyncMissing:
			fmt.Printf("  ? missing  %s (gone on server; kept — use -prune to remove)\n", res.Path)
		case corpus.SyncFailed:
			fmt.Printf("  %s FAILED   %s — %s\n", markFail, res.Path, syncDetail(res))
		}
		// SyncCurrent is intentionally quiet — no news is good news for a freshness loop.
	}

	report, err := corpus.SyncProjects(ctx, r, cfg, projects, *prune, progress)
	if err != nil {
		return err
	}
	printSyncSummary(report)
	if err := maybeReindex(ctx, r, cfg, rf); err != nil {
		return err
	}
	if report.Failed > 0 {
		return fmt.Errorf("%d repo(s) failed to sync (see %s above) — re-run to retry", report.Failed, markFail)
	}
	return nil
}

func runManagedSync(ctx context.Context, r corpus.Runner, cfg corpus.Config, projects []corpus.Project, prune, dryRun bool, rf reindexFlags) error {
	catalog, err := corpus.LoadCatalog(cfg.Root)
	if err != nil {
		return err
	}
	lock, err := corpus.LoadLock(cfg.Root, catalog.Host)
	if err != nil {
		return err
	}
	opts := corpus.ManagedSyncOptions{EnumerationComplete: true, Prune: prune}
	if dryRun {
		plan := corpus.ReconcileManaged(catalog.Host, lock, projects, opts)
		fmt.Printf("managed dry-run for %s: %d action(s), no mutation\n", cfg.Root, len(plan.Actions))
		for _, action := range plan.Actions {
			fmt.Printf("  %-14s project=%d path=%s%s\n", action.Kind, action.ProjectID, managedActionPath(action), managedActionReason(action))
		}
		return nil
	}

	fmt.Printf("Moe is syncing %d curated project(s) into managed corpus %s...\n", len(projects), cfg.Root)
	result, syncErr := corpus.SyncManaged(ctx, r, cfg, projects, opts)
	for _, action := range result.Applied {
		fmt.Printf("  %-14s project=%d path=%s%s\n", action.Kind, action.ProjectID, managedActionPath(action), managedActionReason(action))
	}
	if result.Changed {
		fmt.Printf("managed snapshot committed: %s\n", shortObjectID(result.SuperprojectCommit))
	} else {
		fmt.Println("managed snapshot already current")
	}
	if syncErr != nil {
		return syncErr
	}
	return maybeReindex(ctx, r, cfg, rf)
}

func managedActionPath(action corpus.ManagedAction) string {
	if action.Project != nil {
		return action.Project.PathWithNamespace
	}
	if action.Previous != nil {
		return action.Previous.PathWithNamespace
	}
	return "-"
}

func managedActionReason(action corpus.ManagedAction) string {
	if action.Reason == "" {
		return ""
	}
	return " reason=" + action.Reason
}

func shortObjectID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// printSyncSummary prints the end-of-run tally and lists failures and (when kept)
// missing repos for the operator to review.
func printSyncSummary(rep corpus.SyncReport) {
	fmt.Println()
	fmt.Printf("Moe is done: %d cloned, %d updated, %d already current, %d pruned, %d missing, %d failed.\n",
		rep.Cloned, rep.Updated, rep.Current, rep.Pruned, rep.Missing, rep.Failed)
	if miss := rep.MissingRepos(); len(miss) > 0 {
		fmt.Println("gone on server (kept — re-run with -prune to remove):")
		for _, m := range miss {
			fmt.Printf("  ? %s\n", m.Path)
		}
	}
	if fails := rep.Failures(); len(fails) > 0 {
		fmt.Println("failed repos (carried over for the next run):")
		for _, f := range fails {
			fmt.Printf("  %s %s — %s\n", markFail, f.Path, syncDetail(f))
		}
	}
}

// syncDetail picks the most useful one-line reason for a failed sync op.
func syncDetail(res corpus.SyncResult) string {
	if res.Detail != "" {
		return res.Detail
	}
	if res.Err != nil {
		return res.Err.Error()
	}
	return "unknown error"
}

// ---------------------------------------------------------------------------
// groups
// ---------------------------------------------------------------------------

func runGroups(args []string) error {
	fs := flag.NewFlagSet("groups", flag.ContinueOnError)
	fromDisk := fs.Bool("from-disk", false, "derive the allowlist from the top-level dirs of the corpus root")
	corpusDir := fs.String("corpus", "", "corpus root (default: $MOEDEX_CORPUS or ~/"+corpus.DefaultCorpusDirName+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*fromDisk {
		return fmt.Errorf("groups currently supports only --from-disk")
	}
	root, err := corpus.ResolveRoot(*corpusDir)
	if err != nil {
		return err
	}
	groups, err := corpus.GroupsFromDisk(root)
	if err != nil {
		return fmt.Errorf("read groups from %s: %w", root, err)
	}
	// Header to stderr (not stdout) so the stdout stream is a clean, redirectable
	// allowlist file.
	fmt.Fprintf(os.Stderr, "# %d top-level group(s) derived from %s\n", len(groups), root)
	fmt.Println("# moedex-corpus group allowlist — top-level GitLab namespaces to mirror.")
	fmt.Println("# One group per line; '#' comments and blank lines are ignored.")
	fmt.Println("# Regenerate from an existing mirror with: moedex-corpus groups --from-disk")
	for _, g := range groups {
		fmt.Println(g)
	}
	return nil
}
