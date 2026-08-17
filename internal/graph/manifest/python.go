package manifest

import (
	"bytes"
	"strings"
)

// parseRequirements reads a pip requirements file, extracting the distribution
// name of every requirement line.
//
// Lines that are not requirements are skipped rather than guessed at: option
// lines (-r, -c, -e, --hash, --index-url), comments, blanks, and direct URL
// references such as `git+https://host/repo#egg=name`, where the leading token is
// a URL scheme rather than a name. Version specifiers, extras, and PEP 508
// environment markers are recorded as the requirement's version text; they play
// no part in resolution.
//
// Continuation lines are not joined. A requirement name always begins the logical
// line, and a continuation begins with a specifier, a marker, or an option — none
// of which yields a name — so the simpler scan loses nothing.
func parseRequirements(content []byte) []Declaration {
	var out []Declaration
	eachLine(content, func(offset int, line []byte) {
		code := stripPythonComment(line)
		start := len(code) - len(bytes.TrimLeft(code, " \t"))
		code = code[start:]
		if len(code) == 0 || code[0] == '-' {
			return
		}
		name, rest := splitRequirementName(code)
		if name == "" {
			return
		}
		out = append(out, Declaration{
			Kind:    Requirements,
			Ref:     PackageRef,
			Name:    name,
			Version: strings.TrimSpace(string(rest)),
			Offset:  offset + start,
		})
	})
	return out
}

// splitRequirementName takes the leading distribution name off a requirement,
// returning "" when the line does not begin with one. The delimiter check is what
// rejects a URL: `git+https://…` and `https://…` both continue with a character a
// requirement never does.
func splitRequirementName(code []byte) (string, []byte) {
	i := 0
	for i < len(code) && isRequirementNameByte(code[i]) {
		i++
	}
	if i == 0 {
		return "", nil
	}
	name := string(code[:i])
	rest := code[i:]
	trimmed := bytes.TrimLeft(rest, " \t")
	if len(trimmed) == 0 {
		return name, rest
	}
	switch trimmed[0] {
	case '=', '<', '>', '!', '~', ';', ',', '[', '@', '#':
		return name, rest
	default:
		// '+', ':', '/' and friends: a URL or something else this file format
		// allows that is not a plain requirement.
		return "", nil
	}
}

func isRequirementNameByte(b byte) bool {
	return b == '-' || b == '_' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// stripPythonComment removes a `#` comment. Per PEP 508 a comment starts at the
// beginning of a line or after whitespace, so a `#` inside a URL fragment (an
// `#egg=` reference) does not terminate the line.
func stripPythonComment(line []byte) []byte {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i]
		}
	}
	return line
}

// parsePyProject reads a PEP 621 or Poetry project file for its distribution name
// only.
//
// This is a provider-only parser, and deliberately so. Without it no repo in the
// corpus would ever declare a PyPI identity, and every requirements.txt line
// would resolve to nothing — the parsing would be dead weight. Its dependency
// tables are left to a later phase: reading them properly means a TOML parser,
// which the pure-Go default build has no room for yet.
//
// Only `name` directly under [project] or [tool.poetry] is accepted, so the
// `name` keys inside [[project.authors]] and friends cannot be mistaken for the
// distribution's own.
func parsePyProject(relPath string, content []byte) Manifest {
	m := Manifest{Kind: PyProject, Path: relPath}
	var (
		table       string
		name        string
		nameOffset  int
		fromProject bool
	)
	eachLine(content, func(offset int, line []byte) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			return
		}
		if trimmed[0] == '[' {
			table = strings.TrimSpace(strings.Trim(string(trimmed), "[]"))
			return
		}
		if table != "project" && table != "tool.poetry" {
			return
		}
		// [project] is the standard and wins over [tool.poetry] regardless of
		// which table the file lists first.
		if name != "" && (fromProject || table != "project") {
			return
		}
		key, value, found := strings.Cut(string(trimmed), "=")
		if !found || strings.TrimSpace(key) != "name" {
			return
		}
		candidate := unquoteTOMLString(strings.TrimSpace(value))
		if candidate == "" {
			return
		}
		// Point at the name text itself rather than at the key.
		at := bytes.IndexByte(line, '=')
		off := offset + at + 1
		if raw := bytes.Index(line[at:], []byte(candidate)); raw >= 0 {
			off = offset + at + raw
		}
		name, nameOffset, fromProject = candidate, off, table == "project"
	})
	if name != "" {
		m.Provides = append(m.Provides, Provided{
			Kind:   PyProject,
			Name:   name,
			Offset: nameOffset,
			Rule:   RuleDeclaredName,
		})
	}
	return m
}

// unquoteTOMLString strips a basic or literal TOML string's quotes, rejecting
// anything else (an array, a number, a bare word) as not a name.
func unquoteTOMLString(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return ""
}
