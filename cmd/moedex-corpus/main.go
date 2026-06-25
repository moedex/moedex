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
//	moedex-corpus clone               # shallow-clone the curated corpus, many repos at once
//	moedex-corpus groups --from-disk  # regenerate the group allowlist from an existing mirror
//
// Meet Moe, moedex's eight-tentacled mascot — the corpus wrangler who reaches out
// with many tentacles at once to clone the whole corpus in parallel.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	"moedex/internal/corpus"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "clone":
		err = runClone(os.Args[2:])
	case "groups":
		err = runGroups(os.Args[2:])
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
  moedex-corpus doctor [-corpus DIR] [-groups FILE] [-no-banner]
      Preflight the setup: is glab installed? authenticated to `+corpus.DefaultHost+` only?
      is git installed? If reachable, report how many repos a clone would pull.

  moedex-corpus clone [-corpus DIR] [-groups FILE] [-concurrency N] [-dry-run] [-no-banner]
      Shallow-clone (--depth 1) every curated project into the corpus tree, many
      at once. Idempotent: existing repos are skipped (freshen them with sync).
      Without -groups, the built-in curated allowlist is used.

  moedex-corpus groups --from-disk [-corpus DIR]
      Print the group allowlist derived from the top-level dirs of an existing
      mirror (one group per line, to stdout) — commit it as the default list.

The tool only ever talks to `+corpus.DefaultHost+`.
`)
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

	rep := corpus.Doctor(context.Background(), corpus.ExecRunner{}, cfg)
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

// ---------------------------------------------------------------------------
// clone
// ---------------------------------------------------------------------------

func runClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	corpusDir := fs.String("corpus", "", "corpus root (default: $MOEDEX_CORPUS or ~/"+corpus.DefaultCorpusDirName+")")
	groupsPath := fs.String("groups", "", "group allowlist file (default: built-in curated list)")
	concurrency := fs.Int("concurrency", corpus.DefaultConcurrency(), "max parallel clones (Moe's tentacles)")
	dryRun := fs.Bool("dry-run", false, "list what would be cloned/skipped without running git")
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

	ctx := context.Background()
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
	tentacles := cfg.Concurrency
	if tentacles > total {
		tentacles = total
	}
	fmt.Printf("Moe reaches out with %d tentacle(s)...\n", tentacles)

	var mu sync.Mutex
	var done int
	progress := func(res corpus.CloneResult) {
		mu.Lock()
		defer mu.Unlock()
		done++
		switch res.Outcome {
		case corpus.Cloned:
			fmt.Printf("  [%d/%d] 🦑 cloned   %s\n", done, total, res.Project.PathWithNamespace)
		case corpus.Skipped:
			fmt.Printf("  [%d/%d]  · present  %s\n", done, total, res.Project.PathWithNamespace)
		case corpus.Failed:
			fmt.Printf("  [%d/%d] %s FAILED   %s — %s\n", done, total, markFail, res.Project.PathWithNamespace, failDetail(res))
		}
	}

	report := corpus.CloneProjects(ctx, r, cfg, projects, progress)
	printCloneSummary(report)
	if report.Failed > 0 {
		return fmt.Errorf("%d repo(s) failed to clone (see %s above) — re-run to retry; already-cloned repos are skipped", report.Failed, markFail)
	}
	return nil
}

// printCloneSummary prints the end-of-run tally and lists any failures.
func printCloneSummary(rep corpus.CloneReport) {
	fmt.Println()
	fmt.Printf("Moe is done: %d cloned, %d already present, %d failed.\n", rep.Cloned, rep.Skipped, rep.Failed)
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
