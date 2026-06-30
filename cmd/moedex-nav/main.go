//go:build lsp

// Command moedex-nav is the runnable face of the ADR 0017 LSP-precise navigation
// spike: type-resolved go-to-definition / find-references / find-implementations
// driven by a real language server out of process. It answers the queries the
// syntactic symbol sidecar (ADR 0008) cannot.
//
// It is built only with `-tags lsp` and is NOT part of the default pure-Go
// build. See ADR 0017 for why this is a spike, not a Serena replacement.
//
// It drives navigate.Pool (one server per module root, shared and restarted as
// needed) so overlay, notify, and the query all share one server and -stats can
// surface the Pool's lifecycle counters.
//
// Usage:
//
//	moedex-nav -verb def  internal/index/index.go:42:6
//	moedex-nav -verb refs internal/index/index.go:42:6 -json
//	moedex-nav -verb refs -decl=false foo.go:10:2
//	moedex-nav -lang typescript -verb def src/app.ts:88:14
//	moedex-nav -server pyright-langserver -verb impl m.py:5:7   (explicit server wins)
//	moedex-nav -verb refs -overlay edited.go internal/x/x.go:12:3   (nav over an unsaved buffer)
//	cat buf.go | moedex-nav -verb def -overlay - internal/x/x.go:12:3  (overlay from stdin)
//	moedex-nav -notify internal/a.go,internal/b.go -verb refs internal/a.go:9:5  (invalidate, then query)
//	moedex-nav -notify internal/a.go                              (standalone disk-edit signal, exits 0)
//	moedex-nav -verb def internal/x/x.go:12:3 -stats             (query + counter snapshot)
//
// LINE and COL are 1-based; COL is a byte column (utf-8 positions are negotiated
// with the server).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"moedex/internal/navigate"
)

// navResult is the cmd-local -json output schema. Kept here (not in
// internal/navigate) so the output shape is owned by the CLI.
type navResult struct {
	Verb    string          `json:"verb"`
	Query   string          `json:"query"` // FILE:LINE:COL as given (empty for standalone -notify/-stats)
	Server  string          `json:"server"`
	Lang    string          `json:"lang,omitempty"`
	Count   int             `json:"count"`
	Results []jsonLoc       `json:"results"`
	Stats   *navigate.Stats `json:"stats,omitempty"`
}

type jsonLoc struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	StartCol  int    `json:"start_col"`
	EndLine   int    `json:"end_line,omitempty"`
	EndCol    int    `json:"end_col,omitempty"`
}

