# External evaluation datasets

Public fixtures remain synthetic or drawn from public source. Repository-specific
labels, acquisition scopes, operational observations and calibrated benchmark
thresholds live outside this repository.

For corpus acquisition, set `MOEDEX_GITLAB_HOST` and supply a nonempty allowlist with
`moedex corpus init -groups /path/to/groups.txt` (or set `MOEDEX_CORPUS_GROUPS`). Existing catalog and lock files
retain the configured host. No private namespace list is embedded in the binary.

For corpus evaluation, set `MOEDEX_EVAL_DATASET` to a version-1 JSON manifest and
`MOEDEX_CORPUS_ROOT` (or `MOEDEX_CORPUS`) to the accessible corpus root. Without a
manifest, corpus-specific gates skip; malformed explicit configuration fails.
The manifest contains:

- `repos`: repository labels, root-relative `rel_dir`, optional `include_prefix`,
  `extension`, and `exclude_contains`, `exclude_prefix`, `exclude_suffix` filters.
- `strata`: named arrays of `{ "Query": "...", "Relevant": { "src/File.cs": 2 } }`.
- `pooled_strata`: names of the strata included in the pooled evaluation.
- `thresholds`: named numeric gates between zero and one, calibrated for that dataset.

Repository and relevance paths must remain relative to the selected roots.
Positive grades mark relevance; zero and negative grades preserve judged
distractors for the utility/distraction metric.
Optional `agent_nl` and `seed` strata support the corresponding measurements;
seed measurement also requires `MOEDEX_EVAL_CORPUS` to identify its repository.

Run `go test ./internal/eval -run TestCorpus -count=1 -v` with the manifest and root
configured. Keep private manifests and output logs out of public commits and CI
artifacts. The loader never acquires repositories or changes access permissions.
