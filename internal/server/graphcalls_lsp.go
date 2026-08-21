//go:build lsp

package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moedex/internal/graph"
	"moedex/internal/graph/candidates"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
	"moedex/internal/ingest"
	"moedex/internal/navigate"
	"moedex/internal/symbol"
)

type lspGraphNavigator interface {
	DocumentSymbol(context.Context, string) ([]navigate.Symbol, error)
	References(context.Context, navigate.Pos, bool) ([]navigate.Location, error)
}

type lspIndexedFile struct {
	path     string
	repoRoot string
	route    string
	shard    int
	blobID   uint64
	blob     *index.Blob
}

type lspDefinitionJob struct {
	name        string
	path        string
	repoRoot    string
	route       string
	at          navigate.Pos
	target      diskgraph.Key
	prioritized bool
}

type lspConfirmedCall struct {
	source   diskgraph.Key
	target   diskgraph.Key
	name     string
	evidence graph.Evidence
}

func addLSPCallEdges(ctx context.Context, builder *diskgraph.Builder, merged *symbol.Corpus, shards []*index.Index, seen map[persistedGraphEdge]struct{}, crossRelevant map[string]bool, generation uint64, opts GraphBuildOptions) error {
	rate := opts.LSPRequestsPerSecond
	if rate == 0 {
		rate = DefaultLSPRequestsPerSecond
	}
	timeout := opts.LSPRequestTimeout
	if timeout == 0 {
		timeout = DefaultLSPRequestTimeout
	}
	concurrency := opts.LSPConcurrency
	if concurrency == 0 {
		concurrency = DefaultLSPConcurrency()
	}

	pool := navigate.NewPool(navigate.Config{
		RequestTimeout: timeout,
		IdleTTL:        2 * timeout,
	})
	// Pool teardown is best-effort after every child has been reaped. Some
	// servers (including gopls versions that exit on the cancelled process
	// context after acknowledging shutdown) report a non-zero process status;
	// that must not discard already-confirmed graph results.
	defer func() { _ = pool.Close() }()

	pacer := newLSPRequestPacer(rate)
	calls, stats, err := collectLSPCallEdgesConcurrent(ctx, pool, merged, shards, crossRelevant, timeout, pacer, concurrency)
	if opts.LSPStats != nil {
		*opts.LSPStats = stats
	}
	if err != nil {
		return err
	}
	for _, call := range calls {
		if err := builder.AddNode(call.target); err != nil {
			return err
		}
		edge := diskgraph.Edge{
			Type:         diskgraph.EdgeCalls,
			TargetBlob:   call.target.BlobSHA,
			TargetOffset: call.target.SymbolOffset,
			Confidence:   graph.Proven,
			Evidence:     call.evidence,
			Name:         call.name,
			Generation:   generation,
		}
		record := persistedGraphEdge{
			sourceBlob:     call.source.BlobSHA,
			sourceOffset:   call.source.SymbolOffset,
			typeID:         edge.Type,
			targetBlob:     edge.TargetBlob,
			targetOffset:   edge.TargetOffset,
			confidence:     uint64(edge.Confidence),
			evidenceBlob:   edge.Evidence.BlobSHA,
			evidence:       edge.Evidence.ByteOffset,
			evidenceLength: edge.Evidence.ByteLength,
		}
		if _, duplicate := seen[record]; duplicate {
			continue
		}
		seen[record] = struct{}{}
		if _, err := builder.AddOrUpgradeEdge(call.source, edge); err != nil {
			return err
		}
		stats.CallEdges++
	}
	if opts.LSPStats != nil {
		*opts.LSPStats = stats
	}
	return nil
}

func collectLSPCallEdges(ctx context.Context, nav lspGraphNavigator, merged *symbol.Corpus, shards []*index.Index, crossRelevant map[string]bool, timeout time.Duration, pacer *lspRequestPacer) ([]lspConfirmedCall, LSPGraphStats, error) {
	return collectLSPCallEdgesConcurrent(ctx, nav, merged, shards, crossRelevant, timeout, pacer, 1)
}

