#!/usr/bin/env python3
"""Reproduce the isolated C# SCIP fixture spike; no repository dependencies."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("dotnet", "scip-dotnet", "scip", "fixture", "output"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--projects", nargs="+", help="Optional gold project IDs to run")
    parser.add_argument("--overload-reorder", action="store_true",
                        help="Swap the two Pick declarations in copied Contracts/Types.cs")
    args = parser.parse_args()
    fixture = args.fixture.resolve()
    output = args.output.resolve()
    if output.exists():
        parser.error("output must not exist; retain previous runs for comparison")
    output.mkdir(parents=True)
    workspace = output / "workspace"
    shutil.copytree(fixture, workspace)
    gold = json.loads((workspace / "gold.json").read_text())
    unknown = set(args.projects or []) - {project["id"] for project in gold["projects"]}
    if unknown:
        parser.error("unknown project IDs: " + ", ".join(sorted(unknown)))
    perturbations = []
    if args.overload_reorder:
        path = workspace / "Contracts/Types.cs"
        lines = path.read_text().splitlines(keepends=True)
        positions = [i for i, line in enumerate(lines) if "static string Pick(" in line]
        if len(positions) != 2:
            parser.error("overload perturbation requires exactly two Pick declarations")
        first, second = positions
        before = hashlib.sha256(path.read_bytes()).hexdigest()
        lines[first], lines[second] = lines[second], lines[first]
        path.write_text("".join(lines))
        perturbations.append({"kind": "swap_overload_declarations", "file": "Contracts/Types.cs",
                              "lines": [first + 1, second + 1], "before_sha256": before,
                              "after_sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
    roster = {str(path.relative_to(workspace)): hashlib.sha256(path.read_bytes()).hexdigest()
              for path in sorted(workspace.rglob("*")) if path.is_file()}
    env = os.environ.copy()
    env.update(DOTNET_ROOT=str(args.dotnet.resolve().parent),
               DOTNET_CLI_HOME=str(output / "cli"),
               DOTNET_SKIP_FIRST_TIME_EXPERIENCE="1", DOTNET_CLI_TELEMETRY_OPTOUT="1",
               DOTNET_GENERATE_ASPNET_CERTIFICATE="false",
               DOTNET_NOLOGO="1", NUGET_PACKAGES=str(output / "nuget"),
               PATH=str(args.dotnet.resolve().parent) + os.pathsep + env.get("PATH", ""))
    manifest = {"format_version": 1, "fixture": str(fixture), "workspace": str(workspace),
                "runner_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                "selected_projects": args.projects,
                "gold_sha256": hashlib.sha256((workspace / "gold.json").read_bytes()).hexdigest(),
                "source_sha256": roster,
                "environment": {k: env[k] for k in ("DOTNET_ROOT", "DOTNET_CLI_HOME", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE", "DOTNET_CLI_TELEMETRY_OPTOUT", "DOTNET_GENERATE_ASPNET_CERTIFICATE", "DOTNET_NOLOGO", "NUGET_PACKAGES", "PATH")},
                "projects": gold["projects"], "perturbations": perturbations, "commands": []}

    def run(label, command, stdout=None, timed=False):
        command = [str(part) for part in command]
        actual = (["/usr/bin/time", "-l", "-o", str(output / (label + ".time.txt"))] if timed else []) + command
        start = time.monotonic()
        timed_out = False
        with (output / (stdout or label + ".stdout.txt")).open("wb") as out, (output / (label + ".stderr.txt")).open("wb") as err:
            process = subprocess.Popen(actual, cwd=workspace, env=env, stdout=out,
                                       stderr=err, start_new_session=True)
            try:
                exit_code = process.wait(timeout=300)
            except subprocess.TimeoutExpired:
                timed_out = True
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                exit_code = 124
        manifest["commands"].append({"label": label, "argv": actual, "cwd": str(workspace),
                                     "exit_code": exit_code, "timed_out": timed_out,
                                     "elapsed_seconds": time.monotonic() - start})
        (output / "run.json").write_text(json.dumps(manifest, indent=2) + "\n")
        return exit_code

    run("dotnet-info", [args.dotnet.resolve(), "--info"])
    run("indexer-version", [args.scip_dotnet.resolve(), "--version"])
    run("decoder-version", [args.scip.resolve(), "--version"])
    for project in gold["projects"]:
        name = project["id"]
        if args.projects and name not in args.projects:
            continue
        project_file = workspace / project["project_file"]
        restore = run(name + "-restore", [args.dotnet.resolve(), "restore", project_file, "--ignore-failed-sources"], timed=True)
        if restore:
            raise SystemExit(f"restore failed for {name}; see preserved logs")
        # Broken's nonzero build is expected and recorded rather than hidden.
        run(name + "-build", [args.dotnet.resolve(), "build", project_file, "--no-restore"], timed=True)
        variants = [(name, True)]
        if name in ("ContextA", "ContextB"):
            variants.append((name + "-default", False))
        for label, global_symbols in variants:
            index = output / (label + ".scip")
            command = [args.scip_dotnet.resolve(), "index", project_file,
                       "--working-directory", workspace, "--output", index, "--skip-dotnet-restore"]
            if global_symbols:
                command.append("--allow-global-symbol-definitions")
            if run(label + "-index", command, timed=True):
                raise SystemExit(f"index failed for {label}; see preserved logs")
            if run(label + "-decode", [args.scip.resolve(), "print", "--json", index], stdout=label + ".json"):
                raise SystemExit(f"decode failed for {label}; see preserved logs")
    print(output / "run.json")


if __name__ == "__main__":
    main()
