package server

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"moedex/internal/diskstore"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/index"
)

// ---------------------------------------------------------------------------
// corpus fixtures
// ---------------------------------------------------------------------------

// graphFile is one blob of a synthetic shard dir. The SHA is derived from the
// content exactly as ingest does, so "unchanged file" means "same blob SHA" and
// the refresh's CAS comparison sees what it would see in production.
type graphFile struct {
	repo    string
	path    string
	content string
}

// writeGraphShards writes files into dir as shard-NNNN.idx, perShard blobs each,
// replacing any shards already there. Sidecars in dir are deliberately left
// alone: a refresh has to find the previous graph next to the new shards.
func writeGraphShards(t *testing.T, dir string, files []graphFile, perShard int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := filepath.Glob(filepath.Join(dir, "shard-*.idx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	for start, n := 0, 0; start < len(files); start, n = start+perShard, n+1 {
		end := min(start+perShard, len(files))
		ix := index.New()
		for _, f := range files[start:end] {
			content := []byte(f.content)
			ix.AddFile(f.repo, f.path, "/"+f.repo+"/"+f.path, diskstore.GitBlobSHA1(content), content)
		}
		if err := diskstore.Save(ix, filepath.Join(dir, fmt.Sprintf("shard-%04d.idx", n))); err != nil {
			t.Fatal(err)
		}
	}
}

// copyShards clones just the shard files of src into a fresh dir, so a full
// rebuild can be run over identical content without disturbing src's graph.
func copyShards(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	paths, err := filepath.Glob(filepath.Join(src, "shard-*.idx"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.Base(path)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// synthGraphCorpus builds n Go-ish blobs, each defining its own Widget type and
// Process function and calling three others, so the corpus has a dense
// cross-blob edge set and ~2n distinct exported names to sweep.
func synthGraphCorpus(n int) []graphFile {
	files := make([]graphFile, 0, n)
	for i := 0; i < n; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "package p%03d\n\n", i)
		fmt.Fprintf(&b, "// Widget%03d carries the synthetic payload.\n", i)
		fmt.Fprintf(&b, "type Widget%03d struct{ Field int }\n\n", i)
		fmt.Fprintf(&b, "func Process%03d(w *Widget%03d) int {\n", i, i)
		for k := 1; k <= 3; k++ {
			fmt.Fprintf(&b, "\t_ = Process%03d(nil)\n", (i+k*7)%n)
		}
		fmt.Fprint(&b, "\treturn w.Field\n}\n")
		files = append(files, graphFile{
			repo:    fmt.Sprintf("repo%03d", i),
			path:    fmt.Sprintf("pkg%03d.go", i),
			content: b.String(),
		})
	}
	return files
}

// ---------------------------------------------------------------------------
// graph snapshots
// ---------------------------------------------------------------------------

// graphRecord is one persisted (node, edge) pair, flattened for comparison.
type graphRecord struct {
	key  diskgraph.Key
	edge diskgraph.Edge
}

// readGraph returns every record of the graph at path in on-disk order.
func readGraph(t *testing.T, path string) []graphRecord {
	t.Helper()
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer g.Close()
	var out []graphRecord
	g.EachEdge(func(key diskgraph.Key, edge diskgraph.Edge) bool {
		out = append(out, graphRecord{key: key, edge: edge})
		return true
	})
	return out
}

// withoutGenerations strips the staleness stamps so two graphs can be compared
// on their adjacency alone — the property an incremental refresh must preserve.
func withoutGenerations(records []graphRecord) []graphRecord {
	out := make([]graphRecord, len(records))
	for i, r := range records {
		r.edge.Generation = 0
		out[i] = r
	}
	return out
}

// outDegree counts the outgoing edges per source blob SHA.
func outDegree(records []graphRecord) map[string]int {
	out := make(map[string]int)
	for _, r := range records {
		out[r.key.BlobSHA]++
	}
	return out
}

func requireSameGraph(t *testing.T, what string, got, want []graphRecord) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d edge(s), want %d", what, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: record %d differs\n got  %+v\n want %+v", what, i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestRefreshGraphEqualsFullRebuild is the exactness gate. An incremental
// refresh is only allowed to be cheaper, never different: carrying edges forward
// must reproduce the full sweep record for record, in the same order.
func TestRefreshGraphEqualsFullRebuild(t *testing.T) {
	dir := t.TempDir()
	files := synthGraphCorpus(40)
	writeGraphShards(t, dir, files, 8)
	if _, _, err := BuildGraph(dir); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	// Three kinds of change at once: an edited blob, a brand-new blob, and a
	// deleted one — every arm of the delta in a single refresh.
	files[3].content += "\n// Widget007 is mentioned here too.\nfunc Extra003() { Process011(nil) }\n"
	files = append(files, graphFile{
		repo:    "repoNew",
		path:    "new.go",
		content: "package pnew\n\nfunc Fresh() { Process003(nil); Process019(nil) }\n",
	})
	files = append(files[:9], files[10:]...) // drop file 009
	writeGraphShards(t, dir, files, 8)

	full := copyShards(t, dir)
	fullPath, _, err := BuildGraph(full)
	if err != nil {
		t.Fatalf("full rebuild: %v", err)
	}

	deltaPath, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if stats.FullRebuild {
		t.Fatalf("refresh fell back to a full rebuild: %s", stats.Reason)
	}
	if stats.EdgesCarried == 0 {
		t.Fatal("refresh carried no edges forward; nothing was reused")
	}
	requireSameGraph(t, "incremental vs full rebuild",
		withoutGenerations(readGraph(t, deltaPath)),
		withoutGenerations(readGraph(t, fullPath)))
}

// TestRefreshGraphRecomputesOnlyChangedBlobNames is the phase's headline
// claim: one edited file re-sweeps only the names that file's content mentions,
// every other name's edges are copied forward, and the unchanged files' edge
// counts come out identical.
func TestRefreshGraphRecomputesOnlyChangedBlobNames(t *testing.T) {
	dir := t.TempDir()
	files := synthGraphCorpus(60)
	writeGraphShards(t, dir, files, 10)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}
	before := readGraph(t, path)
	beforeSHA := diskstore.GitBlobSHA1([]byte(files[0].content))

	// Append a trailing comment: the blob's SHA changes, but no symbol inside it
	// moves, so every unchanged file keeps the same number of outgoing edges.
	files[0].content += "\n// trailing note\n"
	afterSHA := diskstore.GitBlobSHA1([]byte(files[0].content))
	writeGraphShards(t, dir, files, 10)

	path, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	after := readGraph(t, path)

	if stats.FullRebuild || stats.Unchanged {
		t.Fatalf("expected a delta refresh, got %+v", stats)
	}
	if stats.BlobsAdded != 1 || stats.BlobsRemoved != 1 {
		t.Fatalf("delta = +%d/-%d blob(s), want +1/-1", stats.BlobsAdded, stats.BlobsRemoved)
	}
	if stats.Generation != stats.PreviousGeneration+1 {
		t.Fatalf("generation %d -> %d, want a single increment", stats.PreviousGeneration, stats.Generation)
	}

	// The one edited file must not drag the whole corpus into the sweep. Its
	// content names Widget000/Process000/Field plus the three Process0NN it
	// calls, so a handful out of ~120 eligible names.
	if stats.NamesRecomputed == 0 {
		t.Fatal("nothing was recomputed; the delta found no work at all")
	}
	if stats.NamesRecomputed*5 >= stats.NamesEligible {
		t.Fatalf("recomputed %d of %d name(s); the delta is not selective",
			stats.NamesRecomputed, stats.NamesEligible)
	}
	if stats.NamesRecomputed+stats.NamesCarried != stats.NamesEligible {
		t.Fatalf("names recomputed(%d) + carried(%d) != eligible(%d)",
			stats.NamesRecomputed, stats.NamesCarried, stats.NamesEligible)
	}
	if stats.EdgesCarried == 0 || stats.EdgesCarried <= stats.EdgesRecomputed {
		t.Fatalf("carried %d edge(s) vs recomputed %d; the bulk should be carried",
			stats.EdgesCarried, stats.EdgesRecomputed)
	}
	if stats.EdgesDropped != 0 {
		t.Fatalf("dropped %d prior edge(s); every carried name should still be eligible", stats.EdgesDropped)
	}
	if got := stats.EdgesCarried + stats.EdgesRecomputed; got != len(after) {
		t.Fatalf("stats account for %d edge(s), file holds %d", got, len(after))
	}

	// Unchanged files keep their exact outgoing edge count.
	beforeDegree, afterDegree := outDegree(before), outDegree(after)
	delete(beforeDegree, beforeSHA)
	delete(afterDegree, afterSHA)
	if len(beforeDegree) != len(afterDegree) {
		t.Fatalf("unchanged source blobs: %d before, %d after", len(beforeDegree), len(afterDegree))
	}
	for sha, want := range beforeDegree {
		if got := afterDegree[sha]; got != want {
			t.Fatalf("unchanged blob %s: %d outgoing edge(s), want %d", sha, got, want)
		}
	}

	// Every edge that touches the changed content was recomputed, and the ones
	// that did not need to be keep their original generation stamp.
	var carriedStamps int
	for _, r := range after {
		touches := r.key.BlobSHA == afterSHA || r.edge.TargetBlob == afterSHA
		if touches && r.edge.Generation != stats.Generation {
			t.Fatalf("edge touching the changed blob is stale: %+v", r)
		}
		if r.edge.Generation == stats.PreviousGeneration {
			carriedStamps++
		}
	}
	if carriedStamps != stats.EdgesCarried {
		t.Fatalf("%d edge(s) carry the prior generation, stats say %d", carriedStamps, stats.EdgesCarried)
	}
	// Nothing may still point at content that left the corpus.
	for _, r := range after {
		if r.key.BlobSHA == beforeSHA || r.edge.TargetBlob == beforeSHA {
			t.Fatalf("edge still references the removed blob: %+v", r)
		}
	}
}

// TestRefreshGraphLinksNewDefinitionFromUnchangedCaller pins the reason
// the delta unit is a name and not a blob. The caller blob never changes and
// starts with no edges at all, so a blob-keyed carry-forward would keep its empty
// adjacency forever; only recomputing the NAME finds the definition that arrived
// in a different file.
func TestRefreshGraphLinksNewDefinitionFromUnchangedCaller(t *testing.T) {
	dir := t.TempDir()
	callerContent := "package caller\n\nfunc Caller() { Lonesome() }\n"
	files := []graphFile{
		{repo: "caller", path: "caller.go", content: callerContent},
		{repo: "other", path: "other.go", content: "package other\n\nfunc Other() {}\n"},
	}
	writeGraphShards(t, dir, files, 1)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}
	callerSHA := diskstore.GitBlobSHA1([]byte(callerContent))
	for _, r := range readGraph(t, path) {
		if r.edge.Name == "Lonesome" {
			t.Fatalf("Lonesome had an edge before anything defined it: %+v", r)
		}
	}

	// A different repo starts defining the name the untouched caller uses.
	defContent := "package def\n\nfunc Lonesome() {}\n"
	files = append(files, graphFile{repo: "def", path: "def.go", content: defContent})
	writeGraphShards(t, dir, files, 1)

	path, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if stats.FullRebuild {
		t.Fatalf("refresh fell back to a full rebuild: %s", stats.Reason)
	}
	defSHA := diskstore.GitBlobSHA1([]byte(defContent))
	var found bool
	for _, r := range readGraph(t, path) {
		if r.edge.Name == "Lonesome" && r.key.BlobSHA == callerSHA && r.edge.TargetBlob == defSHA {
			found = true
		}
	}
	if !found {
		t.Fatal("the unchanged caller was never linked to the newly added definition")
	}

	// And it agrees with a full rebuild, which is the only way to be sure the
	// delta found everything rather than merely something.
	full := copyShards(t, dir)
	fullPath, _, err := BuildGraph(full)
	if err != nil {
		t.Fatalf("full rebuild: %v", err)
	}
	requireSameGraph(t, "incremental vs full rebuild",
		withoutGenerations(readGraph(t, path)),
		withoutGenerations(readGraph(t, fullPath)))
}

