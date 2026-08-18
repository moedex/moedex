package corpus

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"moedex/internal/corpus/catalog"
)

// DefaultHost is the ONLY GitLab host moedex-corpus talks to. The whole tool is
// pinned to TurnCommerce's internal GitLab by design — it never authenticates to
// or clones from anywhere else (no gitlab.com, no other instance). It re-exports
// internal/corpus/catalog.DefaultHost so the two never drift apart.
const DefaultHost = catalog.DefaultHost

// DefaultCorpusDirName is the corpus root under the user's home directory when
// neither an explicit path nor MOEDEX_CORPUS is given. It matches the engine's
// default corpus location.
const DefaultCorpusDirName = "TCGitlab"

// DefaultConcurrency caps parallel git operations (clone/pull). Moe has eight
// tentacles, but we keep some headroom for the rest of the machine.
func DefaultConcurrency() int {
	if n := runtime.NumCPU(); n < 8 {
		return n
	}
	return 8
}

// Config is the resolved settings for a corpus operation.
type Config struct {
	Host        string   // pinned to DefaultHost
	Root        string   // corpus root (absolute)
	Groups      []string // top-level namespace allowlist (sorted, de-duplicated); empty means "no filter"
	Concurrency int      // parallel git operations
}

// ResolveRoot picks the corpus root: the explicit value if non-empty, else
// $MOEDEX_CORPUS, else ~/<DefaultCorpusDirName>. The result is absolute.
func ResolveRoot(explicit string) (string, error) {
	root := explicit
	if root == "" {
		root = os.Getenv("MOEDEX_CORPUS")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, DefaultCorpusDirName)
	}
	return filepath.Abs(root)
}

// ParseGroups parses a group allowlist: one top-level GitLab namespace per line.
// Blank lines and lines beginning with '#' (after trimming) are ignored, and
// surrounding whitespace is stripped. The result is de-duplicated and sorted, so
// it is a stable set regardless of file ordering.
func ParseGroups(r io.Reader) []string {
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// LoadGroups reads and parses an allowlist file from path.
func LoadGroups(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseGroups(f), nil
}

// GroupsFromDisk derives an allowlist from an existing mirror: every immediate
// subdirectory of root (excluding dotfiles like .git/.DS_Store) is a top-level
// group. This reproduces today's curation from a populated ~/TCGitlab so the
// default list can be regenerated and committed. The result is sorted.
func GroupsFromDisk(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}
