package navigate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	configpkg "moedex/internal/config"
	"moedex/internal/corpus/catalog"
)

const (
	workspaceCacheSchema = 1
	workspaceManifest    = "manifest.json"
	workspaceTreeDir     = "workspace"
)

// workspacePathMap translates between the immutable managed-corpus checkout
// exposed by Moe's public APIs and the disposable checkout an external
// language server is allowed to mutate. A zero value is the identity mapping
// used for ordinary, unmanaged working trees.
type workspacePathMap struct {
	canonicalRepo string
	scratchRepo   string
}

func (m workspacePathMap) enabled() bool {
	return m.canonicalRepo != "" && m.scratchRepo != ""
}

func (m workspacePathMap) toScratch(path string) (string, error) {
	if !m.enabled() || path == "" {
		return path, nil
	}
	mapped, ok, err := translateContainedPath(path, m.canonicalRepo, m.scratchRepo)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("navigate: path %q is outside managed repository %q", path, m.canonicalRepo)
	}
	return mapped, nil
}

func (m workspacePathMap) toCanonical(path string) string {
	if !m.enabled() || path == "" {
		return path
	}
	mapped, ok, err := translateContainedPath(path, m.scratchRepo, m.canonicalRepo)
	if err != nil || !ok {
		// Language servers may legitimately return SDK, package-cache, metadata,
		// or virtual-document paths outside the workspace. Only rewrite paths we
		// can prove came from this scratch tree.
		return path
	}
	return mapped
}

func (m workspacePathMap) toScratchPos(pos Pos) (Pos, error) {
	file, err := m.toScratch(pos.File)
	if err != nil {
		return Pos{}, err
	}
	pos.File = file
	return pos, nil
}

func (m workspacePathMap) toCanonicalLocation(location Location) Location {
	location.File = m.toCanonical(location.File)
	location.Start.File = m.toCanonical(location.Start.File)
	location.End.File = m.toCanonical(location.End.File)
	return location
}

func (m workspacePathMap) toCanonicalLocations(locations []Location) {
	for i := range locations {
		locations[i] = m.toCanonicalLocation(locations[i])
	}
}

func (m workspacePathMap) toCanonicalSymbols(symbols []Symbol) {
	for i := range symbols {
		symbols[i].Loc = m.toCanonicalLocation(symbols[i].Loc)
	}
}

func translateContainedPath(path, from, to string) (string, bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("navigate: resolve path %q: %w", path, err)
	}
	absFrom, err := filepath.Abs(from)
	if err != nil {
		return "", false, fmt.Errorf("navigate: resolve root %q: %w", from, err)
	}
	rel, err := filepath.Rel(absFrom, absPath)
	if err != nil {
		return "", false, fmt.Errorf("navigate: relate %q to %q: %w", absPath, absFrom, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, nil
	}
	return filepath.Join(to, rel), true, nil
}

type workspaceCacheManifest struct {
	Schema        int    `json:"schema"`
	ProjectID     int64  `json:"project_id"`
	Commit        string `json:"commit"`
	CanonicalRepo string `json:"canonical_repo"`
}

type managedWorkspaceProject struct {
	root    string
	repo    string
	id      int64
	commit  string
	relRoot string
}

type workspaceBuild struct {
	ready chan struct{}
	path  string
	err   error
}

// workspaceManager lazily materializes one exact, writable checkout per locked
// managed project commit. The on-disk target is shared safely across processes
// by atomic rename; builds is the cheaper in-process singleflight that avoids
// duplicate git archive work for concurrent languages in one repository.
type workspaceManager struct {
	cacheDir string

	mu     sync.Mutex
	builds map[string]*workspaceBuild
}

func newWorkspaceManager(cacheDir string) *workspaceManager {
	return &workspaceManager{
		cacheDir: resolveWorkspaceCacheDir(cacheDir),
		builds:   make(map[string]*workspaceBuild),
	}
}

func resolveWorkspaceCacheDir(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Clean(explicit)
	}
	if configured := strings.TrimSpace(os.Getenv("MOEDEX_LSP_WORKSPACE_DIR")); configured != "" {
		return filepath.Clean(configured)
	}
	if indexDir := strings.TrimSpace(os.Getenv("MOEDEX_INDEX_DIR")); indexDir != "" {
		return filepath.Join(indexDir, "lsp-workspaces")
	}
	if dir := configpkg.DefaultLSPWorkspaceDir(); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), "moedex-lsp-workspaces")
}

