package corpus

import (
	_ "embed"
	"strings"
)

//go:embed default_groups.txt
var defaultGroupsFile string

// DefaultGroups returns no private built-in scope. Acquisition callers must
// supply an explicit nonempty group allowlist.
func DefaultGroups() []string {
	return ParseGroups(strings.NewReader(defaultGroupsFile))
}
