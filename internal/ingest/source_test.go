package ingest

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSourceDependencyBoundary pins the corpus-isolation invariant this
// package must uphold (CLAUDE.md, ARCHITECTURE.md, docs/adr/0019, review
// finding F-17): internal/ingest reads the managed-corpus catalog/lock
// schema from the leaf package internal/corpus/catalog (no os/exec, no
// Runner), and must never regain a transitive production dependency on
// internal/corpus itself — the package that shells out to glab/git and is
// meant to stay quarantined to cmd/moedex-corpus. Without this check, a
// future contributor reaching for the more familiar "corpus" import (or
// adding a helper that does) would silently widen the dependency graph of
// every default-build production binary (moedex, moedex-mcp, moedex-index,
// scale, moedex-serve, moedex-parity all import this package) with no other
// compiler or CI signal — exactly the regression F-17 found already true at
// HEAD before this fix.
func TestSourceDependencyBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "moedex/internal/ingest").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps moedex/internal/ingest: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "moedex/internal/corpus" {
			t.Fatalf("internal/ingest has a transitive production dependency on moedex/internal/corpus " +
				"(the glab/git shell-out package) — depend on moedex/internal/corpus/catalog instead")
		}
	}
}
