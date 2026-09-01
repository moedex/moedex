package graphserve

import (
	"path/filepath"
	"reflect"
	"testing"

	"moedex/internal/symbol"
)

// Three repos, one shard each. "Charge" is DEFINED in billing-core and
// billing-legacy and REFERENCED from orders-api — the cross-repo shape the merged
// lookup exists to answer.
var symbolRepos = map[string]map[string]string{
	"billing-core": {
		"charge.go": "package billing\n\nfunc Charge(amount int) int { return amount }\n",
	},
	"orders-api": {
		"order.go": "package orders\n\nfunc Submit(n int) int { return Charge(n) }\n",
	},
	"billing-legacy": {
		"legacy.go": "package legacy\n\nfunc Charge(cents int) int { return cents * 2 }\n",
	},
}

// repoOf maps a resolved site to its repo for terse set assertions.
func reposOfSites(sites []SymbolSite) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.Repo)
	}
	return out
}

func TestOpenSymbols_ResolvesNamesAcrossShards(t *testing.T) {
	// buildDedupedDir sorts repo labels, so shard IDs follow that order:
	// 0 = billing-core, 1 = billing-legacy, 2 = orders-api.
	dir := buildDedupedDir(t, symbolRepos)
	sc, err := OpenSymbols(dir)
	if err != nil {
		t.Fatalf("OpenSymbols: %v", err)
	}
	defer sc.Close()

	if got := sc.NumShards(); got != 3 {
		t.Fatalf("NumShards = %d, want 3", got)
	}
	if sc.NumNames() == 0 {
		t.Fatal("NumNames = 0, want the merged lookup to cover names")
	}

	refs := sc.References("Charge")
	if len(refs) != 3 {
		t.Fatalf("References(\"Charge\") = %d sites, want 3 (2 defs + 1 ref); got %+v", len(refs), refs)
	}
	// Ordered by shard, so repo order follows the sorted shard set.
	if got, want := reposOfSites(refs), []string{"billing-core", "billing-legacy", "orders-api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("References repos = %v, want %v", got, want)
	}
	if got, want := sc.DefiningRepos("Charge"), []string{"billing-core", "billing-legacy"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DefiningRepos(\"Charge\") = %v, want %v", got, want)
	}
	if got, want := sc.ReferencingRepos("Charge"), []string{"orders-api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ReferencingRepos(\"Charge\") = %v, want %v", got, want)
	}

	// Each site must carry a usable location: distinct shard files, the right
	// file, a 1-based line, and a byte range that spans the name.
	shardsSeen := map[string]bool{}
	for _, s := range refs {
		shardsSeen[s.Shard] = true
		if filepath.Ext(s.Shard) != ".idx" {
			t.Errorf("site %+v: Shard = %q, want a shard basename", s, s.Shard)
		}
		if s.Name != "Charge" {
			t.Errorf("site %+v: Name = %q, want Charge", s, s.Name)
		}
		if s.Line != 3 {
			t.Errorf("%s/%s: Line = %d, want 3", s.Repo, s.RelPath, s.Line)
		}
		if s.End-s.Start != len("Charge") {
			t.Errorf("%s/%s: byte range [%d,%d) does not span %q", s.Repo, s.RelPath, s.Start, s.End, "Charge")
		}
		if s.SHA == "" {
			t.Errorf("%s/%s: empty SHA", s.Repo, s.RelPath)
		}
		if got, want := s.Repos, []string{s.Repo}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s/%s: Repos = %v, want %v", s.Repo, s.RelPath, got, want)
		}
	}
	if len(shardsSeen) != 3 {
		t.Errorf("sites came from %d distinct shard files, want 3: %v", len(shardsSeen), shardsSeen)
	}

	// Roles: the two definitions and the one call site.
	defs := sc.Definitions("Charge")
	if got, want := reposOfSites(defs), []string{"billing-core", "billing-legacy"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Definitions repos = %v, want %v", got, want)
	}
	for _, d := range defs {
		if d.Role != symbol.Definition {
			t.Errorf("Definitions returned role %v for %s", d.Role, d.Repo)
		}
	}

	// A name local to one repo stays local.
	if got, want := sc.DefiningRepos("Submit"), []string{"orders-api"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DefiningRepos(\"Submit\") = %v, want %v", got, want)
	}
	// A name nothing knows resolves to nothing, not a panic.
	if got := sc.References("NoSuchSymbolAnywhere"); got != nil {
		t.Errorf("References(unknown) = %v, want nil", got)
	}
	if got := sc.DefiningRepos("NoSuchSymbolAnywhere"); got != nil {
		t.Errorf("DefiningRepos(unknown) = %v, want nil", got)
	}
}

