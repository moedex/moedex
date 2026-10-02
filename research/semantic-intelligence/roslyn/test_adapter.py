#!/usr/bin/env python3
"""Hermetic regression checks for the experimental project subset, not MSBuild fidelity."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("--dotnet", required=True)
parser.add_argument("--dll", required=True)
parser.add_argument("--framework-dir", required=True)
args = parser.parse_args()


def project(root, name, source, extra="", props=""):
    folder = root / name
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "p.csproj").write_text(
        '<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup>'
        '<TargetFramework>net10.0</TargetFramework>'
        f'<AssemblyName>{name}</AssemblyName>{props}</PropertyGroup>{extra}</Project>'
    )
    (folder / "p.cs").write_bytes(source)
    return folder / "p.csproj"


def run(root, path):
    result = subprocess.run(
        [args.dotnet, args.dll, str(path), "--root", str(root),
         "--framework-dir", args.framework_dir],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60, check=False,
    )
    records = [json.loads(line) for line in result.stdout.splitlines()]
    return result, records


def summary(records, name):
    return next(r for r in records if r["record_type"] == "summary" and r["project"] == f"{name}/p.csproj")


with tempfile.TemporaryDirectory(prefix="moedex-roslyn-regression-") as tmp:
    root = Path(tmp)
    passed = []
    project(root, "BrokenDep", b"public class Usable {} public class Broken { MissingType field; }")
    parent = project(root, "Parent", b"public class Parent { public Usable Value; }",
                     '<ItemGroup><ProjectReference Include="../BrokenDep/p.csproj" /></ItemGroup>')
    result, rows = run(root, parent)
    assert result.returncode == 2, result.stderr
    assert summary(rows, "Parent")["compilation_status"] == "incomplete"
    assert any("incomplete_project_reference:BrokenDep/p.csproj" in r.get("issues", []) for r in rows)
    passed.append("dependency_compiler_errors_propagate")

    for attribute in ['ReferenceOutputAssembly="false"', 'Aliases="restricted"']:
        path = project(root, "RefAttrs", b"public class C {}",
                       f'<ItemGroup><ProjectReference Include="../BrokenDep/p.csproj" {attribute} /></ItemGroup>')
        result, rows = run(root, path)
        assert result.returncode == 2
        assert summary(rows, "RefAttrs")["compilation_status"] == "unsupported"
        assert not any(r["record_type"] in ("declaration", "reference") and r["project"] == "RefAttrs/p.csproj" for r in rows)
    passed.append("project_reference_attributes_rejected")

    path = project(root, "Exclude", b"public class C {}", props='<DefaultItemExcludes>p.cs</DefaultItemExcludes>')
    result, rows = run(root, path)
    assert result.returncode == 2 and summary(rows, "Exclude")["compilation_status"] == "unsupported"
    passed.append("unsupported_default_item_exclusions_rejected")

    path = project(root, "Missing", b"public class C {}", '<ItemGroup><Compile Include="missing.cs" /></ItemGroup>')
    result, rows = run(root, path)
    assert result.returncode == 1 and b"explicit compile source absent" in result.stderr
    passed.append("missing_explicit_compile_input_fails")

    path = project(root, "Remove", b"public class C {}", '<ItemGroup><Compile Remove="missing.cs" /></ItemGroup>')
    result, rows = run(root, path)
    assert result.returncode == 0, result.stderr
    passed.append("nonexistent_compile_remove_allowed")

    text = 'public class Unicode { /* π😀 */ public void Method() { Method(); } }\r\n'
    raw = b'\xef\xbb\xbf' + text.encode('utf-8')
    path = project(root, "Unicode", raw)
    result, rows = run(root, path)
    assert result.returncode == 0, result.stderr
    second, _ = run(root, path)
    assert second.stdout == result.stdout
    occurrences = [r for r in rows if r["record_type"] in ("declaration", "reference")]
    assert occurrences
    for record in occurrences:
        offset, length = record["span"]["byte_offset"], record["span"]["byte_length"]
        assert raw[offset:offset + length].decode('utf-8') == record['source_text'], record
    assert any(r["source_text"] == "Method" and r["span"]["byte_offset"] > r["span"]["utf16_offset"] + 3 for r in occurrences)
    passed.append("bom_unicode_crlf_exact_spans_and_determinism")

    print(json.dumps({"passed": passed, "count": len(passed)}, indent=2))
