package manifest

import (
	"reflect"
	"strings"
	"testing"
)

// addTo parses content as a manifest and registers it under (repo, relPath),
// failing the test if the fixture itself is malformed.
func addTo(t *testing.T, b *Builder, repo, relPath, blob, content string) {
	t.Helper()
	m, err := Parse(relPath, []byte(content))
	if err != nil {
		t.Fatalf("fixture %s/%s does not parse: %v", repo, relPath, err)
	}
	if m.Kind == Unknown {
		t.Fatalf("fixture %s/%s is not recognized as a manifest", repo, relPath)
	}
	b.Add(repo, blob, m)
}

const coreCSProj = `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <PackageId>Acme.Core</PackageId>
  </PropertyGroup>
</Project>
`

const billingCSProj = `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Acme.Core" Version="2.1.0" />
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>
`

func TestResolveCSProjPackageReferenceReachesProvidingRepo(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
	addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)

	edges, report := b.Resolve()
	if len(edges) != 1 {
		t.Fatalf("edges = %#v, want exactly one", edges)
	}
	edge := edges[0]
	if edge.SourceRepo() != "platform/billing" || edge.TargetRepo() != "platform/core" {
		t.Fatalf("edge = %s -> %s, want platform/billing -> platform/core", edge.SourceRepo(), edge.TargetRepo())
	}
	if edge.Confidence != ProvenConfidence {
		t.Errorf("Confidence = %v, want %v: both ends are declarations", edge.Confidence, ProvenConfidence)
	}
	if edge.Contested {
		t.Error("Contested = true, want false: exactly one repo provides Acme.Core")
	}
	if edge.Rule != RuleDeclaredName {
		t.Errorf("Rule = %s, want DeclaredName", edge.Rule)
	}
	if edge.Declaration.Name != "Acme.Core" {
		t.Errorf("Declaration.Name = %q, want Acme.Core", edge.Declaration.Name)
	}
	if edge.Source.Blob != "billing-blob" || edge.Target.Blob != "core-blob" {
		t.Errorf("edge content identity = %q -> %q", edge.Source.Blob, edge.Target.Blob)
	}
	// Both offsets must land on the evidence, since that is what a reader of the
	// persisted edge is pointed at.
	if got, want := edge.Source.Offset, strings.Index(billingCSProj, "Acme.Core"); got != want {
		t.Errorf("Source.Offset = %d, want %d (the PackageReference)", got, want)
	}
	if got, want := edge.Target.Offset, strings.Index(coreCSProj, "Acme.Core"); got != want {
		t.Errorf("Target.Offset = %d, want %d (the PackageId)", got, want)
	}

	if report.Declarations != 2 || report.Resolved != 1 || report.Unresolved != 1 {
		t.Errorf("report = %+v, want 2 declarations, 1 resolved, 1 unresolved", report)
	}
}

