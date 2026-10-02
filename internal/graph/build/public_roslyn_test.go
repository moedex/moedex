package graphbuild

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
)

func TestPinnedRoslynGraphLiteralDefinitions(t *testing.T) {
	root, path := os.Getenv("MOEDEX_ROSLYN_SOURCE"), os.Getenv("MOEDEX_ROSLYN_GRAPH")
	if root == "" || path == "" {
		t.Skip("set MOEDEX_ROSLYN_SOURCE and MOEDEX_ROSLYN_GRAPH for the public gate")
	}
	read := func(rel, hash string, rawOffset int) diskgraph.Key {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != hash {
			t.Fatal("source differs from the frozen Roslyn anchor")
		}
		if rawOffset+4 > len(b) || string(b[rawOffset:rawOffset+4]) != "Main" {
			t.Fatal("source anchor drift")
		}
		// Ingest retains the raw Git identity but strips a UTF-8 BOM from
		// indexed bytes. Graph offsets address those indexed bytes.
		indexed := bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
		return diskgraph.Key{BlobSHA: diskstore.GitBlobSHA1(b), SymbolOffset: uint64(rawOffset - (len(b) - len(indexed)))}
	}
	real := read("src/Tools/CompilerGeneratorTools/Source/CSharpErrorFactsGenerator/Program.cs", "f797971c625c212d0cedd7ace6be8d5772842f4fa2dba0e0e61513fe916237c4", 471)
	quoted := read("src/Compilers/CSharp/Test/Semantic/Semantics/SemanticErrorTests.cs", "4c26cfaf459c6baba75982d94960644e12d557c8c16223a93cc52511a185a517", 1101)
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.NumRecords() >= g.NumEdges() || g.NumTargetSets() == 0 {
		t.Fatal("public corpus did not persist shared candidate targets")
	}
	targets := map[diskgraph.Key]bool{}
	for _, edge := range g.Edges(real) {
		if edge.Name != "Main" || edge.Type != diskgraph.EdgeSiblingDefinition {
			continue
		}
		if edge.Confidence != graph.Candidate {
			t.Fatal("same-name sibling relationship was promoted beyond Candidate")
		}
		targets[diskgraph.Key{BlobSHA: edge.TargetBlob, SymbolOffset: edge.TargetOffset}] = true
	}
	if len(targets) == 0 {
		t.Fatal("real Main declaration has no sibling relationships")
	}
	if targets[quoted] || len(g.Edges(quoted)) != 0 {
		t.Fatal("quoted Main program became a graph definition")
	}
	for i := 0; i < g.NumNodes(); i++ {
		key, _, _, ok := g.NodeAt(i)
		if !ok || key == quoted {
			t.Fatal("invalid node or quoted Main definition in persisted graph")
		}
	}
	targets[real] = true
	t.Logf("nodes=%d logical_edges=%d stored_records=%d target_sets=%d distinct Main definitions including source=%d; quoted anchor excluded and sibling confidence retained", g.NumNodes(), g.NumEdges(), g.NumRecords(), g.NumTargetSets(), len(targets))
}
