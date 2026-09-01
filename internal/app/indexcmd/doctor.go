package indexcmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	configpkg "moedex/internal/config"
	"moedex/internal/navigate"
	server "moedex/internal/serve"
	"moedex/internal/version"
)

// runDoctor is the new-machine / pre-refresh preflight: a READ-ONLY report on
// whether this install is sane. It checks the things that have actually bitten
// us — binary skew (the index-loss incident), shard-dir layout ambiguity (picking
// the wrong refresh), a stale/legacy dense sidecar, a missing or STALE graph
// sidecar (the silent failure mode when a build/refresh's best-effort graph
// rebuild fails and the daemon keeps serving a prior-generation graph), and
// daemon/timer health — and exits non-zero on a CRITICAL problem so the refresh
// script can abort before the destructive swap.
func runDoctor(args []string) error {
	fs := newFlagSet("doctor")
	shardDir := fs.String("shard-dir", os.Getenv("MOEDEX_SHARD_DIR"), "servable shard dir to inspect (default ~/.moedex-state/shards)")
	addr := fs.String("addr", envOrDefault("MOEDEX_MCP_HTTP_ADDR", "127.0.0.1:8081"), "daemon MCP-HTTP address to health-check")
	strict := fs.Bool("strict", false, "exit non-zero on warnings too, not just critical problems")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shardDir == "" {
		*shardDir = configpkg.DefaultShardDir()
	}

	d := &doctorReport{}
	checkBinaries(d)
	checkShardDir(d, *shardDir)
	checkDaemon(d, *addr)
	checkLaunchd(d)
	checkNavServers(d)
	d.print()

	if d.crit > 0 {
		return fmt.Errorf("doctor: %d critical problem(s) — fix before refreshing", d.crit)
	}
	if *strict && d.warn > 0 {
		return fmt.Errorf("doctor: %d warning(s) (-strict)", d.warn)
	}
	return nil
}

// --- report collector ---

type level int

const (
	lvlOK level = iota
	lvlWarn
	lvlCrit
)

type reportLine struct {
	lvl     level
	section string
	msg     string
}

type doctorReport struct {
	lines      []reportLine
	warn, crit int
}

func (d *doctorReport) add(lvl level, section, format string, a ...any) {
	d.lines = append(d.lines, reportLine{lvl, section, fmt.Sprintf(format, a...)})
	switch lvl {
	case lvlWarn:
		d.warn++
	case lvlCrit:
		d.crit++
	}
}
func (d *doctorReport) ok(s, f string, a ...any)    { d.add(lvlOK, s, f, a...) }
func (d *doctorReport) warnf(s, f string, a ...any) { d.add(lvlWarn, s, f, a...) }
func (d *doctorReport) critf(s, f string, a ...any) { d.add(lvlCrit, s, f, a...) }

func (d *doctorReport) print() {
	mark := map[level]string{lvlOK: "[ OK ]", lvlWarn: "[WARN]", lvlCrit: "[CRIT]"}
	var last string
	fmt.Println("moedex doctor")
	for _, l := range d.lines {
		if l.section != last {
			fmt.Printf("\n%s\n", l.section)
			last = l.section
		}
		fmt.Printf("  %s %s\n", mark[l.lvl], l.msg)
	}
	fmt.Printf("\n%d ok, %d warning(s), %d critical\n", len(d.lines)-d.warn-d.crit, d.warn, d.crit)
}

// --- binaries: locate every moedex-* copy, flag shadows and commit skew ---

// operationalBins are the binaries an operator runs; doctor checks they exist, are
// the dense build where it matters, and share one commit (no skew).
var operationalBins = []string{"moedex", "moedex-index", "moedex-corpus", "moedex-mcp", "moedex-serve"}

type binCopy struct {
	path   string
	commit string
	dense  bool
	ok     bool // -version parsed
}