func collectLSPCallEdgesConcurrent(ctx context.Context, nav lspGraphNavigator, merged *symbol.Corpus, shards []*index.Index, crossRelevant map[string]bool, timeout time.Duration, pacer *lspRequestPacer, concurrency int) ([]lspConfirmedCall, LSPGraphStats, error) {
	var stats LSPGraphStats
	if concurrency < 1 {
		concurrency = 1
	}
	files, restricted, err := lspEligibleFiles(merged, shards)
	stats.RestrictedWorkspaces = restricted
	if err != nil {
		return nil, stats, err
	}
	stats.EligibleFiles = len(files)
	if len(files) == 0 {
		return nil, stats, nil
	}
	byPath := make(map[string]lspIndexedFile, len(files))
	for _, file := range files {
		byPath[file.path] = file
	}

	fileGroups := groupLSPFiles(files)
	documentResults := make([]lspDocumentResult, len(fileGroups))
	documentCounters := &lspPhaseCounters{}
	stopDocumentProgress := startLSPPhaseProgress("documentSymbol", len(files), len(fileGroups), concurrency, documentCounters)
	runLSPFileGroups(ctx, concurrency, fileGroups, documentResults, func(group []lspIndexedFile) lspDocumentResult {
		return collectLSPDocumentGroup(ctx, nav, group, byPath, merged, crossRelevant, timeout, pacer, documentCounters)
	})
	stopDocumentProgress()
	jobsByKey := make(map[string]lspDefinitionJob)
	for _, result := range documentResults {
		stats.DocumentRequests += result.requests
		stats.FailedRequests += result.failed
		if result.err != nil {
			return nil, stats, result.err
		}
		for key, job := range result.jobs {
			jobsByKey[key] = job
		}
	}

	jobs := make([]lspDefinitionJob, 0, len(jobsByKey))
	for _, job := range jobsByKey {
		jobs = append(jobs, job)
		if job.prioritized {
			stats.PrioritizedSymbols++
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].prioritized != jobs[j].prioritized {
			return jobs[i].prioritized
		}
		if jobs[i].repoRoot != jobs[j].repoRoot {
			return jobs[i].repoRoot < jobs[j].repoRoot
		}
		if jobs[i].path != jobs[j].path {
			return jobs[i].path < jobs[j].path
		}
		if jobs[i].target.SymbolOffset != jobs[j].target.SymbolOffset {
			return jobs[i].target.SymbolOffset < jobs[j].target.SymbolOffset
		}
		return jobs[i].name < jobs[j].name
	})
	stats.DiscoveredSymbols = len(jobs)

	referenceGroups := groupLSPDefinitionJobs(jobs)
	referenceResults := make([]lspReferenceResult, len(referenceGroups))
	referenceCounters := &lspPhaseCounters{}
	stopReferenceProgress := startLSPPhaseProgress("findReferences", len(jobs), len(referenceGroups), concurrency, referenceCounters)
	runLSPReferenceGroups(ctx, concurrency, referenceGroups, referenceResults, func(group []lspDefinitionJob) lspReferenceResult {
		return collectLSPReferenceGroup(ctx, nav, group, byPath, merged, timeout, pacer, referenceCounters)
	})
	stopReferenceProgress()
	unique := make(map[string]lspConfirmedCall)
	for _, result := range referenceResults {
		stats.ReferenceRequests += result.requests
		stats.FailedRequests += result.failed
		if result.err != nil {
			return nil, stats, result.err
		}
		for key, call := range result.calls {
			unique[key] = call
		}
	}

	out := make([]lspConfirmedCall, 0, len(unique))
	for _, call := range unique {
		out = append(out, call)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].source.BlobSHA != out[j].source.BlobSHA {
			return out[i].source.BlobSHA < out[j].source.BlobSHA
		}
		if out[i].source.SymbolOffset != out[j].source.SymbolOffset {
			return out[i].source.SymbolOffset < out[j].source.SymbolOffset
		}
		if out[i].target.BlobSHA != out[j].target.BlobSHA {
			return out[i].target.BlobSHA < out[j].target.BlobSHA
		}
		if out[i].target.SymbolOffset != out[j].target.SymbolOffset {
			return out[i].target.SymbolOffset < out[j].target.SymbolOffset
		}
		return out[i].evidence.ByteOffset < out[j].evidence.ByteOffset
	})
	return out, stats, nil
}

