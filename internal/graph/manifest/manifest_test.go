package manifest

import (
	"strings"
	"testing"
)

func TestKindOf(t *testing.T) {
	for _, tc := range []struct {
		path string
		want Kind
	}{
		{"src/Acme.Core/Acme.Core.csproj", CSProj},
		{"Legacy/Tools.VBPROJ", CSProj},
		{"analytics/Analytics.fsproj", CSProj},
		{"go.mod", GoMod},
		{"service/go.mod", GoMod},
		{"ui/package.json", PackageJSON},
		{"pyproject.toml", PyProject},
		{"requirements.txt", Requirements},
		{"requirements-dev.txt", Requirements},
		{"requirements/base.txt", Unknown},
		{"package-lock.json", Unknown},
		{"go.sum", Unknown},
		{"src/main.go", Unknown},
	} {
		if got := KindOf(tc.path); got != tc.want {
			t.Errorf("KindOf(%q) = %s, want %s", tc.path, got, tc.want)
		}
	}
}

func TestVendoredPathsAreRecognized(t *testing.T) {
	for _, path := range []string{
		"ui/node_modules/react/package.json",
		"vendor/github.com/pkg/errors/go.mod",
		"Third_Party/Lib/Lib.csproj",
	} {
		if !Vendored(path) {
			t.Errorf("Vendored(%q) = false, want true", path)
		}
	}
	// packages/ is where a JavaScript monorepo keeps its OWN workspaces, so it
	// must not be read as vendored.
	for _, path := range []string{"packages/core-ui/package.json", "src/Billing/Billing.csproj"} {
		if Vendored(path) {
			t.Errorf("Vendored(%q) = true, want false", path)
		}
	}
}

func TestNormalizeIsEcosystemSpecific(t *testing.T) {
	for _, tc := range []struct {
		eco  Ecosystem
		in   string
		want string
	}{
		{NuGet, "Acme.Billing", "acme.billing"},
		{NPM, "@Acme/Core-UI", "@acme/core-ui"},
		{GoModule, "gitlab.example.com/Platform/Core", "gitlab.example.com/Platform/Core"},
		{GoModule, "gitlab.example.com/platform/core/", "gitlab.example.com/platform/core"},
		{PyPI, "Acme_Platform.Core", "acme-platform-core"},
		{PyPI, "acme--platform__core", "acme-platform-core"},
		{ProjectPath, `..\Core\Acme.Core.csproj`, "../core/acme.core.csproj"},
	} {
		if got := Normalize(tc.eco, tc.in); got != tc.want {
			t.Errorf("Normalize(%s, %q) = %q, want %q", tc.eco, tc.in, got, tc.want)
		}
	}
}

const csprojFixture = `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <AssemblyName>Acme.Billing.Assembly</AssemblyName>
    <PackageId>Acme.Billing.Client</PackageId>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Acme.Core" Version="2.1.0" />
    <PackageReference Include="Newtonsoft.Json">
      <Version>13.0.3</Version>
    </PackageReference>
    <!-- <PackageReference Include="Commented.Out" Version="1.0.0" /> -->
    <PackageReference Update="Acme.Core" Version="2.2.0" />
    <ProjectReference Include="..\..\Core\src\Acme.Core\Acme.Core.csproj" />
  </ItemGroup>
</Project>
`

