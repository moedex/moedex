package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"

	"moedex/internal/diskstore"
	"moedex/internal/version"
)

const (
	SnapshotMetaKey = "dev.moedex/snapshot"
	ServerMetaKey   = "dev.moedex/server"
)

// ServerIdentity identifies the running binary independently of the data
// snapshot. It is present even when a request fails before acquiring corpus or
// graph state, and on legacy protocol responses where standard response-server
// metadata is not guaranteed.
type ServerIdentity struct {
	Version string `json:"version"`
}

// SnapshotIdentity describes the immutable source snapshot that produced a
// tool result. It is the sole value accepted by the result builders below, so
// handlers never construct vendor metadata maps themselves.
type SnapshotIdentity struct {
	Cacheable              bool     `json:"cacheable"`
	CorpusFingerprint      string   `json:"corpus_fingerprint,omitempty"`
	GraphCorpusFingerprint string   `json:"graph_corpus_fingerprint,omitempty"`
	GraphGeneration        uint64   `json:"graph_generation,omitempty"`
	GraphBuildID           string   `json:"graph_build_id,omitempty"`
	BlobSHAs               []string `json:"blob_shas,omitempty"`
	UnanchoredPaths        []string `json:"unanchored_paths,omitempty"`
}

// Normalize sorts and deduplicates all identity sets. Unanchored content can
// never be represented honestly as cacheable.
func (s SnapshotIdentity) Normalize() SnapshotIdentity {
	s.BlobSHAs = sortedUnique(s.BlobSHAs)
	s.UnanchoredPaths = sortedUnique(s.UnanchoredPaths)
	if len(s.UnanchoredPaths) > 0 {
		s.Cacheable = false
	}
	return s
}

// MergeSnapshotIdentities composes independently acquired rank and graph
// identities without consulting either holder a second time.
func MergeSnapshotIdentities(left, right SnapshotIdentity) SnapshotIdentity {
	out := left
	if out.CorpusFingerprint == "" {
		out.CorpusFingerprint = right.CorpusFingerprint
	} else if right.CorpusFingerprint != "" && right.CorpusFingerprint != out.CorpusFingerprint {
		out.GraphCorpusFingerprint = right.CorpusFingerprint
	}
	if right.GraphCorpusFingerprint != "" {
		out.GraphCorpusFingerprint = right.GraphCorpusFingerprint
	}
	if right.GraphGeneration != 0 {
		out.GraphGeneration = right.GraphGeneration
	}
	if right.GraphBuildID != "" {
		out.GraphBuildID = right.GraphBuildID
	}
	out.BlobSHAs = append(out.BlobSHAs, right.BlobSHAs...)
	out.UnanchoredPaths = append(out.UnanchoredPaths, right.UnanchoredPaths...)
	out.Cacheable = left.Cacheable && right.Cacheable
	return out.Normalize()
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if value == "" || (len(out) > 0 && out[len(out)-1] == value) {
			continue
		}
		out = append(out, value)
	}
	return out
}

// StructuredResultWithSnapshot is the only successful/error result builder
// that stamps snapshot metadata.
func StructuredResultWithSnapshot(text string, structured any, isError bool, snapshot SnapshotIdentity) map[string]interface{} {
	snapshot = snapshot.Normalize()
	// Errors may retain an acquired snapshot for diagnosis, but they are never
	// reusable tool answers. Keeping this invariant in the sole result builder
	// prevents individual handlers from assigning contradictory cache policy.
	if isError {
		snapshot.Cacheable = false
	}
	return map[string]interface{}{
		"content":           []interface{}{map[string]interface{}{"type": "text", "text": text}},
		"structuredContent": structured,
		"isError":           isError,
		"_meta": map[string]interface{}{
			SnapshotMetaKey: snapshot,
			ServerMetaKey:   ServerIdentity{Version: version.MCP()},
		},
	}
}

// StructuredResult preserves the historical helper for callers that do not
// have a content snapshot. Such results are explicitly non-cacheable.
func StructuredResult(text string, structured any, isError bool) map[string]interface{} {
	return StructuredResultWithSnapshot(text, structured, isError, SnapshotIdentity{Cacheable: false})
}

// StructuredToolError returns the catalog-wide structured error variant.
func StructuredToolError(code, message string, snapshot SnapshotIdentity) map[string]interface{} {
	return StructuredResultWithSnapshot(message, map[string]interface{}{
		"error": map[string]interface{}{"code": code, "message": message},
	}, true, snapshot)
}

// TextResult keeps source compatibility for handlers and tests. Successful
// tools should use StructuredResultWithSnapshot; legacy successes are still
// machine-valid through a small status payload.
func TextResult(text string, isError bool) map[string]interface{} {
	if isError {
		return StructuredToolError("tool_error", text, SnapshotIdentity{Cacheable: false})
	}
	return StructuredResult(text, map[string]interface{}{"status": "resolved", "text": text}, false)
}

// HashFiles computes Git-compatible blob SHA-1 values for unique paths with
// bounded parallelism. Every read observes context cancellation. Unreadable
// paths are returned separately rather than discarding otherwise useful data.
func HashFiles(ctx context.Context, paths []string, parallelism int) (map[string]string, []string) {
	paths = sortedUnique(append([]string(nil), paths...))
	if parallelism <= 0 {
		parallelism = 4
	}
	if parallelism > len(paths) {
		parallelism = len(paths)
	}
	if parallelism == 0 {
		return map[string]string{}, nil
	}

	jobs := make(chan string)
	var mu sync.Mutex
	hashes := make(map[string]string, len(paths))
	var unanchored []string
	var wg sync.WaitGroup
	for range parallelism {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					mu.Lock()
					unanchored = append(unanchored, path)
					mu.Unlock()
					continue
				}
				content, err := os.ReadFile(path)
				if err != nil {
					mu.Lock()
					unanchored = append(unanchored, path)
					mu.Unlock()
					continue
				}
				sha := diskstore.GitBlobSHA1(content)
				mu.Lock()
				hashes[path] = sha
				mu.Unlock()
			}
		}()
	}
	for _, path := range paths {
		select {
		case jobs <- path:
		case <-ctx.Done():
			mu.Lock()
			unanchored = append(unanchored, path)
			mu.Unlock()
		}
	}
	close(jobs)
	wg.Wait()
	return hashes, sortedUnique(unanchored)
}

func resultSnapshot(raw map[string]interface{}) SnapshotIdentity {
	meta, _ := raw["_meta"].(map[string]interface{})
	value := meta[SnapshotMetaKey]
	data, _ := json.Marshal(value)
	var snapshot SnapshotIdentity
	_ = json.Unmarshal(data, &snapshot)
	return snapshot.Normalize()
}

func resultText(raw map[string]interface{}) string {
	contents, _ := raw["content"].([]interface{})
	if len(contents) == 0 {
		return ""
	}
	content, _ := contents[0].(map[string]interface{})
	text, _ := content["text"].(string)
	return text
}

var errRequestTooLarge = errors.New("request exceeds maximum size")
