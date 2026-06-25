package corpus

import (
	_ "embed"
	"strings"
)

//go:embed default_groups.txt
var defaultGroupsFile string

// DefaultGroups returns the built-in curated allowlist: the top-level GitLab
// namespaces that make up the TurnCommerce corpus (the set today's ~484-repo
// mirror spans). It is the scope used when no -groups file is supplied, so the
// binary ships with a sane default and `clone` never accidentally pulls every
// visible project. Regenerate the embedded list from a populated mirror with
// `moedex-corpus groups --from-disk`.
func DefaultGroups() []string {
	return ParseGroups(strings.NewReader(defaultGroupsFile))
}
