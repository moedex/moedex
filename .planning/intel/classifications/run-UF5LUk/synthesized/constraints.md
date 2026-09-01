# Staged Constraints

(None extracted.)

No document in this ingest set was classified `SPEC`. The 25 classifications resolve to
22 `ADR`, 1 `PRD`, and 2 `DOC`; zero `SPEC`.

Absent — not inferred. Several ADRs do carry contract-shaped and NFR-shaped material
(for example ADR-0022's tool input/output schema rules and protocol-version negotiation,
ADR-0021's `.ai-privacy.yml` parser grammar, ADR-0015's structured-result JSON DTO,
ADR-0005's `MOEDEX03` on-disk layout, and ADR-0014's NDCG floors). That material has been
preserved verbatim under its own ADR entry in `decisions.md` rather than reclassified into
this file, because reclassifying an ADR as a SPEC would silently change its precedence
rank. If constraints are wanted as first-class entries, re-tag those sources via
`--manifest` and re-run ingest.