type lspDocumentResult struct {
	jobs     map[string]lspDefinitionJob
	requests int
	failed   int
	err      error
}

type lspReferenceResult struct {
	calls    map[string]lspConfirmedCall
	requests int
	failed   int
	err      error
}

type lspPhaseCounters struct {
	completed atomic.Int64
	failed    atomic.Int64
}

func startLSPPhaseProgress(phase string, total, routes, workers int, counters *lspPhaseCounters) func() {
	started := time.Now()
	log.Printf("server: LSP %s scheduled %d request(s) across %d route(s) on %d worker(s)", phase, total, routes, min(workers, routes))
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				log.Printf("server: LSP %s progress %d/%d request(s), failures=%d, elapsed=%s",
					phase, counters.completed.Load(), total, counters.failed.Load(), time.Since(started).Round(time.Second))
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
		log.Printf("server: LSP %s complete %d/%d request(s), failures=%d, elapsed=%s",
			phase, counters.completed.Load(), total, counters.failed.Load(), time.Since(started).Round(time.Millisecond))
	}
}

func lspRouteKey(language, workspace string) string { return language + "\x00" + workspace }

func groupLSPFiles(files []lspIndexedFile) [][]lspIndexedFile {
	byRoute := make(map[string][]lspIndexedFile)
	for _, file := range files {
		byRoute[file.route] = append(byRoute[file.route], file)
	}
	groups := make([][]lspIndexedFile, 0, len(byRoute))
	for _, group := range byRoute {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i]) != len(groups[j]) {
			return len(groups[i]) > len(groups[j])
		}
		return groups[i][0].route < groups[j][0].route
	})
	return groups
}

func groupLSPDefinitionJobs(jobs []lspDefinitionJob) [][]lspDefinitionJob {
	byRoute := make(map[string][]lspDefinitionJob)
	for _, job := range jobs {
		byRoute[job.route] = append(byRoute[job.route], job)
	}
	groups := make([][]lspDefinitionJob, 0, len(byRoute))
	for _, group := range byRoute {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i]) != len(groups[j]) {
			return len(groups[i]) > len(groups[j])
		}
		return groups[i][0].route < groups[j][0].route
	})
	return groups
}

func runLSPFileGroups(ctx context.Context, concurrency int, groups [][]lspIndexedFile, results []lspDocumentResult, run func([]lspIndexedFile) lspDocumentResult) {
	workers := min(max(concurrency, 1), len(groups))
	if workers == 0 {
		return
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil {
					results[i].err = ctx.Err()
					continue
				}
				results[i] = run(groups[i])
			}
		}()
	}
	for i := range groups {
		work <- i
	}
	close(work)
	wg.Wait()
}

func runLSPReferenceGroups(ctx context.Context, concurrency int, groups [][]lspDefinitionJob, results []lspReferenceResult, run func([]lspDefinitionJob) lspReferenceResult) {
	workers := min(max(concurrency, 1), len(groups))
	if workers == 0 {
		return
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				if ctx.Err() != nil {
					results[i].err = ctx.Err()
					continue
				}
				results[i] = run(groups[i])
			}
		}()
	}
	for i := range groups {
		work <- i
	}
	close(work)
	wg.Wait()
}

