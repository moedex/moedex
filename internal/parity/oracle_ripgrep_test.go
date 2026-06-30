package parity

import "testing"

// TestRipgrepArgsSeparatesMirrorDir guards against a WorkDir that happens to
// start with '-' being misparsed as a flag: mirrorDir is a harness-derived
// positional, not corpus-controlled, but it still needs a "--" separator
// before it since ripgrep (like most CLIs) treats a leading '-' as a flag
// regardless of argument position.
func TestRipgrepArgsSeparatesMirrorDir(t *testing.T) {
	r := &ripgrep{bin: "rg", mirrorDir: "-weird-dir", threads: "0"}
	args := r.args(Query{Pattern: "foo", Literal: true})

	if len(args) < 2 {
		t.Fatalf("args too short: %v", args)
	}
	last := args[len(args)-1]
	if last != r.mirrorDir {
		t.Fatalf("args = %v, want mirrorDir %q as the last element", args, r.mirrorDir)
	}
	if sep := args[len(args)-2]; sep != "--" {
		t.Fatalf("args = %v, want \"--\" immediately before mirrorDir, got %q", args, sep)
	}
}
