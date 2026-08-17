// Package manifest turns package-manager manifests into proven cross-repo
// DEPENDS_ON edges.
//
// Every other arm of the graph layer infers. Phase 3 fans out over names it
// cannot resolve; phase 4 confirms syntax it cannot type-check. A manifest is
// different in kind: `<PackageReference Include="Acme.Billing" />` is the
// author's declaration that this project consumes that package, and
// `<PackageId>Acme.Billing</PackageId>` in another repo is that repo's
// declaration that it produces it. Joining the two is a lookup, not an
// inference, so the resulting edge carries ProvenConfidence.
//
// The package therefore does exactly two things and refuses to guess at either.
// Parse extracts declarations — and the identities a manifest claims for its own
// repo — each with the byte offset of its evidence. Builder.Resolve joins the two
// sides. A declaration naming a package no repo in the corpus provides (every
// third-party dependency) is *skipped and counted*, never an error: an external
// dependency is the normal case, not a defect. A manifest that fails to parse is
// likewise counted, and whatever parsed before the failure is kept.
//
// Scope, honestly: the four consuming forms are .csproj (and its .fsproj/.vbproj
// siblings, which share the schema), go.mod, package.json, and requirements.txt.
// pyproject.toml is parsed for its name only — without it no repo would ever
// provide a PyPI identity and requirements.txt could not resolve. Lock files are
// not read: they restate the same declarations with resolved versions, and
// version resolution is not what an edge needs.
package manifest

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Kind names the manifest form a file was parsed as.
type Kind uint8

const (
	// Unknown is a path that is not a manifest this package reads.
	Unknown Kind = iota
	// CSProj is an MSBuild project file: .csproj, .fsproj, or .vbproj.
	CSProj
	// GoMod is a Go module file.
	GoMod
	// PackageJSON is an npm package manifest.
	PackageJSON
	// Requirements is a pip requirements file.
	Requirements
	// PyProject is a PEP 621 / Poetry project file, read for its name only.
	PyProject
)

// String renders a Kind for diagnostics and tests.
func (k Kind) String() string {
	switch k {
	case CSProj:
		return "CSProj"
	case GoMod:
		return "GoMod"
	case PackageJSON:
		return "PackageJSON"
	case Requirements:
		return "Requirements"
	case PyProject:
		return "PyProject"
	default:
		return "Unknown"
	}
}

// Ecosystem is the namespace a dependency name lives in. It is what makes a
// join sound: `Acme.Billing` as a NuGet id and `acme-billing` as a PyPI
// distribution are unrelated identities that must never resolve to each other,
// and each ecosystem normalizes names by its own rules.
type Ecosystem uint8

const (
	// EcosystemUnknown is an unclassified name; it never resolves.
	EcosystemUnknown Ecosystem = iota
	// NuGet is a .NET package id (case-insensitive).
	NuGet
	// GoModule is a Go module path (case-sensitive, slash-delimited).
	GoModule
	// NPM is an npm package name, optionally scoped.
	NPM
	// PyPI is a Python distribution name, normalized per PEP 503.
	PyPI
	// ProjectPath is a path to a project file rather than a package name — an
	// MSBuild ProjectReference. It resolves against the corpus layout, not
	// against any package registry.
	ProjectPath
)

// String renders an Ecosystem for diagnostics and tests.
func (e Ecosystem) String() string {
	switch e {
	case NuGet:
		return "NuGet"
	case GoModule:
		return "GoModule"
	case NPM:
		return "NPM"
	case PyPI:
		return "PyPI"
	case ProjectPath:
		return "ProjectPath"
	default:
		return "EcosystemUnknown"
	}
}

// RefKind distinguishes a reference by package name from a reference by path to
// another project file.
type RefKind uint8

const (
	// PackageRef names a package in a registry's namespace.
	PackageRef RefKind = iota
	// ProjectRef names another project file by relative path.
	ProjectRef
)

// String renders a RefKind for diagnostics and tests.
func (r RefKind) String() string {
	if r == ProjectRef {
		return "ProjectRef"
	}
	return "PackageRef"
}

// Rule records which manifest fact joined a declaration to a provider. It is
// evidence provenance: every rule below is a documented declaration or a
// documented tool default, never a heuristic, which is what keeps the resulting
// edge at ProvenConfidence.
type Rule uint8

