package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"
	"moedex/internal/app/semanticcmd"
)

func newSemanticCommand() *cobra.Command {
	command := &cobra.Command{Use: "semantic", Short: "Stage, compose and inspect validated offline compiler artifacts", Args: cobra.NoArgs}
	dependencies := &cobra.Command{Use: "dependencies", Short: "Package explicitly provisioned offline compiler dependencies", Args: cobra.NoArgs}
	var packages, bundleOutput string
	var dependencyBytes int64
	pack := &cobra.Command{Use: "pack", Short: "Freeze an extracted NuGet cache into a new local bundle without network access", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := semanticcmd.PackDependenciesWithLimit(cmd.Context(), packages, bundleOutput, dependencyBytes)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	pack.Flags().Int64Var(&dependencyBytes, "max-dependency-bytes", 0, "dependency byte budget; 0 uses 1 GiB, maximum 4 GiB")
	pack.Flags().StringVar(&packages, "packages", "", "caller-provisioned extracted NuGet packages directory")
	pack.Flags().StringVar(&bundleOutput, "output", "", "new bundle directory outside packages")
	_ = pack.MarkFlagRequired("packages")
	_ = pack.MarkFlagRequired("output")
	dependencies.AddCommand(pack)
	command.AddCommand(dependencies)
	var captureOpts semanticcmd.CaptureOptions
	captureCommand := &cobra.Command{
		Use: "capture", Short: "Capture a managed or explicitly pinned Git commit in a retained workspace using offline restore",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := semanticcmd.Capture(cmd.Context(), captureOpts)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	captureCommand.Flags().StringVar(&captureOpts.ManagedRoot, "managed-root", "", "managed acquisition root with pinned repository metadata")
	captureCommand.Flags().Int64Var(&captureOpts.MaxWorkerBytes, "max-worker-bytes", 0, "worker JSONL byte budget; 0 uses 64 MiB, maximum 256 MiB; source and artifact budgets stay unchanged")
	captureCommand.Flags().Int64Var(&captureOpts.MaxDependencyBytes, "max-dependency-bytes", 0, "dependency byte budget; 0 uses 1 GiB, maximum 4 GiB")
	captureCommand.Flags().StringVar(&captureOpts.DependencyBundle, "dependency-bundle", "", "explicit frozen dependency bundle; copied into the private cache with cleared feeds")
	captureCommand.Flags().StringVar(&captureOpts.Checkout, "checkout", "", "public Git checkout to project at the explicitly pinned commit; alternative to managed-root")
	captureCommand.Flags().StringVar(&captureOpts.Commit, "commit", "", "required immutable Git commit for checkout capture")
	captureCommand.Flags().StringVar(&captureOpts.Origin, "origin", "", "expected remote HTTPS URL for checkout capture; audit evidence, not ownership verification")
	captureCommand.Flags().Int64Var(&captureOpts.MaxProjectionBytes, "max-projection-bytes", 0, "projection byte limit; 0 uses 256 MiB, maximum 1 GiB")
	captureCommand.Flags().StringVar(&captureOpts.Repo, "repo", "", "repository namespace; checkout mode must match its directory basename")
	captureCommand.Flags().StringVar(&captureOpts.Project, "project", "", "repository-relative C# project path")
	captureCommand.Flags().StringVar(&captureOpts.Framework, "framework", "", "explicit target framework")
	captureCommand.Flags().StringVar(&captureOpts.Configuration, "configuration", "Debug", "compiler configuration")
	captureCommand.Flags().StringVar(&captureOpts.Dotnet, "dotnet", "", "installed dotnet executable")
	captureCommand.Flags().StringVar(&captureOpts.Worker, "worker", "", "built compiler-worker DLL")
	captureCommand.Flags().StringVar(&captureOpts.SDKPath, "sdk-path", "", "installed SDK directory used by MSBuild")
	captureCommand.Flags().StringVar(&captureOpts.Workspace, "workspace", "", "new retained workspace parent; must not already exist")
	captureCommand.Flags().StringVar(&captureOpts.Output, "output", "", "new validated artifact file; must not already exist")
	captureCommand.Flags().BoolVar(&captureOpts.RestoreOffline, "restore-offline", false, "explicitly permit restore with cleared feeds and only SDK or supplied bundle packages")
	captureCommand.Flags().BoolVar(&captureOpts.RestoreStandardEvaluation, "restore-standard-evaluation", false, "opt out of static-graph restore evaluation for this invocation; does not edit source configuration")
	captureCommand.Flags().BoolVar(&captureOpts.RestoreToolCacheMetadata, "restore-tool-cache-metadata", false, "Record bounded generated NuGet tool-cache JSON changes; keep package payloads immutable")
	captureCommand.Flags().DurationVar(&captureOpts.Timeout, "timeout", semanticcmd.DefaultCaptureTimeout, "deadline for the entire capture")
	for _, name := range []string{"repo", "project", "framework", "dotnet", "worker", "sdk-path", "workspace", "output", "restore-offline"} {
		_ = captureCommand.MarkFlagRequired(name)
	}
	var opts semanticcmd.ImportOptions
	importCommand := &cobra.Command{Use: "import", Short: "Verify a worker capture and stage a complete artifact (never replaces files or CURRENT)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		summary, err := semanticcmd.ImportFile(cmd.Context(), opts)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
	}}
	importCommand.Flags().StringVar(&opts.Input, "input", "", "compiler-worker JSONL capture")
	importCommand.Flags().StringVar(&opts.Output, "output", "", "new staged artifact file")
	importCommand.Flags().StringVar(&opts.Root, "root", "", "isolated source projection used by the worker")
	importCommand.Flags().StringVar(&opts.Repo, "repo", "", "exact repository namespace recorded by the worker")
	importCommand.Flags().Int64Var(&opts.ProjectID, "project-id", 0, "known acquisition project ID; omit when unknown")
	importCommand.Flags().StringVar(&opts.Commit, "commit", "", "known immutable source commit; omit for working-tree sources")
	for _, name := range []string{"input", "output", "root", "repo"} {
		_ = importCommand.MarkFlagRequired(name)
	}
	inspectCommand := &cobra.Command{Use: "inspect ARTIFACT", Short: "Validate artifact integrity and summarize its contents", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := semanticcmd.Inspect(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
	}}
	var composeOpts semanticcmd.ComposeOptions
	composeCommand := &cobra.Command{
		Use: "compose", Short: "Compose complete compiler artifacts into a new immutable artifact",
		Long:    "Compose 1..32 complete compiler artifacts with deterministic exact-record deduplication. Repeat --input for each artifact. Conflicting records are rejected; recorded source and build-context identities remain distinct. This stages a new artifact without overwriting files or changing CURRENT. Composition does not infer dependency compatibility or runtime relationships. The JSON summary lists exact snapshot IDs and contributing inputs. Attach the result separately with snapshot-build --semantic-artifact and --semantic-workspaces FILE: the workspace file maps every exact snapshot ID to its retained source projection root.",
		Example: "moedex semantic compose --input debug.json --input release.json --output composed.json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			summary, err := semanticcmd.ComposeFiles(cmd.Context(), composeOpts)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
		},
	}
	composeCommand.Flags().StringArrayVar(&composeOpts.Inputs, "input", nil, "complete artifact path; repeat for each input (maximum 32)")
	composeCommand.Flags().StringVar(&composeOpts.Output, "output", "", "new immutable artifact file; must not already exist")
	composeCommand.Flags().Int64Var(&composeOpts.MaxBytes, "max-bytes", 0, "aggregate input and output byte limit; 0 uses 64 MiB, maximum 64 MiB")
	_ = composeCommand.MarkFlagRequired("input")
	_ = composeCommand.MarkFlagRequired("output")
	command.AddCommand(captureCommand, importCommand, inspectCommand, composeCommand)
	return command
}
