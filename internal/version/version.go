// Package version exposes the build identity of a moedex binary, so an operator
// (and `moedex-index doctor`) can tell at a glance WHICH build is installed and
// whether the installed binaries are consistent with each other. Binary skew —
// an old binary shadowing a new one on PATH — caused a real index-loss incident,
// so making the build identity visible is a guardrail, not a nicety.
//
// The commit/dirty/time come from the VCS info `go build` embeds automatically
// (runtime/debug.ReadBuildInfo). Canonical Makefile builds additionally inject a
// deterministic dirty-source digest. Tag is an optional human label that CAN be
// set via -ldflags "-X moedex/internal/version.Tag=v0.3".
package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Tag is an optional human version label, settable via
// -ldflags "-X moedex/internal/version.Tag=...". Empty -> reported as "dev".
var Tag = ""

// SourceDigest is the deterministic Git-visible worktree digest injected by
// canonical Makefile builds. Direct dirty `go build` invocations leave it empty
// and are reported honestly as dirty.unknown.
var SourceDigest = ""

// Info is a binary's resolved build identity.
type Info struct {
	Tag      string // optional human label, or "dev"
	Commit   string // VCS revision embedded by `go build`, or "unknown"
	Modified bool   // working tree was dirty at build time
	Source   string // dirty worktree source digest, or empty when unstamped
	Time     string // VCS commit time (RFC3339), if known
	Go       string // Go toolchain version
}

// Get resolves the running binary's build identity from the embedded build info.
func Get() Info {
	info := Info{Tag: Tag, Commit: "unknown", Source: SourceDigest}
	if info.Tag == "" {
		info.Tag = "dev"
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.Go = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			case "vcs.time":
				info.Time = s.Value
			}
		}
	}
	return info
}

// CommitShort is the commit truncated to 12 chars with a content-specific dirty
// suffix; "unknown" passes through. This is the field doctor compares across
// binaries to spot skew.
func (i Info) CommitShort() string {
	c := i.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	if i.Modified {
		c += "+dirty." + i.sourceIdentity()
	}
	return c
}

// MCP returns the compact running-binary identity advertised in MCP
// serverInfo.version. Tagged builds are <tag>+<commit>; untagged builds are
// dev+<commit>; a dirty worktree appends .dirty.<source-digest>.
func MCP() string {
	return Get().MCP()
}

// MCP returns this Info in the same compact form used on the wire.
func (i Info) MCP() string {
	commit := i.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	identity := i.Tag + "+" + commit
	if i.Modified {
		identity += ".dirty." + i.sourceIdentity()
	}
	return identity
}

func (i Info) sourceIdentity() string {
	if source := strings.TrimSpace(i.Source); source != "" {
		return source
	}
	return "unknown"
}

// Line is the single, machine-parseable line printed by every binary's -version
// flag. The `commit=` and `dense=` tokens are stable keys doctor greps for.
//
//	moedex-serve dev commit=a1b2c3d4e5f6 dense=true go=go1.26 built=2026-06-26T...
func Line(name string, dense bool) string {
	i := Get()
	t := i.Time
	if t == "" {
		t = "unknown"
	}
	return fmt.Sprintf("%s %s commit=%s dense=%t go=%s built=%s", name, i.Tag, i.CommitShort(), dense, i.Go, t)
}