func TestResolvePackageReferenceViaProjectFileNameDefault(t *testing.T) {
	b := NewBuilder()
	// No PackageId and no AssemblyName: MSBuild publishes this project under its
	// file's base name, and a consumer declares it by that name.
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob",
		`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)

	edges, _ := b.Resolve()
	if len(edges) != 1 {
		t.Fatalf("edges = %#v, want one", edges)
	}
	if edges[0].TargetRepo() != "platform/core" {
		t.Errorf("target = %q, want platform/core", edges[0].TargetRepo())
	}
	if edges[0].Rule != RuleProjectFileName {
		t.Errorf("Rule = %s, want ProjectFileName", edges[0].Rule)
	}
	if edges[0].Confidence != ProvenConfidence {
		t.Errorf("Confidence = %v, want %v: the default is documented, not guessed", edges[0].Confidence, ProvenConfidence)
	}
	// The evidence is the file's own name, so the target offset is the file head.
	if edges[0].Target.Offset != 0 {
		t.Errorf("Target.Offset = %d, want 0", edges[0].Target.Offset)
	}
}

func TestResolveSkipsExternalPackagesWithoutError(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)
	addTo(t, b, "platform/billing", "go.mod", "billing-gomod", "module gitlab.example.com/platform/billing\n\nrequire github.com/google/uuid v1.6.0\n")
	addTo(t, b, "platform/billing", "ui/package.json", "billing-pkg", `{"name":"@acme/billing-ui","dependencies":{"react":"18.2.0"}}`)
	addTo(t, b, "platform/billing", "requirements.txt", "billing-reqs", "requests>=2.31\n")

	edges, report := b.Resolve()
	if len(edges) != 0 {
		t.Fatalf("edges = %#v, want none: nothing in the corpus provides these packages", edges)
	}
	if report.Declarations != 5 {
		t.Fatalf("Declarations = %d, want 5", report.Declarations)
	}
	if report.Unresolved != 5 {
		t.Errorf("Unresolved = %d, want 5 — an external dependency is skipped and counted", report.Unresolved)
	}
	if report.Failed != 0 {
		t.Errorf("Failed = %d, want 0: an unresolvable package is not a parse failure", report.Failed)
	}
	if report.Resolved != 0 || report.Edges != 0 {
		t.Errorf("report = %+v, want nothing resolved", report)
	}
}

func TestResolveGoModRequiresProduceEdges(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "go.mod", "core-gomod", "module gitlab.example.com/platform/core\n\ngo 1.26\n")
	addTo(t, b, "platform/audit", "go.mod", "audit-gomod", "module gitlab.example.com/platform/audit\n\ngo 1.26\n")
	source := "module gitlab.example.com/platform/billing\n\ngo 1.26\n\nrequire (\n\tgitlab.example.com/platform/core v1.4.0\n\tgitlab.example.com/platform/audit/lib/events v0.2.0\n\tgithub.com/google/uuid v1.6.0 // indirect\n)\n"
	addTo(t, b, "platform/billing", "go.mod", "billing-gomod", source)

	edges, report := b.Resolve()
	if len(edges) != 2 {
		t.Fatalf("edges = %#v, want two", edges)
	}
	byTarget := map[string]Edge{}
	for _, edge := range edges {
		byTarget[edge.TargetRepo()] = edge
	}
	core, ok := byTarget["platform/core"]
	if !ok {
		t.Fatalf("no edge to platform/core in %#v", edges)
	}
	if core.Rule != RuleDeclaredName || core.Confidence != ProvenConfidence {
		t.Errorf("core edge = %s at %v, want DeclaredName at %v", core.Rule, core.Confidence, ProvenConfidence)
	}
	// A require of a package INSIDE a module resolves to the module that contains
	// it, at a path-component boundary.
	audit, ok := byTarget["platform/audit"]
	if !ok {
		t.Fatalf("no edge to platform/audit in %#v", edges)
	}
	if audit.Rule != RuleModulePrefix {
		t.Errorf("audit edge rule = %s, want ModulePrefix", audit.Rule)
	}
	if audit.Declaration.Name != "gitlab.example.com/platform/audit/lib/events" {
		t.Errorf("audit declaration = %q, want the full require path", audit.Declaration.Name)
	}
	if got, want := audit.Source.Offset, strings.Index(source, "gitlab.example.com/platform/audit/lib/events"); got != want {
		t.Errorf("audit Source.Offset = %d, want %d", got, want)
	}
	if report.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1 (github.com/google/uuid)", report.Unresolved)
	}
}

func TestResolveGoModPrefixDoesNotCrossComponentBoundary(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "go.mod", "core-gomod", "module gitlab.example.com/platform/core\n")
	addTo(t, b, "platform/other", "go.mod", "other-gomod", "module gitlab.example.com/platform/billing\n\nrequire gitlab.example.com/platform/corelib v1.0.0\n")

	edges, report := b.Resolve()
	if len(edges) != 0 {
		t.Fatalf("edges = %#v, want none: corelib is not inside core", edges)
	}
	if report.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1", report.Unresolved)
	}
}

func TestResolveGoModPrefersLongestModulePath(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/mono", "go.mod", "mono-gomod", "module gitlab.example.com/platform\n")
	addTo(t, b, "platform/core", "core/go.mod", "core-gomod", "module gitlab.example.com/platform/core\n")
	addTo(t, b, "platform/billing", "go.mod", "billing-gomod", "module gitlab.example.com/platform/billing\n\nrequire gitlab.example.com/platform/core/pkg/ledger v1.0.0\n")

	edges, _ := b.Resolve()
	if len(edges) != 1 {
		t.Fatalf("edges = %#v, want one", edges)
	}
	if edges[0].TargetRepo() != "platform/core" {
		t.Errorf("target = %q, want platform/core: the longest containing module wins", edges[0].TargetRepo())
	}
}

func TestResolvePackageJSONWorkspaceDependency(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "web/core-ui", "package.json", "core-ui-pkg", `{"name":"@acme/core-ui","version":"2.0.0"}`)
	addTo(t, b, "web/billing-ui", "package.json", "billing-ui-pkg",
		`{"name":"@acme/billing-ui","dependencies":{"@acme/core-ui":"^2.0.0","react":"18.2.0"},"devDependencies":{"@acme/core-ui":"^2.0.0"}}`)

	edges, report := b.Resolve()
	// Both the dependencies and devDependencies entry are declarations, so both
	// resolve; the pair is what the repo-level rollup collapses.
	if len(edges) != 2 {
		t.Fatalf("edges = %#v, want two (dependencies and devDependencies)", edges)
	}
	dev := 0
	for _, edge := range edges {
		if edge.TargetRepo() != "web/core-ui" {
			t.Errorf("target = %q, want web/core-ui", edge.TargetRepo())
		}
		if edge.Declaration.Dev {
			dev++
		}
	}
	if dev != 1 {
		t.Errorf("%d dev edges, want 1", dev)
	}
	if report.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1 (react)", report.Unresolved)
	}
	if graph := NewGraph(edges); !reflect.DeepEqual(graph.DependsOn("web/billing-ui"), []string{"web/core-ui"}) {
		t.Errorf("DependsOn = %v, want one repo-level dependency", graph.DependsOn("web/billing-ui"))
	}
}

func TestResolvePythonRequirementReachesProvidingRepo(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core-py", "pyproject.toml", "core-py", "[project]\nname = \"acme-platform-core\"\n")
	addTo(t, b, "platform/etl", "requirements.txt", "etl-reqs", "Acme_Platform.Core==2.3.1\nrequests>=2.31\n")

	edges, report := b.Resolve()
	if len(edges) != 1 {
		t.Fatalf("edges = %#v, want one", edges)
	}
	// PEP 503 normalization is what joins Acme_Platform.Core to acme-platform-core.
	if edges[0].TargetRepo() != "platform/core-py" {
		t.Errorf("target = %q, want platform/core-py", edges[0].TargetRepo())
	}
	if report.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1 (requests)", report.Unresolved)
	}
}

func TestResolveProjectReferenceAcrossRepos(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
	source := `<Project><ItemGroup>
	<ProjectReference Include="..\..\..\core\src\Acme.Core\Acme.Core.csproj" />
</ItemGroup></Project>`
	addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", source)

	edges, _ := b.Resolve()
	if len(edges) != 1 {
		t.Fatalf("edges = %#v, want one", edges)
	}
	edge := edges[0]
	if edge.TargetRepo() != "platform/core" || edge.Target.Path != "src/Acme.Core/Acme.Core.csproj" {
		t.Fatalf("target = %s/%s, want platform/core/src/Acme.Core/Acme.Core.csproj", edge.TargetRepo(), edge.Target.Path)
	}
	if edge.Rule != RuleProjectPath {
		t.Errorf("Rule = %s, want ProjectPath", edge.Rule)
	}
	if edge.Declaration.Ref != ProjectRef {
		t.Errorf("Ref = %s, want ProjectRef", edge.Declaration.Ref)
	}
	if edge.Confidence != ProvenConfidence {
		t.Errorf("Confidence = %v, want %v", edge.Confidence, ProvenConfidence)
	}
}

func TestResolveProjectReferenceFallsBackToFileName(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "Acme.Core.csproj", "core-blob", coreCSProj)
	// A layout the corpus does not reproduce: the relative walk cannot land on
	// the project, but its file name is unambiguous.
	source := `<Project><ItemGroup><ProjectReference Include="..\..\Shared\Libraries\Acme.Core.csproj" /></ItemGroup></Project>`
	addTo(t, b, "platform/billing", "Billing.csproj", "billing-blob", source)

	edges, _ := b.Resolve()
	if len(edges) != 1 {
		t.Fatalf("edges = %#v, want one", edges)
	}
	if edges[0].Rule != RuleProjectBaseName {
		t.Errorf("Rule = %s, want ProjectBaseName", edges[0].Rule)
	}
}

func TestResolveIntraRepoProjectReferenceIsNotACrossRepoEdge(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
	source := `<Project><ItemGroup><ProjectReference Include="..\Acme.Core\Acme.Core.csproj" /></ItemGroup></Project>`
	addTo(t, b, "platform/core", "src/Acme.Api/Acme.Api.csproj", "api-blob", source)

	edges, report := b.Resolve()
	if len(edges) != 0 {
		t.Fatalf("edges = %#v, want none: the reference stays inside its own repo", edges)
	}
	if report.Internal != 1 {
		t.Errorf("Internal = %d, want 1", report.Internal)
	}
	if report.Unresolved != 0 {
		t.Errorf("Unresolved = %d, want 0: the project was found, just in the same repo", report.Unresolved)
	}
}

func TestResolveSelfProvidedPackageIsInternal(t *testing.T) {
	b := NewBuilder()
	// One repo holds both the provider and the consumer of Acme.Core.
	addTo(t, b, "platform/mono", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
	addTo(t, b, "platform/mono", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)
	// Another repo happens to publish the same id; believing it would invent a
	// dependency the manifest does not declare.
	addTo(t, b, "fork/core", "Acme.Core.csproj", "fork-blob", coreCSProj)

	edges, report := b.Resolve()
	if len(edges) != 0 {
		t.Fatalf("edges = %#v, want none: the declaring repo publishes Acme.Core itself", edges)
	}
	if report.Internal != 1 {
		t.Errorf("Internal = %d, want 1", report.Internal)
	}
}

func TestResolveContestedIdentityKeepsEveryClaimant(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
	addTo(t, b, "fork/core", "Acme.Core.csproj", "fork-blob", coreCSProj)
	addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)

	edges, report := b.Resolve()
	if len(edges) != 2 {
		t.Fatalf("edges = %#v, want one per claimant", edges)
	}
	for _, edge := range edges {
		if !edge.Contested {
			t.Errorf("edge to %s: Contested = false, want true", edge.TargetRepo())
		}
		if edge.Confidence != ContestedConfidence {
			t.Errorf("edge to %s: Confidence = %v, want %v", edge.TargetRepo(), edge.Confidence, ContestedConfidence)
		}
	}
	if report.Contested != 1 || report.Resolved != 1 || report.Edges != 2 {
		t.Errorf("report = %+v, want 1 contested declaration resolved to 2 edges", report)
	}
}

func TestResolveIsDeterministic(t *testing.T) {
	build := func() ([]Edge, Report) {
		b := NewBuilder()
		addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
		addTo(t, b, "platform/audit", "go.mod", "audit-gomod", "module gitlab.example.com/platform/audit\n")
		addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)
		addTo(t, b, "platform/billing", "go.mod", "billing-gomod", "module gitlab.example.com/platform/billing\n\nrequire gitlab.example.com/platform/audit v0.2.0\n")
		return b.Resolve()
	}
	first, firstReport := build()
	second, secondReport := build()
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstReport, secondReport) {
		t.Fatal("Resolve is not deterministic across identical corpora")
	}
	if len(first) != 2 {
		t.Fatalf("edges = %#v, want two", first)
	}
}

func TestAddFileCountsVendoredAndFailedManifests(t *testing.T) {
	b := NewBuilder()
	b.AddFile("web/ui", "vendored-blob", "node_modules/react/package.json", []byte(`{"name":"react"}`))
	b.AddFile("web/ui", "broken-blob", "src/Broken/Broken.csproj",
		[]byte(`<Project><ItemGroup><PackageReference Include="Acme.Core" Version="1.0" /><PackageReference Include="Acme.Truncated"`))
	b.AddFile("web/ui", "ignored-blob", "src/main.go", []byte("package main\n"))
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)

	edges, report := b.Resolve()
	if report.Vendored != 1 {
		t.Errorf("Vendored = %d, want 1", report.Vendored)
	}
	if report.Failed != 1 {
		t.Errorf("Failed = %d, want 1", report.Failed)
	}
	// A vendored react must not be registered as a provider of "react", and the
	// truncated project file's readable prefix must still resolve.
	if len(edges) != 1 || edges[0].TargetRepo() != "platform/core" {
		t.Fatalf("edges = %#v, want the one declaration that parsed", edges)
	}
	if report.Manifests != 2 {
		t.Errorf("Manifests = %d, want 2 (the vendored and non-manifest paths are not manifests)", report.Manifests)
	}
}

func TestGraphRollup(t *testing.T) {
	b := NewBuilder()
	addTo(t, b, "platform/core", "src/Acme.Core/Acme.Core.csproj", "core-blob", coreCSProj)
	addTo(t, b, "platform/billing", "src/Billing/Billing.csproj", "billing-blob", billingCSProj)
	addTo(t, b, "platform/api", "src/Api/Api.csproj", "api-blob", billingCSProj)

	edges, _ := b.Resolve()
	graph := NewGraph(edges)
	if got := graph.DependsOn("platform/billing"); !reflect.DeepEqual(got, []string{"platform/core"}) {
		t.Errorf("DependsOn(billing) = %v, want [platform/core]", got)
	}
	if got := graph.Dependents("platform/core"); !reflect.DeepEqual(got, []string{"platform/api", "platform/billing"}) {
		t.Errorf("Dependents(core) = %v, want both consumers sorted", got)
	}
	if got := graph.EdgesBetween("platform/billing", "platform/core"); len(got) != 1 {
		t.Errorf("EdgesBetween = %#v, want one justifying declaration", got)
	}
	if got := graph.Repos(); !reflect.DeepEqual(got, []string{"platform/api", "platform/billing", "platform/core"}) {
		t.Errorf("Repos = %v", got)
	}
	if graph.NumEdges() != 2 {
		t.Errorf("NumEdges = %d, want 2", graph.NumEdges())
	}
	if got := graph.DependsOn("platform/core"); len(got) != 0 {
		t.Errorf("DependsOn(core) = %v, want none", got)
	}
}

func TestResolveEmptyBuilder(t *testing.T) {
	edges, report := NewBuilder().Resolve()
	if len(edges) != 0 || report != (Report{}) {
		t.Fatalf("edges = %#v, report = %+v, want both empty", edges, report)
	}
	if graph := NewGraph(edges); graph.NumEdges() != 0 || len(graph.Repos()) != 0 {
		t.Fatal("an empty edge set produced a non-empty graph")
	}
}
