package semanticrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"moedex/internal/semantic"
	"moedex/internal/semanticimport"
)

type Options struct {
	ManagedRoot, Repo, Project, Framework, Configuration string
	Checkout, Commit, Origin                             string
	MaxProjectionBytes                                   int64
	MaxDependencyBytes                                   int64
	DependencyBundle                                     string
	Dotnet, Worker, SDKPath, Workspace, Output           string
	RestoreOffline                                       bool
	RestoreStandardEvaluation                            bool
	Timeout                                              time.Duration
}

type Result struct {
	Artifact                              *semantic.Artifact
	Path, Workspace, Repo, Commit, Origin string
	ProjectID                             int64
	DependencyBundleSHA256                string
	RestoreStandardEvaluation             bool
	DependencyByteLimit                   int64
}

// Capture executes trusted MSBuild code in a private, exact-commit projection.
// Empty NuGet feeds prevent package downloads, but this is not an OS sandbox.
// Successful workspaces stay at their original absolute path for revalidation.
func Capture(ctx context.Context, o Options) (result Result, err error) {
	if !o.RestoreOffline || !cleanRelative(o.Repo) || !cleanRelative(o.Project) || o.Framework == "" || o.Dotnet == "" || o.Worker == "" || o.SDKPath == "" || o.Workspace == "" || o.Output == "" {
		return result, fmt.Errorf("semantic capture: source root, repo, project, framework, tool paths, new workspace, output and explicit offline restore are required")
	}
	if (o.ManagedRoot == "") == (o.Checkout == "") {
		return result, fmt.Errorf("semantic capture: exactly one managed root or Git checkout is required")
	}
	if o.ManagedRoot != "" && (o.Commit != "" || o.Origin != "") {
		return result, fmt.Errorf("semantic capture: managed acquisition supplies commit and origin; explicit Git identity is not allowed")
	}
	if o.Checkout != "" && (!objectID(o.Commit) || o.Origin == "") {
		return result, fmt.Errorf("semantic capture: Git checkout requires full commit and origin")
	}
	if o.MaxProjectionBytes < 0 || o.MaxProjectionBytes > 1<<30 {
		return result, fmt.Errorf("semantic capture: projection byte limit must be between 1 and 1073741824, or zero for default")
	}
	if o.Timeout <= 0 {
		return result, fmt.Errorf("semantic capture: timeout must be positive")
	}
	dependencyLimit, e := dependencyByteLimit(o.MaxDependencyBytes)
	if e != nil {
		return result, e
	}
	if o.Configuration == "" {
		o.Configuration = "Debug"
	}
	// MSBuild property syntax uses separators even when shell quoting is absent.
	for _, s := range []string{o.Framework, o.Configuration} {
		if strings.ContainsAny(s, ";,\r\n\x00") || strings.HasPrefix(s, "-") {
			return result, fmt.Errorf("semantic capture: invalid build selection")
		}
	}
	for _, p := range []*string{&o.ManagedRoot, &o.Checkout, &o.Dotnet, &o.Worker, &o.SDKPath, &o.Workspace, &o.Output, &o.DependencyBundle} {
		if *p == "" {
			continue
		}
		*p, err = filepath.Abs(*p)
		if err != nil {
			return result, err
		}
	}
	if strings.ContainsAny(o.Workspace, ";,\r\n\x00") {
		return result, fmt.Errorf("semantic capture: workspace contains MSBuild property separators")
	}
	for _, p := range []string{o.Output, o.Workspace} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			return result, fmt.Errorf("semantic capture: destination must be new: %s", p)
		}
	}
	sourceRoot := o.ManagedRoot
	if sourceRoot == "" {
		sourceRoot = o.Checkout
	}
	managed, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return result, err
	}
	if o.Checkout != "" && o.Repo != filepath.Base(managed) {
		return result, fmt.Errorf("semantic capture: public repo must match canonical checkout directory basename for unmanaged attachment")
	}
	var bundleRoot string
	if o.DependencyBundle != "" {
		bundleRoot, err = filepath.EvalSymlinks(o.DependencyBundle)
		if err != nil {
			return result, err
		}
	}
	for _, p := range []string{o.Output, o.Workspace} {
		parent, e := filepath.EvalSymlinks(filepath.Dir(p))
		if e != nil {
			return result, e
		}
		rel, e := filepath.Rel(managed, filepath.Join(parent, filepath.Base(p)))
		if e != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return result, fmt.Errorf("semantic capture: destinations must be outside managed corpus or Git checkout")
		}
		if bundleRoot != "" {
			rel, e = filepath.Rel(bundleRoot, filepath.Join(parent, filepath.Base(p)))
			if e != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return result, fmt.Errorf("semantic capture: destinations must be outside dependency bundle")
			}
		}
	}
	for _, p := range []string{o.Dotnet, o.Worker} {
		info, e := os.Stat(p)
		if e != nil {
			return result, e
		}
		if !info.Mode().IsRegular() {
			return result, fmt.Errorf("semantic capture: tool is not a file: %s", p)
		}
	}
	if info, e := os.Stat(o.SDKPath); e != nil || !info.IsDir() {
		return result, fmt.Errorf("semantic capture: SDK directory unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = os.Mkdir(o.Workspace, 0700); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(o.Workspace)
		}
	}()
	pinnedDotnet, e := stageToolchain(ctx, o.Dotnet, o.SDKPath, filepath.Join(o.Workspace, "toolchain"))
	if e != nil {
		return result, e
	}
	var p *Projection
	var bundle *DependencyBundle
	var bundleSHA string
	var manifestPath string
	if o.DependencyBundle != "" {
		manifestPath = filepath.Join(o.DependencyBundle, "manifest.json")
		manifest, raw, e := readCaptureDependencyManifest(manifestPath)
		if e != nil {
			return result, e
		}
		bundleSHA = sha(raw)
		bundle, err = StageDependencyBundle(ctx, DependencyBundleOptions{Directory: filepath.Join(o.DependencyBundle, "packages"), Files: manifest.Files, MaxBytes: dependencyLimit}, filepath.Join(o.Workspace, "packages"))
		if err != nil {
			return result, err
		}
		if err = os.WriteFile(filepath.Join(o.Workspace, "dependency-manifest.json"), raw, 0600); err != nil {
			return result, err
		}
	}
	verifyBundle := func() error {
		if bundle == nil {
			return nil
		}
		if e := bundle.Verify(ctx); e != nil {
			return e
		}
		for _, path := range []string{manifestPath, filepath.Join(o.Workspace, "dependency-manifest.json")} {
			_, raw, e := readCaptureDependencyManifest(path)
			if e != nil {
				return e
			}
			if sha(raw) != bundleSHA {
				return fmt.Errorf("semantic capture: dependency manifest changed")
			}
		}
		return nil
	}
	if o.ManagedRoot != "" {
		p, err = CreateProjection(ctx, ProjectionOptions{ManagedRoot: o.ManagedRoot, Repo: o.Repo, TempParent: o.Workspace, MaxBytes: o.MaxProjectionBytes})
	} else {
		p, err = CreateGitProjection(ctx, GitProjectionOptions{Checkout: o.Checkout, Repo: o.Repo, Commit: o.Commit, Origin: o.Origin, TempParent: o.Workspace, MaxBytes: o.MaxProjectionBytes})
	}
	if err != nil {
		return result, err
	}
	if _, ok := p.inventory[o.Project]; !ok {
		return result, fmt.Errorf("semantic capture: project is not a committed file")
	}
	for _, name := range []string{"home", "tmp", "packages", "empty-feed"} {
		if name == "packages" && bundle != nil {
			continue
		}
		if err = os.Mkdir(filepath.Join(o.Workspace, name), 0700); err != nil {
			return result, err
		}
	}
	config := filepath.Join(o.Workspace, "NuGet.Config")
	if err = os.WriteFile(config, []byte("<configuration><packageSources><clear /></packageSources><fallbackPackageFolders><clear /></fallbackPackageFolders></configuration>"), 0600); err != nil {
		return result, err
	}
	env := []string{
		"PATH=" + filepath.Dir(pinnedDotnet) + ":/usr/bin:/bin", "HOME=" + filepath.Join(o.Workspace, "home"),
		"TMPDIR=" + filepath.Join(o.Workspace, "tmp"), "DOTNET_CLI_HOME=" + filepath.Join(o.Workspace, "home"),
		"DOTNET_ROOT=" + filepath.Dir(pinnedDotnet), "DOTNET_NOLOGO=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1", "MSBUILDDISABLENODEREUSE=1",
		"NUGET_PACKAGES=" + filepath.Join(o.Workspace, "packages"),
	}
	// Invoke the selected SDK's MSBuild directly; `dotnet restore` would
	// independently select an SDK through global.json or ambient installed SDKs.
	restore := []string{filepath.Join(o.SDKPath, "MSBuild.dll"), "-target:Restore", filepath.Join(p.Root, filepath.FromSlash(o.Project)), "-nologo", "-p:RestoreConfigFile=" + config, "-p:RestorePackagesPath=" + filepath.Join(o.Workspace, "packages"), "-p:RestoreSources=" + filepath.Join(o.Workspace, "empty-feed"), "-p:RestoreDisableParallel=true", "-p:NuGetAudit=false", "-p:TargetFramework=" + o.Framework, "-p:Configuration=" + o.Configuration}
	env = append(env, "MSBuildSDKsPath="+filepath.Join(o.SDKPath, "Sdks"))
	if o.RestoreStandardEvaluation {
		restore = append(restore, "-p:RestoreUseStaticGraphEvaluation=false")
	}
	if err = verifyBundle(); err != nil {
		return result, err
	}
	restored, err := RunProcess(ctx, ProcessSpec{Executable: pinnedDotnet, Args: restore, Dir: p.Root, Env: env, StdoutLimit: 1 << 20, StderrLimit: 64 << 10})
	if err != nil {
		return result, captureProcessError("offline restore", err, append(restored.Stdout, restored.Stderr...))
	}
	if err = p.Verify(ctx); err != nil {
		return result, err
	}
	if err = verifyBundle(); err != nil {
		return result, err
	}
	args := []string{o.Worker, "--repo", p.Repo, "--root", p.Root, "--project", o.Project, "--framework", o.Framework, "--configuration", o.Configuration, "--sdk-path", o.SDKPath}
	raw, err := RunProcess(ctx, ProcessSpec{Executable: pinnedDotnet, Args: args, Dir: p.Root, Env: env, StdoutLimit: 64 << 20, StderrLimit: 64 << 10})
	if err != nil {
		return result, captureProcessError("worker", err, workerFailureDiagnostics(raw.Stderr, raw.Stdout))
	}
	if err = p.Verify(ctx); err != nil {
		return result, err
	}
	if err = verifyBundle(); err != nil {
		return result, err
	}
	a, err := semanticimport.Import(ctx, bytes.NewReader(raw.Stdout), semanticimport.Options{Repo: p.Repo, Root: p.Root, ProjectID: p.ProjectID, Commit: p.Commit, Origin: p.Origin, DependencyBundleSHA256: bundleSHA})
	if err != nil {
		return result, err
	}
	if !a.Complete() {
		return result, fmt.Errorf("semantic capture: compiler capture incomplete")
	}
	if err = p.Verify(ctx); err != nil {
		return result, err
	}
	if err = verifyBundle(); err != nil {
		return result, err
	}
	if err = semantic.Write(o.Output, a); err != nil {
		return result, err
	}
	return Result{Artifact: a, Path: o.Output, Workspace: p.Root, Repo: p.Repo, Commit: p.Commit, Origin: p.Origin, ProjectID: p.ProjectID, DependencyBundleSHA256: bundleSHA, RestoreStandardEvaluation: o.RestoreStandardEvaluation, DependencyByteLimit: dependencyLimit}, nil
}

