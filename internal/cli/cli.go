// Package cli owns the human-facing command tree. Engine and application
// packages must not import it.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"moedex/internal/app/corpuscmd"
	"moedex/internal/app/indexcmd"
	"moedex/internal/app/mcpcmd"
	"moedex/internal/app/navcmd"
	"moedex/internal/app/paritycmd"
	"moedex/internal/app/scalecmd"
	"moedex/internal/app/searchcmd"
	"moedex/internal/app/servecmd"
	configpkg "moedex/internal/config"
	"moedex/internal/embed"
	eventpkg "moedex/internal/event"
	"moedex/internal/tui"
	"moedex/internal/version"
)

// ExitError marks command-line/configuration errors, which use exit status 2.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// Execute runs either the semantic moedex command tree or an argv[0]
// compatibility surface. args must include argv[0].
func Execute(ctx context.Context, args []string) int {
	if len(args) == 0 {
		args = []string{"moedex"}
	}
	name := filepath.Base(args[0])
	if name != "moedex" && name != "moedex.exe" {
		if run, ok := legacyEntrypoints[name]; ok {
			fmt.Fprintf(os.Stderr, "%s is deprecated; use moedex (compatibility ends after two releases)\n", name)
			withArgs(name, args[1:], run)
			return 0
		}
	}
	commandArgs := normalizeGlobalFlags(args[1:])
	for _, arg := range commandArgs {
		if arg == "--no-color" {
			_ = os.Setenv("NO_COLOR", "1")
			break
		}
	}

	root, cleanup := newRoot()
	defer cleanup()
	root.SetArgs(commandArgs)
	err := fang.Execute(ctx, root, fang.WithVersion(version.Line("moedex", embed.ONNXCompiled)))
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return 1
}

// NewRoot creates a fresh command tree for tests and embedding.
func NewRoot() *cobra.Command {
	root, _ := newRoot()
	return root
}

func newRoot() (*cobra.Command, func()) {
	registry := configpkg.DefaultRegistry()
	var configPath string
	var jsonOutput bool
	var noColor bool
	var state *configpkg.State
	var restore func()
	root := &cobra.Command{
		Use:           "moedex",
		Short:         "Local code search and agent context",
		Long:          "Moe indexes local repositories and serves ranked, token-budgeted context to humans and agents.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !jsonOutput && tui.IsInteractive(os.Stdin, os.Stdout) {
				indexDir, shardDir := publishedSource("", "")
				if indexDir == "" && shardDir == "" {
					return usageError("no published index configured; set MOEDEX_INDEX_DIR/MOEDEX_SHARD_DIR or run moedex doctor")
				}
				return tui.Run(cmd.Context(), tui.Options{IndexDir: indexDir, ShardDir: shardDir})
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "load an explicit KEY=VALUE configuration file")
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "emit machine-readable output where supported")
	root.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable color and terminal styling")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if noColor {
			_ = os.Setenv("NO_COLOR", "1")
		}
		resolved, warnings, err := registry.Resolve(os.Environ(), configPath)
		if err != nil {
			return &ExitError{Code: 2, Err: fmt.Errorf("config: %w", err)}
		}
		for _, warning := range warnings {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
		}
		restoreEnv, err := resolved.Apply()
		if err != nil {
			return &ExitError{Code: 2, Err: fmt.Errorf("config: %w", err)}
		}
		state, restore = resolved, restoreEnv
		if jsonOutput {
			cmd.SetContext(eventpkg.WithSink(cmd.Context(), eventpkg.NewJSONSink(cmd.OutOrStdout())))
		} else {
			cmd.SetContext(eventpkg.WithSink(cmd.Context(), eventpkg.NewTextSink(cmd.ErrOrStderr())))
		}
		return nil
	}
	cleanup := func() {
		if restore != nil {
			restore()
			restore = nil
		}
	}
	root.PersistentPostRun = func(_ *cobra.Command, _ []string) { cleanup() }
	root.AddCommand(
		newVersionCommand(),
		newSearchCommand(),
		newIndexCommand(),
		newGraphCommand(),
		newSemanticCommand(),
		newCorpusCommand(),
		legacyCommand("serve", "Serve warm HTTP or MCP endpoints", "moedex-serve", servecmd.Main),
		newMCPCommand(),
		newNavCommand(),
		legacyCommand("parity", "Run the full retrieval parity harness", "moedex-parity", paritycmd.Main),
		legacyPrefixedCommand("doctor", "Diagnose corpus, index, and daemon health", "moedex-index", indexcmd.Main, "doctor"),
		newConfigCommand(&state, &jsonOutput),
	)
	return root, cleanup
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build identity",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.Line("moedex", embed.ONNXCompiled))
		},
	}
}

// normalizeGlobalFlags lets persistent shell flags appear before or after a
// semantic command. Delegated application commands intentionally retain their
// legacy flag parsers, so Cobra cannot discover persistent flags after them.
// Do not inspect anything after --; it belongs to the delegated command.
func normalizeGlobalFlags(args []string) []string {
	var globals, rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case arg == "--json", arg == "--no-color",
			strings.HasPrefix(arg, "--json="), strings.HasPrefix(arg, "--no-color="):
			globals = append(globals, arg)
		case strings.HasPrefix(arg, "--config="):
			globals = append(globals, arg)
		case arg == "--config" && i+1 < len(args):
			globals = append(globals, arg, args[i+1])
			i++
		default:
			rest = append(rest, arg)
		}
	}
	return append(globals, rest...)
}