func checkBinaries(d *doctorReport) {
	const sec = "binaries"
	dirs := binDirs()
	commits := map[string]bool{} // distinct commits across the PATH-resolved set

	for _, name := range operationalBins {
		var copies []binCopy
		seen := map[string]bool{}
		for _, dir := range dirs {
			p := filepath.Join(dir, name)
			rp, err := filepath.EvalSymlinks(p)
			if err != nil {
				continue
			}
			if seen[rp] {
				continue
			}
			if fi, err := os.Stat(rp); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
				continue
			}
			seen[rp] = true
			copies = append(copies, probeVersion(p))
		}
		switch {
		case len(copies) == 0:
			d.warnf(sec, "%s: not found on PATH/known bin dirs", name)
			continue
		case len(copies) > 1:
			var locs []string
			for _, c := range copies {
				locs = append(locs, fmt.Sprintf("%s (%s)", c.path, c.commit))
			}
			d.warnf(sec, "%s: %d copies — SHADOW risk: %s", name, len(copies), strings.Join(locs, ", "))
		}
		// The copy PATH would actually run is the reference for skew + dense checks.
		ref := resolveOnPath(name, copies)
		if ref.ok && ref.commit != "unknown" {
			commits[ref.commit] = true
		}
		if name == "moedex-serve" && ref.ok && !ref.dense {
			d.warnf(sec, "moedex-serve on PATH is the PURE-GO build (dense=false) — the daemon needs the dense build (make install-dense)")
		}
		if ref.ok {
			d.ok(sec, "%s -> %s (%s%s)", name, ref.path, ref.commit, denseTag(name, ref.dense))
		} else {
			d.warnf(sec, "%s -> %s (could not read -version)", name, ref.path)
		}
	}

	switch len(commits) {
	case 0, 1:
		// consistent (or nothing parseable)
	default:
		var cs []string
		for c := range commits {
			cs = append(cs, c)
		}
		d.critf(sec, "binary SKEW: installed binaries are from %d different commits (%s) — reinstall with `make install-dense`", len(commits), strings.Join(cs, ", "))
	}
	// Compare to the running doctor's own commit as a freshness hint.
	self := version.Get().CommitShort()
	if self != "unknown" && len(commits) == 1 {
		for c := range commits {
			if c != self {
				d.warnf(sec, "installed binaries are at %s but this doctor is %s — rebuild/install to sync", c, self)
			}
		}
	}
}

func denseTag(name string, dense bool) string {
	if name != "moedex-serve" {
		return ""
	}
	if dense {
		return ", dense"
	}
	return ", pure-go"
}

// binDirs is the ordered set of dirs to scan: PATH first (what actually resolves),
// then the canonical ~/.local/bin and GOPATH/bin even if not on PATH.
func binDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		dirs = append(dirs, p)
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		add(p)
	}
	add(filepath.Join(homeDir(), ".local", "bin"))
	add(filepath.Join(homeDir(), "go", "bin"))
	return dirs
}

// resolveOnPath returns the copy that exec.LookPath would run, else the first copy.
func resolveOnPath(name string, copies []binCopy) binCopy {
	if p, err := exec.LookPath(name); err == nil {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			for _, c := range copies {
				if cp, err := filepath.EvalSymlinks(c.path); err == nil && cp == rp {
					return c
				}
			}
		}
	}
	return copies[0]
}

// probeVersion runs `<path> -version` and parses the commit= / dense= tokens.
func probeVersion(path string) binCopy {
	c := binCopy{path: path, commit: "unknown"}
	cmd := exec.Command(path, "-version")
	out, err := cmd.Output()
	if err != nil {
		return c
	}
	c.ok = true
	for _, tok := range strings.Fields(strings.TrimSpace(string(out))) {
		switch {
		case strings.HasPrefix(tok, "commit="):
			c.commit = strings.TrimPrefix(tok, "commit=")
		case tok == "dense=true":
			c.dense = true
		}
	}
	return c
}

// --- shard dir: layout + dense/graph sidecar freshness ---

