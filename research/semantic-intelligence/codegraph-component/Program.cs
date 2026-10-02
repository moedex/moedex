using System.Diagnostics;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Security.Cryptography;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.MSBuild;
using Microsoft.Extensions.Logging;
using TC.CodeGraphApi.Extractors.CSharp;
using TC.CodeGraphApi.Models;
using TC.CodeGraphApi.Services.Analyzers;
using TC.CodeGraphApi.Services.Extractors;
using TC.CodeGraphApi.Services.Pipeline;

// This host invokes unchanged extraction components. It deliberately does not
// reproduce the database, private pipeline call resolver, query engine, or MCP.
if (args.Length != 4) throw new ArgumentException("root solution output repo-name required");
var root = Path.GetFullPath(args[0]);
var solution = Path.GetFullPath(args[1]);
var output = Path.GetFullPath(args[2]);
Directory.CreateDirectory(output);
using var cancellation = new CancellationTokenSource(TimeSpan.FromMinutes(8));
using var loggers = LoggerFactory.Create(b => b.AddSimpleConsole(o => {
    o.SingleLine = true;
    o.TimestampFormat = "HH:mm:ss ";
}).AddFilter(level => level >= LogLevel.Information));
var watch = Stopwatch.StartNew();
var lint = new LintResultCache();
var analyzer = new SolutionAnalyzer(loggers.CreateLogger<SolutionAnalyzer>(), lint);
var results = await analyzer.AnalyzeSolutionAsync(solution,
    new ExtractorContext { ProjectName = args[3], RootPath = root }, cancellation.Token);
var extractionMs = watch.Elapsed.TotalMilliseconds;
var json = new JsonSerializerOptions { PropertyNamingPolicy = JsonNamingPolicy.CamelCase, WriteIndented = true };
json.Converters.Add(new JsonStringEnumConverter());
await File.WriteAllTextAsync(Path.Combine(output, "extraction.json"), JsonSerializer.Serialize(results, json));
await File.WriteAllTextAsync(Path.Combine(output, "lint.json"), JsonSerializer.Serialize(lint.Take(args[3]), json));
var buffer = new GraphBuffer();
foreach (var result in results) {
    foreach (var node in result.Nodes) buffer.AddNode(node);
    foreach (var edge in result.Edges) buffer.AddEdge(edge);
    foreach (var call in result.UnresolvedCalls) buffer.AddUnresolvedCall(call);
    foreach (var import in result.UnresolvedImports) buffer.AddUnresolvedImport(import);
}
// Native buffer deduplicates qualified names. Preserve all original results
// above so collisions are visible; these local IDs are adapter storage keys.
var nodes = buffer.AllNodes.OrderBy(n => n.QualifiedName, StringComparer.Ordinal)
    .Select((node, i) => node with { Id = i + 1 }).ToArray();
var ids = nodes.ToDictionary(n => GraphNode.CreateGraphNodeKey(n.Project, n.QualifiedName), n => n.Id);
var edges = buffer.ResolveEdges(args[3], ids, loggers.CreateLogger<GraphBuffer>(), "C#");
await File.WriteAllTextAsync(Path.Combine(output, "buffer.json"), JsonSerializer.Serialize(new {
    nodes, edges,
    unresolvedCalls = buffer.AllUnresolvedCalls,
    unresolvedImports = buffer.AllUnresolvedImports,
    semantics = "Native GraphBuffer.ResolveEdges only; IndexingPipeline.ResolveCalls, database, cross-repo linker and queries are not executed."
}, json));
await File.WriteAllTextAsync(Path.Combine(output, "execution.json"), JsonSerializer.Serialize(new {
    component = "CodeGraph native SolutionAnalyzer/CodeGraphSyntaxWalker/GraphBuffer",
    extractionMs, totalMs = watch.Elapsed.TotalMilliseconds,
    processPeakWorkingSetBytes = Process.GetCurrentProcess().PeakWorkingSet64,
    solution, root, repository = args[3],
    modelCalls = 0, gpuRequested = false,
    limitations = new[] {
        "Component execution only, not the production API/MCP/indexing pipeline.",
        "Native analyzer logs workspace/restore/document failures and may continue; retain logs and lint diagnostics.",
        "Native nodes expose line ranges; extraction edges do not universally carry callsite spans.",
        "No private call resolver, cross-repo linker, AI enrichment, embeddings, or reconstructed query/store behavior."
    }
}, json));

// Supplemental completeness probe: the unchanged native analyzer catches some
// failures rather than rejecting the capture. This does not create graph facts
// and is excluded from the native execution timing above.
using var coverageWorkspace = MSBuildWorkspace.Create();
var workspaceDiagnostics = new List<string>();
coverageWorkspace.WorkspaceFailed += (_, e) => workspaceDiagnostics.Add(e.Diagnostic.ToString());
var coverageSolution = await coverageWorkspace.OpenSolutionAsync(solution, cancellationToken: cancellation.Token);
var coverage = new List<object>();
foreach (var project in coverageSolution.Projects) {
    var compilation = await project.GetCompilationAsync(cancellation.Token);
    coverage.Add(new {
        project = project.Name,
        projectPath = Path.GetRelativePath(root, project.FilePath!),
        compilationAvailable = compilation is not null,
        errors = compilation?.GetDiagnostics(cancellation.Token)
            .Where(d => d.Severity == DiagnosticSeverity.Error).Select(d => d.ToString()).ToArray(),
        documents = project.Documents.Select(d => new {
            path = d.FilePath is null ? null : Path.GetRelativePath(root, d.FilePath),
            sha256 = d.FilePath is not null && File.Exists(d.FilePath)
                ? Convert.ToHexStringLower(SHA256.HashData(File.ReadAllBytes(d.FilePath))) : null
        }).ToArray()
    });
}
await File.WriteAllTextAsync(Path.Combine(output, "coverage.json"), JsonSerializer.Serialize(new {
    semantics = "Supplemental same-settings MSBuildWorkspace probe; not native graph output or a claim native analyzer visited every document.",
    projects = coverage, workspaceDiagnostics
}, json));