func collectLSPDocumentGroup(ctx context.Context, nav lspGraphNavigator, files []lspIndexedFile, byPath map[string]lspIndexedFile, merged *symbol.Corpus, crossRelevant map[string]bool, timeout time.Duration, pacer *lspRequestPacer, counters *lspPhaseCounters) lspDocumentResult {
	result := lspDocumentResult{jobs: make(map[string]lspDefinitionJob)}
	for _, file := range files {
		if err := pacer.Wait(ctx); err != nil {
			result.err = err
			return result
		}
		result.requests++
		requestCtx, cancel := requestContext(ctx, timeout)
		syms, queryErr := nav.DocumentSymbol(requestCtx, file.path)
		cancel()
		counters.completed.Add(1)
		if queryErr != nil {
			if ctx.Err() != nil {
				result.err = ctx.Err()
				return result
			}
			result.failed++
			counters.failed.Add(1)
			continue
		}
		if len(syms) == 0 {
			// Partial-capability servers may not implement documentSymbol. The
			// syntactic sidecar remains a safe find-references work list.
			for _, sym := range merged.Symbols(file.shard, file.blobID) {
				if !candidates.Exported(sym.Name) || sym.NameStart < 0 || sym.NameEnd <= sym.NameStart {
					continue
				}
				job := lspDefinitionJob{
					name: sym.Name, path: file.path, repoRoot: file.repoRoot, route: file.route,
					at:          byteOffsetPos(file.path, file.blob.Content, sym.NameStart),
					target:      diskgraph.Key{BlobSHA: file.blob.SHA, SymbolOffset: uint64(sym.NameStart)},
					prioritized: crossRelevant[sym.Name],
				}
				result.jobs[lspJobKey(job)] = job
			}
			continue
		}
		for _, sym := range syms {
			if !candidates.Exported(sym.Name) {
				continue
			}
			definitionFile, ok := byPath[cleanAbsolute(sym.Loc.File)]
			if !ok || definitionFile.repoRoot != file.repoRoot {
				continue
			}
			start, _, ok := namedSpan(definitionFile.blob.Content, sym.Loc.Start, sym.Loc.End, sym.Name)
			if !ok {
				continue
			}
			job := lspDefinitionJob{
				name: sym.Name, path: definitionFile.path, repoRoot: definitionFile.repoRoot, route: definitionFile.route,
				at:          byteOffsetPos(definitionFile.path, definitionFile.blob.Content, start),
				target:      diskgraph.Key{BlobSHA: definitionFile.blob.SHA, SymbolOffset: uint64(start)},
				prioritized: crossRelevant[sym.Name],
			}
			result.jobs[lspJobKey(job)] = job
		}
	}
	return result
}

func collectLSPReferenceGroup(ctx context.Context, nav lspGraphNavigator, jobs []lspDefinitionJob, byPath map[string]lspIndexedFile, merged *symbol.Corpus, timeout time.Duration, pacer *lspRequestPacer, counters *lspPhaseCounters) lspReferenceResult {
	result := lspReferenceResult{calls: make(map[string]lspConfirmedCall)}
	for _, job := range jobs {
		if err := pacer.Wait(ctx); err != nil {
			result.err = err
			return result
		}
		result.requests++
		requestCtx, cancel := requestContext(ctx, timeout)
		locations, queryErr := nav.References(requestCtx, job.at, false)
		cancel()
		counters.completed.Add(1)
		if queryErr != nil {
			if ctx.Err() != nil {
				result.err = ctx.Err()
				return result
			}
			result.failed++
			counters.failed.Add(1)
			continue
		}
		for _, location := range locations {
			callerFile, ok := byPath[cleanAbsolute(location.File)]
			if !ok || callerFile.repoRoot != job.repoRoot {
				continue
			}
			start, end, ok := namedSpan(callerFile.blob.Content, location.Start, location.End, job.name)
			if !ok || !callSyntaxAt(callerFile.blob.Content, end) {
				continue
			}
			enclosing, ok := merged.Enclosing(callerFile.shard, callerFile.blobID, start)
			if !ok || enclosing.NameStart < 0 {
				continue
			}
			call := lspConfirmedCall{
				source: diskgraph.Key{BlobSHA: callerFile.blob.SHA, SymbolOffset: uint64(enclosing.NameStart)},
				target: job.target,
				name:   job.name,
				evidence: graph.Evidence{
					BlobSHA: callerFile.blob.SHA, ByteOffset: uint64(start), ByteLength: uint64(end - start),
				},
			}
			result.calls[lspCallKey(call)] = call
		}
	}
	return result
}

