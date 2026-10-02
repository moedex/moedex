# Native comparison machine handoff

2026-10-02. The user confirmed that private CodeGraph dependencies require moving
to another machine. All local work for this setup increment is complete.

[Download the handoff packet](results/native-comparison-handoff-20261002/native-comparison-handoff.zip)
and begin with `START-HERE.md` after extraction. It is **234,577 bytes** with SHA-256
`6631648f0a5812fbfa55625d5c1ebc3b6453582dcc27609a9561486b3da3941c`.
The [same resume instructions](codegraph-native/MACHINE-HANDOFF.md) are available
in the repository. Transfer the Moedex project separately for product development;
the packet is not a full project backup.

The packet contains source for the metadata-only native preflight, its controls,
a 1,027-file reference fingerprint, failed-setup evidence, the paired-result
harness and exact setup/accounting instructions. It includes no credentials,
private package binaries, corpora or indexes. Historical reports retain their
original repository-relative links; the standalone operational instructions are
in `START-HERE.md`, and raw setup evidence is in `evidence/setup.json`.

Validation was performed on a fresh extraction, not the original script paths:
41 listed file hashes verified; all 35 journey harness tests and three fingerprint
tests passed; source verification found zero added/missing/changed files; the
probe built offline and passed all five source/PDB controls. This checks local
relocation on macOS, not execution on the destination operating system. The
scripts accept explicit SDK/reference/output paths and do not carry cache access
or host configuration assumptions as verified facts.

The next gate is a coherent API/indexer build and dependency manifest on the new
machine. The existing API has two source checksum mismatches; indexer has 30 and
11 missing documents. Fifteen shared DLLs differ. Native configuration, schema,
authentication, indexing and MCP initialization remain unexecuted. Keep their
setup outcomes separate from quality scores. The portable audit intentionally
never emits `ready: true` on metadata evidence alone.

No new solver assignments, benchmark scores or competitive claims were made.
[Sealed handoff validation](results/native-comparison-handoff-20261002/result.json)
preserves the preceding provenance archive unchanged. Moedex's completed compact
storage result and earlier holdout evidence remain intact.
