package graphbuild

import (
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/symbol"
)

// scopedTypeTarget separates an observed type-name use from a resolved binding.
// These syntax-only passes cannot prove a binding: even a unique same-repository
// target is only Pattern. Candidate retains ambiguous/foreign matches for
// diagnostics without exposing them through the default graph confidence floor.
type scopedTypeTarget struct {
	ref        symbol.ShardRef
	confidence graph.ConfidenceTier
}

// scopedTypeTargets deliberately does not infer namespace, package, overload or
// build-configuration resolution from short names. A Pattern target must be the
// sole distinct same-language type declaration in the source repository. Both
// blobs must have exactly one repository/path context. Content-addressed nodes cannot encode
// different bindings for byte-identical files in different contexts; those
// bindings remain Candidate until the graph can distinguish occurrences.
func scopedTypeTargets(sweep *graphSweep, sourceSHA, name string) []scopedTypeTarget {
	var targets []scopedTypeTarget
	seen := make(map[diskgraph.Key]bool)
	sourceRepo, sourceUnique := singleTypeContext(sweep, sourceSHA)
	sourceLanguage := typeContextLanguage(sweep, sourceSHA)
	localCount, localIndex := 0, -1
	for _, ref := range sweep.merged.Definitions(name) {
		if ref.Shard < 0 || ref.Shard >= len(sweep.idxs) || ref.Start < 0 {
			continue
		}
		blob := sweep.idxs[ref.Shard].Blob(ref.Blob)
		if blob == nil || blob.SHA == "" {
			continue
		}
		kind, ok := queryDefinitionKind(sweep, ref)
		if !ok || kind != symbol.Type {
			continue
		}
		key := diskgraph.Key{BlobSHA: blob.SHA, SymbolOffset: uint64(ref.Start)}
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, scopedTypeTarget{ref: ref, confidence: graph.Candidate})
		if sourceUnique && sourceLanguage != "" && typeContextLanguage(sweep, blob.SHA) == sourceLanguage {
			for _, repo := range sweep.reposBySHA[blob.SHA] {
				if repo == sourceRepo {
					localCount++
					localIndex = len(targets) - 1
					break
				}
			}
		}
	}
	if localCount == 1 {
		ref := targets[localIndex].ref
		blob := sweep.idxs[ref.Shard].Blob(ref.Blob)
		if repo, unique := singleTypeContext(sweep, blob.SHA); unique && repo == sourceRepo {
			targets[localIndex].confidence = graph.Pattern
		}
	}
	return targets
}

// These are the languages understood by the hierarchy/DI passes. Types from
// other source languages remain name-collision candidates, never local bindings.
func typeContextLanguage(sweep *graphSweep, sha string) string {
	sites := sweep.sites[sha]
	if len(sites) == 0 {
		return ""
	}
	blob := sweep.idxs[sites[0].shard].Blob(sites[0].blob)
	if blob == nil {
		return ""
	}
	switch blobPrimaryExtension(blob) {
	case ".cs":
		return "csharp"
	case ".ts", ".tsx":
		return "typescript"
	default:
		return ""
	}
}

func singleTypeContext(sweep *graphSweep, sha string) (string, bool) {
	type contextKey struct{ repo, path string }
	contexts := make(map[contextKey]struct{})
	for _, site := range sweep.sites[sha] {
		blob := sweep.idxs[site.shard].Blob(site.blob)
		if blob == nil {
			return "", false
		}
		for _, file := range blob.Files {
			if file.Repo == "" || file.RelPath == "" {
				return "", false
			}
			contexts[contextKey{file.Repo, file.RelPath}] = struct{}{}
		}
	}
	if len(contexts) != 1 {
		return "", false
	}
	for ctx := range contexts {
		return ctx.repo, true
	}
	return "", false
}
