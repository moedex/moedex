package symbol

import (
	"reflect"
	"testing"

	"moedex/internal/index"
)

// The three shard fixtures deliberately COLLIDE on the name "Add" and on their
// shard-local blob IDs (each shard's only blob is blob 0), so a lookup that
// dropped the shard qualification would silently fold three distinct sites into
// one. shardC both defines and references Add, so the def/ref split is exercised
// within a single shard as well as across shards.
const (
	shardASrc = `package a

func Add(x, y int) int { return x + y }
`
	shardBSrc = `package b

func UseB() int { return Add(1, 2) }
`
	shardCSrc = `package c

func Add(x int) int { return x }

func UseC() int { return Add(3) }
`
)

// shardIndex builds a one-blob shard: a content index holding src at relPath,
// plus the symbol index extracted from it. Both are returned because a ShardRef
// is only resolvable against the shard's own indices.
func shardIndex(t *testing.T, repo, relPath, sha, src string) (*index.Index, *Index) {
	t.Helper()
	ix := index.New()
	ix.AddFile(repo, relPath, "/"+repo+"/"+relPath, sha, []byte(src))
	sym := BuildMulti(ix)
	if sym.NumBlobs() == 0 {
		t.Fatalf("shard %s: BuildMulti extracted no symbols from %s", repo, relPath)
	}
	return ix, sym
}

// threeShards returns the merged corpus over shards A/B/C plus their content
// indices, keyed by shard ID.
func threeShards(t *testing.T) (*Corpus, []*index.Index) {
	t.Helper()
	ixA, symA := shardIndex(t, "repo-a", "a.go", "sha-a", shardASrc)
	ixB, symB := shardIndex(t, "repo-b", "b.go", "sha-b", shardBSrc)
	ixC, symC := shardIndex(t, "repo-c", "c.go", "sha-c", shardCSrc)
	c := Merge(
		Shard{Name: "000.idx", Index: symA},
		Shard{Name: "001.idx", Index: symB},
		Shard{Name: "002.idx", Index: symC},
	)
	return c, []*index.Index{ixA, ixB, ixC}
}

// shardsOf lists the shard IDs of refs in order, with duplicates, for assertions
// about which shards a lookup reached.
func shardsOf(refs []ShardRef) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Shard)
	}
	return out
}

// distinctShards reduces refs to the ascending set of shard IDs they touch.
func distinctShards(refs []ShardRef) []int {
	var out []int
	seen := map[int]bool{}
	for _, r := range refs {
		if !seen[r.Shard] {
			seen[r.Shard] = true
			out = append(out, r.Shard)
		}
	}
	return out
}

// TestCorpus_ReferencesSpanShards is the cross-shard resolution proof: one name
// defined in two shards and referenced in two shards resolves to occurrences
// from EVERY contributing shard in a single lookup, which no per-shard index can
// answer.
func TestCorpus_ReferencesSpanShards(t *testing.T) {
	c, ixs := threeShards(t)

	if got := c.NumShards(); got != 3 {
		t.Fatalf("NumShards = %d, want 3", got)
	}

	refs := c.References("Add")
	if len(refs) == 0 {
		t.Fatal(`References("Add") = nil, want occurrences from several shards`)
	}
	if got := distinctShards(refs); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("References(\"Add\") reached shards %v, want all of [0 1 2]; refs = %+v", got, refs)
	}
	// Shard A: definition only. Shard B: reference only. Shard C: both.
	wantRoles := map[int]map[Role]int{
		0: {Definition: 1},
		1: {Reference: 1},
		2: {Definition: 1, Reference: 1},
	}
	gotRoles := map[int]map[Role]int{}
	for _, r := range refs {
		if gotRoles[r.Shard] == nil {
			gotRoles[r.Shard] = map[Role]int{}
		}
		gotRoles[r.Shard][r.Role]++
	}
	if !reflect.DeepEqual(gotRoles, wantRoles) {
		t.Errorf("per-shard roles = %v, want %v", gotRoles, wantRoles)
	}

	// The blob IDs collide (every shard's hit is in its blob 0), so the shard
	// field is the only thing distinguishing these sites — assert that directly.
	for _, r := range refs {
		if r.Blob != 0 {
			t.Errorf("shard %d: blob = %d, want the shard-local blob 0", r.Shard, r.Blob)
		}
	}
	if got := shardsOf(refs); !sortedAscending(got) {
		t.Errorf("References order = %v, want ascending by shard", got)
	}

	// Every offset must slice back to "Add" in ITS OWN shard's blob content: a
	// ref resolved against the wrong shard's index would land on unrelated bytes.
	for _, r := range refs {
		blob := ixs[r.Shard].Blob(r.Blob)
		if blob == nil {
			t.Fatalf("shard %d: no blob %d in that shard's content index", r.Shard, r.Blob)
		}
		if got := string(blob.Content[r.Start:r.End]); got != "Add" {
			t.Errorf("shard %d [%d,%d) = %q, want %q", r.Shard, r.Start, r.End, got, "Add")
		}
	}
}

