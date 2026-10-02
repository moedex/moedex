package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPublicCorpusParity(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		want       int
	}{
		{"forwarding", "", 0}, {"wrong_commit", "wrong", 2},
		{"dirty", "dirty", 2}, {"existing_destination", "existing", 2},
		{"inside_checkout", "inside", 2}, {"nested_repo", "nested", 2},
		{"inside_moedex", "source", 2},
		{"tracked_scope", "tracked", 2}, {"mirror_scope", "mirror", 2},
		{"source_changed", "changed", 2}, {"tool_failure", "failed", 7},
		{"missing_latency", "no_latency", 2}, {"short_latency", "short_latency", 2},
		{"bad_latency_header", "bad_latency_header", 2}, {"duplicate_query_id", "duplicate_query_id", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			checkout := filepath.Join(dir, "checkout with spaces")
			bin := filepath.Join(dir, "bin")
			for _, path := range []string{filepath.Join(checkout, ".git"), bin} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(checkout, "source.cs"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write("git", `[ "$GIT_NO_REPLACE_OBJECTS" = 1 ] || exit 8
if [ "$1" = --version ]; then echo git-test; exit; fi
shift 2
case "$1 $2" in
 'rev-parse --show-toplevel') printf '%s\n' "$CHECKOUT";;
 'rev-parse HEAD') if [ "$MODE" = wrong ]; then echo wrong; else echo 36d26c5466e4d25940657ccb8d5b9557ccaf7be1; fi;;
 'rev-parse HEAD^{tree}') echo tree-test;;
 'status --porcelain') if [ "$MODE" = dirty ] || { [ "$MODE" = changed ] && [ -f "$MARKER" ]; }; then echo dirty; fi;;
 'ls-files ') n=35115; [ "$MODE" != tracked ] || n=2; awk -v n="$n" 'BEGIN {for(i=0;i<n;i++) print "file"i}';;
 'ls-files -v') printf 'H source.cs\000';;
 'ls-tree -rz') printf '100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\tsource.cs\000';;
 *) exit 9;;
esac
`)
			// Fake inventory isolates orchestration from creating 34,791 files.
			write("find", `case "$1" in
 */work/mirror) n=34791; [ "$MODE" != mirror ] || n=1; awk -v n="$n" 'BEGIN {for(i=0;i<n;i++) print "file"i}';;
 *) printf '%s/.git\n' "$CHECKOUT"; if [ "$MODE" = nested ]; then printf '%s/nested/.git\n' "$CHECKOUT"; fi;;
esac
`)
			write("rg", "echo ripgrep-test\n")
			write("candidate", `printf '%s\n' "$@" > "$ARGS"
touch "$MARKER"
while [ "$#" -gt 0 ]; do
 if [ "$1" = -report ]; then shift; printf 'fixture parity report\n' > "$1"; fi
 if [ "$1" = -latency-csv ]; then
  shift
  if [ "$MODE" != no_latency ]; then
   header='query_id,bucket,literal,ignorecase,nmoe,nanos,candidate_kind,candidate_blobs,candidate_bytes,candidate_lines,all_candidates,query_all,line_filter_kind,lines_entering_re2,verify_workers,pattern'
   [ "$MODE" != bad_latency_header ] || header=wrong
   n=1052; [ "$MODE" != short_latency ] || n=1051
   { printf '%s\n' "$header"; awk -v n="$n" -v mode="$MODE" 'BEGIN {for(i=0;i<n;i++) {id=i; if(mode=="duplicate_query_id") id=0; print id ",a,true,false,1,10,kind,1,1,1,false,false,kind,1,1,pattern"}}'; } > "$1"
  fi
 fi
 shift
done
[ "$MODE" != failed ] || exit 7
`)
			run := filepath.Join(dir, "new run")
			if tc.mode == "existing" {
				if err := os.Mkdir(run, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.mode == "inside" {
				run = filepath.Join(checkout, "run")
			}
			if tc.mode == "source" {
				root, err := filepath.Abs("..")
				if err != nil {
					t.Fatal(err)
				}
				run = filepath.Join(root, "public-corpus-test-nonexistent-"+filepath.Base(filepath.Dir(dir)))
			}
			cmd := exec.Command("sh", "public-corpus-parity.sh", checkout, run, filepath.Join(bin, "candidate"))
			args := filepath.Join(dir, "args")
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CHECKOUT="+checkout, "MODE="+tc.mode, "ARGS="+args, "MARKER="+filepath.Join(dir, "executed"))
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					code = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.want {
				t.Fatalf("exit %d want %d: %s", code, tc.want, out)
			}
			if tc.mode == "" {
				got, err := os.ReadFile(args)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"parity", "-corpus", checkout, "-work", run + "/work", "-report", run + "/PARITY-REPORT.md", "-latency-csv", run + "/latency.csv", "-seed", "20260930", "-floor", "1000", "-scan-parallel", "4", "-rg-parallel", "2", "-rg-threads", "1", "-no-zoekt", "-keep"}
				if string(got) != strings.Join(want, "\n")+"\n" {
					t.Fatalf("argv: %s", got)
				}
				nul, err := os.ReadFile(filepath.Join(run, "argv.nul"))
				if err != nil || string(nul) != run+"/moedex\x00"+strings.Join(want, "\x00")+"\x00" {
					t.Fatalf("lossless argv: %q (%v)", nul, err)
				}
				for _, file := range []string{"provenance.txt", "argv.txt", "run-report.txt", "PARITY-REPORT.md"} {
					if info, err := os.Stat(filepath.Join(run, file)); err != nil || info.Size() == 0 {
						t.Fatalf("missing evidence %s: %v", file, err)
					}
				}
			}
			if tc.mode == "failed" || tc.mode == "mirror" || tc.mode == "changed" {
				b, err := os.ReadFile(filepath.Join(run, "exit-status.txt"))
				if err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(tc.want) {
					t.Fatalf("exit evidence %q: %v", b, err)
				}
				if _, err := os.Stat(filepath.Join(run, "run-report.txt")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPublicCorpusRawSourceVerification(t *testing.T) {
	for _, mode := range []string{"clean", "assume-unchanged", "skip-worktree", "clean-filter"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			git("config", "user.name", "Parity Test")
			git("config", "user.email", "test@example.invalid")
			if err := os.WriteFile(filepath.Join(root, "source.cs"), []byte("class Original {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "clean-filter" {
				git("config", "filter.normalize.clean", "sed s/Changed/Original/g")
				if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.cs filter=normalize\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("commit", "-qm", "fixture")
			if mode == "assume-unchanged" || mode == "skip-worktree" {
				git("update-index", "--"+mode, "source.cs")
			}
			if mode != "clean" {
				if err := os.WriteFile(filepath.Join(root, "source.cs"), []byte("class Changed {}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "clean-filter" {
				// The clean filter stores the original bytes back into the index,
				// leaving different working-tree bytes with an unchanged HEAD.
				git("add", "source.cs")
			}
			if state := git("status", "--porcelain", "--untracked-files=all", "--ignored"); state != "" {
				t.Fatalf("test must demonstrate a clean status, got %q", state)
			}
			out, err := exec.Command("python3", "public-corpus-source-check.py", root).CombinedOutput()
			if mode == "clean" {
				if err != nil || !strings.Contains(string(out), "source_roster_sha256=") {
					t.Fatalf("clean source rejected: %v %s", err, out)
				}
			} else if err == nil {
				t.Fatal("hidden source modification accepted")
			} else if mode == "clean-filter" && !strings.Contains(string(out), "raw source differs") {
				t.Fatalf("expected raw-byte mismatch: %s", out)
			} else if mode != "clean-filter" && !strings.Contains(string(out), "index flags") {
				t.Fatalf("expected hidden-index flag rejection: %s", out)
			}
		})
	}
}