func checkShardDir(d *doctorReport, dir string) {
	section := "shard dir (" + dir + ")"
	info, err := server.InspectShardDir(dir)
	if err != nil {
		d.critf(section, "cannot inspect: %v", err)
		return
	}
	layout := "INLINED build dir"
	if info.HasBlobManifest {
		layout = "CAS-exported dir"
	}
	if info.Deduped {
		layout += " (deduped MOEDEX05 shards + shared blobs.dat)"
	}
	d.ok(section, "%d shard(s), %s", info.Shards, layout)
	d.ok(section, "refresh with: %s", info.RefreshCommand())

	// Layout sanity: deduped shards with neither a blobs.dat nor a blobmanifest are
	// the exact ambiguous/broken state the incident left behind.
	if info.Deduped && !info.HasBlobsDat && !info.HasBlobManifest {
		d.critf(section, "deduped shards but no blobs.dat AND no blobmanifest.json — content store missing/ambiguous")
	}

	switch {
	case !info.StoreExists:
		d.warnf(section, "no dense embedding store (corpus-embeddings.store) — dense arm will be off until built")
	case !info.StoreMetaPresent:
		d.warnf(section, "embedding store present but no .meta — cannot validate freshness")
	default:
		ver := "v" + strconv.Itoa(int(info.StoreVersion))
		incr := "incremental-ready"
		if info.StoreVersion < 2 {
			incr = "legacy keyless — re-key with `moedex-serve -build-embeddings`"
		}
		d.ok(section, "embedding store %s, %d chunks, model %q (%s)", ver, info.StoreChunks, info.StoreModel, incr)
		if info.StoreFresh {
			d.ok(section, "embedding store is FRESH (fingerprint matches shards)")
		} else {
			d.warnf(section, "embedding store is STALE vs shards — refresh will rebuild it")
		}
	}

	switch {
	case !info.GraphExists:
		d.warnf(section, "no graph sidecar (%s) — graph-annotated search_context results and graph tools are unavailable until the next successful build/refresh", server.GraphFileName)
	case info.GraphOpenErr != "":
		d.warnf(section, "graph sidecar present but failed to open (%v) — likely corrupt/truncated; the next build/refresh will fall back to a full graph rebuild", info.GraphOpenErr)
	case info.GraphStale:
		d.warnf(section, "graph sidecar (generation %d, %d node(s), %d edge(s)) is STALE — a shard was rewritten more recently than the graph, which means the last build/refresh's graph rebuild failed silently (see its stderr) and the daemon is serving a prior-generation graph; re-run the refresh and check for a \"warning: build graph\" line", info.GraphGeneration, info.GraphNodes, info.GraphEdges)
	default:
		d.ok(section, "graph sidecar generation %d: %d node(s), %d edge(s)", info.GraphGeneration, info.GraphNodes, info.GraphEdges)
	}
	if info.GraphExists && info.GraphOpenErr == "" {
		switch {
		case !info.ClusterExists:
			d.warnf(section, "no cluster sidecar (%s) — list_clusters will report unavailable until graph rebuild/refresh", server.ClusterFileName)
		case info.ClusterOpenErr != "":
			d.warnf(section, "cluster sidecar unavailable: %s", info.ClusterOpenErr)
		case info.ClusterStatus == "over_cap":
			d.warnf(section, "cluster sidecar over cap: %d eligible node(s), %d edge(s), cap %d — raise MOEDEX_GRAPH_CLUSTER_MAX_NODES and rebuild if clustering is required", info.ClusterEligibleNodes, info.ClusterEligibleEdges, info.ClusterCap)
		case info.ClusterStatus == "under_covered":
			d.warnf(section, "cluster sidecar under-covered: %d of %d graph node(s) eligible — communities are unavailable until representative topology is rebuilt", info.ClusterEligibleNodes, info.GraphNodes)
		default:
			d.ok(section, "cluster sidecar %s: %d eligible node(s), %d edge(s), cap %d", info.ClusterStatus, info.ClusterEligibleNodes, info.ClusterEligibleEdges, info.ClusterCap)
		}
	}
}

// --- daemon health ---

func checkDaemon(d *doctorReport, addr string) {
	section := "daemon (" + addr + ")"
	client := &http.Client{Timeout: 2 * time.Second}
	base := "http://" + addr
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		d.warnf(section, "not reachable (%v) — fine if you don't intend it running", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		d.warnf(section, "/healthz returned %d", resp.StatusCode)
		return
	}
	d.ok(section, "healthy")

	mresp, err := client.Get(base + "/metrics")
	if err != nil {
		return
	}
	defer mresp.Body.Close()
	body, _ := io.ReadAll(mresp.Body)
	if chunks, ok := metricValue(string(body), "moedex_corpus_dense_chunks"); ok {
		if chunks > 0 {
			d.ok(section, "serving %d dense chunks", chunks)
		} else {
			d.warnf(section, "serving 0 dense chunks — dense arm is off (lexical-only)")
		}
	}
}

