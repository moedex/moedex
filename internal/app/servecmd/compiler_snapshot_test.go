package servecmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"moedex/internal/semantic"
	"moedex/internal/semanticindex"
	server "moedex/internal/serve"
	"moedex/internal/snapshot"
)

func compilerLeaseArtifact(marker string) *semantic.Artifact {
	hash := func(text string) string { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
	snap := semantic.SourceSnapshot{Repo: "repo", InputFingerprint: hash(marker)}
	snap.ID = snap.ComputeID()
	source := semantic.Source{SnapshotID: snap.ID, Path: "Code.cs", RawSHA256: hash(marker), ByteSize: uint64(len(marker))}
	source.ID = source.ComputeID()
	build := semantic.BuildContext{SnapshotID: snap.ID, Project: "Project.csproj", Capture: `{"fixture":"` + marker + `"}`, Extractor: "fixture", ExtractorVersion: "1", Status: "complete", SourceIDs: []string{source.ID}}
	build.InputFingerprint = hash(build.Capture)
	build.ID = build.ComputeID()
	sym := semantic.Symbol{Key: semantic.SymbolKey{Language: "csharp", NamespaceKind: "project", Namespace: "repo/Project.csproj", Descriptor: "T:" + marker, DescriptorKind: "documentation_comment_id"}}
	sym.ID = sym.ComputeID()
	occurrence := semantic.Occurrence{SourceID: source.ID, ContextID: build.ID, Role: "declaration", Kind: "declaration", Length: uint64(len(marker))}
	occurrence.ID = occurrence.ComputeID()
	binding := semantic.Binding{OccurrenceID: occurrence.ID, Status: "resolved", SymbolID: sym.ID, Method: "fixture", Extractor: "fixture", ExtractorVersion: "1"}
	binding.ID = binding.ComputeID()
	return &semantic.Artifact{Version: semantic.FormatVersion, Snapshots: []semantic.SourceSnapshot{snap}, Sources: []semantic.Source{source}, Contexts: []semantic.BuildContext{build}, Symbols: []semantic.Symbol{sym}, Occurrences: []semantic.Occurrence{occurrence}, Bindings: []semantic.Binding{binding}}
}
func publishCompilerLeaseFixture(t *testing.T, indexDir, id string, withCompiler bool) snapshot.Resolved {
	t.Helper()
	tx, err := snapshot.Begin(indexDir, id)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Abort()
	m, err := snapshot.StageLegacy(tx, makeShardDir(t, id), snapshot.Producer{})
	if err != nil {
		t.Fatal(err)
	}
	if withCompiler {
		a := compilerLeaseArtifact(id)
		if err := os.MkdirAll(filepath.Join(tx.Dir(), "semantic"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := semantic.Write(filepath.Join(tx.Dir(), snapshot.SemanticPath), a); err != nil {
			t.Fatal(err)
		}
		audit, err := snapshot.CaptureArtifact(tx.Dir(), snapshot.SemanticPath)
		if err != nil {
			t.Fatal(err)
		}
		m.Components[snapshot.SemanticComponent] = snapshot.Component{Format: snapshot.SemanticFormat, Version: snapshot.SemanticVersion, InputFingerprint: m.CorpusFingerprint, Metadata: map[string]string{"coverage": snapshot.SemanticCoverage, "usage": snapshot.SemanticUsage}, Artifacts: []snapshot.Artifact{audit}}
		if err := semanticindex.Build(filepath.Join(tx.Dir(), snapshot.SemanticIndexPath), a, semanticindex.Provenance{ArtifactSHA256: audit.SHA256, CorpusFingerprint: m.CorpusFingerprint}); err != nil {
			t.Fatal(err)
		}
		index, err := snapshot.CaptureArtifact(tx.Dir(), snapshot.SemanticIndexPath)
		if err != nil {
			t.Fatal(err)
		}
		m.Components[snapshot.SemanticIndexComponent] = snapshot.Component{Format: snapshot.SemanticIndexFormat, Version: snapshot.SemanticIndexVersion, InputFingerprint: m.CorpusFingerprint, Metadata: map[string]string{"source_artifact_sha256": audit.SHA256, "query_scope": snapshot.SemanticIndexScope}, Artifacts: []snapshot.Artifact{index}}
	}
	if err := tx.Commit(m); err != nil {
		t.Fatal(err)
	}
	resolved, err := snapshot.Resolve(indexDir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
func compilerLeaseHolder(t *testing.T, resolved snapshot.Resolved) *servingHolder {
	t.Helper()
	rank, err := server.OpenRank(context.Background(), resolved.ShardDir(), server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := openServingGraph(resolved.ShardDir())
	if err != nil {
		rank.Close()
		t.Fatal(err)
	}
	holder, err := newServingHolderResolved(rank, graph, &resolved)
	if err != nil {
		closeServingComponents(graph, rank)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := holder.Close(); err != nil {
			t.Error(err)
		}
	})
	return holder
}
func TestCompilerSnapshotLeaseSurvivesReloadAndAbsentDropsMapping(t *testing.T) {
	root := t.TempDir()
	a := publishCompilerLeaseFixture(t, root, "Alpha", true)
	holder := compilerLeaseHolder(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	old, err := holder.AcquireCompilerSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	active, release, err := holder.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldRank := active.rank
	release()
	b := publishCompilerLeaseFixture(t, root, "Beta", true)
	done := make(chan error, 1)
	go func() {
		openErr, closeErr := holder.ReloadResolved(ctx, b, server.RankConfig{})
		if openErr != nil {
			done <- openErr
		} else {
			done <- closeErr
		}
	}()
	awaitServingPublication(t, ctx, holder, oldRank)
	if rows, err := old.Reader.Bindings(ctx, semanticindex.Position{Repo: "repo", Path: "Code.cs"}, 10); err != nil || len(rows.Results) != 1 || rows.Results[0].Symbol.Key.Descriptor != "T:Alpha" {
		t.Fatalf("old lease: %+v %v", rows, err)
	}
	next, err := holder.AcquireCompilerSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := next.Reader.Bindings(ctx, semanticindex.Position{Repo: "repo", Path: "Code.cs"}, 10); err != nil || rows.Results[0].Symbol.Key.Descriptor != "T:Beta" || next.SnapshotID != b.ID {
		t.Fatalf("new lease: %+v %v", rows, err)
	}
	next.Release()
	select {
	case err := <-done:
		t.Fatalf("retired while old lease held: %v", err)
	default:
	}
	old.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c := publishCompilerLeaseFixture(t, root, "Gamma", false)
	if e, c := holder.ReloadResolved(ctx, c, server.RankConfig{}); e != nil || c != nil {
		t.Fatalf("absent reload: %v %v", e, c)
	}
	absent, err := holder.AcquireCompilerSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer absent.Release()
	if absent.Reader != nil {
		t.Fatal("absent generation retained previous compiler mapping")
	}
	if absent.SnapshotID != c.ID {
		t.Fatalf("absent generation identity = %q, want %q", absent.SnapshotID, c.ID)
	}
	names := map[string]bool{}
	for _, tool := range holder.Tools() {
		names[tool.Name()] = true
	}
	if !names["compiler_binding_at"] || !names["compiler_definitions"] {
		t.Fatal("absent generation lost stable tool registration")
	}
}

func TestCompilerSnapshotRejectsRankFromDifferentResolvedDirectory(t *testing.T) {
	root := t.TempDir()
	a := publishCompilerLeaseFixture(t, root, "Alpha", true)
	b := publishCompilerLeaseFixture(t, root, "Beta", true)
	rank, err := server.OpenRank(context.Background(), a.ShardDir(), server.RankConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer rank.Close()
	graph, err := openServingGraph(a.ShardDir())
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close()
	if _, err := newServingHolderResolved(rank, graph, &b); err == nil {
		t.Fatal("rank A + compiler manifest B accepted")
	}
	b = a
	b.ID = "other-id"
	if _, err := newServingHolderResolved(rank, graph, &b); err == nil {
		t.Fatal("resolved/manifest ID mismatch accepted")
	}
}
func TestCompilerSnapshotCorruptionRetainsPairAndLegacyDoesNotInfer(t *testing.T) {
	root := t.TempDir()
	a := publishCompilerLeaseFixture(t, root, "Alpha", true)
	holder := compilerLeaseHolder(t, a)
	b := publishCompilerLeaseFixture(t, root, "Beta", true)
	path := filepath.Join(b.Root, snapshot.SemanticIndexPath)
	if err := os.WriteFile(path, []byte("corrupt"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveReloadSnapshot(root, a.ShardDir(), false); err == nil {
		t.Fatal("corrupt CURRENT resolved")
	}
	if openErr, _ := holder.ReloadResolved(context.Background(), b, server.RankConfig{}); openErr == nil {
		t.Fatal("corrupt explicit resolved input published")
	}
	session, err := holder.AcquireCompilerSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.SnapshotID != a.ID || session.Reader == nil {
		t.Fatal("corrupt reload changed pair")
	}
	session.Release()
	// Reusing a manifest's shard path alone must not infer the compiler sibling.
	if openErr, closeErr := holder.Reload(context.Background(), a.ShardDir(), server.RankConfig{}); openErr != nil || closeErr != nil {
		t.Fatalf("legacy reload: %v %v", openErr, closeErr)
	}
	session, err = holder.AcquireCompilerSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Reader != nil {
		t.Fatal("legacy dir inferred sibling compiler artifact")
	}
}