func TestParseCSProj(t *testing.T) {
	m, err := Parse("src/Billing/Billing.csproj", []byte(csprojFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Kind != CSProj {
		t.Fatalf("Kind = %s, want CSProj", m.Kind)
	}
	// PackageId wins over AssemblyName, which is MSBuild's own precedence.
	if len(m.Provides) != 1 || m.Provides[0].Name != "Acme.Billing.Client" || m.Provides[0].Rule != RuleDeclaredName {
		t.Fatalf("Provides = %#v, want the declared PackageId", m.Provides)
	}
	if got, want := m.Provides[0].Offset, strings.Index(csprojFixture, "Acme.Billing.Client"); got != want {
		t.Errorf("PackageId offset = %d, want %d", got, want)
	}

	want := []Declaration{
		{Kind: CSProj, Ref: PackageRef, Name: "Acme.Core", Version: "2.1.0"},
		{Kind: CSProj, Ref: PackageRef, Name: "Newtonsoft.Json", Version: "13.0.3"},
		{Kind: CSProj, Ref: ProjectRef, Name: `..\..\Core\src\Acme.Core\Acme.Core.csproj`},
	}
	if len(m.Requires) != len(want) {
		t.Fatalf("Requires = %#v, want %d declarations", m.Requires, len(want))
	}
	for i, expected := range want {
		got := m.Requires[i]
		if got.Name != expected.Name || got.Ref != expected.Ref || got.Version != expected.Version {
			t.Errorf("Requires[%d] = %#v, want %#v", i, got, expected)
		}
		if got.Offset < 0 || got.Offset >= len(csprojFixture) ||
			!strings.HasPrefix(csprojFixture[got.Offset:], expected.Name) {
			t.Errorf("Requires[%d].Offset = %d does not point at %q", i, got.Offset, expected.Name)
		}
	}
}

func TestParseCSProjIgnoresCommentedAndUpdatedReferences(t *testing.T) {
	m, err := Parse("a/A.csproj", []byte(csprojFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range m.Requires {
		if declaration.Name == "Commented.Out" {
			t.Error("a commented-out PackageReference was parsed as a dependency")
		}
	}
	// Update= retargets a version for an already-declared package; it must not
	// double-count Acme.Core.
	count := 0
	for _, declaration := range m.Requires {
		if declaration.Name == "Acme.Core" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Acme.Core declared %d times, want 1", count)
	}
}

func TestParseCSProjFallsBackToProjectFileName(t *testing.T) {
	content := []byte("<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>")
	m, err := Parse("src/Acme.Core/Acme.Core.csproj", content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Provides) != 1 {
		t.Fatalf("Provides = %#v, want one identity", m.Provides)
	}
	if m.Provides[0].Name != "Acme.Core" || m.Provides[0].Rule != RuleProjectFileName {
		t.Errorf("Provides[0] = %#v, want Acme.Core by project file name", m.Provides[0])
	}
}

func TestParseCSProjSkipsUnevaluatedPackageID(t *testing.T) {
	content := []byte(`<Project><PropertyGroup><PackageId>$(MSBuildProjectName).Client</PackageId></PropertyGroup></Project>`)
	m, err := Parse("src/Widgets/Widgets.csproj", content)
	if err != nil {
		t.Fatal(err)
	}
	// An unevaluated MSBuild property is not a name any consumer could declare,
	// so the documented file-name default is used instead.
	if len(m.Provides) != 1 || m.Provides[0].Name != "Widgets" || m.Provides[0].Rule != RuleProjectFileName {
		t.Fatalf("Provides = %#v, want the project file name", m.Provides)
	}
}

func TestParseCSProjMalformedKeepsWhatItRead(t *testing.T) {
	content := []byte(`<Project><ItemGroup>
	<PackageReference Include="Acme.Core" Version="1.0.0" />
	<PackageReference Include="Acme.Broken" Version="2.0.0"`)
	m, err := Parse("src/Billing/Billing.csproj", content)
	if err == nil {
		t.Fatal("Parse of a truncated project file returned no error")
	}
	if len(m.Requires) != 1 || m.Requires[0].Name != "Acme.Core" {
		t.Fatalf("Requires = %#v, want the one declaration that parsed", m.Requires)
	}
	if len(m.Provides) != 1 || m.Provides[0].Name != "Billing" {
		t.Fatalf("Provides = %#v, want the project file name", m.Provides)
	}
}

const goModFixture = `module gitlab.example.com/platform/billing

go 1.26

require (
	gitlab.example.com/platform/core v1.4.0
	github.com/google/uuid v1.6.0 // indirect
)

require gitlab.example.com/platform/audit v0.2.0

exclude (
	gitlab.example.com/platform/legacy v0.1.0
)

replace gitlab.example.com/platform/core => ../core
`

func TestParseGoMod(t *testing.T) {
	m, err := Parse("go.mod", []byte(goModFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Provides) != 1 || m.Provides[0].Name != "gitlab.example.com/platform/billing" {
		t.Fatalf("Provides = %#v, want the module path", m.Provides)
	}
	if got, want := m.Provides[0].Offset, strings.Index(goModFixture, "gitlab.example.com/platform/billing"); got != want {
		t.Errorf("module offset = %d, want %d", got, want)
	}

	want := []Declaration{
		{Name: "gitlab.example.com/platform/core", Version: "v1.4.0"},
		{Name: "github.com/google/uuid", Version: "v1.6.0", Indirect: true},
		{Name: "gitlab.example.com/platform/audit", Version: "v0.2.0"},
	}
	if len(m.Requires) != len(want) {
		t.Fatalf("Requires = %#v, want %d", m.Requires, len(want))
	}
	for i, expected := range want {
		got := m.Requires[i]
		if got.Name != expected.Name || got.Version != expected.Version || got.Indirect != expected.Indirect {
			t.Errorf("Requires[%d] = %#v, want %#v", i, got, expected)
		}
		if !strings.HasPrefix(goModFixture[got.Offset:], expected.Name) {
			t.Errorf("Requires[%d].Offset = %d does not point at %q", i, got.Offset, expected.Name)
		}
	}
}

func TestParseGoModIgnoresNonRequireBlocks(t *testing.T) {
	m, err := Parse("go.mod", []byte(goModFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range m.Requires {
		if strings.HasSuffix(declaration.Name, "legacy") {
			t.Error("an exclude block entry was parsed as a requirement")
		}
	}
}

const packageJSONFixture = `{
  "name": "@acme/billing-ui",
  "version": "1.0.0",
  "dependencies": {
    "@acme/core-ui": "^2.0.0",
    "react": "18.2.0"
  },
  "devDependencies": {
    "typescript": "^5.4.0"
  },
  "peerDependencies": {
    "@acme/theme": "^1.0.0"
  },
  "scripts": { "build": "tsc -p ." },
  "workspaces": ["packages/*"]
}
`

func TestParsePackageJSON(t *testing.T) {
	m, err := Parse("ui/package.json", []byte(packageJSONFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Provides) != 1 || m.Provides[0].Name != "@acme/billing-ui" {
		t.Fatalf("Provides = %#v, want the package name", m.Provides)
	}
	if got, want := m.Provides[0].Offset, strings.Index(packageJSONFixture, "@acme/billing-ui"); got != want {
		t.Errorf("name offset = %d, want %d", got, want)
	}

	want := []Declaration{
		{Name: "@acme/core-ui", Version: "^2.0.0"},
		{Name: "react", Version: "18.2.0"},
		{Name: "typescript", Version: "^5.4.0", Dev: true},
	}
	if len(m.Requires) != len(want) {
		t.Fatalf("Requires = %#v, want %d (peerDependencies are out of scope)", m.Requires, len(want))
	}
	for i, expected := range want {
		got := m.Requires[i]
		if got.Name != expected.Name || got.Version != expected.Version || got.Dev != expected.Dev {
			t.Errorf("Requires[%d] = %#v, want %#v", i, got, expected)
		}
		if !strings.HasPrefix(packageJSONFixture[got.Offset:], expected.Name) {
			t.Errorf("Requires[%d].Offset = %d does not point at %q", i, got.Offset, expected.Name)
		}
	}
}

const requirementsFixture = `# internal platform packages
acme_platform_core==2.3.1
requests>=2.31,<3
uvicorn[standard]==0.29.0 ; python_version >= "3.9"
-r other-requirements.txt
--index-url https://pypi.example.com/simple
git+https://gitlab.example.com/platform/tools.git#egg=acme-tools

  # indented comment
`

func TestParseRequirements(t *testing.T) {
	m, err := Parse("requirements.txt", []byte(requirementsFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Provides) != 0 {
		t.Errorf("Provides = %#v, want none: a requirements file declares no identity", m.Provides)
	}
	want := []string{"acme_platform_core", "requests", "uvicorn"}
	if len(m.Requires) != len(want) {
		t.Fatalf("Requires = %#v, want %v", m.Requires, want)
	}
	for i, name := range want {
		got := m.Requires[i]
		if got.Name != name {
			t.Errorf("Requires[%d].Name = %q, want %q", i, got.Name, name)
		}
		if !strings.HasPrefix(requirementsFixture[got.Offset:], name) {
			t.Errorf("Requires[%d].Offset = %d does not point at %q", i, got.Offset, name)
		}
	}
}

func TestParsePyProjectReadsProjectNameOnly(t *testing.T) {
	content := []byte(`[build-system]
requires = ["setuptools"]

[project]
name = "acme-platform-core"
version = "2.3.1"

[[project.authors]]
name = "Platform Team"
`)
	m, err := Parse("pyproject.toml", content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Provides) != 1 || m.Provides[0].Name != "acme-platform-core" {
		t.Fatalf("Provides = %#v, want only the project name", m.Provides)
	}
	if got, want := m.Provides[0].Offset, strings.Index(string(content), "acme-platform-core"); got != want {
		t.Errorf("name offset = %d, want %d", got, want)
	}
	if len(m.Requires) != 0 {
		t.Errorf("Requires = %#v, want none: pyproject is read for its identity only", m.Requires)
	}
}

func TestParsePyProjectPrefersProjectOverPoetry(t *testing.T) {
	content := []byte("[tool.poetry]\nname = \"legacy-name\"\n\n[project]\nname = \"acme-core\"\n")
	m, err := Parse("pyproject.toml", content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Provides) != 1 || m.Provides[0].Name != "acme-core" {
		t.Fatalf("Provides = %#v, want the [project] name", m.Provides)
	}
}

func TestParseUnknownPathIsNotAnError(t *testing.T) {
	m, err := Parse("src/main.go", []byte("package main\n"))
	if err != nil {
		t.Fatalf("Parse of a non-manifest returned an error: %v", err)
	}
	if m.Kind != Unknown || len(m.Provides) != 0 || len(m.Requires) != 0 {
		t.Fatalf("Parse of a non-manifest returned %v", m)
	}
}