// metricValue extracts a single gauge value from Prometheus text output.
func metricValue(body, name string) (int, bool) {
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, name+" ") {
			f := strings.Fields(ln)
			if len(f) == 2 {
				if v, err := strconv.ParseFloat(f[1], 64); err == nil {
					return int(v), true
				}
			}
		}
	}
	return 0, false
}

// --- launchd (macOS) ---

func checkLaunchd(d *doctorReport) {
	const sec = "launchd"
	if runtime.GOOS != "darwin" {
		d.ok(sec, "skipped (not macOS)")
		return
	}
	uid := strconv.Itoa(os.Getuid())
	for _, label := range []string{"com.moedex.serve", "com.moedex.refresh"} {
		cmd := exec.Command("launchctl", "print", "gui/"+uid+"/"+label)
		if err := cmd.Run(); err == nil {
			d.ok(sec, "%s loaded", label)
		} else {
			d.warnf(sec, "%s not loaded", label)
		}
	}
}

// --- small helpers ---

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// checkNavServers reports which LSP navigation servers (ADR 0017) are on PATH.
// The set, commands, and capability notes come from navigate.Servers() — the
// engine's own registry — so this never drifts from what the daemon actually
// routes. All findings are warnings, never critical: navigation is an optional
// arm behind the -tags lsp daemon and degrades gracefully when a server is
// absent. The fix-it pointer is scripts/install-lsp-servers.sh.
func checkNavServers(d *doctorReport) {
	checkNavServersWith(d, navigate.Servers(), exec.LookPath, func(path string, args []string) error {
		return probeLSPStartup(path, args, 300*time.Millisecond)
	})
}

type lookPathFunc func(string) (string, error)
type lspStartupProbeFunc func(string, []string) error

func checkNavServersWith(d *doctorReport, servers []navigate.LangSpec, lookPath lookPathFunc, probe lspStartupProbeFunc) {
	const sec = "lsp navigation servers (optional; -tags lsp daemon — see scripts/install-lsp-servers.sh)"
	for _, s := range servers {
		note := ""
		if s.Note != "" {
			note = " — " + s.Note
		}
		if p, err := lookPath(s.Command); err == nil {
			// cflsp is installed as a tiny wrapper around a managed JavaScript
			// bundle. A surviving wrapper can remain on PATH after that bundle is
			// removed, so PATH presence alone is not a meaningful health signal.
			if s.Language == "cfml" {
				if err := probe(p, s.Args); err != nil {
					d.warnf(sec, "%-11s %s (%s) failed startup health check: %v%s", s.Language, s.Command, p, err, note)
					continue
				}
			}
			d.ok(sec, "%-11s %s (%s)%s", s.Language, s.Command, p, note)
		} else {
			d.warnf(sec, "%-11s %s not on PATH%s", s.Language, s.Command, note)
		}
	}
}

// probeLSPStartup verifies that a stdio language-server process survives its
// immediate startup window. It intentionally does not send initialize: doctor
// is compiled in the pure-Go build too. Keeping stdin open distinguishes a
// server waiting for LSP traffic from a stale wrapper or missing runtime that
// exits before it can read a request.
func probeLSPStartup(path string, args []string, grace time.Duration) error {
	cmd := exec.Command(path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open stdin: %w", err)
	}
	defer stdin.Close()
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case err := <-exited:
		detail := startupErrorDetail(stderr.String())
		if detail != "" {
			return fmt.Errorf("exited during startup (%v): %s", err, detail)
		}
		return fmt.Errorf("exited during startup: %v", err)
	case <-timer.C:
		// Surviving the bounded window is the health signal. Stop the disposable
		// probe and always reap it so doctor never leaks a language server.
		_ = cmd.Process.Kill()
		<-exited
		return nil
	}
}

func startupErrorDetail(stderr string) string {
	var first string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.HasPrefix(line, "Error:") {
			return line
		}
	}
	return first
}
