# titer

How much of your control catalog actually has evidence behind it? `titer` computes that number by family, surfaces the gaps, and tells you what's not applicable so it doesn't inflate the count.

```bash
go get github.com/Formulary-Labs/titer
```

## In the ecosystem

Formulary is a **pick-and-choose** suite of small, single-purpose CLIs. Install only the tools your workflow needs; each runs standalone. Shared contracts live in [`substrate`](https://github.com/Formulary-Labs/substrate). Optional Make pipelines (or an agent such as `regimen`) compose tools into a run — plug-and-play, not all-or-nothing.

**This tool:** control coverage matrix and gap analysis.  
**Works with:** `formula` (SoA optional), `exhibit` / `vital` (consumers).  
**Pipeline stage:** packet.


## What it does

`titer` reads an OSCAL catalog or a gemara `ControlCatalog` and an optional SOA CSV, then computes a `CoverageMatrix` — a structured snapshot of what is evidenced, implemented, gapped, or not applicable across every control family. The same computation works for any framework represented as OSCAL or gemara: ISO 27001, ISO 42001, IEC 62443, NIST SP 800-53, or a custom catalog.

## Usage

```go
import "github.com/Formulary-Labs/titer/coverage"

matrix, err := coverage.Compute(coverage.Options{
    Program:     "my-program",
    CatalogPath: "catalog.yaml",
    SOAPath:     "output/soa.csv",   // optional — overlays real implementation status
    EvidenceMap: map[string]string{
        "A.5.1": "evidence/policy-review-2026.pdf",
        "A.8.3": "evidence/access-log-review-2026-Q3.pdf",
    },
    OwnerMap: map[string]string{
        "A.5.1": "security-team",
        "A.8.3": "it-ops",
    },
})
```

Without a `SOAPath`, `titer` uses the evidence map alone to determine status. With a SOA CSV, it overlays the SOA's inclusion/exclusion decisions and implementation status on top of the catalog.

## Coverage formula

```
coverage_pct = (evidenced + implemented_no_evidence) / (total - not_applicable)
```

### Control statuses

| Status | Meaning |
|---|---|
| `evidenced` | Control is implemented and evidence is linked |
| `implemented_no_evidence` | Control is likely implemented; no evidence artifact linked yet |
| `gap` | Control is not implemented, or no information is available |
| `not_applicable` | Control is explicitly excluded from scope |

`not_applicable` controls are excluded from the denominator. The coverage percentage reflects only in-scope controls.

## Output

`CoverageMatrix` serializes to JSON or Markdown.

### JSON

```json
{
  "framework": "iso27001",
  "assessment_date": "2026-09-18",
  "program": "my-program",
  "totals": {
    "total": 114,
    "evidenced": 82,
    "implemented_no_evidence": 10,
    "gap": 18,
    "not_applicable": 4,
    "coverage_pct": 81
  },
  "families": [...],
  "controls": [...],
  "coverage_gaps": [
    { "control_id": "A.8.3", "title": "...", "priority": "high" }
  ],
  "owner_gaps": [
    { "control_id": "A.12.1", "title": "...", "suggested_type": "operational" }
  ],
  "evidence_gaps": [
    { "control_id": "A.5.2" }
  ]
}
```

The three gap lists — `coverage_gaps`, `owner_gaps`, `evidence_gaps` — are the primary action items. They feed directly into `specimen` as risk entries and into `exhibit` as the coverage section of the auditor dashboard.

### Markdown

```go
md := matrix.ToMarkdown()
```

Produces a summary metrics block followed by a per-family table and the three gap lists. Paste it into a status document or weekly brief.

## Gap types

| Gap type | What it means | Downstream action |
|---|---|---|
| Coverage gap | Control not implemented or no information available | Log risk in `specimen` (`source: coverage_gap`) |
| Owner gap | Control has no assigned responsible party | Assign owner; log risk in `specimen` (`source: owner_gap`) |
| Evidence gap | Control is implemented but no evidence artifact is linked | Collect and link evidence |

## Catalogs

`titer` reads an OSCAL catalog or a gemara `ControlCatalog` to compute the coverage matrix. See [CATALOGS.md](https://github.com/Formulary-Labs/.github/blob/main/CATALOGS.md) for known upstream catalog sources and guidance on authoring your own.

## Pipeline context

`titer` runs after `assay` produces an assessment result (which is the most complete source for implementation status). It is also useful earlier in a cycle as a lightweight coverage snapshot from just a SOA CSV and evidence map, before a full assessment run.

```bash
titer --catalog catalog.yaml --soa output/soa.csv --program iso27001 > coverage.json

# Gap severity filter
titer --catalog catalog.yaml --soa output/soa.csv --severity high

# FAIR ALE cross-reference: filter gaps to those with an annualized loss
# exposure at or above a USD threshold, using a specimen register export
titer --catalog catalog.yaml --soa output/soa.csv \
  --risk-register risks-list.json --min-exposure-usd 50000
```

Also available: `--owners` (JSON owner map `{control_id: owner}`) and `--quiet`. Output formats: `json` (default), `md`, `csv`.

Output feeds `vital` (coverage health dimension), `exhibit` (coverage section), and `formula` (for SOA generation with gap annotations).

## License

Apache License 2.0
