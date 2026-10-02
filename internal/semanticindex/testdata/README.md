# Semantic index compatibility fixture

`domain-v2.msi` is a genuine version 2 index captured with the pre-version-3
writer before contract postings were implemented. It contains the synthetic
`domainFixture()` artifact: three occurrences and one DI registration, with no
source checkout or user data. It is not a version 3 file with a changed header.

- Bytes: 3276
- SHA256: `046c44132964bbc91dd4689643dc4b40716cb628a82e235cc2b12dc7681ac906`
- Audit provenance: SHA256 of literal `artifact`, `c7c5c1d70c5dec4416ab6158afd0b223ef40c29b1dc1f97ed9428b94d4cadb1c`
- Corpus provenance: SHA256 of literal `corpus`, `6b17b8fcc411a533c822a5117e33cf73fa6766739d7d36ac86dc4bea883af4fe`

`TestContractImpactGenuineV2` verifies that its binding/domain evidence remains
readable and contract reverse lookup reports unsupported without a corpus scan.
