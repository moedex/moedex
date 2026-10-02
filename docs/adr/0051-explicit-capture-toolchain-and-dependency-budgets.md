# ADR 0051: Explicit capture toolchain and dependency budgets

Status: accepted. Date: 2026-10-01.

A requested SDK path did not control `dotnet restore` or Roslyn's out-of-process
build-host discovery. Complete NuGet candidate caches could also exceed the
fixed 1 GiB limit even for a small application closure.

Capture now invokes the selected SDK's MSBuild directly and creates an owned
one-SDK toolchain directory for child discovery. Installed runtimes/SDK files are
referenced, and the small dotnet executable is privately copied. Application
`global.json` and tracked inputs are not rewritten. Trusted toolchain installations
must remain present; the directory is not an execution sandbox or tooling seal.

Dependency packing and capture retain a 1 GiB default with independent explicit
budgets up to 4 GiB. A manifest cannot raise a caller's budget. Full file membership,
digests, private copies, post-execution verification and existing cleanup limits
remain mandatory. Effective budgets are returned in command summaries.

An opt-in pinned Roslyn4.11 worker build supports the tested SDK8 Razor closure.
The default package-free SDK-bundled build remains unchanged. Compiler identity and
assembly hashes distinguish toolchains without changing extraction rule version9.
Worker errors surface only bounded diagnostic fields, never source record dumps.

The [acceptance record](../../research/semantic-intelligence/CAPTURE-PORTABILITY-ACCEPTANCE.md)
records both application captures, source-based coverage gaps and retained failures.