// isolate returns the server root and path map for root. Unmanaged roots are
// returned unchanged. Managed roots are served from an exact git-archive
// projection so untracked or modified files in the acquisition checkout can
// neither enter the LSP view nor be created there by the language server.
func (m *workspaceManager) isolate(ctx context.Context, root string) (string, workspacePathMap, error) {
	project, managed, err := findManagedWorkspaceProject(root)
	if err != nil {
		return "", workspacePathMap{}, err
	}
	if !managed {
		return root, workspacePathMap{}, nil
	}

	scratchRepo, err := m.materialize(ctx, project)
	if err != nil {
		return "", workspacePathMap{}, err
	}
	mapping := workspacePathMap{canonicalRepo: project.repo, scratchRepo: scratchRepo}
	scratchRoot := filepath.Join(scratchRepo, project.relRoot)
	info, err := os.Stat(scratchRoot)
	if err != nil {
		return "", workspacePathMap{}, fmt.Errorf("navigate: inspect isolated workspace root %q: %w", scratchRoot, err)
	}
	if !info.IsDir() {
		return "", workspacePathMap{}, fmt.Errorf("navigate: isolated workspace root %q is not a directory", scratchRoot)
	}
	return scratchRoot, mapping, nil
}

func findManagedWorkspaceProject(root string) (managedWorkspaceProject, bool, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return managedWorkspaceProject{}, false, fmt.Errorf("navigate: resolve workspace root: %w", err)
	}
	search := absRoot
	for {
		marker := catalog.CatalogPath(search)
		_, statErr := os.Lstat(marker)
		switch {
		case statErr == nil:
			return projectFromManagedRoot(search, absRoot)
		case !errors.Is(statErr, os.ErrNotExist):
			return managedWorkspaceProject{}, false, fmt.Errorf("navigate: inspect managed corpus marker %q: %w", marker, statErr)
		}
		parent := filepath.Dir(search)
		if parent == search {
			return managedWorkspaceProject{}, false, nil
		}
		search = parent
	}
}

func projectFromManagedRoot(managedRoot, workspaceRoot string) (managedWorkspaceProject, bool, error) {
	cat, err := catalog.LoadCatalog(managedRoot)
	if err != nil {
		return managedWorkspaceProject{}, true, err
	}
	lock, err := catalog.LoadLock(managedRoot, cat.Host)
	if err != nil {
		return managedWorkspaceProject{}, true, err
	}
	for _, project := range lock.Projects {
		repo := filepath.Join(managedRoot, filepath.FromSlash(project.PathWithNamespace))
		rel, err := filepath.Rel(repo, workspaceRoot)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return managedWorkspaceProject{
			root:    managedRoot,
			repo:    repo,
			id:      project.ID,
			commit:  strings.ToLower(project.DefaultCommit),
			relRoot: rel,
		}, true, nil
	}
	return managedWorkspaceProject{}, true, fmt.Errorf("navigate: workspace root %q is inside managed corpus %q but outside every locked project", workspaceRoot, managedRoot)
}

func (m *workspaceManager) materialize(ctx context.Context, project managedWorkspaceProject) (string, error) {
	key := project.repo + "\x00" + fmt.Sprintf("%d/%s", project.id, project.commit)
	m.mu.Lock()
	if build := m.builds[key]; build != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-build.ready:
			return build.path, build.err
		}
	}
	build := &workspaceBuild{ready: make(chan struct{})}
	m.builds[key] = build
	m.mu.Unlock()

	build.path, build.err = m.materializeOnce(ctx, project)
	close(build.ready)
	if build.err != nil {
		// A request cancellation, transient disk failure, or interrupted git
		// archive must not poison this project for the lifetime of a warm daemon.
		m.mu.Lock()
		if m.builds[key] == build {
			delete(m.builds, key)
		}
		m.mu.Unlock()
	}
	return build.path, build.err
}

