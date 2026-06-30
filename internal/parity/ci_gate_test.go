package parity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests pin a CI/process invariant rather than runtime behavior: per
// CLAUDE.md, changes to internal/{query,search,parity} "must keep make
// verify green" (the ripgrep-parity gate), but .gitlab-ci.yml historically
// ran `make parity` only on schedule/tag pipelines and the gate image had no
// `rg`, so even the rg-backed unit tests in internal/search skipped on every
// merge request. They parse .gitlab-ci.yml with a small hand-rolled
// top-level-block splitter (not a real YAML parser) so this package does not
// pull in a YAML dependency into the default build.

// findRepoRoot walks up from the current test's working directory to the
// nearest ancestor containing go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found in any ancestor directory")
		}
		dir = parent
	}
}

func readCIConfig(t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".gitlab-ci.yml"))
	if err != nil {
		t.Fatalf("read .gitlab-ci.yml: %v", err)
	}
	return string(b)
}

var topLevelKeyRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+):[ \t]*(#.*)?$`)

// ciBlocks splits a flat GitLab CI YAML document into top-level blocks keyed
// by their top-level key (job name, "stages", "variables", etc). It is
// deliberately minimal: good enough for this project's single-document, two-
// space-indent .gitlab-ci.yml, not a general YAML parser.
func ciBlocks(content string) map[string]string {
	blocks := map[string]string{}
	var current string
	var buf []string
	flush := func() {
		if current != "" {
			blocks[current] = strings.Join(buf, "\n")
		}
	}
	for line := range strings.SplitSeq(content, "\n") {
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if m := topLevelKeyRe.FindStringSubmatch(line); m != nil {
				flush()
				current = m[1]
				buf = nil
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				// Column-0 blank/comment lines sit between blocks (often
				// documenting the NEXT job); attribute them to neither.
				continue
			}
		}
		buf = append(buf, line)
	}
	flush()
	return blocks
}

// stripComments drops any line whose trimmed form starts with "#", so
// substring checks below only see real keys/script content, not commentary
// that happens to mention a job or command by name (e.g. a docstring on the
// health job that explains its relationship to "make parity").
func stripComments(body string) string {
	var kept []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestCIGate_GateRunsOnRipgrepCapableImage asserts that whichever job runs on
// every push/MR (the "gate" stage) makes `rg` available before `go test`/
// `make health` runs, so internal/search's rg-backed parity unit tests
// (parity_test.go) actually execute instead of skipping via requireRipgrep.
func TestCIGate_GateRunsOnRipgrepCapableImage(t *testing.T) {
	blocks := ciBlocks(readCIConfig(t))

	var gateJob, gateBody string
	for name, body := range blocks {
		code := stripComments(body)
		if strings.Contains(code, `CI_PIPELINE_SOURCE == "merge_request_event"`) &&
			(strings.Contains(code, "make health") || strings.Contains(code, "go test")) {
			gateJob, gateBody = name, code
			break
		}
	}
	if gateJob == "" {
		t.Fatal("no job runs on merge_request_event and invokes make health / go test — the fast unit gate is not wired to MRs")
	}

	if !strings.Contains(strings.ToLower(gateBody), "ripgrep") {
		t.Errorf("job %q runs on every MR but never installs ripgrep (no \"ripgrep\" install step in its script/before_script) — "+
			"internal/search's parity_test.go shells out to `rg` and silently skips without it, so MRs never exercise rg-backed parity", gateJob)
	}
}

// TestCIGate_FullCorpusParityRunsOnCriticalPathMRs asserts that an MR
// touching internal/query, internal/search, or internal/parity — the
// packages CLAUDE.md singles out as "must keep make verify green" — triggers
// the full-corpus `make parity` gate, not just on schedule/tag pipelines.
func TestCIGate_FullCorpusParityRunsOnCriticalPathMRs(t *testing.T) {
	blocks := ciBlocks(readCIConfig(t))

	var parityJob, parityBody string
	for name, body := range blocks {
		code := stripComments(body)
		if strings.Contains(code, "make parity") {
			parityJob, parityBody = name, code
			break
		}
	}
	if parityJob == "" {
		t.Fatal("no job runs `make parity`")
	}

	if !strings.Contains(parityBody, `CI_PIPELINE_SOURCE == "merge_request_event"`) {
		t.Errorf("job %q (make parity) never triggers on merge_request_event — "+
			"a regression in internal/{query,search,parity} can merge without the full-corpus ripgrep-parity gate running", parityJob)
	}

	criticalPaths := []string{"internal/query", "internal/search", "internal/parity"}
	for _, p := range criticalPaths {
		if !strings.Contains(parityBody, p) {
			t.Errorf("job %q has no `changes:` rule covering %q — an MR touching that package would not trigger full-corpus parity", parityJob, p)
		}
	}
}