// TestCorpus_DefinitionsAndShardSets covers the "which repos define / reference
// this" queries the merge exists to answer.
func TestCorpus_DefinitionsAndShardSets(t *testing.T) {
	c, _ := threeShards(t)

	defs := c.Definitions("Add")
	if got := distinctShards(defs); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Errorf("Definitions(\"Add\") shards = %v, want [0 2]", got)
	}
	for _, d := range defs {
		if d.Role != Definition {
			t.Errorf("Definitions returned role %v in shard %d", d.Role, d.Shard)
		}
	}
	if got := c.DefiningShards("Add"); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Errorf("DefiningShards(\"Add\") = %v, want [0 2]", got)
	}
	if got := c.ReferencingShards("Add"); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("ReferencingShards(\"Add\") = %v, want [1 2]", got)
	}

	// A name confined to one shard resolves to that shard alone.
	if got := c.DefiningShards("UseB"); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("DefiningShards(\"UseB\") = %v, want [1]", got)
	}
	if got := c.DefiningShards("UseC"); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("DefiningShards(\"UseC\") = %v, want [2]", got)
	}

	// Names carry shard identity back to the caller.
	if got := c.ShardName(1); got != "001.idx" {
		t.Errorf("ShardName(1) = %q, want %q", got, "001.idx")
	}
	if got := c.ShardName(99); got != "" {
		t.Errorf("ShardName(out of range) = %q, want \"\"", got)
	}
}

// TestCorpus_UnknownName pins the miss path: an unknown name is nil, not a panic,
// and never reports shards.
func TestCorpus_UnknownName(t *testing.T) {
	c, _ := threeShards(t)
	if got := c.References("NoSuchSymbol"); got != nil {
		t.Errorf("References(unknown) = %v, want nil", got)
	}
	if got := c.Definitions("NoSuchSymbol"); got != nil {
		t.Errorf("Definitions(unknown) = %v, want nil", got)
	}
	if got := c.DefiningShards("NoSuchSymbol"); got != nil {
		t.Errorf("DefiningShards(unknown) = %v, want nil", got)
	}
	if got := c.ReferencingShards("NoSuchSymbol"); got != nil {
		t.Errorf("ReferencingShards(unknown) = %v, want nil", got)
	}
}

// TestCorpus_DedupedContentResolvesToEveryShard documents the intended
// interaction with content dedup: the same content carried by two shards is a
// definition in BOTH, because a cross-shard lookup answers "which shards contain
// this", and the SHAs let a caller collapse it to one content node.
func TestCorpus_DedupedContentResolvesToEveryShard(t *testing.T) {
	ix1, sym1 := shardIndex(t, "repo-1", "vendor/lib.go", "sha-shared", shardASrc)
	ix2, sym2 := shardIndex(t, "repo-2", "third_party/lib.go", "sha-shared", shardASrc)
	c := Merge(Shard{Name: "s1", Index: sym1}, Shard{Name: "s2", Index: sym2})

	defs := c.Definitions("Add")
	if got := distinctShards(defs); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("shared content: Definitions shards = %v, want [0 1]", got)
	}
	// Same SHA on both sides: the caller's cue that these are one content node.
	sha0 := ix1.Blob(defs[0].Blob).SHA
	sha1 := ix2.Blob(defs[1].Blob).SHA
	if sha0 != sha1 {
		t.Errorf("shared content SHAs differ: %q vs %q", sha0, sha1)
	}
}

