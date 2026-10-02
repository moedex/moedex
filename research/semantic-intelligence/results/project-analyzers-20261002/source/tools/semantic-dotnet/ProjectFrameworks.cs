using Microsoft.Build.Evaluation;
using Microsoft.CodeAnalysis;

static partial class Worker
{
    // Workspace global TargetFramework would incorrectly retarget every loaded
    // project, including netstandard analyzers. Match each loaded project to one
    // declared framework using its evaluated output identity instead.
    static Dictionary<string, string> ProjectProperties(Microsoft.CodeAnalysis.Project project, Dictionary<string, string> common)
    {
        RequireInside(project.FilePath!);
        RequireNoEscapingLink(project.FilePath!);
        using var initial = new ProjectCollection(common);
        var outer = initial.LoadProject(project.FilePath!);
        var frameworks = (outer.GetPropertyValue("TargetFrameworks") is { Length: > 0 } multi
            ? multi.Split(';', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
            : new[] { outer.GetPropertyValue("TargetFramework") }).Distinct(StringComparer.Ordinal).ToArray();
        var matches = new List<Dictionary<string, string>>();
        foreach (var framework in frameworks)
        {
            if (string.IsNullOrEmpty(framework)) continue;
            var properties = new Dictionary<string, string>(common) { ["TargetFramework"] = framework };
            using var collection = new ProjectCollection(properties);
            var evaluated = collection.LoadProject(project.FilePath!);
            var output = evaluated.GetPropertyValue("TargetPath");
            if (!string.IsNullOrEmpty(output) && project.OutputFilePath is { } actual &&
                Path.GetFullPath(output, Path.GetDirectoryName(project.FilePath!)!) == Path.GetFullPath(actual))
                matches.Add(properties);
        }
        if (matches.Count != 1)
            throw new InvalidDataException($"project framework identity is missing or ambiguous: {Relative(project.FilePath!)}");
        return matches[0];
    }
}
