using System.Reflection;
using System.Runtime.Loader;
using Microsoft.Build.Locator;

// Registration must precede JIT compilation of code referring to MSBuild types.
try
{
    var options = new Dictionary<string, string>(StringComparer.Ordinal);
    for (int i = 0; i < args.Length; i += 2)
    {
        if (i + 1 >= args.Length || !args[i].StartsWith("--")) throw new ArgumentException("expected --option value pairs");
        if (!options.TryAdd(args[i], args[i + 1])) throw new ArgumentException("duplicate option " + args[i]);
    }
    foreach (var key in new[] { "--repo", "--root", "--project", "--framework", "--sdk-path" })
        if (!options.TryGetValue(key, out var value) || string.IsNullOrWhiteSpace(value)) throw new ArgumentException(key + " is required");
    foreach (var key in options.Keys)
        if (!new[] { "--repo", "--root", "--project", "--framework", "--sdk-path", "--configuration" }.Contains(key)) throw new ArgumentException("unknown option " + key);
    AssemblyLoadContext.Default.Resolving += (context, name) =>
    {
        if (name.Name?.StartsWith("Microsoft.Build", StringComparison.Ordinal) == true) return null;
        var path = Path.Combine(AppContext.BaseDirectory, name.Name + ".dll");
        return File.Exists(path) ? context.LoadFromAssemblyPath(path) : null;
    };
    MSBuildLocator.RegisterMSBuildPath(Path.GetFullPath(options["--sdk-path"]));
    return await Worker.Run(options);
}
catch (Exception e)
{
    Console.Error.WriteLine($"semantic worker failed: {e.GetType().Name}: {e.Message}");
    // No terminal record: a consumer must reject the partial stream.
    return 1;
}
