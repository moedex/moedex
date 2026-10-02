# Resume the native comparison on the build-capable machine

The current machine has completed everything in this packet without private-feed
access. The user confirmed CodeGraph dependencies require another machine.
This bundle carries the probe, failed-setup evidence, reference fingerprint and
paired-result harness. It contains no credentials, private package binaries,
corpora, indexes or running services. Transfer the Moedex project separately if
continuing product development; this packet is not a full project backup.

## What is already established

- Moedex's compact artifact gate passes on Quartz: 56,658,037 bytes, complete
  capture/publication/native MCP, with bounded expansion and legacy reads.
- CodeGraph API/indexer prebuilt directories differ from the checked-out source;
  source checksum mismatches are 2 and 30 respectively, with 11 missing indexer
  documents. Fifteen common DLL filenames differ in hash.
- The paired-result harness is implemented and tested; no native CodeGraph paired
  result exists. A setup failure is not a zero quality score.
- The source fingerprint covers 1,027 reference files in the recorded scope.
  It is not an exhaustive external build-input manifest or trusted attestation.

## Check the packet, then the reference

From the extracted packet root:

```sh
python3 verify_bundle.py
python3 codegraph-native/fingerprint.py --checkout /path/to/CodeGraph --verify reference-fingerprint.json
```

A fingerprint mismatch is evidence of a different reference. Retain the report
and freeze that successor explicitly before benchmarking; do not relabel it as
the old reference. New source may be desirable, but both the product identity and
protocol must record it. The fingerprint lists paths and hashes, not source bytes.

## Build and test the metadata probe offline

Choose a fresh scratch directory and a .NET 10 SDK. The example variables are
local task variables; replace paths for the new machine.

```sh
CG_DOTNET=/path/to/dotnet
CG_SCRATCH=/path/to/new-scratch
mkdir -p "$CG_SCRATCH/probe"
cp codegraph-native/Program.cs codegraph-native/Provenance.csproj "$CG_SCRATCH/probe/"
cp offline.NuGet.Config "$CG_SCRATCH/probe/NuGet.Config"
"$CG_DOTNET" build "$CG_SCRATCH/probe/Provenance.csproj" --configfile "$CG_SCRATCH/probe/NuGet.Config"
python3 codegraph-native/test_probe.py --dotnet "$CG_DOTNET" --probe "$CG_SCRATCH/probe/bin/Debug/net10.0/Provenance.dll" --output "$CG_SCRATCH/controls"
python3 -m unittest discover -s codegraph-native -p 'test_fingerprint.py'
python3 -m unittest discover -s agent-journeys -p 'test_*.py'
```

## Rebuild the actual native products

The old `evidence/setup.json` lists exact package identities from each host's
assets file, including 62 API and 58 indexer TC.* entries. Use the new machine's
authorized dependency setup. Do not copy credentials into this packet or reports.
Do not fabricate NuGet packages from loose DLLs. Freeze the resolved packages and
SDK with hashes after restore and retain restore/build logs. The intended host
projects are:

- `src/TC.CodeGraphApi/TC.CodeGraphApi.csproj`
- `src/TC.CodeGraphApi.Indexer.Host/TC.CodeGraphApi.Indexer.Host.csproj`

Build both from the same frozen source in a fresh workspace. The local audit
adapter expects `bin/Debug/net10.0`; explicitly update and record that selection
if the coherent build uses a different configuration/framework. Then run:

```sh
python3 codegraph-native/audit.py --checkout /path/to/coherent/CodeGraph --dotnet "$CG_DOTNET" --probe "$CG_SCRATCH/probe/bin/Debug/net10.0/Provenance.dll" --output "$CG_SCRATCH/native-audit"
```

Its `ready: false` is intentional: it checks provenance consistency, not native
host readiness. Review every mismatch/missing/unmapped document and runtime
closure; a PDB match alone is insufficient provenance.

## Native setup and benchmark sequence

Use a disposable service environment with fresh data/configuration and restricted
network reach. The inspected hosts have explicit all-interface listeners on
5037/5042; API startup uses Consul-oriented configuration, PAT-authenticated MCP
and background documentation generation. No launch recipe is claimed tested.
Verify native configuration/schema/auth and background-service behavior before
launch. Keep embeddings/model-backed enrichment choices explicit for both arms;
none were used in the completed Moedex Quartz storage smoke.

Record native initialization and the complete tool catalog before assigning any
solver. The Moedex `prepare.py` transport adapter must not be assumed compatible
with CodeGraph's native session/authentication behavior. Verify indexing completion
and tool access on a development fixture before choosing a fresh heldout corpus.

Freeze a common source-authored corpus/prompt/rubric/protocol/budget contract,
product manifests and allowed capabilities before scored queries. Current default
journey budgets are 24 attempted calls, 131072 full response bytes, 600 seconds
from assignment, and an 8192-byte display cap. Read
`agent-journeys/COMPARISON.md` for exact input contracts and exclusions. Independent
solvers/reviewers still need an explicit delegation scope; the completed harness
subagent authorization was not a standing authorization for scoring teams.

After native runs and independent transcript/scoring review:

```sh
python3 agent-journeys/compare.py --root /path/to/evidence --contract /path/to/evidence/contract.json --arm /path/to/evidence/moedex.json --arm /path/to/evidence/codegraph.json --output /path/to/new-comparison.json
```

Check reported coverage, not just exit status. Do not reuse Quartz,
CleanArchitecture or Sample-Outbox as fresh heldout evidence after tuning. Preserve
all failed setup attempts and historical results. No claim of paired superiority
is warranted until the native execution and reviewed outcomes exist.