// TestRefreshGraphNoticesRefiledContent guards the case a plain blob-SHA
// roster would miss. Symbol extraction picks its extractor from the blob's first
// file ref and verification tries languages in file-ref order, so the SAME bytes
// graph differently depending on the extension they are filed under. Renaming a
// file without editing it leaves the content SHA untouched, so the roster has to
// carry the path context or the refresh would carry stale edges forward.
func TestRefreshGraphNoticesRefiledContent(t *testing.T) {
	dir := t.TempDir()
	callerBody := "package caller\n\nfunc Caller() { Target() }\n"
	files := []graphFile{
		{repo: "target", path: "target.go", content: "package target\n\nfunc Target() {}\n"},
		{repo: "caller", path: "caller.txt", content: callerBody},
	}
	writeGraphShards(t, dir, files, 1)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}
	callerSHA := diskstore.GitBlobSHA1([]byte(callerBody))
	asText := edgesFrom(readGraph(t, path), callerSHA)
	if len(asText) == 0 {
		t.Fatal("the caller blob produced no edges to begin with")
	}

	// Same bytes, new extension: nothing about the content changed, but the Go
	// extractor and the Go verification patterns now apply to it.
	files[1].path = "caller.go"
	writeGraphShards(t, dir, files, 1)

	path, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if stats.Unchanged {
		t.Fatal("refresh saw no delta; a rename with identical content went unnoticed")
	}
	asGo := edgesFrom(readGraph(t, path), callerSHA)
	if reflect.DeepEqual(withoutGenerations(asText), withoutGenerations(asGo)) {
		t.Fatalf("the rename changed nothing about the caller's adjacency (%+v), so this fixture no longer exercises the roster's path context", asGo)
	}

	full := copyShards(t, dir)
	fullPath, _, err := BuildGraph(full)
	if err != nil {
		t.Fatalf("full rebuild: %v", err)
	}
	requireSameGraph(t, "incremental vs full rebuild after a rename",
		withoutGenerations(readGraph(t, path)),
		withoutGenerations(readGraph(t, fullPath)))
}

