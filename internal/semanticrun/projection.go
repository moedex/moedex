package semanticrun

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"moedex/internal/corpus/catalog"
	"moedex/internal/ingest"
)

type ProjectionOptions struct {
	ManagedRoot, Repo, TempParent string
	ProjectID                     int64
	MaxFiles                      int
	MaxBytes                      int64
}
type Projection struct {
	Root, Repo, CanonicalRepo, Commit, PrivacyFingerprint string
	Origin                                                string
	ProjectID                                             int64
	directory, managedRoot, lockDigest                    string
	inventory                                             map[string]projectedFile
}
type projectedFile struct {
	hash string
	size int64
}
type treeEntry struct {
	name, oid string
	mode      os.FileMode
}

func sha(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func cleanRelative(s string) bool {
	return s != "" && s != "." && path.Clean(s) == s && !strings.HasPrefix(s, "/") && s != ".." && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\:\x00")
}
func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func lockIdentity(root string) (string, error) {
	cat, e := catalog.LoadCatalog(root)
	if e != nil {
		return "", e
	}
	lock, e := catalog.LoadLock(root, cat.Host)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal([]any{cat, lock})
	if e != nil {
		return "", e
	}
	return sha(b), nil
}

// CreateProjection materializes exact locked Git blob bytes in a fresh private
// directory. It performs no checkout filters, archive substitution, project
// evaluation or restore. Symlinks and submodules are explicitly unsupported.
func CreateProjection(ctx context.Context, o ProjectionOptions) (p *Projection, err error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if o.ManagedRoot == "" || o.TempParent == "" || o.ProjectID < 0 || (o.ProjectID == 0 && !cleanRelative(o.Repo)) {
		return nil, fmt.Errorf("semanticrun: managed root, project and temporary parent required")
	}
	if o.MaxFiles == 0 {
		o.MaxFiles = 100000
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 256 << 20
	}
	if o.MaxFiles < 1 || o.MaxFiles > 100000 || o.MaxBytes < 1 || o.MaxBytes > 1<<30 {
		return nil, fmt.Errorf("semanticrun: invalid projection limits")
	}
	cat, e := catalog.LoadCatalog(o.ManagedRoot)
	if e != nil {
		return nil, e
	}
	lock, e := catalog.LoadLock(o.ManagedRoot, cat.Host)
	if e != nil {
		return nil, e
	}
	lockBytes, e := json.Marshal([]any{cat, lock})
	if e != nil {
		return nil, e
	}
	p = &Projection{managedRoot: o.ManagedRoot, lockDigest: sha(lockBytes), inventory: map[string]projectedFile{}}
	for _, project := range lock.Projects {
		if (o.ProjectID == 0 || project.ID == o.ProjectID) && (o.Repo == "" || project.PathWithNamespace == o.Repo) {
			if p.Repo != "" {
				return nil, fmt.Errorf("semanticrun: ambiguous managed project")
			}
			p.Repo = project.PathWithNamespace
			p.ProjectID = project.ID
			p.Commit = strings.ToLower(project.DefaultCommit)
		}
	}
	if !cleanRelative(p.Repo) || p.ProjectID <= 0 || !objectID(p.Commit) {
		return nil, fmt.Errorf("semanticrun: project has no valid locked identity")
	}
	if groups := cat.GroupPolicy.TopLevelGroups; len(groups) > 0 {
		allowed := false
		top := strings.SplitN(p.Repo, "/", 2)[0]
		for _, group := range groups {
			if group == top {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("semanticrun: project excluded by managed group policy")
		}
	}
	managed, e := filepath.EvalSymlinks(o.ManagedRoot)
	if e != nil {
		return nil, e
	}
	managed, e = filepath.Abs(managed)
	if e != nil {
		return nil, e
	}
	parent, e := filepath.EvalSymlinks(o.TempParent)
	if e != nil {
		return nil, e
	}
	parent, e = filepath.Abs(parent)
	if e != nil {
		return nil, e
	}
	rel, e := filepath.Rel(managed, parent)
	if e != nil {
		return nil, e
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("semanticrun: projection must be outside managed corpus")
	}
	p.CanonicalRepo = filepath.Join(managed, filepath.FromSlash(p.Repo))
	canonical, e := filepath.EvalSymlinks(p.CanonicalRepo)
	if e != nil {
		return nil, e
	}
	if canonical != p.CanonicalRepo {
		return nil, fmt.Errorf("semanticrun: managed repository contains symlink")
	}
	allowed, e := ingest.LSPWorkspaceAllowed(p.CanonicalRepo)
	if e != nil {
		return nil, e
	}
	if !allowed {
		return nil, fmt.Errorf("semanticrun: restricted workspace inputs")
	}
	p.PrivacyFingerprint, e = ingest.AIPrivacyFingerprint(p.CanonicalRepo)
	if e != nil {
		return nil, e
	}
	p.directory, e = os.MkdirTemp(o.TempParent, "semantic-projection-")
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	p.Root = filepath.Join(p.directory, "source")
	if e = os.Mkdir(p.Root, 0700); e != nil {
		return p, e
	}
	if e = p.materialize(ctx, o.MaxFiles, o.MaxBytes); e != nil {
		return p, e
	}
	allowed, e = ingest.LSPWorkspaceAllowed(p.Root)
	if e != nil {
		return p, e
	}
	policy, e2 := ingest.AIPrivacyFingerprint(p.Root)
	if e2 != nil {
		return p, e2
	}
	if !allowed || policy != p.PrivacyFingerprint {
		return p, fmt.Errorf("semanticrun: projected privacy policy differs or restricts workspace")
	}
	if e = p.Verify(ctx); e != nil {
		return p, e
	}
	return p, nil
}

func (p *Projection) extract(ctx context.Context, git string, env []string, requests string, entries []treeEntry, limit int64) error {
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); ok {
		ctx, cancel = context.WithCancel(ctx)
	} else {
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "--no-replace-objects", "-C", p.CanonicalRepo, "cat-file", "--batch")
	cmd.Dir = p.Root
	cmd.Env = env
	cmd.Stdin = strings.NewReader(requests)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	kill := func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.Cancel = kill
	stderr := &boundedCapture{limit: 64 << 10, cancel: cancel}
	cmd.Stderr = stderr
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	read := func() error {
		r := bufio.NewReaderSize(stdout, 4096)
		var total int64
		for _, entry := range entries {
			if e := ctx.Err(); e != nil {
				return e
			}
			header, e := r.ReadSlice('\n')
			if e != nil {
				return e
			}
			fields := strings.Fields(string(header))
			if len(fields) != 3 || fields[0] != entry.oid || fields[1] != "blob" {
				return fmt.Errorf("semanticrun: inconsistent git object")
			}
			size, e := strconv.ParseInt(fields[2], 10, 64)
			if e != nil || size < 0 || size > limit-total {
				return fmt.Errorf("semanticrun: projection byte limit exceeded")
			}
			total += size
			target := filepath.Join(p.Root, filepath.FromSlash(entry.name))
			if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
				return e
			}
			f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, entry.mode)
			if e != nil {
				return e
			}
			h := sha256.New()
			_, copyErr := io.CopyN(io.MultiWriter(f, h), r, size)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			separator, e := r.ReadByte()
			if e != nil || separator != '\n' {
				return fmt.Errorf("semanticrun: invalid git object delimiter")
			}
			p.inventory[entry.name] = projectedFile{hex.EncodeToString(h.Sum(nil)), size}
		}
		if _, e := r.ReadByte(); e != io.EOF {
			return fmt.Errorf("semanticrun: excess git object data")
		}
		return nil
	}
	readErr := read()
	if readErr != nil {
		_ = kill()
	}
	waitErr := cmd.Wait()
	_ = kill()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	return waitErr
}