// Worker stdout contains source records, so failures report only its diagnostic
// stream. Retain a short tail for actionable restore/build errors.
func captureProcessError(phase string, cause error, diagnostic []byte) error {
	const maxDiagnostic = 4096
	if len(diagnostic) > maxDiagnostic {
		diagnostic = diagnostic[len(diagnostic)-maxDiagnostic:]
	}
	message := strings.TrimSpace(strings.ToValidUTF8(string(diagnostic), "�"))
	if message == "" {
		return fmt.Errorf("semantic capture: %s: %w", phase, cause)
	}
	return fmt.Errorf("semantic capture: %s: %w: %s", phase, cause, message)
}

// Only explicit error diagnostics are copied from stdout. Declarations, source
// text and capture manifests never become error messages.
func workerFailureDiagnostics(stderr, stdout []byte) []byte {
	out := append([]byte(nil), stderr...)
	for len(stdout) > 0 {
		line, rest, _ := bytes.Cut(stdout, []byte("\n"))
		stdout = rest
		if len(line) > 64<<10 {
			continue
		}
		var row struct {
			Type     string `json:"record_type"`
			Severity string `json:"severity"`
			Project  string `json:"project"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil || row.Type != "diagnostic" || row.Severity != "error" {
			continue
		}
		diagnostic, _ := json.Marshal(map[string]string{"project": row.Project, "code": row.Code, "message": row.Message})
		if len(diagnostic) > 2048 {
			diagnostic = diagnostic[:2048]
		}
		out = append(out, '\n')
		out = append(out, diagnostic...)
		if len(out) >= 4096 {
			break
		}
	}
	return out
}
