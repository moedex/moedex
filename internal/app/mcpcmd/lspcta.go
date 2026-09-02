package mcpcmd

// lspcta.go builds a precise, detected recommendation for enabling LSP-precise
// navigation — not a blanket "you might want LSP servers" nudge, and not an
// auto-installer. It surveys the languages actually present in this ingest
// against internal/navigate/registry.go (the single source of truth for the
// engine's language-server set, shared with scripts/install-lsp-servers.sh and
// `moedex doctor`) and reports, per language, whether the server is already on
// PATH. The result is surfaced through the MCP initialize Instructions field
// (mcp.WithExtraInstructions) — a channel the connecting agent actually reads,
// unlike stderr.

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"moedex/internal/ingest"
	"moedex/internal/navigate"
)

type langServerStatus struct {
	lang, command string
	installed     bool
}

// lspRecommendation returns "" when there is nothing to say: no file in files
// maps to a registered language, or (lspBuild is true and every relevant
// server is already installed). lspBuild reports whether this binary was built
// with -tags lsp, so the message can say whether navigation is actually live
// or just would-be-live once installed and rebuilt.
func lspRecommendation(files []ingest.File, lspBuild bool) string {
	langs := map[string]bool{}
	for _, f := range files {
		if lang, ok := navigate.LanguageForPath(f.RelPath); ok {
			langs[lang] = true
		}
	}
	if len(langs) == 0 {
		return ""
	}

	var present, missing []langServerStatus
	for lang := range langs {
		spec, ok := navigate.SpecForLanguage(lang)
		if !ok {
			continue
		}
		st := langServerStatus{lang: lang, command: spec.Command}
		if _, err := exec.LookPath(spec.Command); err == nil {
			st.installed = true
			present = append(present, st)
		} else {
			missing = append(missing, st)
		}
	}
	if len(missing) == 0 && (lspBuild || len(present) == 0) {
		return "" // nothing missing, or nothing navigable was even detected
	}
	sort.Slice(present, func(i, j int) bool { return present[i].lang < present[j].lang })
	sort.Slice(missing, func(i, j int) bool { return missing[i].lang < missing[j].lang })

	var b strings.Builder
	b.WriteString("moedex supports LSP-precise navigation (find_definition, find_references, find_implementations) for this repo's languages")
	if !lspBuild {
		b.WriteString(", but this plugin binary was built without -tags lsp, so navigation stays off regardless of what's installed locally")
	}
	b.WriteString(".\n")
	if len(present) > 0 {
		fmt.Fprintf(&b, "Already installed: %s.\n", joinStatuses(present))
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "Detected but not installed: %s. Install with scripts/install-lsp-servers.sh.\n", joinStatuses(missing))
	}
	if !lspBuild {
		b.WriteString("Then switch to the -tags lsp plugin binary variant to enable navigation.")
	}
	return strings.TrimSpace(b.String())
}

func joinStatuses(statuses []langServerStatus) string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = fmt.Sprintf("%s (%s)", s.lang, s.command)
	}
	return strings.Join(names, ", ")
}