// edgesFrom returns the records whose source node is sha, in on-disk order.
func edgesFrom(records []graphRecord, sha string) []graphRecord {
	var out []graphRecord
	for _, r := range records {
		if r.key.BlobSHA == sha {
			out = append(out, r)
		}
	}
	return out
}

// TestRefreshGraphCarriesNamelessEdgesForward is F-01's regression gate.
// Edges from the whole-corpus passes (HTTP, manifest, hierarchy, injection,
// queries, renders, and — outside this build — LSP calls and SIMILAR_TO) carry
// no symbol name, so the incremental carry-forward loop's `sweep.eligible[name]`
// eligibility check — meant to catch a name that no longer exists anywhere in
// the corpus — used to reject every one of them unconditionally, since the
// empty string is never an eligible name. A delta touching any unrelated blob
// therefore dropped every EXTENDS/IMPLEMENTS/CONTAINS_METHOD/etc. edge in the
// corpus, forever. This pins a C# EXTENDS edge surviving a refresh that never
// touches either endpoint.
func TestRefreshGraphCarriesNamelessEdgesForward(t *testing.T) {
	dir := t.TempDir()
	files := []graphFile{
		{repo: "hier", path: "Bar.cs", content: "public class Bar { }\n"},
		{repo: "hier", path: "Foo.cs", content: "public class Foo : Bar { }\n"},
		{repo: "hier", path: "other.go", content: "package other\n\nfunc Other() {}\n"},
	}
	writeGraphShards(t, dir, files, 3)
	path, report, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}
	if report.Hierarchy.ExtendsEdges == 0 {
		t.Fatal("fixture produced no EXTENDS edge to begin with")
	}
	wantExtends := countEdgeType(readGraph(t, path), diskgraph.EdgeExtends)
	if wantExtends == 0 {
		t.Fatal("initial graph holds no EXTENDS edge")
	}
	for _, r := range readGraph(t, path) {
		if r.edge.Type == diskgraph.EdgeExtends && r.edge.Name != "" {
			t.Fatalf("EXTENDS edge unexpectedly carries a symbol name: %+v", r)
		}
	}

	// Touch a file that names neither Foo nor Bar, so the delta's dirty-name
	// computation never marks either type stale and the EXTENDS edge between
	// them is a pure carry-forward candidate.
	files[2].content += "\n// touched\n"
	writeGraphShards(t, dir, files, 3)

	refreshed, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if stats.FullRebuild || stats.Unchanged {
		t.Fatalf("expected a delta refresh, got %+v", stats)
	}

	gotExtends := countEdgeType(readGraph(t, refreshed), diskgraph.EdgeExtends)
	if gotExtends != wantExtends {
		t.Fatalf("EXTENDS edges after refresh = %d, want %d; a nameless edge was dropped by an incremental refresh that never touched either endpoint", gotExtends, wantExtends)
	}

	// And it agrees with a full rebuild, exactly like every other delta case.
	full := copyShards(t, dir)
	fullPath, _, err := BuildGraph(full)
	if err != nil {
		t.Fatalf("full rebuild: %v", err)
	}
	requireSameGraph(t, "incremental vs full rebuild",
		withoutGenerations(readGraph(t, refreshed)),
		withoutGenerations(readGraph(t, fullPath)))
}

