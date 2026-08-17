package server

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"moedex/internal/diskstore"
	"moedex/internal/graph"
	"moedex/internal/graph/diskgraph"
	"moedex/internal/graph/manifest"
	"moedex/internal/index"
)

const (
	coreProject = `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <PackageId>Acme.Core</PackageId>
  </PropertyGroup>
</Project>
`
	billingProject = `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Acme.Core" Version="2.1.0" />
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>
`
	coreGoMod    = "module gitlab.example.com/platform/core\n\ngo 1.26\n"
	billingGoMod = "module gitlab.example.com/platform/billing\n\ngo 1.26\n\nrequire (\n\tgitlab.example.com/platform/core v1.4.0\n\tgithub.com/google/uuid v1.6.0\n)\n"
)

func writeManifestShards(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	core := index.New()
	core.AddFile("platform/core", "src/Acme.Core/Acme.Core.csproj", "/core/src/Acme.Core/Acme.Core.csproj", "core-csproj-sha", []byte(coreProject))
	core.AddFile("platform/core", "go.mod", "/core/go.mod", "core-gomod-sha", []byte(coreGoMod))
	if err := diskstore.Save(core, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}

	billing := index.New()
	billing.AddFile("platform/billing", "src/Billing/Billing.csproj", "/billing/src/Billing/Billing.csproj", "billing-csproj-sha", []byte(billingProject))
	billing.AddFile("platform/billing", "go.mod", "/billing/go.mod", "billing-gomod-sha", []byte(billingGoMod))
	if err := diskstore.Save(billing, filepath.Join(dir, "shard-0001.idx")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildGraphPersistsProvenPackageDependency(t *testing.T) {
	dir := writeManifestShards(t)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatalf("open graph: %v", err)
	}
	defer g.Close()

	edges := g.Load("billing-csproj-sha", 0)
	if len(edges) != 1 {
		t.Fatalf("Billing.csproj adjacency = %#v, want exactly one DEPENDS_ON edge", edges)
	}
	got := edges[0]
	if got.Type != diskgraph.EdgeDependsOn || got.TargetBlob != "core-csproj-sha" {
		t.Fatalf("edge = %#v, want a DEPENDS_ON to core-csproj-sha", got)
	}
	if got.Confidence != graph.Proven {
		t.Errorf("Confidence = %v, want Proven", got.Confidence)
	}
	if got.Evidence.BlobSHA != "billing-csproj-sha" {
		t.Errorf("Evidence.BlobSHA = %q, want billing-csproj-sha", got.Evidence.BlobSHA)
	}
	wantOffset := uint64(strings.Index(billingProject, "Acme.Core"))
	if got.Evidence.ByteOffset != wantOffset {
		t.Errorf("Evidence.ByteOffset = %d, want %d", got.Evidence.ByteOffset, wantOffset)
	}
	if got.Evidence.ByteLength != uint64(len("Acme.Core")) {
		t.Errorf("Evidence.ByteLength = %d, want %d", got.Evidence.ByteLength, len("Acme.Core"))
	}
}

func TestBuildGraphPersistsGoModuleDependency(t *testing.T) {
	dir := writeManifestShards(t)
	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	edges := g.Load("billing-gomod-sha", 0)
	if len(edges) != 1 {
		t.Fatalf("go.mod adjacency = %#v, want one edge: the uuid require is not in the corpus", edges)
	}
	got := edges[0]
	if got.Type != diskgraph.EdgeDependsOn || got.TargetBlob != "core-gomod-sha" {
		t.Fatalf("edge = %#v, want a DEPENDS_ON to core-gomod-sha", got)
	}
	if got.Confidence != graph.Proven {
		t.Errorf("Confidence = %v, want Proven", got.Confidence)
	}
	wantModOffset := uint64(strings.Index(billingGoMod, "gitlab.example.com/platform/core"))
	if got.Evidence.ByteOffset != wantModOffset {
		t.Errorf("Evidence.ByteOffset = %d, want %d", got.Evidence.ByteOffset, wantModOffset)
	}
}

func TestManifestGraphResolvesToTargetRepo(t *testing.T) {
	dir := writeManifestShards(t)
	graph, report, err := ManifestGraph(dir)
	if err != nil {
		t.Fatalf("ManifestGraph: %v", err)
	}
	if got := graph.DependsOn("platform/billing"); !reflect.DeepEqual(got, []string{"platform/core"}) {
		t.Fatalf("DependsOn(platform/billing) = %v, want [platform/core]", got)
	}
	if got := graph.Dependents("platform/core"); !reflect.DeepEqual(got, []string{"platform/billing"}) {
		t.Fatalf("Dependents(platform/core) = %v, want [platform/billing]", got)
	}
	if graph.NumEdges() != 2 {
		t.Fatalf("NumEdges = %d, want 2 (the NuGet package and the Go module)", graph.NumEdges())
	}
	if report.Declarations != 4 || report.Resolved != 2 || report.Unresolved != 2 {
		t.Errorf("report = %+v, want 4 declarations, 2 resolved, 2 unresolved", report)
	}
}

func TestManifestEdgesSkipsExternalPackagesWithoutError(t *testing.T) {
	dir := t.TempDir()
	only := index.New()
	only.AddFile("platform/billing", "src/Billing/Billing.csproj", "/billing/Billing.csproj", "billing-csproj-sha", []byte(billingProject))
	only.AddFile("platform/billing", "requirements.txt", "/billing/requirements.txt", "billing-reqs-sha", []byte("requests>=2.31\n"))
	if err := diskstore.Save(only, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}

	edges, report, err := ManifestEdges(dir)
	if err != nil {
		t.Fatalf("ManifestEdges: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges = %#v, want none", edges)
	}
	if report.Declarations != 3 || report.Unresolved != 3 {
		t.Errorf("report = %+v, want all 3 declarations unresolved", report)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph with only external dependencies: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if got := g.Load("billing-csproj-sha", 0); len(got) != 0 {
		t.Fatalf("adjacency = %#v, want none", got)
	}
}

func TestManifestEdgesFoldsDedupedContentToOneRecord(t *testing.T) {
	dir := t.TempDir()
	core := index.New()
	core.AddFile("platform/core", "src/Acme.Core/Acme.Core.csproj", "/core/Acme.Core.csproj", "core-csproj-sha", []byte(coreProject))
	if err := diskstore.Save(core, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	consumers := index.New()
	consumers.AddFile("platform/billing", "src/Billing/Billing.csproj", "/billing/Billing.csproj", "shared-csproj-sha", []byte(billingProject))
	consumers.AddFile("platform/invoicing", "src/Billing/Billing.csproj", "/invoicing/Billing.csproj", "shared-csproj-sha", []byte(billingProject))
	if err := diskstore.Save(consumers, filepath.Join(dir, "shard-0001.idx")); err != nil {
		t.Fatal(err)
	}

	graph, _, err := ManifestGraph(dir)
	if err != nil {
		t.Fatalf("ManifestGraph: %v", err)
	}
	if got := graph.Dependents("platform/core"); !reflect.DeepEqual(got, []string{"platform/billing", "platform/invoicing"}) {
		t.Fatalf("Dependents(platform/core) = %v, want both consumers", got)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatal(err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if got := g.Load("shared-csproj-sha", 0); len(got) != 1 {
		t.Fatalf("adjacency = %#v, want one record for the shared content", got)
	}
}

func TestManifestEdgesFromDedupedShards(t *testing.T) {
	dir := t.TempDir()
	coreSHA := diskstore.GitBlobSHA1([]byte(coreProject))
	billingSHA := diskstore.GitBlobSHA1([]byte(billingProject))

	core := index.New()
	core.AddFile("platform/core", "src/Acme.Core/Acme.Core.csproj", "/core/Acme.Core.csproj", coreSHA, []byte(coreProject))
	billing := index.New()
	billing.AddFile("platform/billing", "src/Billing/Billing.csproj", "/billing/Billing.csproj", billingSHA, []byte(billingProject))

	writer, err := diskstore.NewContentStoreWriter()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := diskstore.SaveDeduped(core, filepath.Join(dir, "shard-0000.idx"), writer); err != nil {
		t.Fatal(err)
	}
	if err := diskstore.SaveDeduped(billing, filepath.Join(dir, "shard-0001.idx"), writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(filepath.Join(dir, diskstore.ContentStoreName)); err != nil {
		t.Fatal(err)
	}

	path, _, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph deduped: %v", err)
	}
	g, err := diskgraph.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	dedupedEdges := g.Load(billingSHA, 0)
	if len(dedupedEdges) != 1 || dedupedEdges[0].Type != diskgraph.EdgeDependsOn || dedupedEdges[0].TargetBlob != coreSHA {
		t.Fatalf("deduped adjacency = %#v", dedupedEdges)
	}
	if dedupedEdges[0].Confidence != graph.Proven {
		t.Errorf("Confidence = %v, want Proven", dedupedEdges[0].Confidence)
	}
}

func TestManifestEdgesReportsAnEmptyShardDir(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := ManifestEdges(dir); err == nil {
		t.Fatal("ManifestEdges on an empty shard dir returned no error")
	}
	if _, _, err := BuildGraph(dir); err == nil {
		t.Fatal("BuildGraph on an empty shard dir returned no error")
	}
}

func TestManifestEdgesWithoutAnyManifest(t *testing.T) {
	dir := t.TempDir()
	only := index.New()
	only.AddFile("platform/core", "src/main.go", "/core/src/main.go", "main-sha", []byte("package main\n\nfunc main() {}\n"))
	if err := diskstore.Save(only, filepath.Join(dir, "shard-0000.idx")); err != nil {
		t.Fatal(err)
	}
	edges, report, err := ManifestEdges(dir)
	if err != nil {
		t.Fatalf("ManifestEdges: %v", err)
	}
	if len(edges) != 0 || report != (manifest.Report{}) {
		t.Fatalf("edges = %#v, report = %+v, want both empty", edges, report)
	}
}
