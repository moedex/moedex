package indexcmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"moedex/internal/diskstore"
	"moedex/internal/ingest"
	"moedex/internal/parity"
	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
	"moedex/internal/semanticindex"
	indexsnapshot "moedex/internal/snapshot"
)

// attachSemantic associates historical compiler evidence with this generation's
// indexed bytes. It does not rerun project evaluation or advertise live semantic
// freshness. The artifact stays outside the warm serving tree.
func attachSemantic(ctx context.Context, path string, tx *indexsnapshot.Transaction, manifest *indexsnapshot.Manifest, sources *parity.Manifest, defaultProjection, workspaceFile string) error {
	a, err := semantic.Read(path, semantic.Limits{})
	if err != nil {
		return err
	}
	if defaultProjection != "" && len(a.Snapshots) != 1 {
		return fmt.Errorf("semantic attachment: explicit workspace requires one repository snapshot")
	}
	projections, err := semanticWorkspaces(workspaceFile, a)
	if err != nil {
		return err
	}
	heads := make(map[string]parity.RepoHead, len(sources.Heads))
	for _, head := range sources.Heads {
		if _, exists := heads[head.Label]; exists {
			return fmt.Errorf("semantic attachment: ambiguous repository %q", head.Label)
		}
		heads[head.Label] = head
	}
	roots := make(map[string]string)
	repos := make(map[string]string)
	var policies []string
	for _, source := range a.Snapshots {
		projection := defaultProjection
		if workspaceFile != "" {
			projection = projections[source.ID]
		}
		head, ok := heads[source.Repo]
		if !ok {
			return fmt.Errorf("semantic attachment: repository %q is absent from index", source.Repo)
		}
		if source.Commit != "" && source.Commit != head.Head {
			return fmt.Errorf("semantic attachment: commit mismatch for %s", source.Repo)
		}
		if source.ProjectID != "" && source.ProjectID != strconv.FormatInt(head.ProjectID, 10) {
			return fmt.Errorf("semantic attachment: project identity mismatch for %s", source.Repo)
		}
		// Diagnostics and generated output can derive from any project input.
		// Until captures have sanitized-workspace provenance, use the existing
		// whole-workspace eligibility rule, not just per-file search filtering.
		allowed, err := ingest.LSPWorkspaceAllowed(head.Dir)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("semantic attachment: repository %s has restricted workspace inputs", source.Repo)
		}
		policy, err := ingest.AIPrivacyFingerprint(head.Dir)
		if err != nil {
			return err
		}
		if policy != head.PrivacyFingerprint {
			return fmt.Errorf("semantic attachment: privacy policy changed for %s", source.Repo)
		}
		policies = append(policies, source.Repo+"\x00"+policy)
		roots[source.ID], repos[source.ID] = head.Dir, source.Repo
		if projection != "" {
			allowed, err := ingest.LSPWorkspaceAllowed(projection)
			if err != nil {
				return err
			}
			projectedPolicy, err := ingest.AIPrivacyFingerprint(projection)
			if err != nil {
				return err
			}
			if !allowed || projectedPolicy != policy {
				return fmt.Errorf("semantic attachment: workspace privacy differs")
			}
			roots[source.ID] = projection
		}
	}
	if err := semanticimport.Revalidate(ctx, a, roots); err != nil {
		return err
	}
	if err := matchSemanticIndex(ctx, a, repos, sources); err != nil {
		return err
	}
	// Recheck policy after source/configuration validation as ingestion does.
	for _, source := range a.Snapshots {
		projection := defaultProjection
		if workspaceFile != "" {
			projection = projections[source.ID]
		}
		head := heads[source.Repo]
		policy, err := ingest.AIPrivacyFingerprint(head.Dir)
		if err != nil || policy != head.PrivacyFingerprint {
			return fmt.Errorf("semantic attachment: privacy policy changed for %s: %v", source.Repo, err)
		}
		if projection != "" {
			projectedPolicy, err := ingest.AIPrivacyFingerprint(projection)
			if err != nil || projectedPolicy != policy {
				return fmt.Errorf("semantic attachment: workspace privacy changed")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	destination := filepath.Join(tx.Dir(), filepath.FromSlash(indexsnapshot.SemanticPath))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := semantic.Write(destination, a); err != nil {
		return err
	}
	artifact, err := indexsnapshot.CaptureArtifact(tx.Dir(), indexsnapshot.SemanticPath)
	if err != nil {
		return err
	}
	sort.Strings(policies)
	policyHash := sha256.Sum256([]byte(strings.Join(policies, "\n")))
	manifest.Components[indexsnapshot.SemanticComponent] = indexsnapshot.Component{
		Format: semantic.Format, Version: semantic.FormatVersion,
		InputFingerprint: manifest.CorpusFingerprint,
		Metadata:         map[string]string{"coverage": "explicit-project-subset", "usage": "offline-only", "privacy_fingerprint": hex.EncodeToString(policyHash[:])},
		Artifacts:        []indexsnapshot.Artifact{artifact},
	}
	indexPath := filepath.Join(tx.Dir(), filepath.FromSlash(indexsnapshot.SemanticIndexPath))
	provenance := semanticindex.Provenance{ArtifactSHA256: artifact.SHA256, CorpusFingerprint: manifest.CorpusFingerprint}
	if err := semanticindex.Build(indexPath, a, provenance); err != nil {
		return fmt.Errorf("build compiler lookup index: %w", err)
	}
	reader, err := semanticindex.Open(indexPath, provenance, semanticindex.Limits{})
	if err != nil {
		return fmt.Errorf("validate compiler lookup index: %w", err)
	}
	if err := reader.Close(); err != nil {
		return err
	}
	indexed, err := indexsnapshot.CaptureArtifact(tx.Dir(), indexsnapshot.SemanticIndexPath)
	if err != nil {
		return err
	}
	manifest.Components[indexsnapshot.SemanticIndexComponent] = indexsnapshot.Component{
		Format: semanticindex.Format, Version: semanticindex.Version,
		InputFingerprint: manifest.CorpusFingerprint,
		Metadata:         map[string]string{"source_artifact_sha256": artifact.SHA256, "query_scope": indexsnapshot.SemanticIndexScope},
		Artifacts:        []indexsnapshot.Artifact{indexed},
	}
	return indexsnapshot.VerifySemanticComponent(tx.Dir(), manifest)
}

type semanticIndexedInput struct {
	hash            string
	size            uint64
	required, found bool
}

// Match by repository AND relative path; blob sharing does not imply shared
// bindings. Only ingestion's documented leading-BOM normalization is permitted.
func matchSemanticIndex(ctx context.Context, a *semantic.Artifact, repos map[string]string, manifest *parity.Manifest) error {
	inputs := map[string][]*semanticIndexedInput{}
	add := func(repo, path, hash string, size uint64, required bool) error {
		key := repo + "\x00" + path
		for _, prior := range inputs[key] {
			if (prior.required || required) && (prior.hash != hash || prior.size != size) {
				return fmt.Errorf("semantic attachment: conflicting captured input %s/%s", repo, path)
			}
		}
		for _, prior := range inputs[key] {
			if prior.hash == hash && prior.size == size {
				prior.required = prior.required || required
				return nil
			}
		}
		// Optional build inputs can differ between retained projections (for
		// example generated editorconfig files containing absolute paths).
		// Revalidate checked each projection already. If this path is indexed,
		// every recorded variant must still match the indexed bytes below.
		inputs[key] = append(inputs[key], &semanticIndexedInput{hash: hash, size: size, required: required})
		return nil
	}
	for _, source := range a.Sources {
		if strings.HasPrefix(source.Path, ".generated/") {
			continue
		}
		if err := add(repos[source.SnapshotID], source.Path, source.RawSHA256, source.ByteSize, !source.Generated); err != nil {
			return err
		}
	}
	// Compare captured configuration inputs when indexed too. Derived obj/assets
	// and binary inputs may be absent from lexical shards; Revalidate checks
	// their recorded bytes against the projection instead.
	for _, build := range a.Contexts {
		var value any
		dec := json.NewDecoder(strings.NewReader(build.Capture))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return err
		}
		var visit func(any) error
		visit = func(value any) error {
			switch v := value.(type) {
			case []any:
				for _, child := range v {
					if err := visit(child); err != nil {
						return err
					}
				}
			case map[string]any:
				if v["scope"] == "source" {
					path, _ := v["path"].(string)
					hash, _ := v["sha256"].(string)
					if hash != "" {
						number, ok := v["byte_size"].(json.Number)
						if !ok {
							return fmt.Errorf("semantic attachment: input size missing")
						}
						size, err := strconv.ParseUint(string(number), 10, 64)
						if err != nil {
							return err
						}
						if err := add(repos[build.SnapshotID], path, hash, size, path == build.Project); err != nil {
							return err
						}
					}
				}
				for _, child := range v {
					if err := visit(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := visit(value); err != nil {
			return err
		}
	}
	for _, shard := range manifest.Shards {
		if err := ctx.Err(); err != nil {
			return err
		}
		blobs, err := diskstore.LoadBlobs(shard.Path)
		if err != nil {
			return err
		}
		for _, blob := range blobs {
			for _, file := range blob.Files {
				for _, want := range inputs[file.Repo+"\x00"+file.RelPath] {
					h := sha256.New()
					switch want.size {
					case uint64(len(blob.Content)):
					case uint64(len(blob.Content)) + 3:
						_, _ = h.Write([]byte{0xef, 0xbb, 0xbf})
					default:
						return fmt.Errorf("semantic attachment: indexed size differs for %s/%s", file.Repo, file.RelPath)
					}
					_, _ = h.Write(blob.Content)
					if hex.EncodeToString(h.Sum(nil)) != want.hash {
						return fmt.Errorf("semantic attachment: indexed bytes differ for %s/%s", file.Repo, file.RelPath)
					}
					want.found = true
				}
			}
		}
	}
	for key, variants := range inputs {
		for _, input := range variants {
			if input.required && !input.found {
				return fmt.Errorf("semantic attachment: source not indexed: %s", strings.ReplaceAll(key, "\x00", "/"))
			}
		}
	}
	return nil
}
