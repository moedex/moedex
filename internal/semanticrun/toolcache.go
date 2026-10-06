package semanticrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

const maxToolCacheJSONBytes = 16 << 20

type ToolCacheObservation struct {
	Phase  string         `json:"phase"`
	Before DependencyFile `json:"before"`
	After  DependencyFile `json:"after"`
}

type toolCacheReceipt struct {
	Schema               string                 `json:"schema"`
	OriginalBundleSHA256 string                 `json:"original_bundle_sha256"`
	Status               string                 `json:"status"`
	Observations         []ToolCacheObservation `json:"observations"`
	Error                string                 `json:"error,omitempty"`
	ValidationErrors     []string               `json:"validation_errors,omitempty"`
}

func cloneDependencies(files map[string]DependencyFile) map[string]DependencyFile {
	out := make(map[string]DependencyFile, len(files))
	for name, entry := range files {
		out[name] = entry
	}
	return out
}

// Only the two generated NuGet JSON records in an already frozen tool directory
// can change. Package payloads and the original canonical manifest stay exact.
func toolCachePath(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 5 || parts[0] != ".tools" || !cleanRelative(name) {
		return false
	}
	return parts[4] == "project.assets.json" || parts[4] == parts[1]+".nuget.cache"
}

func validToolCacheJSON(name string, raw []byte) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return false
	}
	var version int
	if json.Unmarshal(obj["version"], &version) != nil {
		return false
	}
	if path.Base(name) == "project.assets.json" {
		if version != 3 {
			return false
		}
		for _, key := range []string{"targets", "libraries", "project"} {
			var value map[string]json.RawMessage
			if json.Unmarshal(obj[key], &value) != nil || value == nil {
				return false
			}
		}
		return true
	}
	var success *bool
	var hash, project string
	return version == 2 && json.Unmarshal(obj["success"], &success) == nil && success != nil &&
		json.Unmarshal(obj["dgSpecHash"], &hash) == nil && hash != "" &&
		json.Unmarshal(obj["projectFilePath"], &project) == nil && project != ""
}

// adoptToolCache is called only immediately after restore/reference preparation,
// with an explicit capture option. It returns observations even when validation
// fails, and commits the adopted map only after every immutable input rechecks.
func (b *DependencyBundle) adoptToolCache(ctx context.Context, phase string) ([]ToolCacheObservation, error) {
	if b == nil || b.owned == "" {
		return nil, fmt.Errorf("semanticrun: dependency bundle closed")
	}
	if err := b.verifyDirectory(ctx, b.canonical); err != nil {
		return nil, err
	}
	candidate := cloneDependencies(b.stagedFiles)
	names := make([]string, 0)
	for name := range b.files {
		if !toolCachePath(name) {
			continue
		}
		parts := strings.Split(name, "/")
		packagePrefix := parts[1] + "/" + parts[2] + "/"
		archive, archiveOK := b.files[packagePrefix+parts[1]+"."+parts[2]+".nupkg"]
		nuspec, nuspecOK := b.files[packagePrefix+parts[1]+".nuspec"]
		if !archiveOK || !nuspecOK || archive.Size == 0 || nuspec.Size == 0 {
			return nil, fmt.Errorf("semanticrun: tool cache has no frozen package archive and nuspec: %s", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	r, err := os.OpenRoot(b.owned)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	observations := make([]ToolCacheObservation, 0, len(names))
	var validationErr error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return observations, err
		}
		info, err := r.Lstat(name)
		if err != nil {
			return observations, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 != 0 || info.Size() > maxToolCacheJSONBytes {
			return observations, fmt.Errorf("semanticrun: invalid derived tool cache type, mode or size: %s", name)
		}
		f, err := r.Open(name)
		if err != nil {
			return observations, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(dependencyReader{ctx, f}, maxToolCacheJSONBytes+1))
		closeErr := f.Close()
		if readErr != nil {
			return observations, readErr
		}
		if closeErr != nil {
			return observations, closeErr
		}
		after := DependencyFile{Path: name, Size: int64(len(raw)), SHA256: sha(raw)}
		observations = append(observations, ToolCacheObservation{Phase: phase, Before: b.stagedFiles[name], After: after})
		if len(raw) > maxToolCacheJSONBytes || int64(len(raw)) != info.Size() || !validToolCacheJSON(name, raw) {
			validationErr = fmt.Errorf("semanticrun: malformed or oversized derived tool cache JSON: %s", name)
		}
		candidate[name] = after
	}
	if validationErr != nil {
		return observations, validationErr
	}
	var total int64
	for _, entry := range candidate {
		if entry.Size > b.maxBytes-total {
			return observations, fmt.Errorf("semanticrun: derived tool cache exceeds dependency byte budget")
		}
		total += entry.Size
	}
	if err := b.verifyFiles(ctx, b.owned, candidate); err != nil {
		return observations, err
	}
	b.stagedFiles = candidate
	return observations, nil
}
