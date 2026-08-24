// Package sourcehash computes a deterministic identity for the Git-visible
// worktree used by canonical Moedex builds.
package sourcehash

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Digest returns a SHA-256 manifest identity over every tracked or untracked,
// non-ignored path in the worktree. Each manifest entry binds the path, Git
// mode, and Git-compatible blob SHA; tracked deletions receive an explicit
// tombstone. This identifies dirty source without exposing its contents.
func Digest(ctx context.Context, dir string) (string, error) {
	root, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root = strings.TrimSpace(root)
	listed, err := gitOutputBytes(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	paths := splitNUL(listed)
	sort.Strings(paths)

	manifest := sha256.New()
	for _, path := range paths {
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				writeManifestEntry(manifest, path, "deleted", "-")
				continue
			}
			return "", fmt.Errorf("source identity %s: %w", path, err)
		}

		var mode, blob string
		switch {
		case info.Mode().IsRegular():
			mode = "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			blob, err = gitBlobFile(full, info.Size())
		case info.Mode()&os.ModeSymlink != 0:
			mode = "120000"
			var target string
			target, err = os.Readlink(full)
			if err == nil {
				blob = gitBlobBytes([]byte(target))
			}
		case info.IsDir():
			return "", fmt.Errorf("source identity %s: Git submodules are not supported", path)
		default:
			return "", fmt.Errorf("source identity %s: unsupported file mode %s", path, info.Mode())
		}
		if err != nil {
			return "", fmt.Errorf("source identity %s: %w", path, err)
		}
		writeManifestEntry(manifest, path, mode, blob)
	}
	return hex.EncodeToString(manifest.Sum(nil)), nil
}

// CheckClean rejects tracked, staged, submodule, and untracked non-ignored
// changes. Canonical installation uses this after tests and before replacing a
// running binary.
func CheckClean(ctx context.Context, dir string) error {
	status, err := gitOutputBytes(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return fmt.Errorf("worktree is dirty; commit tracked and untracked source changes before install")
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOutputBytes(ctx, dir, args...)
	return string(out), err
}

func gitOutputBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func splitNUL(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			out = append(out, string(part))
		}
	}
	return out
}

func writeManifestEntry(dst io.Writer, path, mode, blob string) {
	io.WriteString(dst, strconv.Itoa(len(path)))
	io.WriteString(dst, ":")
	io.WriteString(dst, path)
	io.WriteString(dst, "\x00")
	io.WriteString(dst, mode)
	io.WriteString(dst, "\x00")
	io.WriteString(dst, blob)
	io.WriteString(dst, "\n")
}

func gitBlobFile(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", size)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gitBlobBytes(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
