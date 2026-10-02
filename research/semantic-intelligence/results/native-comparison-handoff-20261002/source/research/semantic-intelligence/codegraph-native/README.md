# Native CodeGraph provenance preflight

This offline preflight reads PE/portable-PDB metadata without loading or executing
CodeGraph assemblies. It compares PDB document checksums with the checked-out
source, verifies CodeView GUID/stamp linkage, inventories API/indexer binary
hashes, and checks package-directory presence in recorded/default NuGet caches.
It does not read NuGet credentials, contact feeds, start services, modify the
reference checkout, or query a product.

Build the probe in a **fresh scratch directory**, preserving the reference:

```sh
mkdir -p /scratch/codegraph-probe
cp research/semantic-intelligence/codegraph-native/{Program.cs,Provenance.csproj} /scratch/codegraph-probe/
printf '%s\n' '<configuration><packageSources><clear /></packageSources></configuration>' > /scratch/codegraph-probe/NuGet.Config
/path/to/dotnet build /scratch/codegraph-probe/Provenance.csproj --configfile /scratch/codegraph-probe/NuGet.Config
python3 research/semantic-intelligence/codegraph-native/audit.py \
  --checkout .references/CodeGraph --dotnet /path/to/dotnet \
  --probe /scratch/codegraph-probe/bin/Debug/net10.0/Provenance.dll \
  --output /scratch/codegraph-audit
```

Requires an installed .NET 10 SDK with its reference packs. The probe has no
external package references. Current adapter paths explicitly select the
reference's `bin/Debug/net10.0` API and indexer builds; adapt and record the
selection for other layouts. Input PDBs/checkouts are trusted local files.
Generated `obj` documents are reported separately, never hidden. Documents
outside a `/src/` suffix are reported as unmapped; the probe does not read their
original machine paths. It reports documents present in PDBs, not an exhaustive
build-input or repository-source roster.

`setup.json` always says `ready: false`: this audit cannot establish native
service readiness even if all checksums match. This is a compatible failed-setup
record for the [comparison harness](../agent-journeys/COMPARISON.md), not a
competitor score. A process exit of zero means the audit completed. Its output
directory must not exist; existing evidence is never replaced.

PDB identity/hash consistency is not a trusted build attestation. Missing package
folders do not rule out alternative caches/feeds, and directory presence does not
verify package bytes. Different DLL hashes alone do not establish runtime
incompatibility. A rebuild must freeze all build inputs and dependencies, then
repeat source consistency and runtime setup checks.

Run the five fixture controls in a fresh output directory:

```sh
python3 research/semantic-intelligence/codegraph-native/test_probe.py \
  --dotnet /path/to/dotnet \
  --probe /scratch/codegraph-probe/bin/Debug/net10.0/Provenance.dll \
  --output /scratch/codegraph-probe-controls
```

The controls verify matching source, changed source, missing source, an unrelated
PDB and a missing PDB. They exercise the metadata reader without any CodeGraph
service or package dependency. See the [recorded audit](../CODEGRAPH-NATIVE-PREFLIGHT.md).