func (m *workspaceManager) materializeOnce(ctx context.Context, project managedWorkspaceProject) (string, error) {
	rootSum := sha256.Sum256([]byte(project.root))
	rootKey := fmt.Sprintf("corpus-%x", rootSum[:8])
	target := filepath.Join(m.cacheDir, fmt.Sprintf("v%d", workspaceCacheSchema), rootKey, fmt.Sprintf("project-%d", project.id), project.commit)
	manifest := workspaceCacheManifest{
		Schema:        workspaceCacheSchema,
		ProjectID:     project.id,
		Commit:        project.commit,
		CanonicalRepo: project.repo,
	}
	if ok, err := validWorkspaceCache(target, manifest); err != nil {
		return "", err
	} else if ok {
		return filepath.Join(target, workspaceTreeDir), nil
	}

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("navigate: create LSP workspace cache parent: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, ".materialize-")
	if err != nil {
		return "", fmt.Errorf("navigate: create temporary LSP workspace: %w", err)
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = os.RemoveAll(tmp)
		}
	}()
	tree := filepath.Join(tmp, workspaceTreeDir)
	if err := os.Mkdir(tree, 0o755); err != nil {
		return "", fmt.Errorf("navigate: create temporary LSP workspace tree: %w", err)
	}
	if err := extractGitArchive(ctx, project.repo, project.commit, tree); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("navigate: encode LSP workspace manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(tmp, workspaceManifest), data, 0o600); err != nil {
		return "", fmt.Errorf("navigate: write LSP workspace manifest: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		// Another Moe process may have won the same exact project/commit race.
		// Accept only a fully validated winner; never replace or clean an
		// unfamiliar directory at the deterministic cache target.
		if ok, checkErr := validWorkspaceCache(target, manifest); checkErr != nil {
			return "", checkErr
		} else if !ok {
			return "", fmt.Errorf("navigate: publish isolated LSP workspace %q: %w", target, err)
		}
		return filepath.Join(target, workspaceTreeDir), nil
	}
	keepTmp = true
	return filepath.Join(target, workspaceTreeDir), nil
}

func validWorkspaceCache(target string, expected workspaceCacheManifest) (bool, error) {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("navigate: inspect LSP workspace cache %q: %w", target, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("navigate: refusing unfamiliar non-directory LSP workspace cache target %q", target)
	}
	data, err := os.ReadFile(filepath.Join(target, workspaceManifest))
	if err != nil {
		return false, fmt.Errorf("navigate: refusing unverified LSP workspace cache %q: %w", target, err)
	}
	var actual workspaceCacheManifest
	if err := json.Unmarshal(data, &actual); err != nil {
		return false, fmt.Errorf("navigate: refusing malformed LSP workspace cache manifest at %q: %w", target, err)
	}
	if actual != expected {
		return false, fmt.Errorf("navigate: refusing mismatched LSP workspace cache manifest at %q", target)
	}
	tree, err := os.Stat(filepath.Join(target, workspaceTreeDir))
	if err != nil || !tree.IsDir() {
		return false, fmt.Errorf("navigate: refusing incomplete LSP workspace cache at %q", target)
	}
	return true, nil
}

func extractGitArchive(ctx context.Context, repo, commit, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", commit)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("navigate: open git archive stream for %q: %w", repo, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("navigate: start git archive for %q: %w", repo, err)
	}
	extractErr := extractTar(stdout, dest)
	if extractErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if extractErr != nil {
		return fmt.Errorf("navigate: extract locked project %q at %s: %w", repo, shortWorkspaceCommit(commit), extractErr)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("navigate: git archive %q at %s: %w: %s", repo, shortWorkspaceCommit(commit), waitErr, detail)
		}
		return fmt.Errorf("navigate: git archive %q at %s: %w", repo, shortWorkspaceCommit(commit), waitErr)
	}
	return nil
}

func extractTar(reader io.Reader, dest string) error {
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		target := filepath.Join(dest, name)
		if _, ok, err := translateContainedPath(target, dest, dest); err != nil || !ok {
			return fmt.Errorf("unsafe archive target %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tarReader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if !header.ModTime.IsZero() {
				_ = os.Chtimes(target, header.ModTime, header.ModTime)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			resolvedTarget := header.Linkname
			if !filepath.IsAbs(resolvedTarget) {
				resolvedTarget = filepath.Join(filepath.Dir(target), filepath.FromSlash(resolvedTarget))
			}
			if _, ok, err := translateContainedPath(resolvedTarget, dest, dest); err != nil || !ok {
				return fmt.Errorf("archive symlink %q escapes the isolated workspace", header.Name)
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			// Metadata-only PAX records are consumed by archive/tar.
		default:
			return fmt.Errorf("unsupported archive entry type %d for %q", header.Typeflag, header.Name)
		}
	}
}

func shortWorkspaceCommit(commit string) string {
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}
