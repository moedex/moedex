package semanticrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type DependencyPackSummary struct {
	Path           string `json:"path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	ByteLimit      int64  `json:"byte_limit"`
}

// PackDependencyBundle freezes a caller-provisioned extracted NuGet tree. It
// performs no download, restore or execution. The digest covers exact manifest
// bytes, including the final newline; successful output remains caller-owned.
func PackDependencyBundle(ctx context.Context, packages, output string) (result DependencyPackSummary, err error) {
	return PackDependencyBundleWithLimit(ctx, packages, output, 0)
}

// PackDependencyBundleWithLimit allows an explicit budget up to 4 GiB. Zero
// preserves the original 1 GiB default; the manifest still covers every file.
func PackDependencyBundleWithLimit(ctx context.Context, packages, output string, maxBytes int64) (result DependencyPackSummary, err error) {
	maxBytes, err = dependencyByteLimit(maxBytes)
	if err != nil {
		return result, err
	}
	result.ByteLimit = maxBytes
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if packages == "" || output == "" {
		return result, fmt.Errorf("semantic dependencies pack: packages and new output required")
	}
	source, err := filepath.EvalSymlinks(packages)
	if err != nil {
		return result, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return result, err
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return result, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return result, err
	}
	output = filepath.Join(parent, filepath.Base(output))
	rel, err := filepath.Rel(source, output)
	if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return result, fmt.Errorf("semantic dependencies pack: output must be outside packages")
	}
	if _, e := os.Lstat(output); !os.IsNotExist(e) {
		return result, fmt.Errorf("semantic dependencies pack: output must be new")
	}
	r, err := os.OpenRoot(source)
	if err != nil {
		return result, err
	}
	defer r.Close()
	manifest := DependencyManifest{Version: 1, Files: []DependencyFile{}}
	entries := 0
	err = fs.WalkDir(r.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		entries++
		if entries > MaxDependencyFiles*4 {
			return fmt.Errorf("semantic dependencies pack: directory entry limit exceeded")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || !cleanRelative(name) {
			return fmt.Errorf("semantic dependencies pack: unsupported file %q", name)
		}
		if len(manifest.Files) >= MaxDependencyFiles {
			return fmt.Errorf("semantic dependencies pack: file limit exceeded")
		}
		f, e := r.Open(name)
		if e != nil {
			return e
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxBytes-result.Bytes {
			return fmt.Errorf("semantic dependencies pack: file type or byte limit exceeded")
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(dependencyReader{ctx, f}, info.Size()+1))
		if e != nil {
			return e
		}
		if n != info.Size() {
			return fmt.Errorf("semantic dependencies pack: input changed")
		}
		manifest.Files = append(manifest.Files, DependencyFile{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Executable: info.Mode().Perm()&0111 != 0})
		result.Bytes += n
		return nil
	})
	if err != nil {
		return result, err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	raw, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	raw = append(raw, '\n')
	if len(raw) > 16<<20 {
		return result, fmt.Errorf("semantic dependencies pack: manifest exceeds 16 MiB")
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(output)
		}
	}()
	_, err = StageDependencyBundle(ctx, DependencyBundleOptions{Directory: source, Files: manifest.Files, MaxBytes: maxBytes}, filepath.Join(output, "packages"))
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(output, "manifest.json"), raw, 0600); err != nil {
		return result, err
	}
	digest := sha256.Sum256(raw)
	result.Path = output
	result.ManifestSHA256 = hex.EncodeToString(digest[:])
	result.Files = len(manifest.Files)
	return result, nil
}