func lspEligibleFiles(merged *symbol.Corpus, shards []*index.Index) ([]lspIndexedFile, int, error) {
	if merged == nil {
		return nil, 0, fmt.Errorf("server: nil symbol corpus for LSP graph pass")
	}
	byPath := make(map[string]lspIndexedFile)
	roots := make(map[string]struct{})
	for shardID, ix := range shards {
		if ix == nil {
			continue
		}
		for blobID := uint64(0); blobID < uint64(ix.NumBlobs()); blobID++ {
			blob := ix.Blob(blobID)
			if blob == nil {
				continue
			}
			for _, ref := range blob.Files {
				path := cleanAbsolute(ref.AbsPath)
				if path == "" {
					continue
				}
				if _, supported := navigate.LanguageForPath(path); !supported {
					continue
				}
				root, ok := indexedRepoRoot(path, ref.RelPath)
				if !ok || !currentFileMatches(path, blob.Content) {
					continue
				}
				if _, duplicate := byPath[path]; duplicate {
					continue
				}
				language, workspace := navigate.WorkspaceRoute(path)
				byPath[path] = lspIndexedFile{
					path: path, repoRoot: root, route: lspRouteKey(language, workspace),
					shard: shardID, blobID: blobID, blob: blob,
				}
				roots[root] = struct{}{}
			}
		}
	}

	rootAllowed := make(map[string]bool, len(roots))
	restricted := 0
	rootNames := make([]string, 0, len(roots))
	for root := range roots {
		rootNames = append(rootNames, root)
	}
	sort.Strings(rootNames)
	// Validate every workspace before the first LSP request. One malformed
	// policy aborts the pass fail-closed, and one level-1 path rejects the whole
	// workspace because the external server can scan beyond the queried file.
	for _, root := range rootNames {
		allowed, err := ingest.LSPWorkspaceAllowed(root)
		if err != nil {
			return nil, restricted, fmt.Errorf("server: LSP workspace privacy preflight %s: %w", root, err)
		}
		rootAllowed[root] = allowed
		if !allowed {
			restricted++
		}
	}

	files := make([]lspIndexedFile, 0, len(byPath))
	for _, file := range byPath {
		if rootAllowed[file.repoRoot] {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, restricted, nil
}

func indexedRepoRoot(absPath, relPath string) (string, bool) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." || rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	root := absPath
	for part := rel; part != "."; part = filepath.Dir(part) {
		root = filepath.Dir(root)
	}
	root = filepath.Clean(root)
	return root, filepath.Clean(filepath.Join(root, rel)) == filepath.Clean(absPath)
}

func currentFileMatches(path string, indexed []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	current = bytes.TrimPrefix(current, []byte{0xEF, 0xBB, 0xBF})
	return bytes.Equal(current, indexed)
}

func cleanAbsolute(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func byteOffsetPos(path string, content []byte, offset int) navigate.Pos {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	line := 1 + bytes.Count(content[:offset], []byte{'\n'})
	lineStart := bytes.LastIndexByte(content[:offset], '\n') + 1
	return navigate.Pos{File: path, Line: line, Col: offset - lineStart + 1}
}

func byteOffset(content []byte, pos navigate.Pos) (int, bool) {
	if pos.Line < 1 || pos.Col < 1 {
		return 0, false
	}
	lineStart := 0
	for line := 1; line < pos.Line; line++ {
		next := bytes.IndexByte(content[lineStart:], '\n')
		if next < 0 {
			return 0, false
		}
		lineStart += next + 1
	}
	lineEnd := len(content)
	if next := bytes.IndexByte(content[lineStart:], '\n'); next >= 0 {
		lineEnd = lineStart + next
	}
	offset := lineStart + pos.Col - 1
	if offset < lineStart || offset > lineEnd {
		return 0, false
	}
	return offset, true
}

func namedSpan(content []byte, startPos, endPos navigate.Pos, name string) (int, int, bool) {
	start, ok := byteOffset(content, startPos)
	if !ok || name == "" {
		return 0, 0, false
	}
	end, endOK := byteOffset(content, endPos)
	if endOK && end >= start && end <= len(content) {
		if at := identifierOccurrence(content, start, end, name); at >= 0 {
			return at, at + len(name), true
		}
	}
	if start+len(name) <= len(content) && string(content[start:start+len(name)]) == name && identifierBounds(content, start, start+len(name)) {
		return start, start + len(name), true
	}
	return 0, 0, false
}

func identifierOccurrence(content []byte, start, end int, name string) int {
	if start < 0 || end < start || end > len(content) {
		return -1
	}
	window := content[start:end]
	for from := 0; from <= len(window)-len(name); {
		rel := bytes.Index(window[from:], []byte(name))
		if rel < 0 {
			return -1
		}
		at := start + from + rel
		if identifierBounds(content, at, at+len(name)) {
			return at
		}
		from += rel + 1
	}
	return -1
}

func identifierBounds(content []byte, start, end int) bool {
	return (start == 0 || !lspIdentifierByte(content[start-1])) &&
		(end == len(content) || !lspIdentifierByte(content[end]))
}

func lspIdentifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= 0x80 ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func callSyntaxAt(content []byte, nameEnd int) bool {
	at := skipLSPSpaces(content, nameEnd)
	if at < len(content) && content[at] == '[' {
		var ok bool
		at, ok = skipBalanced(content, at, '[', ']')
		if !ok {
			return false
		}
		at = skipLSPSpaces(content, at)
	}
	if at < len(content) && content[at] == '<' {
		var ok bool
		at, ok = skipBalanced(content, at, '<', '>')
		if !ok {
			return false
		}
		at = skipLSPSpaces(content, at)
	}
	return at < len(content) && content[at] == '('
}

func skipLSPSpaces(content []byte, at int) int {
	for at < len(content) {
		switch content[at] {
		case ' ', '\t', '\r', '\n':
			at++
		default:
			return at
		}
	}
	return at
}

func skipBalanced(content []byte, at int, open, close byte) (int, bool) {
	depth := 0
	for ; at < len(content); at++ {
		switch content[at] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return at + 1, true
			}
		case '\n', '\r', ';', '{', '}':
			return at, false
		}
	}
	return at, false
}

func requestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func lspJobKey(job lspDefinitionJob) string {
	return fmt.Sprintf("%s\x00%d\x00%s", job.path, job.target.SymbolOffset, job.name)
}

func lspCallKey(call lspConfirmedCall) string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s\x00%d\x00%d", call.source.BlobSHA, call.source.SymbolOffset, call.target.BlobSHA, call.target.SymbolOffset, call.evidence.BlobSHA, call.evidence.ByteOffset, call.evidence.ByteLength)
}

type lspRequestPacer struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

func newLSPRequestPacer(requestsPerSecond float64) *lspRequestPacer {
	return &lspRequestPacer{
		interval: time.Duration(float64(time.Second) / requestsPerSecond),
		now:      time.Now,
		sleep:    sleepLSPRequest,
	}
}

// Wait spaces request starts globally across the entire graph pass. The graph
// scheduler serializes each (language, workspace) route separately while this
// pacer bounds aggregate starts across concurrently active routes.
func (p *lspRequestPacer) Wait(ctx context.Context) error {
	if p == nil || p.interval <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.last.IsZero() {
		if delay := p.last.Add(p.interval).Sub(now); delay > 0 {
			if err := p.sleep(ctx, delay); err != nil {
				return err
			}
			now = p.now()
		}
	}
	p.last = now
	return nil
}

func sleepLSPRequest(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