// countEdgeType counts records of the given type.
func countEdgeType(records []graphRecord, want diskgraph.EdgeType) int {
	n := 0
	for _, r := range records {
		if r.edge.Type == want {
			n++
		}
	}
	return n
}

// TestRefreshGraphUnchangedCorpusRewritesNothing checks the cheapest
// case: identical content means an identical graph, so the file on disk is
// already the answer and must not be churned (which would reset every edge's
// staleness stamp).
func TestRefreshGraphUnchangedCorpusRewritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeGraphShards(t, dir, synthGraphCorpus(12), 4)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("initial build: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeRecords := readGraph(t, path)

	_, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !stats.Unchanged {
		t.Fatalf("expected an unchanged corpus, got %+v", stats)
	}
	if stats.NamesRecomputed != 0 || stats.EdgesRecomputed != 0 {
		t.Fatalf("unchanged corpus recomputed %d name(s) / %d edge(s)", stats.NamesRecomputed, stats.EdgesRecomputed)
	}
	if stats.Generation != diskgraph.FirstGeneration {
		t.Fatalf("generation = %d, want it held at %d", stats.Generation, diskgraph.FirstGeneration)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("graph was rewritten despite identical corpus content")
	}
	requireSameGraph(t, "untouched graph", readGraph(t, path), beforeRecords)
}

