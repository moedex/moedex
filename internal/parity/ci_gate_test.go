package parity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests pin the CI/process policy rather than runtime behavior: ordinary
// pushes and MRs exercise rg-backed unit parity, while corpus-dependent checks
// run on schedules/tags and remain available locally through make verify.
// They parse .gitlab-ci.yml with a small hand-rolled
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
	if !strings.Contains(gateBody, `CI_PIPELINE_SOURCE == "push"`) {
		t.Errorf("job %q does not run the fast unit gate on pushes", gateJob)
	}

	if !strings.Contains(strings.ToLower(gateBody), "ripgrep") {
		t.Errorf("job %q runs on every MR but never installs ripgrep (no \"ripgrep\" install step in its script/before_script) — "+
			"internal/search's parity_test.go shells out to `rg` and silently skips without it, so MRs never exercise rg-backed parity", gateJob)
	}
}

// TestCIGate_CorpusChecksStayOutsideOrdinaryPipelines prevents an MR or branch
// push from queuing checks that require externally mounted corpora. It also
// keeps the scheduled checks and tagged ripgrep parity wired up. A fallback or
// changes-only rule would defeat this policy, so every rule must be explicit.
func TestCIGate_CorpusChecksStayOutsideOrdinaryPipelines(t *testing.T) {
	blocks := ciBlocks(readCIConfig(t))
	for _, job := range []struct {
		name    string
		command string
		tagged  bool
	}{
		{"parity", "make parity", true},
		{"graph-eval-private", "make graph-eval-private", false},
	} {
		t.Run(job.name, func(t *testing.T) {
			body, ok := blocks[job.name]
			if !ok || !strings.Contains(stripComments(body), job.command) {
				t.Fatalf("job %q must retain %q", job.name, job.command)
			}
			// Reuse the block splitter one indentation level down to inspect
			// only rules, excluding scripts and artifact when: always settings.
			var unindented []string
			for line := range strings.SplitSeq(body, "\n") {
				unindented = append(unindented, strings.TrimPrefix(line, "  "))
			}
			rules := ciBlocks(strings.Join(unindented, "\n"))["rules"]
			allowed := map[string]bool{`- if: '$CI_PIPELINE_SOURCE == "schedule"'`: true}
			if job.tagged {
				allowed[`- if: '$CI_COMMIT_TAG'`] = true
			}
			seen := map[string]bool{}
			for line := range strings.SplitSeq(stripComments(rules), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if !allowed[line] {
					t.Errorf("job %q has an unexpected corpus pipeline rule: %s", job.name, line)
				}
				seen[line] = true
			}
			for rule := range allowed {
				if !seen[rule] {
					t.Errorf("job %q is missing required corpus pipeline rule: %s", job.name, rule)
				}
			}
		})
	}
}