// Verify rechecks immutable inputs and acquisition policy/lock, while permitting
// newly generated build outputs. It never labels those extra files committed.
func (p *Projection) Verify(ctx context.Context) error {
	if p == nil || p.Root == "" {
		return fmt.Errorf("semanticrun: closed projection")
	}
	if e := p.verifyInventory(ctx, p.Root); e != nil {
		return e
	}
	if p.Origin != "" {
		if e := p.verifyGitIdentity(ctx); e != nil {
			return e
		}
		if e := p.verifyInventory(ctx, p.CanonicalRepo); e != nil {
			return e
		}
	} else {
		lock, e := lockIdentity(p.managedRoot)
		if e != nil {
			return e
		}
		if lock != p.lockDigest {
			return fmt.Errorf("semanticrun: managed lock changed")
		}
	}
	policy, e := ingest.AIPrivacyFingerprint(p.CanonicalRepo)
	if e != nil {
		return e
	}
	projected, e := ingest.AIPrivacyFingerprint(p.Root)
	if e != nil {
		return e
	}
	if policy != p.PrivacyFingerprint || projected != p.PrivacyFingerprint {
		return fmt.Errorf("semanticrun: privacy policy changed")
	}
	return ctx.Err()
}
func (p *Projection) Close() error {
	if p == nil || p.directory == "" {
		return nil
	}
	e := os.RemoveAll(p.directory)
	p.directory = ""
	p.Root = ""
	return e
}