// TestOpenSymbols_LegacyDir covers the inlined-content (MOEDEX03/04) shard format:
// no shared content store, so Close has nothing to release and content is
// heap-resident.
func TestOpenSymbols_LegacyDir(t *testing.T) {
	dir := t.TempDir()
	buildShard(t, dir, "shard-0000.idx", map[string]string{
		"pay.go": "package pay\n\nfunc Settle(id string) {}\n",
	})
	buildShard(t, dir, "shard-0001.idx", map[string]string{
		"call.go": "package call\n\nfunc Run(id string) { Settle(id) }\n",
	})

	sc, err := OpenSymbols(dir)
	if err != nil {
		t.Fatalf("OpenSymbols(legacy): %v", err)
	}
	defer sc.Close()

	got := sc.Merged().References("Settle")
	if len(got) != 2 {
		t.Fatalf("Settle occurrences = %d, want 2 (def in shard 0, ref in shard 1); got %+v", len(got), got)
	}
	if got[0].Shard != 0 || got[0].Role != symbol.Definition {
		t.Errorf("first occurrence = shard %d role %v, want shard 0 Definition", got[0].Shard, got[0].Role)
	}
	if got[1].Shard != 1 || got[1].Role != symbol.Reference {
		t.Errorf("second occurrence = shard %d role %v, want shard 1 Reference", got[1].Shard, got[1].Role)
	}
	if names := sc.Merged().DefiningShards("Settle"); !reflect.DeepEqual(names, []int{0}) {
		t.Errorf("DefiningShards(\"Settle\") = %v, want [0]", names)
	}
	// Shard names are the served basenames, in sorted order.
	if got, want := sc.Merged().ShardName(1), "shard-0001.idx"; got != want {
		t.Errorf("ShardName(1) = %q, want %q", got, want)
	}
}

// TestOpenSymbols_SharedContentAcrossShards pins the content-dedup interaction: a
// definition in content carried by two repos resolves to a site in each shard,
// both reporting the SAME blob SHA — one content node, several homes.
func TestOpenSymbols_SharedContentAcrossShards(t *testing.T) {
	shared := "package vendorlib\n\nfunc Helper(x int) int { return x }\n"
	dir := buildDedupedDir(t, map[string]map[string]string{
		"svc-one": {"vendor/lib.go": shared},
		"svc-two": {"third_party/lib.go": shared},
	})
	sc, err := OpenSymbols(dir)
	if err != nil {
		t.Fatalf("OpenSymbols: %v", err)
	}
	defer sc.Close()

	defs := sc.Definitions("Helper")
	if len(defs) != 2 {
		t.Fatalf("Helper definitions = %d, want one per shard; got %+v", len(defs), defs)
	}
	if defs[0].SHA != defs[1].SHA {
		t.Errorf("shared content SHAs differ: %q vs %q", defs[0].SHA, defs[1].SHA)
	}
	if defs[0].Shard == defs[1].Shard {
		t.Errorf("both definitions came from shard %q, want distinct shards", defs[0].Shard)
	}
	if got, want := sc.DefiningRepos("Helper"), []string{"svc-one", "svc-two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DefiningRepos(\"Helper\") = %v, want %v", got, want)
	}
}

func TestOpenSymbols_NoShards(t *testing.T) {
	if _, err := OpenSymbols(t.TempDir()); err == nil {
		t.Fatal("OpenSymbols on an empty dir succeeded, want an error")
	}
}