func main() {
	verb := flag.String("verb", "def", "navigation verb: def | refs | impl")
	includeDecl := flag.Bool("decl", true, "for refs: include the declaration itself")
	server := flag.String("server", "", "language server command (overrides -lang); default resolved from -lang or file extension")
	lang := flag.String("lang", "", "language selector: go|typescript|python|rust|c (default: inferred from the query file extension)")
	root := flag.String("root", "", "workspace root fallback (Pool routes by module root per file; this only seeds cfg)")
	overlay := flag.String("overlay", "", "read overlay (unsaved-buffer) content from PATH ('-' = stdin) for the query file")
	notify := flag.String("notify", "", "comma-separated list of changed-on-disk paths to invalidate before the query")
	jsonOut := flag.Bool("json", false, "emit structured JSON instead of file:line:col lines")
	stats := flag.Bool("stats", false, "after the query (or standalone), print Pool counters")
	timeout := flag.Duration("timeout", 60*time.Second, "overall query timeout")
	flag.Parse()

	// A position arg is required for a query, but standalone -notify or -stats
	// invocations (no query) are allowed with NArg()==0.
	standalone := flag.NArg() == 0 && (*notify != "" || *stats)
	if flag.NArg() != 1 && !standalone {
		fmt.Fprintln(os.Stderr, "usage: moedex-nav -verb def|refs|impl FILE:LINE:COL")
		fmt.Fprintln(os.Stderr, "       moedex-nav -notify a.go,b.go   (standalone disk-edit signal)")
		os.Exit(2)
	}

	var at navigate.Pos
	var queryArg string
	if flag.NArg() == 1 {
		queryArg = flag.Arg(0)
		p, err := parsePos(queryArg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "moedex-nav:", err)
			os.Exit(2)
		}
		at = p
	}

	// Resolve launch mode: explicit -server pins one command; -lang forces a
	// registry spec; otherwise Config.Server is left empty so the Pool routes
	// per file through navigate's registry (the multi-language path).
	cfg, canonical := buildConfig(*lang, *server, at.File)
	cfg.RootDir = *root
	srvName := displayServer(cfg, at.File)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pool := navigate.NewPool(cfg)
	defer pool.Close()

	// Overlay (unsaved-buffer) content, applied to the query file before querying.
	if *overlay != "" {
		if at.File == "" {
			fmt.Fprintln(os.Stderr, "moedex-nav: -overlay needs a FILE:LINE:COL query arg naming the overlaid file")
			os.Exit(2)
		}
		content, err := readOverlay(*overlay)
		if err != nil {
			fmt.Fprintln(os.Stderr, "moedex-nav:", err)
			os.Exit(1)
		}
		if err := pool.SetOverlay(ctx, at.File, content); err != nil {
			fmt.Fprintln(os.Stderr, "moedex-nav:", err)
			os.Exit(1)
		}
		// Best-effort cleanup; the process exits anyway in one-shot mode.
		defer pool.DropOverlay(context.Background(), at.File)
	}

	// Disk-edit invalidation signal (dependency-free; an orchestrator knows what
	// it edited). May be combined with a query or used standalone.
	if *notify != "" {
		paths := splitCSV(*notify)
		if len(paths) > 0 {
			if err := pool.NotifyChanged(ctx, paths...); err != nil {
				fmt.Fprintln(os.Stderr, "moedex-nav:", err)
				os.Exit(1)
			}
		}
	}

	// Standalone -notify/-stats with no query: report stats if asked, exit 0.
	if flag.NArg() == 0 {
		if *stats {
			emitStats(pool.Stats(), *jsonOut, navResult{Verb: *verb, Server: srvName, Lang: canonical})
		}
		return
	}

	var locs []navigate.Location
	var err error
	switch *verb {
	case "def":
		locs, err = pool.Definition(ctx, at)
	case "refs":
		locs, err = pool.References(ctx, at, *includeDecl)
	case "impl":
		locs, err = pool.Implementations(ctx, at)
	default:
		fmt.Fprintf(os.Stderr, "moedex-nav: unknown verb %q (want def|refs|impl)\n", *verb)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "moedex-nav:", err)
		os.Exit(1)
	}

	if *jsonOut {
		res := navResult{
			Verb:    *verb,
			Query:   queryArg,
			Server:  srvName,
			Lang:    canonical,
			Count:   len(locs),
			Results: toJSONLocs(locs),
		}
		if *stats {
			s := pool.Stats()
			res.Stats = &s
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		if len(locs) == 0 {
			os.Exit(1)
		}
		return
	}

	// Plain mode: keep the exact backward-compatible file:line:col stdout format.
	for _, l := range locs {
		fmt.Printf("%s:%d:%d\n", l.File, l.Start.Line, l.Start.Col)
	}
	if *stats {
		s := pool.Stats()
		fmt.Fprintf(os.Stderr, "# stats: queries=%d spawns=%d restarts=%d evictions=%d live=%d\n",
			s.Queries, s.Spawns, s.Restarts, s.Evictions, s.Live)
	}
	if len(locs) == 0 {
		fmt.Fprintln(os.Stderr, "moedex-nav: no results")
		os.Exit(1)
	}
}

// emitStats prints a Pool snapshot for a standalone (no-query) invocation.
func emitStats(s navigate.Stats, jsonOut bool, base navResult) {
	if jsonOut {
		base.Stats = &s
		base.Results = []jsonLoc{}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(base)
		return
	}
	fmt.Fprintf(os.Stderr, "# stats: queries=%d spawns=%d restarts=%d evictions=%d live=%d\n",
		s.Queries, s.Spawns, s.Restarts, s.Evictions, s.Live)
}

// toJSONLocs converts navigate Locations to the cmd-local JSON shape.
func toJSONLocs(locs []navigate.Location) []jsonLoc {
	out := make([]jsonLoc, 0, len(locs))
	for _, l := range locs {
		jl := jsonLoc{
			File:      l.File,
			StartLine: l.Start.Line,
			StartCol:  l.Start.Col,
		}
		if l.End.Line != 0 || l.End.Col != 0 {
			jl.EndLine = l.End.Line
			jl.EndCol = l.End.Col
		}
		out = append(out, jl)
	}
	return out
}

// readOverlay reads overlay content from a path, or stdin when path is "-".
func readOverlay(path string) ([]byte, error) {
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading overlay from stdin: %w", err)
		}
		return b, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading overlay %q: %w", path, err)
	}
	return b, nil
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parsePos parses FILE:LINE:COL. COL is optional and defaults to 1.
//
// It peels off up to two trailing ":NUMBER" groups from the right rather than
// splitting the whole string on ':', so a path containing ':' before the
// LINE/COL suffix (e.g. a Windows drive letter like `C:\x.go:10:2`) still
// parses: only colons that introduce a purely-numeric trailing segment are
// treated as FILE/LINE/COL separators.
func parsePos(s string) (navigate.Pos, error) {
	rest := s
	var nums []int
	for len(nums) < 2 {
		idx := strings.LastIndexByte(rest, ':')
		if idx < 0 {
			break
		}
		n, err := strconv.Atoi(rest[idx+1:])
		if err != nil {
			break
		}
		nums = append([]int{n}, nums...)
		rest = rest[:idx]
	}
	if len(nums) == 0 || rest == "" {
		return navigate.Pos{}, fmt.Errorf("bad position %q, want FILE:LINE[:COL]", s)
	}
	col := 1
	if len(nums) == 2 {
		col = nums[1]
	}
	return navigate.Pos{File: rest, Line: nums[0], Col: col}, nil
}
