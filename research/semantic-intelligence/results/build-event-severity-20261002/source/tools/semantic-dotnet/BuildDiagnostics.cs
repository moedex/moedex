using Microsoft.Build.Framework;
using Microsoft.Build.Logging;
using Microsoft.CodeAnalysis;

static partial class Worker
{
    // Use the actual workspace build, not a second evaluation or a text-based
    // warning allowlist. Unknown log formats or diagnostic correspondence fail
    // closed. Raw binlogs can contain environment data; keep them temporary.
    sealed class BuildDiagnostics : IDisposable
    {
        readonly string directory = Directory.CreateTempSubdirectory("moedex-build-events-").FullName;
        public BinaryLogger Logger => new() { Parameters = Path.Combine(directory, "load.binlog") };
        public bool Verified { get; private set; }
        public object Evidence { get; private set; } = new { Policy = "native-build-events-v1", Verified = false };

        public void Verify(IEnumerable<string> projectPaths, WorkspaceDiagnostic[] diagnostics)
        {
            var warnings = new List<BuildWarningEventArgs>();
            var errors = new List<BuildErrorEventArgs>();
            var projects = new Dictionary<string, (int Started, int Finished)>(StringComparer.Ordinal);
            int started = 0, finished = 0;
            bool failed = false;
            var logs = Directory.GetFiles(directory, "*.binlog").Order(StringComparer.Ordinal).ToArray();
            if (logs.Length > 64 || logs.Sum(log => new FileInfo(log).Length) > 256L * 1024 * 1024)
                throw new InvalidDataException("design-time build logs exceed aggregate limit");
            foreach (var log in logs)
            {
                int beforeStarted = started, beforeFinished = finished;
                if (new FileInfo(log).Length > 128 * 1024 * 1024)
                    throw new InvalidDataException("design-time build log exceeds limit");
                var replay = new BinaryLogReplayEventSource { AllowForwardCompatibility = false };
                replay.RecoverableReadError += _ => failed = true;
                replay.BuildStarted += (_, _) => started++;
                replay.BuildFinished += (_, e) => { finished++; failed |= !e.Succeeded; };
                replay.ProjectStarted += (_, e) =>
                {
                    var key = e.ProjectFile ?? "";
                    var count = projects.GetValueOrDefault(key);
                    projects[key] = (count.Started + 1, count.Finished);
                };
                replay.ProjectFinished += (_, e) =>
                {
                    var key = e.ProjectFile ?? "";
                    var count = projects.GetValueOrDefault(key);
                    projects[key] = (count.Started, count.Finished + 1);
                    failed |= !e.Succeeded;
                };
                replay.WarningRaised += (_, e) => warnings.Add(e);
                replay.ErrorRaised += (_, e) => errors.Add(e);
                replay.Replay(log);
                // Older supported MSBuild readers lack this public property;
                // they still reject unsupported formats with forward compatibility
                // disabled. Honor the additional warning when a newer reader has it.
                failed |= typeof(BinaryLogReplayEventSource).GetProperty("FormatVersionMismatchWarning")?.GetValue(replay) is not null;
                failed |= started == beforeStarted || started - beforeStarted != finished - beforeFinished;
            }
            // The public workspace API discards build severity. Correlate exact
            // project/message identities and multiplicity against typed events.
            // Formatting changes/localization are unsupported, never guessed.
            var unmatched = warnings.GroupBy(e => WorkspaceMessage(e.ProjectFile, e.Message), StringComparer.Ordinal)
                .ToDictionary(g => g.Key, g => g.Count(), StringComparer.Ordinal);
            int matched = 0;
            foreach (var diagnostic in diagnostics)
            {
                if (unmatched.TryGetValue(diagnostic.Message, out var count) && count > 0)
                { unmatched[diagnostic.Message] = count - 1; matched++; }
            }
            // Older workspace releases ignore the logger overload. Retain their
            // existing strict policy only when there are no workspace diagnostics;
            // absence of logs can never justify downgrading a diagnostic.
            bool cleanWorkspace = logs.Length == 0 && diagnostics.Length == 0;
            Verified = cleanWorkspace || logs.Length > 0 && started > 0 && started == finished && !failed && errors.Count == 0 &&
                projects.All(p => p.Key.Length > 0 && p.Value.Started > 0 && p.Value.Started == p.Value.Finished) &&
                projectPaths.All(p => projects.TryGetValue(p, out var count) && count.Finished > 0) &&
                warnings.All(e => !string.IsNullOrEmpty(e.ProjectFile) && projects.ContainsKey(e.ProjectFile)) &&
                matched == diagnostics.Length && unmatched.Values.All(n => n == 0);
            Evidence = new
            {
                Policy = cleanWorkspace ? "workspace-no-diagnostics-v1" : "native-build-events-v1", Verified, Logs = logs.Length, BuildsStarted = started, BuildsFinished = finished,
                ReaderAssembly = FileInput(typeof(BinaryLogReplayEventSource).Assembly.Location),
                FailedBuildOrLog = failed, WorkspaceDiagnostics = diagnostics.Length, MatchedWarnings = matched,
                Projects = projects.OrderBy(p => p.Key, StringComparer.Ordinal).Select(p => new { Project = DiagnosticProject(p.Key), p.Value.Started, p.Value.Finished }).ToArray(),
                Warnings = warnings.Select(e => new { Project = DiagnosticProject(e.ProjectFile), e.Code, e.Message })
                    .OrderBy(e => e.Project, StringComparer.Ordinal).ThenBy(e => e.Code, StringComparer.Ordinal).ThenBy(e => e.Message, StringComparer.Ordinal).ToArray(),
                Errors = errors.Select(e => new { Project = DiagnosticProject(e.ProjectFile), e.Code, e.Message })
                    .OrderBy(e => e.Project, StringComparer.Ordinal).ThenBy(e => e.Code, StringComparer.Ordinal).ThenBy(e => e.Message, StringComparer.Ordinal).ToArray(),
            };
        }

        static string DiagnosticProject(string? path) => string.IsNullOrEmpty(path) ? "" : Relative(path);

        static string WorkspaceMessage(string? project, string? message) =>
            $"Msbuild failed when processing the file '{project}' with message: {message}";

        public void Dispose() => Directory.Delete(directory, recursive: true);
    }
}
