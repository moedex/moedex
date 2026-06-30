package main

import (
	"strings"
	"testing"

	"moedex/internal/corpus"
)

// Both runClone and runSync must reject a malformed -reindex flag triple
// (-reindex without -cas-dir and -shard-dir) before doing any clone/sync work,
// not after. We force corpus.Doctor's "glab installed" check to fail
// deterministically (empty PATH, no real exec) so any code path that runs
// Doctor before validating -reindex surfaces a "setup not ready" error
// instead of the -reindex-specific one — that's the bug signature.

func TestRunCloneValidatesReindexFlagsBeforeDoctor(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("MOEDEX_CORPUS", t.TempDir())

	err := runClone([]string{"-reindex", "-no-banner"})
	if err == nil {
		t.Fatal("runClone(-reindex) with no -cas-dir/-shard-dir: got nil error, want one")
	}
	const want = "-reindex requires -cas-dir and -shard-dir"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("runClone(-reindex) error = %q, want it to contain %q (validation must run before Doctor/clone)", err.Error(), want)
	}
}

func TestRunSyncValidatesReindexFlagsBeforeDoctor(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("MOEDEX_CORPUS", t.TempDir())

	err := runSync([]string{"-reindex", "-no-banner"})
	if err == nil {
		t.Fatal("runSync(-reindex) with no -cas-dir/-shard-dir: got nil error, want one")
	}
	const want = "-reindex requires -cas-dir and -shard-dir"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("runSync(-reindex) error = %q, want it to contain %q (validation must run before Doctor/sync)", err.Error(), want)
	}
}

// TestProgressLine_UnknownOutcomeNotSilentlyDropped is the regression for
// F-046: progressLine's switch had no default case, so a CloneOutcome value
// none of the cases recognize (e.g. a future addition to the enum that this
// switch wasn't updated for) silently produced an empty line — invisible in
// live output with no indication a result was ever processed.
func TestProgressLine_UnknownOutcomeNotSilentlyDropped(t *testing.T) {
	res := corpus.CloneResult{
		Project: corpus.Project{PathWithNamespace: "g/mystery"},
		Outcome: corpus.CloneOutcome(99), // not a real outcome
	}
	line := progressLine(1, 1, res)
	if line == "" {
		t.Fatal("progressLine(unknown outcome) = \"\", want a visible fallback line, not silence")
	}
	if !strings.Contains(line, "g/mystery") {
		t.Fatalf("progressLine(unknown outcome) = %q, want it to still mention the project", line)
	}
}

// TestDisplayTentacles is the regression for F-080: the printed "Moe reaches
// out with N tentacle(s)" line must never show 0 or negative, even though
// that's what -concurrency 0 (or a misconfigured negative value) takes
// literally — CloneProjects itself clamps the real worker count to >= 1.
func TestDisplayTentacles(t *testing.T) {
	cases := []struct {
		name        string
		concurrency int
		total       int
		want        int
	}{
		{"zero concurrency floors to 1", 0, 5, 1},
		{"negative concurrency floors to 1", -3, 5, 1},
		{"concurrency above total caps to total", 10, 3, 3},
		{"concurrency within range passes through", 4, 10, 4},
		{"zero total with positive concurrency keeps concurrency", 4, 0, 4},
		{"zero total and zero concurrency floors to 1", 0, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayTentacles(tc.concurrency, tc.total); got != tc.want {
				t.Errorf("displayTentacles(%d, %d) = %d, want %d", tc.concurrency, tc.total, got, tc.want)
			}
		})
	}
}

func TestValidateReindexFlags(t *testing.T) {
	mk := func(enabled bool, casDir, shardDir string) reindexFlags {
		return reindexFlags{
			enabled:    &enabled,
			casDir:     &casDir,
			shardDir:   &shardDir,
			shardBytes: new(int64),
			indexBin:   new(string),
			reload:     new(string),
		}
	}

	cases := []struct {
		name    string
		rf      reindexFlags
		wantErr bool
	}{
		{"disabled, nothing set", mk(false, "", ""), false},
		{"enabled, both set", mk(true, "/cas", "/shard"), false},
		{"enabled, missing cas-dir", mk(true, "", "/shard"), true},
		{"enabled, missing shard-dir", mk(true, "/cas", ""), true},
		{"enabled, missing both", mk(true, "", ""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReindexFlags(tc.rf)
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}