func newConfigCommand(state **configpkg.State, jsonOutput *bool) *cobra.Command {
	var diff bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show effective configuration and provenance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if *state == nil {
				return errors.New("configuration state was not initialized")
			}
			values := (*state).Values(diff)
			if *jsonOutput {
				return configpkg.WriteJSON(cmd.OutOrStdout(), values)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d settings registered · showing %d\n\n", len(configpkg.DefaultRegistry().Settings()), len(values))
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			for _, value := range values {
				detail := string(value.Source)
				if value.SourceDetail != "" {
					detail += " " + value.SourceDetail
				}
				fmt.Fprintf(writer, "%s\t%s\t%s\n", value.Name, value.Value, detail)
			}
			return writer.Flush()
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false, "show only non-default values")
	return cmd
}

func newSearchCommand() *cobra.Command {
	var repo, indexDir, shardDir string
	var regex, tuiMode bool
	var limit, tokenBudget, topK, graphDepth int
	cmd := &cobra.Command{
		Use:   "search [PATTERN]",
		Short: "Search a published index",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo != "" && (indexDir != "" || shardDir != "") {
				return usageError("--repo is mutually exclusive with --index-dir and --shard-dir")
			}
			if indexDir != "" && shardDir != "" {
				return usageError("--index-dir and --shard-dir are mutually exclusive")
			}
			if tuiMode || len(args) == 0 {
				if repo != "" {
					return usageError("interactive search requires a published index; --repo is not supported with --tui")
				}
				if !tui.IsInteractive(os.Stdin, os.Stdout) {
					return usageError("interactive search requires a TTY; supply PATTERN for non-interactive search")
				}
				indexDir, shardDir = publishedSource(indexDir, shardDir)
				if indexDir == "" && shardDir == "" {
					return usageError("no published index configured; set MOEDEX_INDEX_DIR/MOEDEX_SHARD_DIR or run moedex doctor")
				}
				initial := ""
				if len(args) == 1 {
					initial = args[0]
				}
				return tui.Run(cmd.Context(), tui.Options{
					IndexDir: indexDir, ShardDir: shardDir, InitialQuery: initial,
					TokenBudget: tokenBudget, TopK: topK, GraphDepth: graphDepth,
				})
			}
			if repo != "" {
				legacyArgs := []string{"-repo", repo}
				if regex {
					legacyArgs = append(legacyArgs, "-regex")
				}
				legacyArgs = append(legacyArgs, args[0])
				withArgs("moedex", legacyArgs, searchcmd.Main)
				return nil
			}
			indexDir, shardDir = publishedSource(indexDir, shardDir)
			if indexDir == "" && shardDir == "" {
				return usageError("no published index configured; set MOEDEX_INDEX_DIR/MOEDEX_SHARD_DIR or run moedex doctor")
			}
			legacyArgs := []string{"-q", args[0]}
			if indexDir != "" {
				legacyArgs = append(legacyArgs, "-index-dir", indexDir)
			} else {
				legacyArgs = append(legacyArgs, "-shard-dir", shardDir)
			}
			if regex {
				legacyArgs = append(legacyArgs, "-regex")
			}
			if limit != 0 {
				legacyArgs = append(legacyArgs, "-limit", fmt.Sprint(limit))
			}
			withArgs("moedex-serve", legacyArgs, servecmd.Main)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "index and search this repository in memory")
	cmd.Flags().StringVar(&indexDir, "index-dir", "", "published snapshot root containing CURRENT")
	cmd.Flags().StringVar(&shardDir, "shard-dir", "", "directory of prebuilt shards")
	cmd.Flags().BoolVar(&regex, "regex", false, "treat PATTERN as a regular expression")
	cmd.Flags().IntVar(&limit, "limit", 0, "cap matching lines (0 means no cap)")
	cmd.Flags().BoolVar(&tuiMode, "tui", false, "open the interactive terminal search UI")
	cmd.Flags().IntVar(&tokenBudget, "token-budget", 0, "interactive context-window token budget")
	cmd.Flags().IntVar(&topK, "top-k", 0, "interactive ranked candidate count")
	cmd.Flags().IntVar(&graphDepth, "graph-depth", 1, "interactive graph annotation depth")
	return cmd
}

func publishedSource(indexDir, shardDir string) (string, string) {
	if indexDir != "" || shardDir != "" {
		return indexDir, shardDir
	}
	if indexDir = os.Getenv("MOEDEX_INDEX_DIR"); indexDir != "" {
		return indexDir, ""
	}
	return "", os.Getenv("MOEDEX_SHARD_DIR")
}

func newIndexCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "index", Short: "Build, refresh, and inspect published indexes"}
	for _, verb := range []string{"build", "check", "refresh"} {
		cmd.AddCommand(legacyPrefixedContextCommand(verb, title(verb)+" an index", "moedex-index", indexcmd.MainContext, verb))
	}
	cas := &cobra.Command{Use: "cas", Short: "Manage the content-addressable store"}
	for _, verb := range []string{"build", "refresh", "export", "compact"} {
		cas.AddCommand(legacyPrefixedContextCommand(verb, title(verb)+" CAS data", "moedex-index", indexcmd.MainContext, "cas-"+verb))
	}
	snapshot := &cobra.Command{Use: "snapshot", Short: "Manage immutable index generations"}
	for _, verb := range []string{"list", "inspect", "rollback", "migrate", "build"} {
		snapshot.AddCommand(legacyPrefixedContextCommand(verb, title(verb)+" snapshots", "moedex-index", indexcmd.MainContext, "snapshot-"+verb))
	}
	cmd.AddCommand(cas, snapshot)
	return cmd
}

func newGraphCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "graph", Short: "Build and audit the code graph"}
	cmd.AddCommand(
		legacyPrefixedContextCommand("build", "Build or refresh the graph sidecar", "moedex-index", indexcmd.MainContext, "graph"),
		legacyPrefixedContextCommand("audit", "Audit graph candidates and resolution", "moedex-index", indexcmd.MainContext, "graph-audit"),
	)
	return cmd
}

func newCorpusCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "corpus", Short: "Set up and synchronize managed repositories"}
	for _, verb := range []string{"doctor", "init", "clone", "sync", "groups"} {
		cmd.AddCommand(legacyPrefixedCommand(verb, title(verb)+" the managed corpus", "moedex-corpus", corpuscmd.Main, verb))
	}
	cmd.AddCommand(legacyCommand("scale", "Measure corpus indexing scale", "scale", scalecmd.Main))
	return cmd
}

func newMCPCommand() *cobra.Command {
	cmd := legacyCommand("mcp", "Serve MCP over stdio", "moedex-serve", servecmd.Main)
	original := cmd.Run
	cmd.Run = func(cmd *cobra.Command, args []string) {
		for _, arg := range args {
			if arg == "-repo" || arg == "--repo" || strings.HasPrefix(arg, "-repo=") || strings.HasPrefix(arg, "--repo=") {
				withArgs("moedex-mcp", args, mcpcmd.Main)
				return
			}
		}
		args = append([]string{"-mcp"}, args...)
		original(cmd, args)
	}
	return cmd
}

func newNavCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "nav", Short: "Navigate definitions, references, and implementations"}
	for _, verb := range []string{"def", "refs", "impl"} {
		cmd.AddCommand(legacyPrefixedCommand(verb+" FILE:LINE:COL", "Navigate to "+verb, "moedex-nav", navcmd.Main, "-verb", verb))
	}
	return cmd
}

func legacyCommand(use, short, binary string, run func()) *cobra.Command {
	return legacyPrefixedCommand(use, short, binary, run)
}

func legacyPrefixedCommand(use, short, binary string, run func(), prefix ...string) *cobra.Command {
	return legacyPrefixedContextCommand(use, short, binary, func(context.Context) { run() }, prefix...)
}

func legacyPrefixedContextCommand(use, short, binary string, run func(context.Context), prefix ...string) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			jsonMode, _ := cmd.Root().PersistentFlags().GetBool("json")
			started := time.Now()
			_ = eventpkg.Emit(cmd.Context(), eventpkg.Event{
				Command: cmd.CommandPath(), Phase: cmd.Name(), State: "started", Message: "started",
			})
			withArgsContextMode(cmd.Context(), binary, append(append([]string{}, prefix...), args...), run, jsonMode)
			_ = eventpkg.Emit(cmd.Context(), eventpkg.Event{
				Command: cmd.CommandPath(), Phase: cmd.Name(), State: "completed", Message: "completed",
				Fields: map[string]any{"elapsed_ms": time.Since(started).Milliseconds()},
			})
		},
	}
}

func withArgs(binary string, args []string, run func()) {
	withArgsMode(binary, args, run, false)
}

func withArgsMode(binary string, args []string, run func(), redirectStdout bool) {
	withArgsContextMode(context.Background(), binary, args, func(context.Context) { run() }, redirectStdout)
}

func withArgsContextMode(ctx context.Context, binary string, args []string, run func(context.Context), redirectStdout bool) {
	previous := os.Args
	previousStdout := os.Stdout
	os.Args = append([]string{binary}, args...)
	if redirectStdout {
		os.Stdout = os.Stderr
	}
	defer func() {
		os.Args = previous
		os.Stdout = previousStdout
	}()
	run(ctx)
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func usageError(message string) error {
	return &ExitError{Code: 2, Err: errors.New(message)}
}

var legacyEntrypoints = map[string]func(){
	"moedex":        searchcmd.Main,
	"moedex-index":  indexcmd.Main,
	"moedex-serve":  servecmd.Main,
	"moedex-mcp":    mcpcmd.Main,
	"moedex-corpus": corpuscmd.Main,
	"moedex-parity": paritycmd.Main,
	"moedex-nav":    navcmd.Main,
	"scale":         scalecmd.Main,
}
