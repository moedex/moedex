package semanticrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const MaxDependencyFiles = 100000
const MaxDependencyBytes int64 = 1 << 30 // default retained for existing callers
const MaxDependencyBudgetBytes int64 = 4 << 30

func dependencyByteLimit(n int64) (int64, error) {
	if n == 0 {
		return MaxDependencyBytes, nil
	}
	if n < 1 || n > MaxDependencyBudgetBytes {
		return 0, fmt.Errorf("semanticrun: dependency byte budget must be 1..4294967296, or zero for 1 GiB default")
	}
	return n, nil
}

type DependencyManifest struct {
	Version int              `json:"version"`
	Files   []DependencyFile `json:"files"`
}

// ReadDependencyManifest accepts one versioned JSON value bounded to 16 MiB.
// StageDependencyBundle validates entry paths, digests, sizes and membership.
func ReadDependencyManifest(path string) (DependencyManifest, error) {
	m, _, err := readCaptureDependencyManifest(path)
	return m, err
}

func readCaptureDependencyManifest(path string) (DependencyManifest, []byte, error) {
	var m DependencyManifest
	info, err := os.Lstat(path)
	if err != nil {
		return m, nil, err
	}
	if !info.Mode().IsRegular() {
		return m, nil, fmt.Errorf("semanticrun: dependency manifest must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return m, nil, err
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil {
		return m, nil, err
	}
	if !s.Mode().IsRegular() || s.Size() > 16<<20 {
		return m, nil, fmt.Errorf("semanticrun: dependency manifest exceeds limit or is not regular")
	}
	b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return m, nil, err
	}
	if len(b) > 16<<20 {
		return m, nil, fmt.Errorf("semanticrun: dependency manifest exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, nil, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return m, nil, fmt.Errorf("semanticrun: trailing dependency manifest data")
	}
	if m.Version != 1 || len(m.Files) > MaxDependencyFiles {
		return m, nil, fmt.Errorf("semanticrun: unsupported dependency manifest version or file count")
	}
	return m, b, nil
}

type DependencyFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Executable records any source execute bit; staged permissions normalize to
	// owner-only 0700 or 0600 and never preserve special permission bits.
	Executable bool `json:"executable,omitempty"`
}

type DependencyBundleOptions struct {
	Directory string
	Files     []DependencyFile
	MaxFiles  int
	MaxBytes  int64
}

// DependencyBundle owns a private copy of a caller-supplied extracted NuGet
// global-packages tree. It never downloads or evaluates package contents.
type DependencyBundle struct {
	Root             string
	canonical, owned string
	files            map[string]DependencyFile
	stagedFiles      map[string]DependencyFile
	maxBytes         int64
}

// StageDependencyBundle creates destination exclusively. Its parent must exist.
// The complete file manifest excludes no files: unlisted files and symlinks are
// errors. Empty directories are allowed. Failure removes only the newly owned
// destination; Close never removes the caller's bundle.
func StageDependencyBundle(ctx context.Context, o DependencyBundleOptions, destination string) (_ *DependencyBundle, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = MaxDependencyFiles
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = MaxDependencyBytes
	}
	if o.Directory == "" || destination == "" || o.MaxFiles < 1 || o.MaxFiles > MaxDependencyFiles || o.MaxBytes < 1 || o.MaxBytes > MaxDependencyBudgetBytes || len(o.Files) > o.MaxFiles {
		return nil, fmt.Errorf("semanticrun: invalid dependency bundle or limits")
	}
	canonical, err := filepath.EvalSymlinks(o.Directory)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return nil, err
	}
	destination, err = filepath.Abs(filepath.Join(parent, filepath.Base(destination)))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(canonical, destination)
	if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, fmt.Errorf("semanticrun: dependency destination must be outside bundle")
	}
	b := &DependencyBundle{Root: destination, canonical: canonical, files: make(map[string]DependencyFile, len(o.Files)), maxBytes: o.MaxBytes}
	var total int64
	for _, f := range o.Files {
		digest, e := hex.DecodeString(f.SHA256)
		if !cleanRelative(f.Path) || f.Size < 0 || len(digest) != sha256.Size || e != nil || f.SHA256 != strings.ToLower(f.SHA256) || f.Size > o.MaxBytes-total {
			return nil, fmt.Errorf("semanticrun: invalid dependency manifest entry %q", f.Path)
		}
		if _, ok := b.files[f.Path]; ok {
			return nil, fmt.Errorf("semanticrun: duplicate dependency path %q", f.Path)
		}
		b.files[f.Path] = f
		total += f.Size
	}
	if err = b.verifyDirectory(ctx, canonical); err != nil {
		return nil, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return nil, err
	}
	b.owned = destination
	defer func() {
		if err != nil {
			_ = b.Close()
		}
	}()
	source, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	for _, f := range b.files {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		target := filepath.Join(destination, filepath.FromSlash(f.Path))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		if err = copyDependency(ctx, source, f, target); err != nil {
			return nil, err
		}
	}
	b.stagedFiles = cloneDependencies(b.files)
	if err = b.Verify(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

type dependencyReader struct {
	ctx context.Context
	r   io.Reader
}

func (r dependencyReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}

func copyDependency(ctx context.Context, source *os.Root, entry DependencyFile, target string) error {
	in, err := source.Open(entry.Path)
	if err != nil {
		return err
	}
	defer in.Close()
	stat, err := in.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() != entry.Size || (stat.Mode().Perm()&0111 != 0) != entry.Executable {
		return fmt.Errorf("semanticrun: dependency changed: %s", entry.Path)
	}
	mode := os.FileMode(0600)
	if entry.Executable {
		mode = 0700
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := out.Chmod(mode); err != nil {
		out.Close()
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(dependencyReader{ctx, in}, entry.Size+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("semanticrun: dependency changed: %s", entry.Path)
	}
	return nil
}

func (b *DependencyBundle) verifyDirectory(ctx context.Context, directory string) error {
	return b.verifyFiles(ctx, directory, b.files)
}

func (b *DependencyBundle) verifyFiles(ctx context.Context, directory string, files map[string]DependencyFile) error {
	r, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer r.Close()
	seen := 0
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
			return fmt.Errorf("semanticrun: dependency directory entry limit exceeded")
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("semanticrun: nonregular dependency %q", name)
		}
		entry, ok := files[name]
		if !ok {
			return fmt.Errorf("semanticrun: unlisted dependency %q", name)
		}
		f, e := r.Open(name)
		if e != nil {
			return e
		}
		defer f.Close()
		stat, e := f.Stat()
		if e != nil {
			return e
		}
		if !stat.Mode().IsRegular() || stat.Size() != entry.Size || (stat.Mode().Perm()&0111 != 0) != entry.Executable {
			return fmt.Errorf("semanticrun: dependency size/type/executable mode changed: %s", name)
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(dependencyReader{ctx, f}, entry.Size+1))
		if e != nil {
			return e
		}
		if n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("semanticrun: dependency hash changed: %s", name)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(files) {
		return fmt.Errorf("semanticrun: missing dependency files")
	}
	return nil
}

// Verify rechecks both the canonical bundle and staged package tree. This is
// recorded-input integrity, not a sandbox against hostile concurrent writers.
func (b *DependencyBundle) Verify(ctx context.Context) error {
	if b == nil || b.owned == "" {
		return fmt.Errorf("semanticrun: dependency bundle closed")
	}
	if err := b.verifyDirectory(ctx, b.canonical); err != nil {
		return err
	}
	return b.verifyFiles(ctx, b.owned, b.stagedFiles)
}

func (b *DependencyBundle) Close() error {
	if b == nil || b.owned == "" {
		return nil
	}
	err := os.RemoveAll(b.owned)
	if err == nil {
		b.owned = ""
	}
	return err
}