const (
	// RuleUnknown is the zero value; no join was made.
	RuleUnknown Rule = iota
	// RuleDeclaredName is an exact match against an identity the provider
	// declares outright: <PackageId>, <AssemblyName>, go.mod's module path,
	// package.json's name, or pyproject.toml's project name.
	RuleDeclaredName
	// RuleProjectFileName is an exact match against MSBuild's documented
	// default, where a project that declares no PackageId or AssemblyName
	// publishes under its project file's base name.
	RuleProjectFileName
	// RuleModulePrefix is a Go require path that lies under a declared module
	// path at a path-component boundary — the import path of a package inside
	// that module.
	RuleModulePrefix
	// RuleProjectPath is a ProjectReference whose relative path resolves onto a
	// project file known to the corpus.
	RuleProjectPath
	// RuleProjectBaseName is a ProjectReference whose path could not be resolved
	// against the corpus layout but whose file name matches known project files.
	// The corpus does not necessarily mirror the directory layout the reference
	// was written against, so this is the last resort before giving up.
	RuleProjectBaseName
)

// String renders a Rule for diagnostics and tests.
func (r Rule) String() string {
	switch r {
	case RuleDeclaredName:
		return "DeclaredName"
	case RuleProjectFileName:
		return "ProjectFileName"
	case RuleModulePrefix:
		return "ModulePrefix"
	case RuleProjectPath:
		return "ProjectPath"
	case RuleProjectBaseName:
		return "ProjectBaseName"
	default:
		return "Unknown"
	}
}

// Declaration is one dependency a manifest declares. Name and Version are the
// text as written; Offset is the byte offset of Name's first byte within the
// manifest content, so an edge built from this declaration can point a reader at
// the exact evidence.
type Declaration struct {
	Kind    Kind
	Ref     RefKind
	Name    string
	Version string
	Offset  int
	// Indirect is go.mod's `// indirect` marker: the module is required to build
	// a dependency rather than by this module's own code. The requirement is
	// still a declared fact, so it is retained and flagged.
	Indirect bool
	// Dev marks a package.json devDependencies entry.
	Dev bool
}

// Ecosystem reports the namespace this declaration's name lives in.
func (d Declaration) Ecosystem() Ecosystem {
	if d.Ref == ProjectRef {
		return ProjectPath
	}
	switch d.Kind {
	case CSProj:
		return NuGet
	case GoMod:
		return GoModule
	case PackageJSON:
		return NPM
	case Requirements, PyProject:
		return PyPI
	default:
		return EcosystemUnknown
	}
}

// Provided is an identity a manifest claims for the repo that contains it: the
// name by which other repos' manifests may declare a dependency on it. Offset is
// the byte offset of the evidence within the manifest content, or 0 when the
// evidence is the file's own name (RuleProjectFileName).
type Provided struct {
	Kind   Kind
	Name   string
	Offset int
	Rule   Rule
}

// Ecosystem reports the namespace this identity is published in.
func (p Provided) Ecosystem() Ecosystem {
	switch p.Kind {
	case CSProj:
		return NuGet
	case GoMod:
		return GoModule
	case PackageJSON:
		return NPM
	case PyProject, Requirements:
		return PyPI
	default:
		return EcosystemUnknown
	}
}

// Manifest is one parsed manifest file: the identities it provides and the
// dependencies it declares.
type Manifest struct {
	Kind     Kind
	Path     string
	Provides []Provided
	Requires []Declaration
}

// KindOf classifies a repo-relative path as a manifest form, returning Unknown
// for everything else. Matching is on the file name only; whether the path is
// vendored is a separate question (see Vendored).
func KindOf(relPath string) Kind {
	base := strings.ToLower(path.Base(relPath))
	switch base {
	case "go.mod":
		return GoMod
	case "package.json":
		return PackageJSON
	case "pyproject.toml":
		return PyProject
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".csproj", ".fsproj", ".vbproj":
		return CSProj
	}
	// requirements.txt and its conventional siblings (requirements-dev.txt,
	// requirements_test.txt). A directory of them (requirements/base.txt) is not
	// matched: the name carries no signal that the file is a requirements file.
	if strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt") {
		return Requirements
	}
	return Unknown
}

