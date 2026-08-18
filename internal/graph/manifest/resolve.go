package manifest

import (
	"path"
	"sort"
	"strings"
)

// Confidence values for a resolved dependency edge.
const (
	// ProvenConfidence is phase 11's Proven tier. Both ends of the edge are
	// declarations: one repo says it consumes a name, another says it publishes
	// that name. Nothing was inferred, so nothing needs to be discounted.
	ProvenConfidence = 1.0
	// ContestedConfidence applies when several distinct repos declare the same
	// identity. The declaration is still a fact; which of the claimants satisfies
	// it is not, and at most one of the emitted edges is the real one. They are
	// emitted anyway — dropping them would cost recall silently, the one thing
	// the graph layer never does — and marked so a later tier can arbitrate.
	ContestedConfidence = 0.9
)

// Site is one place a manifest occurs, with the byte offset of the declaration or
// identity it contributed. Repo is the corpus repo label and Path is repo-relative;
// Blob is the git blob SHA of the manifest content, which is what the graph
// graph keys on.
type Site struct {
	Repo   string
	Path   string
	Blob   string
	Offset int
}

// Provider is a manifest site that declares an identity, and the rule under which
// it declares it.
type Provider struct {
	Site
	Name string
	Rule Rule
}

// Edge is a resolved cross-repo dependency: the manifest at Source declares a
// dependency that the manifest at Target publishes. Source.Offset is the byte
// offset of the declaration; Target.Offset is the byte offset of the identity.
type Edge struct {
	Source      Site
	Target      Site
	Declaration Declaration
	Rule        Rule
	Confidence  float64
	// Contested records that more than one repo claimed Declaration.Name. See
	// ContestedConfidence.
	Contested bool
}

// SourceRepo and TargetRepo name the two repos the edge relates.
func (e Edge) SourceRepo() string { return e.Source.Repo }

// TargetRepo names the repo the dependency resolves to.
func (e Edge) TargetRepo() string { return e.Target.Repo }

// Report accounts for every manifest and declaration the sweep saw, so a small
// edge count is never mistaken for a small corpus. Declarations equals
// Resolved + Internal + Unresolved.
type Report struct {
	// Manifests is the number of distinct manifest sites added.
	Manifests int
	// Vendored is the number of manifest paths skipped as vendored copies.
	Vendored int
	// Failed is the number of manifests that hit a syntax error. Whatever parsed
	// before the error was still added.
	Failed int
	// Identities is the number of provided identities registered.
	Identities int
	// Declarations is the number of dependency declarations parsed.
	Declarations int
	// Resolved is the number of declarations that matched at least one other repo.
	Resolved int
	// Internal is the number of declarations satisfied within the declaring repo
	// itself, which produce no cross-repo edge.
	Internal int
	// Unresolved is the number of declarations no repo in the corpus provides —
	// every third-party dependency, and the normal case. Skipped, not an error.
	Unresolved int
	// Contested is the number of declarations that matched more than one repo.
	Contested int
	// Edges is the number of edges emitted.
	Edges int
}

// identity is a resolvable name: an ecosystem plus its normalized name. Keying on
// the ecosystem is what stops a NuGet id from joining a PyPI distribution.
type identity struct {
	eco  Ecosystem
	name string
}

// Builder collects manifests from a corpus and then resolves their declarations
// against each other. Providers must all be registered before any declaration can
// be resolved, so collection and resolution are separate passes: Add, then Resolve.
type Builder struct {
	providers map[identity][]Provider
	// modules holds Go module providers ordered by descending path length, so the
	// longest declared module path that contains a require path wins.
	modules []identity

	// project reference lookups, in the order Resolve tries them.
	projectsInRepo map[string][]Provider // repo + "\x00" + normalized path
	projectsByPath map[string][]Provider // normalized repo-relative path
	projectsByBase map[string][]Provider // normalized file name
	reposByDir     map[string][]string   // last path component of a repo label

	sources []sourceSite
	report  Report
}