func (p *Projection) materialize(ctx context.Context, maxFiles int, maxBytes int64) error {
	git, e := exec.LookPath("git")
	if e != nil {
		return e
	}
	env := []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1"}
	typeResult, e := RunProcess(ctx, ProcessSpec{Executable: git, Args: []string{"--no-replace-objects", "-C", p.CanonicalRepo, "cat-file", "-t", p.Commit}, Dir: p.Root, Env: env, StdoutLimit: 128, StderrLimit: 64 << 10})
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(typeResult.Stdout)) != "commit" {
		return fmt.Errorf("semanticrun: locked object is not a commit")
	}
	listing, e := RunProcess(ctx, ProcessSpec{Executable: git, Args: []string{"--no-replace-objects", "-C", p.CanonicalRepo, "ls-tree", "-rz", "--full-tree", p.Commit}, Dir: p.Root, Env: env, StdoutLimit: 16 << 20, StderrLimit: 64 << 10})
	if e != nil {
		return e
	}
	var entries []treeEntry
	var requests strings.Builder
	for _, line := range bytes.Split(listing.Stdout, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return fmt.Errorf("semanticrun: invalid git tree record")
		}
		fields := strings.Fields(string(parts[0]))
		name := string(parts[1])
		if len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") || !objectID(fields[2]) || !cleanRelative(name) {
			return fmt.Errorf("semanticrun: unsupported or unsafe git tree entry")
		}
		if len(entries) >= maxFiles {
			return fmt.Errorf("semanticrun: projection file limit exceeded")
		}
		mode := os.FileMode(0600)
		if fields[0] == "100755" {
			mode = 0700
		}
		entries = append(entries, treeEntry{name, fields[2], mode})
		requests.WriteString(fields[2])
		requests.WriteByte('\n')
	}
	if e = p.extract(ctx, git, env, requests.String(), entries, maxBytes); e != nil {
		return e
	}

	return nil
}

func (p *Projection) verifyInventory(ctx context.Context, directory string) error {
	root, e := os.OpenRoot(directory)
	if e != nil {
		return e
	}
	defer root.Close()
	for name, want := range p.inventory {
		if e := ctx.Err(); e != nil {
			return e
		}
		info, e := root.Lstat(name)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() != want.size {
			return fmt.Errorf("semanticrun: projected input changed: %s", name)
		}
		f, e := root.Open(name)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, want.size+1))
		f.Close()
		if e != nil {
			return e
		}
		if n != want.size || hex.EncodeToString(h.Sum(nil)) != want.hash {
			return fmt.Errorf("semanticrun: projected input changed: %s", name)
		}
	}

	return nil
}