// TestCorpus_EmptyShardKeepsIDsAligned pins the ID contract: a shard with no
// symbol index still consumes its position, so shard IDs keep indexing the
// caller's own shard list.
func TestCorpus_EmptyShardKeepsIDsAligned(t *testing.T) {
	_, symA := shardIndex(t, "repo-a", "a.go", "sha-a", shardASrc)
	_, symB := shardIndex(t, "repo-b", "b.go", "sha-b", shardBSrc)
	c := Merge(
		Shard{Name: "empty.idx"}, // no symbols at all (e.g. an all-binary shard)
		Shard{Name: "a.idx", Index: symA},
		Shard{Name: "b.idx", Index: symB},
	)
	if got := c.NumShards(); got != 3 {
		t.Fatalf("NumShards = %d, want 3 (the empty shard holds its slot)", got)
	}
	if got := c.ShardIndex(0); got != nil {
		t.Errorf("ShardIndex(0) = %v, want nil for an index-less shard", got)
	}
	if got := c.DefiningShards("Add"); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("DefiningShards(\"Add\") = %v, want [1] — IDs must not shift", got)
	}
	if got := c.ReferencingShards("Add"); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("ReferencingShards(\"Add\") = %v, want [2] — IDs must not shift", got)
	}
}

// TestCorpus_EnclosingPerShard checks that a cross-shard ref widens to the
// enclosing definition in its OWN shard: shard B's reference to Add sits inside
// UseB, not inside shard A's Add.
func TestCorpus_EnclosingPerShard(t *testing.T) {
	c, ixs := threeShards(t)

	refs := c.References("Add")
	var bRef ShardRef
	found := false
	for _, r := range refs {
		if r.Shard == 1 {
			bRef, found = r, true
		}
	}
	if !found {
		t.Fatal("no shard-1 occurrence of Add")
	}
	s, ok := c.Enclosing(bRef.Shard, bRef.Blob, bRef.Start)
	if !ok {
		t.Fatal("Enclosing(shard 1) found no enclosing symbol for the Add call site")
	}
	if s.Name != "UseB" {
		t.Errorf("enclosing symbol = %q, want %q", s.Name, "UseB")
	}
	body := string(ixs[1].Blob(bRef.Blob).Content[s.BodyStart:s.BodyEnd])
	if !contains(body, "Add(1, 2)") {
		t.Errorf("enclosing body %q does not contain the call site", body)
	}
	if _, ok := c.Enclosing(99, 0, 0); ok {
		t.Error("Enclosing(out-of-range shard) reported ok, want false")
	}
	if got := c.Symbols(99, 0); got != nil {
		t.Errorf("Symbols(out-of-range shard) = %v, want nil", got)
	}
}

// TestCorpus_EachNameCoversEveryShard checks the enumeration seam sees names
// contributed by all shards, and honors an early stop.
func TestCorpus_EachNameCoversEveryShard(t *testing.T) {
	c, _ := threeShards(t)

	seen := map[string]bool{}
	c.EachName(func(n string) bool {
		seen[n] = true
		return true
	})
	for _, want := range []string{"Add", "UseB", "UseC"} {
		if !seen[want] {
			t.Errorf("EachName did not yield %q; saw %v", want, seen)
		}
	}
	if got, want := c.NumNames(), len(seen); got != want {
		t.Errorf("NumNames = %d, but EachName yielded %d distinct names", got, want)
	}

	n := 0
	c.EachName(func(string) bool { n++; return false })
	if n != 1 {
		t.Errorf("EachName ignored the early stop: called fn %d times, want 1", n)
	}
}

// TestCorpus_AddShardOnEmptyCorpus covers NewCorpus + incremental AddShard (the
// path a caller building shard-by-shard takes) and its returned IDs.
func TestCorpus_AddShardOnEmptyCorpus(t *testing.T) {
	c := NewCorpus()
	if c.NumShards() != 0 || c.NumNames() != 0 {
		t.Fatalf("NewCorpus is not empty: %d shards, %d names", c.NumShards(), c.NumNames())
	}
	if got := c.References("Add"); got != nil {
		t.Errorf("References on an empty corpus = %v, want nil", got)
	}
	_, symA := shardIndex(t, "repo-a", "a.go", "sha-a", shardASrc)
	_, symB := shardIndex(t, "repo-b", "b.go", "sha-b", shardBSrc)
	if id := c.AddShard("a.idx", symA); id != 0 {
		t.Errorf("first AddShard returned ID %d, want 0", id)
	}
	if id := c.AddShard("b.idx", symB); id != 1 {
		t.Errorf("second AddShard returned ID %d, want 1", id)
	}
	if got := distinctShards(c.References("Add")); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("References(\"Add\") shards = %v, want [0 1]", got)
	}
}

// sortedAscending reports whether xs is non-decreasing.
func sortedAscending(xs []int) bool {
	for i := 1; i < len(xs); i++ {
		if xs[i] < xs[i-1] {
			return false
		}
	}
	return true
}
