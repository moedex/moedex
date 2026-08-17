package manifest

import (
	"bytes"
	"strings"
)

// parseGoMod reads a go.mod file: the module path it declares (the identity every
// other module's require line names it by) and its require directives.
//
// The parse is line-based rather than a call into golang.org/x/mod/modfile, which
// would be the first dependency in this repo's default build — see the pure-Go
// invariant in CLAUDE.md. It tracks the block verb so that exclude, replace, and
// retract blocks are read and discarded rather than mistaken for requirements,
// which is the one thing a naive line scan gets wrong.
//
// `// indirect` requirements are kept and flagged. They are declared facts about
// what the module builds against, and for a cross-repo graph an indirect internal
// dependency is exactly as real as a direct one; a consumer that wants only
// direct edges can filter on Declaration.Indirect.
func parseGoMod(relPath string, content []byte) (Manifest, error) {
	m := Manifest{Kind: GoMod, Path: relPath}
	block := ""

	eachLine(content, func(offset int, line []byte) {
		code, comment := splitGoComment(line)
		fields := lineFields(code)
		if len(fields) == 0 {
			return
		}

		if block != "" {
			if fields[0].text == ")" {
				block = ""
				return
			}
			if block == "require" {
				if decl, ok := goRequirement(fields, offset, comment); ok {
					m.Requires = append(m.Requires, decl)
				}
			}
			return
		}

		verb := fields[0].text
		rest := fields[1:]
		// A block opens with the verb followed by "(" — possibly the verb's only
		// remaining token, possibly after a same-line requirement in malformed
		// input, so the check is on the last token.
		if len(rest) > 0 && rest[len(rest)-1].text == "(" {
			switch verb {
			case "require", "exclude", "replace", "retract":
				block = verb
			}
			return
		}
		switch verb {
		case "module":
			if len(rest) == 0 {
				return
			}
			name := unquoteField(rest[0])
			if name.text == "" {
				return
			}
			m.Provides = append(m.Provides, Provided{
				Kind:   GoMod,
				Name:   name.text,
				Offset: offset + name.off,
				Rule:   RuleDeclaredName,
			})
		case "require":
			if decl, ok := goRequirement(rest, offset, comment); ok {
				m.Requires = append(m.Requires, decl)
			}
		}
	})
	return m, nil
}

// goRequirement turns "example.com/mod v1.2.3" into a declaration. A line whose
// first token is not a plausible module path (a bare version, a stray delimiter)
// yields nothing rather than a bogus dependency name.
func goRequirement(fields []field, lineOffset int, comment string) (Declaration, bool) {
	if len(fields) == 0 {
		return Declaration{}, false
	}
	name := unquoteField(fields[0])
	if !plausibleModulePath(name.text) {
		return Declaration{}, false
	}
	version := ""
	if len(fields) > 1 {
		version = unquoteField(fields[1]).text
	}
	return Declaration{
		Kind:     GoMod,
		Ref:      PackageRef,
		Name:     name.text,
		Version:  version,
		Offset:   lineOffset + name.off,
		Indirect: hasIndirectMarker(comment),
	}, true
}

// plausibleModulePath rejects tokens that cannot be a module path. It is a
// necessary condition only: anything that could be a path is admitted, and
// resolution — not this check — decides whether the corpus provides it.
func plausibleModulePath(name string) bool {
	if name == "" || name == "(" || name == ")" || name == "=>" {
		return false
	}
	switch name[0] {
	case '/', '.', '-', ',', '[', ']', '>', '<', '=':
		return false
	}
	// A version token is never a module path.
	return !isGoVersionToken(name)
}

func isGoVersionToken(token string) bool {
	if len(token) < 2 || token[0] != 'v' {
		return false
	}
	return token[1] >= '0' && token[1] <= '9'
}

// splitGoComment separates a line's code from its `//` comment. A module path
// cannot contain "//", so the first occurrence is unambiguous.
func splitGoComment(line []byte) (code []byte, comment string) {
	if i := bytes.Index(line, []byte("//")); i >= 0 {
		return line[:i], string(line[i+2:])
	}
	return line, ""
}

// hasIndirectMarker reports whether a require line's comment carries go.mod's
// `indirect` marker as its own word.
func hasIndirectMarker(comment string) bool {
	for _, word := range strings.FieldsFunc(comment, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ';' || r == ','
	}) {
		if word == "indirect" {
			return true
		}
	}
	return false
}
