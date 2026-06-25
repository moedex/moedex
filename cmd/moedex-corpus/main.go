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
// Phase 1 ships two subcommands:
//
//	moedex-corpus doctor              # preflight: glab? authed to tcdevops only? git? projected repo count
//	moedex-corpus groups --from-disk  # regenerate the group allowlist from an existing mirror
//
// Meet Moe, moedex's eight-tentacled mascot — the corpus wrangler who (soon)
// reaches out with many tentacles at once to clone the whole corpus in parallel.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

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
	groupsPath := fs.String("groups", "", "group allowlist file (limits the projected clone count to these top-level groups)")
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
	cfg := corpus.Config{Host: corpus.DefaultHost, Root: root, Concurrency: corpus.DefaultConcurrency()}
	if *groupsPath != "" {
		groups, err := corpus.LoadGroups(*groupsPath)
		if err != nil {
			return fmt.Errorf("load groups %s: %w", *groupsPath, err)
		}
		cfg.Groups = groups
	}

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