// Vendored reports whether relPath is a copy of someone else's manifest checked
// into the tree. A vendored manifest describes its upstream package, not this
// repo, so registering its identity would let any repo that vendors a dependency
// masquerade as that dependency's provider.
//
// The list is deliberately confined to directory names that mean "not our code"
// by tool convention. `packages/` in particular is NOT on it: a JavaScript
// monorepo keeps its own workspaces there, and excluding it would drop exactly
// the internal providers this phase exists to find.
func Vendored(relPath string) bool {
	for _, component := range strings.Split(path.Clean(relPath), "/") {
		switch strings.ToLower(component) {
		case "node_modules", "vendor", "bower_components", "third_party", "thirdparty":
			return true
		}
	}
	return false
}

// Normalize renders name in the comparison form of its ecosystem, so that a
// provider's identity and a consumer's declaration join iff the ecosystem's own
// rules say they name the same thing.
func Normalize(eco Ecosystem, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	switch eco {
	case NuGet, NPM:
		// NuGet ids are case-insensitive; npm names are required to be lower
		// case, so folding is a no-op on a valid name and a repair otherwise.
		return strings.ToLower(name)
	case GoModule:
		// Module paths are case-sensitive: example.com/M and example.com/m are
		// different modules. Only the trailing slash is noise.
		return strings.TrimSuffix(name, "/")
	case PyPI:
		return normalizePyPI(name)
	case ProjectPath:
		return strings.ToLower(path.Clean(strings.ReplaceAll(name, `\`, "/")))
	default:
		return name
	}
}

// normalizePyPI applies PEP 503: fold case and collapse runs of the separator
// characters to a single dash, so Acme_Billing, acme.billing, and acme--billing
// are one name.
func normalizePyPI(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	dash := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '-' || c == '_' || c == '.' {
			dash = true
			continue
		}
		if dash && b.Len() > 0 {
			b.WriteByte('-')
		}
		dash = false
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Parse reads content as the manifest form implied by relPath. It returns
// whatever it could parse together with any syntax error, so a malformed
// manifest degrades to its readable prefix instead of disappearing. An
// unrecognized path returns a zero Manifest and no error.
func Parse(relPath string, content []byte) (Manifest, error) {
	kind := KindOf(relPath)
	switch kind {
	case CSProj:
		return parseCSProj(relPath, content)
	case GoMod:
		return parseGoMod(relPath, content)
	case PackageJSON:
		return parsePackageJSON(relPath, content)
	case Requirements:
		return Manifest{Kind: Requirements, Path: relPath, Requires: parseRequirements(content)}, nil
	case PyProject:
		return parsePyProject(relPath, content), nil
	default:
		return Manifest{}, nil
	}
}

// ---------------------------------------------------------------------------
// shared lexical helpers
// ---------------------------------------------------------------------------

// eachLine calls fn with the byte offset and content of every line, excluding
// the line terminator. Offsets are absolute so a declaration's Offset survives.
func eachLine(content []byte, fn func(offset int, line []byte)) {
	for off := 0; off < len(content); {
		idx := bytes.IndexByte(content[off:], '\n')
		if idx < 0 {
			fn(off, trimCR(content[off:]))
			return
		}
		fn(off, trimCR(content[off:off+idx]))
		off += idx + 1
	}
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

// field is a whitespace-delimited token with its offset within its line.
type field struct {
	text string
	off  int
}

// lineFields splits line on spaces and tabs, retaining each token's offset.
func lineFields(line []byte) []field {
	var out []field
	for i := 0; i < len(line); {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		out = append(out, field{text: string(line[start:i]), off: start})
	}
	return out
}

// unquoteField strips a surrounding double-quoted string, moving the offset onto
// the first byte of the quoted text so Offset still points at the name.
func unquoteField(f field) field {
	if len(f.text) >= 2 && f.text[0] == '"' && f.text[len(f.text)-1] == '"' {
		if s, err := strconv.Unquote(f.text); err == nil {
			return field{text: s, off: f.off + 1}
		}
	}
	return f
}

// String renders a Manifest for test failure messages.
func (m Manifest) String() string {
	return fmt.Sprintf("%s(%s): %d provided, %d required", m.Kind, m.Path, len(m.Provides), len(m.Requires))
}