// TestRefreshGraphFallsBackToFullRebuild covers the two ways a refresh
// can lose its seed. Both must produce a correct graph and say what happened;
// the graph is a derived cache, so an unreadable one is never fatal.
func TestRefreshGraphFallsBackToFullRebuild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    func(t *testing.T, dir string)
		wantMsg string
	}{
		{
			name:    "no previous graph",
			seed:    func(*testing.T, string) {},
			wantMsg: "no previous graph",
		},
		{
			name: "corrupt previous graph",
			seed: func(t *testing.T, dir string) {
				if err := os.WriteFile(GraphPath(dir), []byte("not a graph"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "unusable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGraphShards(t, dir, synthGraphCorpus(10), 5)
			tc.seed(t, dir)

			path, stats, err := RefreshGraph(dir)
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if !stats.FullRebuild {
				t.Fatalf("expected a full rebuild, got %+v", stats)
			}
			if !strings.Contains(stats.Reason, tc.wantMsg) {
				t.Fatalf("reason = %q, want it to mention %q", stats.Reason, tc.wantMsg)
			}
			if stats.Generation != diskgraph.FirstGeneration {
				t.Fatalf("generation = %d, want %d", stats.Generation, diskgraph.FirstGeneration)
			}
			full := copyShards(t, dir)
			fullPath, _, err := BuildGraph(full)
			if err != nil {
				t.Fatal(err)
			}
			requireSameGraph(t, "fallback rebuild", readGraph(t, path), readGraph(t, fullPath))
		})
	}
}

// TestRefreshGraphIsFasterThanFullRebuild measures the point of the
// phase. The deterministic half is the work ratio; the wall-clock half is
// compared against the BEST of two full rebuilds so a warm-cache full run is the
// bar the delta has to beat.
func TestRefreshGraphIsFasterThanFullRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("timing comparison needs the larger synthetic corpus")
	}
	dir := t.TempDir()
	files := synthGraphCorpus(320)
	writeGraphShards(t, dir, files, 20)
	if _, _, err := BuildGraph(dir); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	files[5].content += "\n// touched\n"
	writeGraphShards(t, dir, files, 20)

	full := copyShards(t, dir)
	fullTime := time.Duration(1<<63 - 1)
	for i := 0; i < 2; i++ {
		start := time.Now()
		if _, _, err := BuildGraph(full); err != nil {
			t.Fatalf("full rebuild: %v", err)
		}
		fullTime = min(fullTime, time.Since(start))
	}

	start := time.Now()
	_, stats, err := RefreshGraph(dir)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	deltaTime := time.Since(start)

	if stats.FullRebuild {
		t.Fatalf("refresh fell back to a full rebuild: %s", stats.Reason)
	}
	t.Logf("full rebuild %s (%d names) vs delta %s (%d names, %d edges carried)",
		fullTime, stats.NamesEligible, deltaTime, stats.NamesRecomputed, stats.EdgesCarried)

	// Deterministic: the sweep is per-name, so recomputing a small fraction of
	// the names is the saving, independent of machine load.
	if stats.NamesRecomputed*20 >= stats.NamesEligible {
		t.Fatalf("recomputed %d of %d name(s); expected under 5%%",
			stats.NamesRecomputed, stats.NamesEligible)
	}
	// Wall clock: a plain inequality against the faster of two full rebuilds. The
	// work ratio above is the tight bound; this only has to show the saving is
	// real rather than bookkeeping.
	if deltaTime >= fullTime {
		t.Fatalf("delta refresh took %s, full rebuild took %s", deltaTime, fullTime)
	}
}
