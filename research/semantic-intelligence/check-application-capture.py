#!/usr/bin/env python3
"""Capture and publish pinned Sample-Outbox using provisioned offline dependencies.

Run only after independently labelled source gold is frozen. This runner does
not derive gold from compiler output. Public package acquisition is separate.
"""
import argparse
import base64
import hashlib
import json
import pathlib
import subprocess

PIN = "1ab8e66ebf96e5733e68c2f4d2201276f38ed9c5"
ORIGIN = "https://github.com/MassTransit/Sample-Outbox.git"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for flag in ("source", "output", "moe", "dotnet", "sdk", "worker", "bundle", "gold"):
        p.add_argument("--" + flag, required=True, type=pathlib.Path)
    a = p.parse_args()
    gold = json.loads(a.gold.read_text())
    if gold.get("commit") != PIN or gold.get("repo") != "Sample-Outbox":
        raise ValueError("gold does not identify the pinned Sample-Outbox source")
    a.output.mkdir(parents=True, exist_ok=False)
    commands = []

    def run(label, argv):
        argv = list(map(str, argv))
        commands.append({"label": label, "argv": argv})
        (a.output / "commands.json").write_text(json.dumps(commands, indent=2) + "\n")
        with (a.output / (label + ".stdout")).open("w") as out, (a.output / (label + ".stderr")).open("w") as err:
            subprocess.run(argv, stdout=out, stderr=err, check=True, timeout=600)
        return (a.output / (label + ".stdout")).read_text()

    assert subprocess.check_output(["git", "-C", str(a.source), "rev-parse", "HEAD"], text=True).strip() == PIN
    tracked = subprocess.check_output(["git", "-C", str(a.source), "ls-files", "-z"]).decode().split("\0")
    before = {name: digest(a.source / name) for name in tracked if name}
    assert not subprocess.check_output(["git", "-C", str(a.source), "diff", "HEAD", "--", "."])
    evidence = {"commit": PIN, "origin": ORIGIN, "gold_sha256": digest(a.gold), "worker_sha256": digest(a.worker), "source_sha256": before}
    (a.output / "inputs.json").write_text(json.dumps(evidence, indent=2) + "\n")
    roots = {}
    artifacts = []
    for project in ("Api", "Worker"):
        artifact = a.output / (project + ".semantic")
        result = json.loads(run("capture-" + project, [a.moe, "semantic", "capture", "--checkout", a.source,
            "--commit", PIN, "--origin", ORIGIN, "--repo", "Sample-Outbox", "--project", f"src/Sample.{project}/Sample.{project}.csproj",
            "--framework", "net8.0", "--configuration", "Debug", "--dotnet", a.dotnet, "--sdk-path", a.sdk,
            "--worker", a.worker, "--workspace", a.output / ("workspace-" + project), "--output", artifact,
            "--dependency-bundle", a.bundle, "--restore-offline", "--timeout", "5m"]))
        payload = json.loads(base64.b64decode(json.loads(artifact.read_text())["payload"]))
        for snapshot in payload["snapshots"]:
            roots[snapshot["id"]] = result["workspace"]
        artifacts.append(artifact)
    composed = a.output / "composed.semantic"
    run("compose", [a.moe, "semantic", "compose", "--input", artifacts[0], "--input", artifacts[1], "--output", composed])
    mapping = a.output / "roots.json"
    mapping.write_text(json.dumps(roots, indent=2) + "\n")
    corpus = a.output / "corpus"
    corpus.mkdir()
    run("clone-publication", ["git", "clone", "--no-hardlinks", a.source, corpus / "Sample-Outbox"])
    run("publish", [a.moe, "index", "snapshot", "build", "--corpus", corpus, "--index-dir", a.output / "index",
        "--id", "application-real", "--graph=false", "--semantic-artifact", composed, "--semantic-workspaces", mapping])
    assert before == {name: digest(a.source / name) for name in before}
    evidence["source_unchanged"] = True
    evidence["composed_sha256"] = digest(composed)
    (a.output / "inputs.json").write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    main()
