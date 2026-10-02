package indexcmd

import (
	"context"
	"moedex/internal/diskstore"
	"moedex/internal/index"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionBuildSplitsLargeRepository(t *testing.T) {
	for _, selective := range []bool{false, true} {
		name := "eager"
		var sel index.GramSelector
		if selective {
			name = "selective"
			sel = index.FrequencyThresholdSelector{MaxDocFraction: 0.5}
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			initRepo(t, repo, map[string]string{"a.cs": strings.Repeat("a", 150), "b.cs": strings.Repeat("b", 40), "c.cs": strings.Repeat("c", 40), "d.cs": strings.Repeat("d", 40)})
			m, n, files, e := buildShards(context.Background(), root, t.TempDir(), 100, sel, func(string, ...any) {})
			if e != nil {
				t.Fatal(e)
			}
			if n != 3 || files != 4 || m.ShardBytes != 100 {
				t.Fatalf("shards=%d files=%d target=%d", n, files, m.ShardBytes)
			}
			seen := map[string]bool{}
			for _, s := range m.Shards {
				if len(s.Repos) != 1 || s.Repos[0] != repo {
					t.Fatal("source membership missing")
				}
				blobs, e := diskstore.LoadBlobs(s.Path)
				if e != nil {
					t.Fatal(e)
				}
				count := 0
				for _, b := range blobs {
					for _, f := range b.Files {
						if seen[f.RelPath] {
							t.Fatal("duplicate occurrence")
						}
						seen[f.RelPath] = true
						count++
					}
				}
				if s.ContentBytes > 100 && count != 1 {
					t.Fatal("oversize file co-packed")
				}
			}
			if len(seen) != 4 {
				t.Fatal("lost files")
			}
		})
	}
}
