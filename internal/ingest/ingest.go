// Package ingest reads a git repo's tracked files for indexing.
//
// We walk `git ls-files` (so .gitignore and the .git directory are handled by
// git, matching Zoekt/Waldo), take the blob SHA git already computed as content
// identity, and skip binary blobs so the indexed universe is text-only — the
// same universe ripgrep searches in the parity harness.
package ingest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// File is one tracked, text file ready for indexing.
type File struct {
	Repo    string
	RelPath string
	AbsPath string
	SHA     string
	Content []byte
}

// Head returns the git commit the repo at dir currently points at
// (`git -C dir rev-parse HEAD`), trimmed of trailing whitespace. It is the
// freshness key: re-indexing can be skipped for a repo whose HEAD is unchanged
// since it was last indexed. An empty repo (no commits) yields an error from
// git, which the caller may treat as "no recorded head".
func Head(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Repo returns the text files tracked in the git repo at dir, labeled under
// repoName. Binary files (those containing a NUL byte, as ripgrep detects them)
// are skipped.
func Repo(repoName, dir string) ([]File, error) {
	// -s gives "<mode> <sha> <stage>\t<path>"; -z makes entries NUL-separated.
	out, err := exec.Command("git", "-C", dir, "ls-files", "-s", "-z").Output()
	if err != nil {
		return nil, err
	}

	var files []File
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		tab := bytes.IndexByte(entry, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(string(entry[:tab]))
		if len(fields) < 3 {
			continue
		}
		sha := fields[1]
		rel := string(entry[tab+1:])
		abs := filepath.Join(dir, rel)

		content, err := os.ReadFile(abs)
		if err != nil {
			continue // tracked but absent from the working tree
		}
		if bytes.IndexByte(content, 0) >= 0 {
			continue // binary
		}
		// Strip a leading UTF-8 BOM, as ripgrep does — it's an encoding artifact,
		// not content, and indexing it would desync line/match boundaries.
		content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
		files = append(files, File{Repo: repoName, RelPath: rel, AbsPath: abs, SHA: sha, Content: content})
	}
	return files, nil
}
