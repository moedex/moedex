using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using System.Xml.Linq;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Text;

// Experimental compiler adapter, not an MSBuild evaluator or production format.
return Spike.Run(args);

static class Spike
{
    const string Schema = "moedex.roslyn-spike.v1";
    const string Extractor = "direct-roslyn-subset/1";
    static readonly UTF8Encoding Utf8 = new(false, true);
    static readonly JsonSerializerOptions Json = new() { PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower };
    static readonly Dictionary<string, string> Digests = new(StringComparer.Ordinal);
    static readonly Dictionary<string, Project> Projects = new(StringComparer.Ordinal);
    static readonly List<string> FrameworkFiles = [];
    static string Root = "";
    static string FrameworkDir = "";
    static string Compiler => typeof(CSharpCompilation).Assembly.GetName().Version?.ToString() ?? "unknown";

    public static int Run(string[] args)
    {
        try
        {
            if (args.Length == 0) throw new ArgumentException("usage: roslyn-spike <project.csproj|solution.sln|solution.slnx> --root <snapshot-root> --framework-dir <reference-pack/ref/net10.0>");
            var input = Path.GetFullPath(args[0]);
            Root = Path.GetDirectoryName(input)!;
            for (int i = 1; i < args.Length; i += 2)
            {
                if (i + 1 == args.Length) throw new ArgumentException($"missing value for {args[i]}");
                switch (args[i])
                {
                    case "--root": Root = Path.GetFullPath(args[i + 1]); break;
                    case "--framework-dir": FrameworkDir = Path.GetFullPath(args[i + 1]); break;
                    default: throw new ArgumentException($"unsupported option {args[i]}");
                }
            }
            if (FrameworkDir.Length == 0) throw new ArgumentException("--framework-dir is required: target framework references must be explicit");
            FrameworkFiles.AddRange(Directory.GetFiles(FrameworkDir, "*.dll").Order(StringComparer.Ordinal));
            if (FrameworkFiles.Count == 0) throw new ArgumentException("framework directory contains no reference assemblies");
            var roots = InputProjects(input).ToArray();
            if (roots.Length == 0) throw new ArgumentException("input contains no C# projects");
            foreach (var project in roots) Load(project);
            foreach (var project in Projects.Values.OrderBy(p => p.Path, StringComparer.Ordinal).ToArray()) Compile(project, []);
            bool incomplete = false;
            foreach (var project in Projects.Values.OrderBy(p => Relative(p.Path), StringComparer.Ordinal))
            {
                EmitProject(project);
                incomplete |= project.Compilation is null || project.Issues.Count != 0 || project.Compilation.GetDiagnostics().Any(d => d.Severity == DiagnosticSeverity.Error);
            }
            // Incomplete output is still useful evidence, never a complete success.
            return incomplete ? 2 : 0;
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"roslyn-spike failed: {ex.GetType().Name}: {ex.Message}");
            return 1;
        }
    }

    static IEnumerable<string> InputProjects(string input)
    {
        if (input.EndsWith(".csproj", StringComparison.OrdinalIgnoreCase)) return [input];
        if (input.EndsWith(".slnx", StringComparison.OrdinalIgnoreCase))
            return XDocument.Load(input).Descendants("Project").Select(e => Path.GetFullPath(e.Attribute("Path")!.Value.Replace('\\', '/'), Path.GetDirectoryName(input)!));
        if (!input.EndsWith(".sln", StringComparison.OrdinalIgnoreCase)) throw new ArgumentException("input must be .csproj, .sln or .slnx");
        return Regex.Matches(File.ReadAllText(input), "Project\\([^\\n]+?=\\s*\"[^\"]*\",\\s*\"([^\"]+\\.csproj)\"")
            .Select(m => Path.GetFullPath(m.Groups[1].Value.Replace('\\', '/'), Path.GetDirectoryName(input)!));
    }

    static Project Load(string file)
    {
        file = Path.GetFullPath(file);
        if (Projects.TryGetValue(file, out var existing)) return existing;
        var p = new Project(file);
        Projects[file] = p;
        if (!File.Exists(file)) { p.Issues.Add("missing_project"); return p; }
        var xml = XDocument.Load(file);
        if (xml.Root?.Attribute("Sdk")?.Value != "Microsoft.NET.Sdk") p.Issues.Add("unsupported_msbuild:Sdk");
        var supportedProperties = new HashSet<string>(StringComparer.Ordinal) {
            "TargetFramework", "AssemblyName", "DefineConstants", "Nullable", "LangVersion", "AllowUnsafeBlocks",
            "EnableDefaultCompileItems", "ImplicitUsings", "EnableDefaultItems", "OutputType", "AssemblyVersion"
        };
        foreach (var group in xml.Descendants().Where(e => e.Name.LocalName == "PropertyGroup"))
            foreach (var property in group.Elements())
                if (!supportedProperties.Contains(property.Name.LocalName)) p.Issues.Add("unsupported_msbuild:Property:" + property.Name.LocalName);
        string Property(string name, string fallback = "") => xml.Descendants().LastOrDefault(e => e.Name.LocalName == name)?.Value.Trim() ?? fallback;
        p.AssemblyName = Property("AssemblyName", Path.GetFileNameWithoutExtension(file));
        p.Framework = Property("TargetFramework");
        p.Defines = Property("DefineConstants").Split(';', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries).Order(StringComparer.Ordinal).ToArray();
        p.Nullable = Property("Nullable", "disable");
        p.LanguageVersion = Property("LangVersion", "default");
        var unsupported = new HashSet<string>(StringComparer.Ordinal) { "Import", "Target", "PackageReference", "Analyzer", "AdditionalFiles", "TargetFrameworks", "Using", "FrameworkReference" };
        foreach (var element in xml.Descendants())
        {
            if (unsupported.Contains(element.Name.LocalName)) p.Issues.Add("unsupported_msbuild:" + element.Name.LocalName);
            if (element.Attribute("Condition") is not null) p.Issues.Add("unsupported_msbuild:Condition");
            if (element.Value.Contains("$(", StringComparison.Ordinal)) p.Issues.Add("unsupported_msbuild:PropertyExpansion");
        }
        if (Property("ImplicitUsings") is "enable" or "true") p.Issues.Add("unsupported_msbuild:ImplicitUsings");
        if (Property("EnableDefaultItems") == "false") p.Issues.Add("unsupported_msbuild:EnableDefaultItems");
        if (Property("DefaultItemExcludes").Length != 0 || Property("DefaultItemExcludesInProjectFolder").Length != 0) p.Issues.Add("unsupported_msbuild:DefaultItemExcludes");
        if (Property("OutputType", "Library") is not ("Library" or "library")) p.Issues.Add("unsupported_msbuild:OutputType");
        if (Property("AllowUnsafeBlocks", "false") == "true") p.AllowUnsafe = true;
        var assemblyVersion = Property("AssemblyVersion");
        if (assemblyVersion.Length != 0) p.Issues.Add("unsupported_msbuild:GeneratedAssemblyVersion");
        for (var parent = new DirectoryInfo(p.Directory); parent is not null; parent = parent.Parent)
            foreach (var name in new[] { "Directory.Build.props", "Directory.Build.targets", "Directory.Packages.props" })
            {
                string path = Path.Combine(parent.FullName, name);
                if (File.Exists(path)) { p.ConfigFiles.Add(path); p.Issues.Add("unsupported_import:" + Relative(path)); }
            }
        if (Path.GetFileName(FrameworkDir) != p.Framework) p.Issues.Add("framework_reference_mismatch:" + p.Framework);
        var sources = new HashSet<string>(StringComparer.Ordinal);
        if (Property("EnableDefaultCompileItems", "true") != "false")
            foreach (var path in Directory.GetFiles(p.Directory, "*.cs", SearchOption.AllDirectories))
                if (!Excluded(path, p.Directory)) sources.Add(Path.GetFullPath(path));
        foreach (var item in xml.Descendants().Where(e => e.Name.LocalName == "Compile"))
        {
            if (item.Attribute("Update") is not null || item.Attribute("Exclude") is not null) p.Issues.Add("unsupported_msbuild:CompileUpdateOrExclude");
            if (item.Attribute("Include") is { } include) foreach (var path in Expand(p.Directory, include.Value, true)) sources.Add(path);
            if (item.Attribute("Remove") is { } remove) foreach (var path in Expand(p.Directory, remove.Value)) sources.Remove(path);
        }
        foreach (var path in sources.Order(StringComparer.Ordinal))
        {
            var bytes = File.ReadAllBytes(path);
            int bom = bytes.AsSpan().StartsWith(new byte[] { 0xef, 0xbb, 0xbf }) ? 3 : 0;
            p.Sources.Add(new Source(path, bytes, Utf8.GetString(bytes, bom, bytes.Length - bom), bom));
        }
        foreach (var item in xml.Descendants().Where(e => e.Name.LocalName == "ProjectReference"))
        {
            var include = item.Attribute("Include")?.Value;
            if (include is null) { p.Issues.Add("unsupported_msbuild:ProjectReferenceWithoutInclude"); continue; }
            if (item.Elements().Any() || item.Attributes().Any(a => a.Name.LocalName != "Include")) p.Issues.Add("unsupported_msbuild:ProjectReferenceMetadata");
            p.Dependencies.Add(Load(Path.GetFullPath(include.Replace('\\', '/'), p.Directory)));
        }
        foreach (var item in xml.Descendants().Where(e => e.Name.LocalName == "Reference"))
        {
            var hint = item.Elements().FirstOrDefault(e => e.Name.LocalName == "HintPath")?.Value;
            if (hint is null) { p.Issues.Add("unsupported_msbuild:ReferenceWithoutHintPath"); continue; }
            var path = Path.GetFullPath(hint.Replace('\\', '/'), p.Directory);
            if (!File.Exists(path)) p.Issues.Add("missing_reference:" + Relative(path)); else p.References.Add(path);
            if (item.Attributes().Any(a => a.Name.LocalName != "Include") || item.Elements().Any(e => e.Name.LocalName is not ("HintPath" or "Private"))) p.Issues.Add("unsupported_msbuild:ReferenceMetadata");
        }
        return p;
    }

    static bool Excluded(string file, string directory) => Path.GetRelativePath(directory, file).Split(Path.DirectorySeparatorChar).Any(p => p is "bin" or "obj" or ".git");
    static IEnumerable<string> Expand(string directory, string pattern, bool requireExplicit = false)
    {
        if (pattern.Contains(';') || pattern.Contains("$(", StringComparison.Ordinal) || pattern.Contains("**", StringComparison.Ordinal)) throw new ArgumentException("unsupported compile glob: " + pattern);
        string absolute = Path.GetFullPath(pattern.Replace('\\', '/'), directory);
        string parent = Path.GetDirectoryName(absolute)!;
        if (requireExplicit && !pattern.Contains('*') && !pattern.Contains('?') && !File.Exists(absolute))
            throw new FileNotFoundException("explicit compile source absent", absolute);
        if (!Directory.Exists(parent)) throw new FileNotFoundException("compile directory absent", parent);
        return Directory.GetFiles(parent, Path.GetFileName(absolute)).Select(Path.GetFullPath).Order(StringComparer.Ordinal);
    }

    static void Compile(Project p, HashSet<string> visiting)
    {
        if (p.Compiled) return;
        if (!visiting.Add(p.Path)) { p.Issues.Add("cyclic_project_reference"); return; }
        foreach (var dep in p.Dependencies.OrderBy(d => d.Path, StringComparer.Ordinal)) Compile(dep, visiting);
        visiting.Remove(p.Path);
        var inputs = new List<string> { Schema, Extractor, Compiler, Relative(p.Path), p.AssemblyName, p.Framework, p.Nullable, p.LanguageVersion, p.AllowUnsafe.ToString(), string.Join(';', p.Defines) };
        if (File.Exists(p.Path)) inputs.Add("project:" + DigestFile(p.Path));
        inputs.AddRange(p.ConfigFiles.Order(StringComparer.Ordinal).Select(f => "config:" + Relative(f) + ":" + DigestFile(f)));
        inputs.AddRange(p.Sources.Select(s => "source:" + Relative(s.Path) + ":" + Hash(s.Bytes)));
        inputs.AddRange(FrameworkFiles.Concat(p.References).Order(StringComparer.Ordinal).Select(f => "reference:" + Path.GetFileName(f) + ":" + DigestFile(f)));
        inputs.AddRange(p.Dependencies.OrderBy(d => d.Path, StringComparer.Ordinal).Select(d => "project-reference:" + Relative(d.Path) + ":" + d.ContextId));
        p.ContextId = Hash(Utf8.GetBytes(string.Join('\n', inputs)));
        p.Compiled = true;
        if (p.Issues.Any(i => i.StartsWith("unsupported_", StringComparison.Ordinal) || i.StartsWith("framework_reference_mismatch", StringComparison.Ordinal) || i == "missing_project" || i == "cyclic_project_reference")) return;
        if (!LanguageVersionFacts.TryParse(p.LanguageVersion, out var lang)) { p.Issues.Add("unsupported_language_version"); return; }
        var options = new CSharpParseOptions(lang, preprocessorSymbols: p.Defines);
        foreach (var source in p.Sources) source.Tree = CSharpSyntaxTree.ParseText(SourceText.From(source.Text, Utf8), options, source.Path);
        var references = FrameworkFiles.Concat(p.References).Distinct(StringComparer.Ordinal).Select(f => MetadataReference.CreateFromFile(f)).Cast<MetadataReference>().ToList();
        foreach (var dep in p.Dependencies)
        {
            if (dep.Compilation is null || dep.Issues.Count != 0 || dep.Compilation.GetDiagnostics().Any(d => d.Severity == DiagnosticSeverity.Error))
                p.Issues.Add("incomplete_project_reference:" + Relative(dep.Path));
            if (dep.Compilation is not null) references.Add(dep.Compilation.ToMetadataReference());
        }
        var nullable = p.Nullable switch { "enable" => NullableContextOptions.Enable, "annotations" => NullableContextOptions.Annotations, "warnings" => NullableContextOptions.Warnings, _ => NullableContextOptions.Disable };
        p.Compilation = CSharpCompilation.Create(p.AssemblyName, p.Sources.Select(s => s.Tree!), references,
            new CSharpCompilationOptions(OutputKind.DynamicallyLinkedLibrary, allowUnsafe: p.AllowUnsafe, deterministic: true, nullableContextOptions: nullable));
    }

    static void EmitProject(Project p)
    {
        var diagnostics = p.Compilation?.GetDiagnostics() ?? [];
        string completeness = p.Compilation is null ? "unsupported" : p.Issues.Count != 0 || diagnostics.Any(d => d.Severity == DiagnosticSeverity.Error) ? "incomplete" : "complete";
        Emit(new { Schema, RecordType = "project", Extractor, CompilerVersion = Compiler, Project = Relative(p.Path), BuildContext = p.ContextId,
            BuildMode = "explicit-source-csharp-compilation", CompilationStatus = completeness, AssemblyIdentity = p.Compilation?.Assembly.Identity.ToString(), p.Framework, p.Defines,
            ReferencePack = Path.GetFileName(Path.GetDirectoryName(Path.GetDirectoryName(FrameworkDir))),
            ReferenceManifest = FrameworkFiles.Concat(p.References).Order(StringComparer.Ordinal).Select(f => new { Name = Path.GetFileName(f), Sha256 = DigestFile(f) }),
            Sources = p.Sources.Select(s => new { Path = Relative(s.Path), Sha256 = Hash(s.Bytes), BlobSha = GitBlob(s.Bytes), Generated = Generated(s.Path) }),
            ProjectReferences = p.Dependencies.OrderBy(d => d.Path, StringComparer.Ordinal).Select(d => new { Project = Relative(d.Path), BuildContext = d.ContextId }),
            ConfigurationSha256 = File.Exists(p.Path) ? DigestFile(p.Path) : null,
            Issues = p.Issues.Distinct().Order(StringComparer.Ordinal).ToArray(),
            Capabilities = new[] { "compiler_declarations", "compiler_name_references", "invocation_binding", "explicit_project_references", "utf8_source_spans" },
            Limitations = new[] { "bounded_project_subset_not_msbuild", "no_source_generator_execution", "no_package_restore", "no_runtime_dispatch_edges", "no_incremental_cache", "sdk_generated_assembly_info_not_synthesized", "sdk_implicit_defines_not_applied" } });
        int declarations = 0, references = 0, unresolved = 0, ambiguous = 0;
        if (p.Compilation is not null)
            foreach (var source in p.Sources)
            {
                var model = p.Compilation.GetSemanticModel(source.Tree!);
                foreach (var node in source.Tree!.GetRoot().DescendantNodes())
                {
                    var token = DeclarationToken(node);
                    if (token.RawKind != 0 && model.GetDeclaredSymbol(node) is { } declared)
                    {
                        EmitOccurrence(p, source, "declaration", token.Span, declared, [], "resolved", "declaration", null, "None"); declarations++;
                    }
                    if (node is SimpleNameSyntax name)
                    {
                        SyntaxNode bind = name;
                        if (name.Parent is MemberAccessExpressionSyntax member && member.Name == name && member.Parent is InvocationExpressionSyntax invocation && invocation.Expression == member) bind = invocation;
                        else if (name.Parent is InvocationExpressionSyntax direct && direct.Expression == name) bind = direct;
                        else if (name.Parent is MemberBindingExpressionSyntax binding && binding.Parent is InvocationExpressionSyntax conditional) bind = conditional;
                        var info = model.GetSymbolInfo(bind);
                        // Names inside declarations' namespace headers and using directives
                        // are retained as namespace references; declaration identifier tokens
                        // themselves are not SimpleName nodes and are not double-emitted.
                        var symbol = info.Symbol;
                        string status = symbol is not null && symbol is not IErrorTypeSymbol ? "resolved" : info.CandidateSymbols.Length > 1 ? "ambiguous" : "unresolved";
                        EmitOccurrence(p, source, "reference", name.Identifier.Span, symbol, info.CandidateSymbols, status,
                            bind is InvocationExpressionSyntax ? "invocation" : "name", model.GetEnclosingSymbol(name.SpanStart), info.CandidateReason.ToString());
                        references++; if (status == "unresolved") unresolved++; if (status == "ambiguous") ambiguous++;
                    }
                    if (node is BaseObjectCreationExpressionSyntax creation)
                    {
                        var info = model.GetSymbolInfo(creation);
                        var tokenSpan = creation is ObjectCreationExpressionSyntax explicitCreation ? TypeToken(explicitCreation.Type).Span : creation.NewKeyword.Span;
                        string status = info.Symbol is not null ? "resolved" : info.CandidateSymbols.Length > 1 ? "ambiguous" : "unresolved";
                        EmitOccurrence(p, source, "reference", tokenSpan, info.Symbol, info.CandidateSymbols, status, "constructor", model.GetEnclosingSymbol(creation.SpanStart), info.CandidateReason.ToString());
                        references++; if (status == "unresolved") unresolved++; if (status == "ambiguous") ambiguous++;
                    }
                }
            }
        foreach (var d in diagnostics.OrderBy(d => d.Location.SourceTree?.FilePath, StringComparer.Ordinal).ThenBy(d => d.Location.SourceSpan.Start).ThenBy(d => d.Id, StringComparer.Ordinal))
        {
            var source = p.Sources.FirstOrDefault(s => s.Tree == d.Location.SourceTree);
            Emit(new { Schema, RecordType = "diagnostic", Project = Relative(p.Path), BuildContext = p.ContextId,
                Code = d.Id, Severity = d.Severity.ToString().ToLowerInvariant(), Message = d.GetMessage(CultureInfo.InvariantCulture),
                SourcePath = source is null ? null : Relative(source.Path), Span = source is null ? null : Span(source, d.Location.SourceSpan) });
        }
        Emit(new { Schema, RecordType = "summary", Project = Relative(p.Path), BuildContext = p.ContextId, CompilationStatus = completeness,
            Declarations = declarations, References = references, Unresolved = unresolved, Ambiguous = ambiguous,
            Errors = diagnostics.Count(d => d.Severity == DiagnosticSeverity.Error), Warnings = diagnostics.Count(d => d.Severity == DiagnosticSeverity.Warning) });
    }

    static SyntaxToken TypeToken(TypeSyntax type) => type switch
    {
        SimpleNameSyntax name => name.Identifier,
        QualifiedNameSyntax qualified => TypeToken(qualified.Right),
        AliasQualifiedNameSyntax alias => alias.Name.Identifier,
        _ => type.GetFirstToken()
    };

    static SyntaxToken DeclarationToken(SyntaxNode node) => node switch
    {
        BaseTypeDeclarationSyntax n => n.Identifier,
        DelegateDeclarationSyntax n => n.Identifier,
        MethodDeclarationSyntax n => n.Identifier,
        ConstructorDeclarationSyntax n => n.Identifier,
        DestructorDeclarationSyntax n => n.Identifier,
        PropertyDeclarationSyntax n => n.Identifier,
        EventDeclarationSyntax n => n.Identifier,
        EnumMemberDeclarationSyntax n => n.Identifier,
        VariableDeclaratorSyntax n => n.Identifier,
        ParameterSyntax n => n.Identifier,
        TypeParameterSyntax n => n.Identifier,
        IndexerDeclarationSyntax n => n.ThisKeyword,
        OperatorDeclarationSyntax n => n.OperatorToken,
        ConversionOperatorDeclarationSyntax n => n.ImplicitOrExplicitKeyword,
        LocalFunctionStatementSyntax n => n.Identifier,
        _ => default
    };

    static void EmitOccurrence(Project p, Source source, string kind, TextSpan span, ISymbol? symbol,
        IEnumerable<ISymbol> candidates, string status, string referenceKind, ISymbol? enclosing, string candidateReason)
    {
        Emit(new { Schema, RecordType = kind, Project = Relative(p.Path), BuildContext = p.ContextId,
            SourcePath = Relative(source.Path), SourceSha256 = Hash(source.Bytes), BlobSha = GitBlob(source.Bytes), Generated = Generated(source.Path),
            Span = Span(source, span), SourceText = source.Text.Substring(span.Start, span.Length), BindingStatus = status,
            BindingMethod = "roslyn-semantic-model", ReferenceKind = referenceKind, CandidateReason = candidateReason,
            Symbol = Symbol(symbol), EnclosingSymbol = Symbol(enclosing),
            Candidates = candidates.Select(Symbol).OrderBy(s => JsonSerializer.Serialize(s, Json), StringComparer.Ordinal).ToArray() });
    }

    static object? Symbol(ISymbol? input)
    {
        if (input is null) return null;
        var symbol = input is IMethodSymbol { ReducedFrom: { } unreduced } ? unreduced.OriginalDefinition : input.OriginalDefinition;
        var descriptor = symbol.GetDocumentationCommentId();
        var assembly = symbol.ContainingAssembly?.Identity.ToString();
        var sourceLocation = symbol.Locations.FirstOrDefault(l => l.IsInSource);
        // Bind source identity to the project which owns the referenced assembly,
        // not to the project making the reference or its identical content blob.
        var owner = Projects.Values.FirstOrDefault(p => p.Compilation is not null && SymbolEqualityComparer.Default.Equals(p.Compilation.Assembly, symbol.ContainingAssembly));
        if (owner is null && sourceLocation is not null)
        {
            var matches = Projects.Values.Where(p => p.Compilation?.Assembly.Identity.Equals(symbol.ContainingAssembly?.Identity) == true && p.Sources.Any(s => s.Path == sourceLocation.SourceTree?.FilePath)).ToArray();
            if (matches.Length == 1) owner = matches[0];
        }
        string descriptorKind = descriptor is null ? "compiler_display_with_local_location" : "documentation_comment_id";
        descriptor ??= symbol.ToDisplayString(SymbolDisplayFormat.FullyQualifiedFormat) + "@" + (sourceLocation is null ? "metadata" : Relative(sourceLocation.SourceTree!.FilePath) + ":" + sourceLocation.SourceSpan.Start.ToString(CultureInfo.InvariantCulture));
        return new { Id = (owner is null ? "assembly:" + assembly : "project:" + Relative(owner.Path) + ":" + owner.ContextId) + "|" + descriptor,
            Descriptor = descriptor, DescriptorKind = descriptorKind, DisplayName = symbol.ToDisplayString(SymbolDisplayFormat.CSharpErrorMessageFormat),
            Kind = symbol.Kind.ToString(), AssemblyIdentity = assembly,
            Project = owner is null ? null : Relative(owner.Path), BuildContext = owner?.ContextId,
            Origin = sourceLocation is null ? "metadata" : "source", IsErrorType = symbol is IErrorTypeSymbol,
            Declarations = symbol.Locations.Where(l => l.IsInSource).OrderBy(l => l.SourceTree!.FilePath, StringComparer.Ordinal).ThenBy(l => l.SourceSpan.Start).Select(l => {
                var source = owner?.Sources.FirstOrDefault(s => s.Path == l.SourceTree!.FilePath) ?? Projects.Values.SelectMany(p => p.Sources).FirstOrDefault(s => s.Path == l.SourceTree!.FilePath);
                return new { SourcePath = Relative(l.SourceTree!.FilePath), Span = source is null ? null : Span(source, l.SourceSpan), SourceSha256 = source is null ? null : Hash(source.Bytes) };
            }).ToArray() };
    }

    static object Span(Source source, TextSpan span)
    {
        int start = source.BomBytes + Utf8.GetByteCount(source.Text.AsSpan(0, span.Start));
        int length = Utf8.GetByteCount(source.Text.AsSpan(span.Start, span.Length));
        var line = source.Tree?.GetLineSpan(span).StartLinePosition;
        return new { ByteOffset = start, ByteLength = length, Utf16Offset = span.Start, Utf16Length = span.Length,
            Line = (line?.Line ?? 0) + 1, Utf16Column = (line?.Character ?? 0) + 1 };
    }
    static bool Generated(string path) => path.EndsWith(".g.cs", StringComparison.OrdinalIgnoreCase) || path.EndsWith(".generated.cs", StringComparison.OrdinalIgnoreCase);
    static string Relative(string path) => Path.GetRelativePath(Root, path).Replace('\\', '/');
    static string DigestFile(string path) => Digests.TryGetValue(path, out var value) ? value : Digests[path] = Hash(File.ReadAllBytes(path));
    static string Hash(byte[] bytes) => Convert.ToHexStringLower(SHA256.HashData(bytes));
    static string GitBlob(byte[] bytes) => Convert.ToHexStringLower(SHA1.HashData(Utf8.GetBytes($"blob {bytes.Length}\0").Concat(bytes).ToArray()));
    static void Emit(object value) => Console.WriteLine(JsonSerializer.Serialize(value, Json));

    sealed class Project(string path)
    {
        public string Path = path, AssemblyName = "", Framework = "", Nullable = "disable", LanguageVersion = "default", ContextId = "";
        public string Directory => System.IO.Path.GetDirectoryName(Path)!;
        public string[] Defines = [];
        public bool AllowUnsafe, Compiled;
        public List<string> Issues = [], References = [], ConfigFiles = [];
        public List<Source> Sources = [];
        public List<Project> Dependencies = [];
        public CSharpCompilation? Compilation;
    }
    sealed class Source(string path, byte[] bytes, string text, int bom)
    {
        public string Path = path, Text = text;
        public byte[] Bytes = bytes;
        public int BomBytes = bom;
        public SyntaxTree? Tree;
    }
}
