package semanticrun

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"moedex/internal/ingest"
)

// GitProjectionOptions describes an explicit public Git source without managed
// GitLab catalog identity. Origin is the expected credential-free HTTPS origin;
// Commit must be a full object ID, and the checkout HEAD must match it.
type GitProjectionOptions struct {
	Checkout, Repo, Commit, Origin, TempParent string
	MaxFiles                                   int
	MaxBytes                                   int64
}

// CreateGitProjection copies only committed regular-file inputs. Untracked
// files, including benign build outputs, are excluded. Unlike managed snapshots,
// this public adapter requires canonical committed-file bytes to equal the pin,
// so later lexical attachment cannot silently use a dirty working tree.
func CreateGitProjection(ctx context.Context, o GitProjectionOptions) (p *Projection, err error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	u, e := url.Parse(o.Origin)
	if e != nil || len(o.Origin) > 2048 || strings.ContainsAny(o.Origin, "\\ #") || strings.IndexFunc(o.Origin, unicode.IsControl) >= 0 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Path == "" || u.Path == "/" {
		return nil, fmt.Errorf("semanticrun: explicit credential-free HTTPS origin required")
	}
	if o.Checkout == "" || o.TempParent == "" || !cleanRelative(o.Repo) || !objectID(o.Commit) {
		return nil, fmt.Errorf("semanticrun: checkout, repo, full commit and temporary parent required")
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
	canonical, e := filepath.EvalSymlinks(o.Checkout)
	if e != nil {
		return nil, e
	}
	canonical, e = filepath.Abs(canonical)
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
	rel, e := filepath.Rel(canonical, parent)
	if e != nil {
		return nil, e
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("semanticrun: projection must be outside checkout")
	}
	p = &Projection{Repo: o.Repo, CanonicalRepo: canonical, Commit: strings.ToLower(o.Commit), Origin: o.Origin, inventory: map[string]projectedFile{}}
	if e = p.verifyGitIdentity(ctx); e != nil {
		return nil, e
	}
	allowed, e := ingest.LSPWorkspaceAllowed(canonical)
	if e != nil {
		return nil, e
	}
	if !allowed {
		return nil, fmt.Errorf("semanticrun: restricted workspace inputs")
	}
	p.PrivacyFingerprint, e = ingest.AIPrivacyFingerprint(canonical)
	if e != nil {
		return nil, e
	}
	p.directory, e = os.MkdirTemp(parent, "semantic-git-projection-")
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
	policy, e := ingest.AIPrivacyFingerprint(p.Root)
	if e != nil {
		return p, e
	}
	if !allowed || policy != p.PrivacyFingerprint {
		return p, fmt.Errorf("semanticrun: projected privacy policy differs or restricts workspace")
	}
	if e = p.Verify(ctx); e != nil {
		return p, e
	}
	return p, nil
}

func (p *Projection) verifyGitIdentity(ctx context.Context) error {
	git, e := exec.LookPath("git")
	if e != nil {
		return e
	}
	run := func(args ...string) (string, error) {
		r, e := RunProcess(ctx, ProcessSpec{Executable: git, Args: append([]string{"--no-replace-objects", "-C", p.CanonicalRepo}, args...), Dir: p.CanonicalRepo, Env: []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1"}, StdoutLimit: 16 << 10, StderrLimit: 64 << 10})
		return strings.TrimSpace(string(r.Stdout)), e
	}
	top, e := run("rev-parse", "--show-toplevel")
	if e != nil {
		return e
	}
	top, e = filepath.EvalSymlinks(top)
	if e != nil {
		return e
	}
	if top != p.CanonicalRepo {
		return fmt.Errorf("semanticrun: checkout must be the repository root")
	}
	head, e := run("rev-parse", "--verify", "HEAD^{commit}")
	if e != nil {
		return e
	}
	if head != p.Commit {
		return fmt.Errorf("semanticrun: checkout HEAD differs from pinned commit")
	}
	origin, e := run("config", "--local", "--get-all", "remote.origin.url")
	if e != nil {
		return e
	}
	if origin != p.Origin {
		return fmt.Errorf("semanticrun: checkout origin differs from expected URL")
	}
	return nil
}
