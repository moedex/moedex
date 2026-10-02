package indexcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	configpkg "moedex/internal/config"
	"moedex/internal/embed"
	"moedex/internal/index"
	"moedex/internal/parity"
	server "moedex/internal/serve"
	indexsnapshot "moedex/internal/snapshot"
	"moedex/internal/version"
)

func defaultIndexDir() string {
	if configured := os.Getenv("MOEDEX_INDEX_DIR"); configured != "" {
		return configured
	}
	return configpkg.DefaultIndexDir()
}

func runSnapshotList(args []string) error {
	fs := newFlagSet("snapshot-list")
	indexDir := fs.String("index-dir", defaultIndexDir(), "snapshot index root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	current := ""
	if manifest, _, err := indexsnapshot.Current(*indexDir); err == nil {
		current = manifest.ID
	}
	list, err := indexsnapshot.List(*indexDir)
	if err != nil {
		return err
	}
	for _, manifest := range list {
		marker := " "
		if manifest.ID == current {
			marker = "*"
		}
		fmt.Printf("%s %s sequence=%d parent=%s created=%s capabilities=%s\n", marker, manifest.ID,
			manifest.Sequence, manifest.ParentID, manifest.CreatedAt.UTC().Format(time.RFC3339),
			strings.Join(manifest.Capabilities, ","))
	}
	return nil
}

func runSnapshotInspect(args []string) error {
	fs := newFlagSet("snapshot-inspect")
	indexDir := fs.String("index-dir", defaultIndexDir(), "snapshot index root")
	id := fs.String("id", "", "snapshot ID (default CURRENT)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var (
		manifest *indexsnapshot.Manifest
		err      error
	)
	if *id == "" {
		manifest, _, err = indexsnapshot.Current(*indexDir)
	} else {
		manifest, err = indexsnapshot.Load(*indexDir, *id)
	}
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func runSnapshotRollback(args []string) error {
	fs := newFlagSet("snapshot-rollback")
	indexDir := fs.String("index-dir", defaultIndexDir(), "snapshot index root")
	id := fs.String("id", "", "complete snapshot ID to make current")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("snapshot-rollback requires -id")
	}
	if err := indexsnapshot.SetCurrent(*indexDir, *id); err != nil {
		return err
	}
	fmt.Printf("CURRENT -> %s\n", *id)
	return nil
}

func runSnapshotMigrate(args []string) error {
	fs := newFlagSet("snapshot-migrate")
	indexDir := fs.String("index-dir", defaultIndexDir(), "snapshot index root")
	shardDir := fs.String("shard-dir", "", "legacy servable shard directory to copy")
	id := fs.String("id", "", "new snapshot ID (default timestamp-sequence)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *shardDir == "" {
		return fmt.Errorf("snapshot-migrate requires -shard-dir")
	}
	if *id == "" {
		list, err := indexsnapshot.List(*indexDir)
		if err != nil {
			return err
		}
		sequence := uint64(1)
		if len(list) > 0 {
			sequence = list[0].Sequence + 1
		}
		*id = indexsnapshot.NextID(time.Now(), sequence)
	}
	tx, err := indexsnapshot.Begin(*indexDir, *id)
	if err != nil {
		return err
	}
	defer tx.Abort()
	build := version.Get()
	tags := []string{}
	if embed.ONNXCompiled {
		tags = append(tags, "onnx")
	}
	manifest, err := indexsnapshot.StageLegacy(tx, *shardDir, indexsnapshot.Producer{
		Version: build.Tag, Commit: build.CommitShort(), GoVersion: build.Go, BuildTags: tags,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(manifest); err != nil {
		return err
	}
	fmt.Printf("published snapshot %s from %s; CURRENT advanced atomically\n", manifest.ID, *shardDir)
	return nil
}

// runSnapshotBuild writes lexical/rank and requested optional artifacts into a
// private transaction and publishes only after the complete tree validates.
func runSnapshotBuild(args []string) error {
	return runSnapshotBuildContext(context.Background(), args)
}

func runSnapshotBuildContext(ctx context.Context, args []string) error {
	fs := newFlagSet("snapshot-build")
	indexDir := fs.String("index-dir", defaultIndexDir(), "snapshot index root")
	corpus := fs.String("corpus", os.Getenv("MOEDEX_CORPUS"), "corpus root")
	id := fs.String("id", "", "new snapshot ID (default timestamp-sequence)")
	shardBytes := fs.Int64("shard-bytes", parity.DefaultShardBytes, "target indexed-content bytes per shard")
	verbose := fs.Bool("v", false, "log per-shard progress")
	selective := fs.Bool("selective", false, "use the parity-safe selective trigram index")
	gramMaxDF := fs.Float64("gram-max-df", 0.9, "with -selective: maximum trigram document-frequency fraction")
	dense := fs.Bool("dense", false, "build the ONNX dense component before publication (requires -tags onnx)")
	graph := fs.Bool("graph", true, "build the heuristic graph component before publication")
	semanticArtifact := fs.String("semantic-artifact", "", "attach a complete compiler artifact (including composed captures) after checking recorded inputs")
	semanticWorkspace := fs.String("semantic-workspace", "", "retained compiler projection for recorded source/configuration revalidation")
	semanticWorkspaces := fs.String("semantic-workspaces", "", "JSON map of every source snapshot ID to its retained compiler workspace; alternative to semantic-workspace")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" {
		return fmt.Errorf("snapshot-build requires -corpus")
	}
	if (*semanticWorkspace != "" || *semanticWorkspaces != "") && *semanticArtifact == "" {
		return fmt.Errorf("snapshot-build: semantic workspace options require semantic-artifact")
	}
	if *semanticWorkspace != "" && *semanticWorkspaces != "" {
		return fmt.Errorf("snapshot-build: semantic-workspace and semantic-workspaces are mutually exclusive")
	}
	root, err := filepath.Abs(*corpus)
	if err != nil {
		return err
	}
	if *id == "" {
		resolved, err := nextSnapshotID(*indexDir)
		if err != nil {
			return err
		}
		*id = resolved
	}
	tx, err := indexsnapshot.Begin(*indexDir, *id)
	if err != nil {
		return err
	}
	defer tx.Abort()
	serveDir := filepath.Join(tx.Dir(), "serve")
	if err := os.MkdirAll(serveDir, 0o755); err != nil {
		return err
	}

	var selector index.GramSelector
	if *selective {
		selector = index.FrequencyThresholdSelector{MaxDocFraction: *gramMaxDF}
	}
	m, nShards, nFiles, err := buildShards(ctx, root, serveDir, *shardBytes, selector, mkLogf(*verbose))
	if err != nil {
		return err
	}
	if err := parity.WriteManifest(filepath.Join(serveDir, parity.ManifestName), m); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if _, _, err := server.BuildSidecars(serveDir); err != nil {
		return fmt.Errorf("build ranking components: %w", err)
	}
	if *graph {
		if _, stats, err := buildGraph(serveDir); err != nil {
			return fmt.Errorf("build graph component: %w", err)
		} else {
			printGraphStats(stats)
		}
	}
	if *dense {
		if err := buildSnapshotDense(serveDir); err != nil {
			return fmt.Errorf("build dense component: %w", err)
		}
	}
	finalServeDir := filepath.Join(indexsnapshot.SnapshotPath(*indexDir, *id), "serve")
	if err := rewriteManifestPathsTo(serveDir, finalServeDir); err != nil {
		return err
	}
	manifest, err := indexsnapshot.CaptureServingTree(tx, snapshotProducer())
	if err != nil {
		return err
	}
	if *semanticArtifact != "" {
		if err := attachSemantic(ctx, *semanticArtifact, tx, manifest, m, *semanticWorkspace, *semanticWorkspaces); err != nil {
			return fmt.Errorf("attach semantic artifact: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(manifest); err != nil {
		return err
	}
	fmt.Printf("published snapshot %s: %d shard(s), %d file(s), %d repo(s); CURRENT advanced atomically\n",
		manifest.ID, nShards, nFiles, len(m.Heads))
	return nil
}

func nextSnapshotID(indexDir string) (string, error) {
	list, err := indexsnapshot.List(indexDir)
	if err != nil {
		return "", err
	}
	sequence := uint64(1)
	if len(list) > 0 {
		sequence = list[0].Sequence + 1
	}
	return indexsnapshot.NextID(time.Now(), sequence), nil
}

func snapshotProducer() indexsnapshot.Producer {
	build := version.Get()
	tags := []string{}
	if embed.ONNXCompiled {
		tags = append(tags, "onnx")
	}
	return indexsnapshot.Producer{
		Version: build.Tag, Commit: build.CommitShort(), GoVersion: build.Go, BuildTags: tags,
	}
}