type sourceSite struct {
	site     Site
	manifest Manifest
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder {
	return &Builder{
		providers:      make(map[identity][]Provider),
		projectsInRepo: make(map[string][]Provider),
		projectsByPath: make(map[string][]Provider),
		projectsByBase: make(map[string][]Provider),
		reposByDir:     make(map[string][]string),
	}
}

// AddFile parses one candidate manifest and registers it. A path that is not a
// manifest is ignored; a vendored path is counted and skipped; a manifest that
// fails to parse is counted and its readable prefix registered anyway. AddFile
// never returns an error because none of those outcomes is one.
func (b *Builder) AddFile(repo, blobSHA, relPath string, content []byte) {
	if b == nil || KindOf(relPath) == Unknown {
		return
	}
	if Vendored(relPath) {
		b.report.Vendored++
		return
	}
	m, err := Parse(relPath, content)
	if err != nil {
		b.report.Failed++
	}
	if m.Kind == Unknown {
		return
	}
	b.Add(repo, blobSHA, m)
}

// Add registers one parsed manifest occurrence: the identities it provides and
// the dependencies it declares. The same content may be added under several
// (repo, path) sites — content dedup means one blob can belong to many repos —
// and each site provides and resolves independently.
func (b *Builder) Add(repo, blobSHA string, m Manifest) {
	if b == nil || m.Kind == Unknown {
		return
	}
	b.report.Manifests++
	site := Site{Repo: repo, Path: m.Path, Blob: blobSHA}

	for _, provided := range m.Provides {
		eco := provided.Ecosystem()
		name := Normalize(eco, provided.Name)
		if eco == EcosystemUnknown || name == "" {
			continue
		}
		key := identity{eco: eco, name: name}
		provider := Provider{
			Site: Site{Repo: repo, Path: m.Path, Blob: blobSHA, Offset: provided.Offset},
			Name: provided.Name,
			Rule: provided.Rule,
		}
		if _, known := b.providers[key]; !known && eco == GoModule {
			b.modules = append(b.modules, key)
		}
		b.providers[key] = append(b.providers[key], provider)
		b.report.Identities++
	}

	if m.Kind == CSProj {
		b.addProjectFile(site)
	}
	if len(m.Requires) > 0 {
		b.sources = append(b.sources, sourceSite{site: site, manifest: m})
	}
}

// addProjectFile registers a project file under every key a ProjectReference
// might name it by.
func (b *Builder) addProjectFile(site Site) {
	normalized := Normalize(ProjectPath, site.Path)
	provider := Provider{Site: site, Name: site.Path, Rule: RuleProjectPath}
	b.projectsInRepo[repoPathKey(site.Repo, normalized)] = append(b.projectsInRepo[repoPathKey(site.Repo, normalized)], provider)
	b.projectsByPath[normalized] = append(b.projectsByPath[normalized], provider)
	base := strings.ToLower(path.Base(normalized))
	b.projectsByBase[base] = append(b.projectsByBase[base], provider)

	dir := strings.ToLower(path.Base(site.Repo))
	if dir != "" && !contains(b.reposByDir[dir], site.Repo) {
		b.reposByDir[dir] = append(b.reposByDir[dir], site.Repo)
	}
}

// Resolve joins every collected declaration to the repos that provide it and
// returns the cross-repo edges in a deterministic order together with the sweep's
// accounting.
func (b *Builder) Resolve() ([]Edge, Report) {
	if b == nil {
		return nil, Report{}
	}
	// Longest module path first: example.com/a/b must win over example.com/a for
	// a require of example.com/a/b/c.
	sort.Slice(b.modules, func(i, j int) bool {
		if len(b.modules[i].name) != len(b.modules[j].name) {
			return len(b.modules[i].name) > len(b.modules[j].name)
		}
		return b.modules[i].name < b.modules[j].name
	})

	report := b.report
	var edges []Edge
	for _, source := range b.sources {
		for _, declaration := range source.manifest.Requires {
			report.Declarations++
			providers, rule := b.lookup(source.site, declaration)
			if len(providers) == 0 {
				report.Unresolved++
				continue
			}
			byRepo, internal := groupByRepo(providers, source.site.Repo)
			if internal {
				// The declaring repo publishes the name itself: the dependency is
				// satisfied in-repo and is not a cross-repo edge. Believing an
				// identically-named package elsewhere instead would invent one.
				report.Internal++
				continue
			}
			if len(byRepo) == 0 {
				report.Unresolved++
				continue
			}
			report.Resolved++
			contested := len(byRepo) > 1
			if contested {
				report.Contested++
			}
			confidence := ProvenConfidence
			if contested {
				confidence = ContestedConfidence
			}
			for _, repo := range sortedRepos(byRepo) {
				edges = append(edges, Edge{
					Source:      Site{Repo: source.site.Repo, Path: source.site.Path, Blob: source.site.Blob, Offset: declaration.Offset},
					Target:      byRepo[repo].Site,
					Declaration: declaration,
					Rule:        joinRule(rule, byRepo[repo].Rule),
					Confidence:  confidence,
					Contested:   contested,
				})
			}
		}
	}
	sortEdges(edges)
	report.Edges = len(edges)
	return edges, report
}

// lookup returns the providers of one declaration and the rule that found them.
func (b *Builder) lookup(site Site, declaration Declaration) ([]Provider, Rule) {
	eco := declaration.Ecosystem()
	if eco == ProjectPath {
		return b.lookupProject(site, declaration)
	}
	name := Normalize(eco, declaration.Name)
	if name == "" {
		return nil, RuleUnknown
	}
	if providers := b.providers[identity{eco: eco, name: name}]; len(providers) > 0 {
		return providers, RuleUnknown // the provider's own rule describes the join
	}
	if eco != GoModule {
		return nil, RuleUnknown
	}
	// A require of example.com/mod/pkg/sub names a package inside the module
	// example.com/mod. Containment is checked at a path-component boundary, so
	// example.com/modular is not read as being inside example.com/mod.
	for _, module := range b.modules {
		if strings.HasPrefix(name, module.name+"/") {
			return b.providers[module], RuleModulePrefix
		}
	}
	return nil, RuleUnknown
}

// lookupProject resolves an MSBuild ProjectReference path.
//
// The path is written against the author's checkout layout, which the corpus does
// not have to reproduce, so resolution proceeds from the most faithful reading to
// the least: resolve it lexically against the referencing project's directory;
// if it escapes the repo root, try the escaped remainder as another repo's
// directory plus path, then as a repo-relative path anywhere in the corpus; and
// only then fall back to the project file's name.
func (b *Builder) lookupProject(site Site, declaration Declaration) ([]Provider, Rule) {
	rel := Normalize(ProjectPath, declaration.Name)
	if rel == "" {
		return nil, RuleUnknown
	}
	inside, outside := resolveRelative(Normalize(ProjectPath, path.Dir(site.Path)), rel)
	if outside == "" {
		if providers := b.projectsInRepo[repoPathKey(site.Repo, inside)]; len(providers) > 0 {
			return providers, RuleProjectPath
		}
		// Lexically in-repo but absent from the corpus: the project is not
		// indexed. Nothing to resolve, and nothing else can be true of it.
		return nil, RuleUnknown
	}

	first, rest, _ := strings.Cut(outside, "/")
	if rest != "" {
		var providers []Provider
		for _, repo := range b.reposByDir[first] {
			providers = append(providers, b.projectsInRepo[repoPathKey(repo, rest)]...)
		}
		if len(providers) > 0 {
			return providers, RuleProjectPath
		}
	}
	if providers := b.projectsByPath[outside]; len(providers) > 0 {
		return providers, RuleProjectPath
	}
	if rest != "" {
		if providers := b.projectsByPath[rest]; len(providers) > 0 {
			return providers, RuleProjectPath
		}
	}
	if providers := b.projectsByBase[path.Base(outside)]; len(providers) > 0 {
		return providers, RuleProjectBaseName
	}
	return nil, RuleUnknown
}

// resolveRelative applies rel to dir, both already normalized to forward slashes.
// It returns the repo-relative result when rel stays inside the repo, or the
// remainder after the ".." that escaped the repo root — the path as it would read
// from the corpus root.
func resolveRelative(dir, rel string) (inside, outside string) {
	var stack []string
	if dir != "" && dir != "." && dir != "/" {
		stack = strings.Split(dir, "/")
	}
	var beyond []string
	escaped := false
	for _, component := range strings.Split(rel, "/") {
		switch component {
		case "", ".":
		case "..":
			switch {
			case escaped && len(beyond) > 0:
				beyond = beyond[:len(beyond)-1]
			case escaped:
				// Another level above the corpus root: unresolvable, and no
				// further component can bring it back.
			case len(stack) > 0:
				stack = stack[:len(stack)-1]
			default:
				escaped = true
			}
		default:
			if escaped {
				beyond = append(beyond, component)
			} else {
				stack = append(stack, component)
			}
		}
	}
	if escaped {
		return "", strings.Join(beyond, "/")
	}
	return strings.Join(stack, "/"), ""
}

// groupByRepo picks one representative provider per repo and reports whether the
// declaring repo is among the providers. The representative is the lowest path
// then lowest offset, so an edge does not move when the shard order does.
func groupByRepo(providers []Provider, sourceRepo string) (map[string]Provider, bool) {
	out := make(map[string]Provider, len(providers))
	internal := false
	for _, provider := range providers {
		if provider.Repo == sourceRepo {
			internal = true
			continue
		}
		current, seen := out[provider.Repo]
		if !seen || provider.Path < current.Path ||
			(provider.Path == current.Path && provider.Offset < current.Offset) {
			out[provider.Repo] = provider
		}
	}
	if internal {
		return nil, true
	}
	return out, false
}

func sortedRepos(byRepo map[string]Provider) []string {
	out := make([]string, 0, len(byRepo))
	for repo := range byRepo {
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// joinRule prefers the rule the lookup used when it is more specific than the
// provider's own declaration rule.
func joinRule(lookup, provider Rule) Rule {
	if lookup != RuleUnknown {
		return lookup
	}
	return provider
}

// sortEdges orders edges by source repo, source path, declaration offset, then
// target, so a rebuild of an unchanged corpus produces an identical edge list.
func sortEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		left, right := edges[i], edges[j]
		switch {
		case left.Source.Repo != right.Source.Repo:
			return left.Source.Repo < right.Source.Repo
		case left.Source.Path != right.Source.Path:
			return left.Source.Path < right.Source.Path
		case left.Source.Offset != right.Source.Offset:
			return left.Source.Offset < right.Source.Offset
		case left.Target.Repo != right.Target.Repo:
			return left.Target.Repo < right.Target.Repo
		case left.Target.Path != right.Target.Path:
			return left.Target.Path < right.Target.Path
		default:
			return left.Target.Offset < right.Target.Offset
		}
	})
}

func repoPathKey(repo, normalizedPath string) string { return repo + "\x00" + normalizedPath }

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}
