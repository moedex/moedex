// No build tag: this is the F-03 regression for the privacy preflight inside
// Pool.NavigatorFor, which must run — and must reject a Restricted workspace —
// in BOTH build arms, before NewLSP is ever attempted (NewLSP itself differs
// per arm: it always errors in the default build, and would actually spawn a
// process under -tags lsp). Asserting on Stats().SpawnAttempts staying at 0
// proves the rejection happens before any process would be launched, without
// needing a language server binary on PATH in either arm.

package navigate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"moedex/internal/ingest"
)

// TestNavigatorFor_RejectsRestrictedWorkspace is F-03: a workspace whose
// .ai-privacy.yml marks it Restricted (level 1) must never reach NewLSP.
// Before this fix, only internal/server/graphcalls_lsp.go remembered to call
// ingest.LSPWorkspaceAllowed itself; moedex-serve's LSP tools and moedex-nav's
// CLI passed a caller-supplied root straight to the Pool with no such check.
// Pinning the guarantee inside NavigatorFor covers every caller, present and
// future, structurally.
func TestNavigatorFor_RejectsRestrictedWorkspace(t *testing.T) {
	root := t.TempDir()
	writePrivacyPolicy(t, root, "global_privacy_level: 1\n")

	p := NewPool(Config{})
	t.Cleanup(func() { _ = p.Close() })

	nav, err := p.NavigatorFor(context.Background(), "go", root)
	if err == nil {
		t.Fatalf("NavigatorFor on a Restricted workspace = nil error, nav %+v; want a fail-closed error", nav)
	}
	if !errors.Is(err, ErrPrivacyRestricted) {
		t.Fatalf("NavigatorFor err = %v, want errors.Is(err, ErrPrivacyRestricted)", err)
	}
	if got := p.Stats().SpawnAttempts; got != 0 {
		t.Errorf("SpawnAttempts = %d, want 0 — a Restricted root must be rejected before NewLSP is ever attempted", got)
	}
}

// TestNavigatorFor_RejectsPathLevelRestriction proves a path-scoped override
// (not just the workspace-wide global_privacy_level) is honored: a Restricted
// override anywhere under the root rejects the whole workspace, since an
// external language server can scan beyond the queried file. Mirrors the
// override-based case in ingest.TestLSPWorkspaceAllowedRejectsAnyRestrictedScope.
func TestNavigatorFor_RejectsPathLevelRestriction(t *testing.T) {
	root := t.TempDir()
	writePrivacyPolicy(t, root, "global_privacy_level: 3\nprivacy_levels:\n  - path: /secrets/\n    privacy_level: 1\n")

	p := NewPool(Config{})
	t.Cleanup(func() { _ = p.Close() })

	if _, err := p.NavigatorFor(context.Background(), "go", root); !errors.Is(err, ErrPrivacyRestricted) {
		t.Fatalf("NavigatorFor err = %v, want errors.Is(err, ErrPrivacyRestricted)", err)
	}
}

// TestNavigatorFor_MalformedPolicyFailsClosed proves a policy that fails to
// parse rejects the workspace too (fail-closed), rather than silently
// treating an unreadable/invalid policy as permissive.
func TestNavigatorFor_MalformedPolicyFailsClosed(t *testing.T) {
	root := t.TempDir()
	writePrivacyPolicy(t, root, "unsupported: true\n")

	p := NewPool(Config{})
	t.Cleanup(func() { _ = p.Close() })

	_, err := p.NavigatorFor(context.Background(), "go", root)
	if err == nil {
		t.Fatal("NavigatorFor with a malformed .ai-privacy.yml = nil error; want fail-closed")
	}
	// errors.As (which IsPrivacyPolicyError uses) walks the whole %w chain, so
	// this holds even though pool.go wraps the raw ingest error with its own
	// "navigate: privacy preflight %s: %w" context.
	if !ingest.IsPrivacyPolicyError(err) {
		t.Fatalf("NavigatorFor err = %v, want an ingest.PrivacyPolicyError somewhere in the chain", err)
	}
	if got := p.Stats().SpawnAttempts; got != 0 {
		t.Errorf("SpawnAttempts = %d, want 0 — a malformed policy must be rejected before NewLSP is ever attempted", got)
	}
}

// TestNavigatorFor_AllowedWorkspaceStillAttemptsSpawn is the control: an
// ordinary workspace (no policy file at all — the documented default,
// level 3, which permits an LSP) must still reach NewLSP. It asserts only
// SpawnAttempts >= 1 rather than the resulting error/nav or an exact count:
// NewLSP's own outcome (and therefore how many times NavigatorFor's retry
// loop spins) differs per build arm — it always errors without -tags lsp
// (retrying up to maxRestart times), and may actually spawn a real server on
// the first attempt under -tags lsp if one happens to be installed.
func TestNavigatorFor_AllowedWorkspaceStillAttemptsSpawn(t *testing.T) {
	root := t.TempDir() // no .ai-privacy.yml -> default level 3, allowed

	p := NewPool(Config{})
	t.Cleanup(func() { _ = p.Close() })

	nav, err := p.NavigatorFor(context.Background(), "go", root)
	if err != nil && errors.Is(err, ErrPrivacyRestricted) {
		t.Fatalf("NavigatorFor on an unrestricted workspace was rejected as privacy-restricted: %v", err)
	}
	if nav != nil {
		t.Cleanup(func() { _ = nav.Close() })
	}
	if got := p.Stats().SpawnAttempts; got < 1 {
		t.Errorf("SpawnAttempts = %d, want >= 1 — an allowed workspace must still reach NewLSP", got)
	}
}

func writePrivacyPolicy(t *testing.T, root, content string) {
	t.Helper()
	path := filepath.Join(root, ingest.AIPrivacyFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
