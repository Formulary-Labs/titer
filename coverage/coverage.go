// Package coverage implements the control coverage matrix computation for
// titer. It reads a gemara ControlCatalog and optional evidence sources to
// produce a CoverageMatrix — the structured output consumed by risk registers,
// auditor views, and portfolio health dashboards.
//
// The core formula (from functions/control-coverage-spec.md):
//
//	coverage_pct = (evidenced + implemented_no_evidence) / (total - not_applicable)
package coverage

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	gemara "github.com/gemaraproj/go-gemara"

	"github.com/Formulary-Labs/substrate/artifact"
)

// CoverageStatus represents the coverage state of a single control.
type CoverageStatus string //nolint:revive // stutter is intentional

const (
	// Evidenced means implementation is documented and evidence is available.
	Evidenced CoverageStatus = "evidenced"
	// ImplementedNoEvidence means implementation is likely but no artifact exists.
	ImplementedNoEvidence CoverageStatus = "implemented_no_evidence"
	// Gap means the control is not implemented or there is no information.
	Gap CoverageStatus = "gap"
	// NotApplicable means the control is explicitly scoped out.
	NotApplicable CoverageStatus = "not_applicable"
)

// ControlCoverage is the coverage record for a single control.
type ControlCoverage struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Family         string         `json:"family"`
	Status         CoverageStatus `json:"status"`
	Evidence       string         `json:"evidence,omitempty"`
	Owner          string         `json:"owner,omitempty"`
	Notes          string         `json:"notes,omitempty"`
	Severity       string         `json:"severity,omitempty"` // from catalog tier/severity metadata
	InferenceFlags []string       `json:"inference_flags,omitempty"`
}

// FamilyCoverage is the aggregate coverage for a control family or group.
type FamilyCoverage struct {
	Family                string `json:"family"`
	Total                 int    `json:"total"`
	Evidenced             int    `json:"evidenced"`
	ImplementedNoEvidence int    `json:"implemented_no_evidence"`
	Gap                   int    `json:"gap"`
	NotApplicable         int    `json:"not_applicable"`
	CoveragePct           *int   `json:"coverage_pct"` // nil when denominator is 0
	Owner                 string `json:"owner,omitempty"`
}

// Totals is the aggregate across all families.
type Totals struct {
	Total                 int  `json:"total"`
	Evidenced             int  `json:"evidenced"`
	ImplementedNoEvidence int  `json:"implemented_no_evidence"`
	Gap                   int  `json:"gap"`
	NotApplicable         int  `json:"not_applicable"`
	CoveragePct           *int `json:"coverage_pct"`
}

// CoverageMatrix is the full output of a titer run.
type CoverageMatrix struct { //nolint:revive // stutter is intentional
	Framework      string            `json:"framework"`
	AssessmentDate string            `json:"assessment_date"`
	Program        string            `json:"program"`
	MaterialsBasis []string          `json:"materials_basis"`
	Totals         Totals            `json:"totals"`
	Families       []FamilyCoverage  `json:"families"`
	Controls       []ControlCoverage `json:"controls"` // control-level detail
	CoverageGaps   []CoverageGap     `json:"coverage_gaps,omitempty"`
	OwnerGaps      []OwnerGap        `json:"owner_gaps,omitempty"`
	EvidenceGaps   []EvidenceGap     `json:"evidence_gaps,omitempty"`
}

// CoverageGap describes a control with Gap status.
type CoverageGap struct { //nolint:revive // stutter is intentional
	ControlID   string  `json:"control_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Priority    string  `json:"priority"`      // high, medium, low
	ALE         float64 `json:"ale,omitempty"` // Annualized Loss Expectancy in USD (from specimen risk register)
}

// OwnerGap describes a control with no assigned owner.
type OwnerGap struct {
	ControlID     string `json:"control_id"`
	Title         string `json:"title"`
	SuggestedType string `json:"suggested_type"`
}

// EvidenceGap describes a control that is implemented but not evidenced.
type EvidenceGap struct {
	ControlID     string `json:"control_id"`
	Title         string `json:"title"`
	WhatExists    string `json:"what_exists"`
	WhatNeeded    string `json:"what_needed"`
	EffortToClose string `json:"effort_to_close"` // low, medium, high
}

// Options configures a titer run.
type Options struct {
	Program         string
	CatalogPath     string
	SOAPath         string                    // optional soa.csv from CDG
	EvidenceMap     map[string]string         // control_id → evidence description
	OwnerMap        map[string]string         // control_id → owner name
	StatusOverrides map[string]CoverageStatus // control_id → explicit status
	ALEMap          map[string]float64        // control_id → ALE in USD (from specimen risk register)
}

// Compute reads the catalog and evidence sources and produces a CoverageMatrix.
func Compute(opts Options) (*CoverageMatrix, error) {
	catalog, err := artifact.LoadControlCatalog(opts.CatalogPath)
	if err != nil {
		return nil, fmt.Errorf("loading control catalog: %w", err)
	}

	// Load SOA CSV if provided.
	soaRows := map[string]soaRow{}
	if opts.SOAPath != "" {
		rows, err := loadSOA(opts.SOAPath)
		if err != nil {
			return nil, fmt.Errorf("loading SOA CSV: %w", err)
		}
		for _, r := range rows {
			soaRows[r.ControlID] = r
		}
	}

	// Build control-level coverage.
	controls := computeControls(catalog, soaRows, opts)

	// Aggregate into families.
	families := aggregateFamilies(controls)

	// Compute totals.
	totals := computeTotals(families)

	// Gap analysis.
	cGaps, oGaps, eGaps := analyzeGaps(controls, opts.ALEMap)

	basis := []string{opts.CatalogPath}
	if opts.SOAPath != "" {
		basis = append(basis, opts.SOAPath)
	}

	return &CoverageMatrix{
		Framework:      catalog.Metadata.Id,
		AssessmentDate: time.Now().UTC().Format("2006-01-02"),
		Program:        opts.Program,
		MaterialsBasis: basis,
		Totals:         totals,
		Families:       families,
		Controls:       controls,
		CoverageGaps:   cGaps,
		OwnerGaps:      oGaps,
		EvidenceGaps:   eGaps,
	}, nil
}

// computeControls builds a ControlCoverage for every control in the catalog.
func computeControls(catalog *gemara.ControlCatalog, soaRows map[string]soaRow, opts Options) []ControlCoverage {
	var controls []ControlCoverage

	for _, c := range catalog.Controls {
		family := familyFromGroup(c.Group, catalog)
		cc := ControlCoverage{
			ID:     c.Id,
			Title:  c.Title,
			Family: family,
		}

		// Determine status: status override > SOA row > evidence map > default Gap.
		if s, ok := opts.StatusOverrides[c.Id]; ok {
			cc.Status = s
		} else if row, ok := soaRows[c.Id]; ok {
			cc.Status = soaStatusToCoverage(row.ImplementationStatus)
			cc.Evidence = row.EvidenceRef
			cc.Owner = row.Owner
		} else if ev, ok := opts.EvidenceMap[c.Id]; ok {
			cc.Status = Evidenced
			cc.Evidence = ev
		} else {
			cc.Status = Gap
			cc.InferenceFlags = []string{"[INSUFFICIENT DATA — requires assessment]"}
		}

		if owner, ok := opts.OwnerMap[c.Id]; ok && cc.Owner == "" {
			cc.Owner = owner
		}

		controls = append(controls, cc)
	}

	return controls
}

// aggregateFamilies groups controls by family and computes per-family counts.
func aggregateFamilies(controls []ControlCoverage) []FamilyCoverage {
	familyMap := map[string]*FamilyCoverage{}
	familyOrder := []string{}

	for _, c := range controls {
		fam := c.Family
		if fam == "" {
			fam = "Ungrouped"
		}
		if _, ok := familyMap[fam]; !ok {
			familyMap[fam] = &FamilyCoverage{Family: fam}
			familyOrder = append(familyOrder, fam)
		}
		fc := familyMap[fam]
		fc.Total++
		switch c.Status {
		case Evidenced:
			fc.Evidenced++
		case ImplementedNoEvidence:
			fc.ImplementedNoEvidence++
		case Gap:
			fc.Gap++
		case NotApplicable:
			fc.NotApplicable++
		}
		if c.Owner != "" && fc.Owner == "" {
			fc.Owner = c.Owner
		}
	}

	// Calculate coverage %.
	for _, fam := range familyOrder {
		fc := familyMap[fam]
		denom := fc.Total - fc.NotApplicable
		if denom > 0 {
			pct := (fc.Evidenced + fc.ImplementedNoEvidence) * 100 / denom
			fc.CoveragePct = &pct
		}
	}

	result := make([]FamilyCoverage, 0, len(familyOrder))
	for _, fam := range familyOrder {
		result = append(result, *familyMap[fam])
	}
	return result
}

// computeTotals aggregates family counts into overall totals.
func computeTotals(families []FamilyCoverage) Totals {
	t := Totals{}
	for _, f := range families {
		t.Total += f.Total
		t.Evidenced += f.Evidenced
		t.ImplementedNoEvidence += f.ImplementedNoEvidence
		t.Gap += f.Gap
		t.NotApplicable += f.NotApplicable
	}
	denom := t.Total - t.NotApplicable
	if denom > 0 {
		pct := (t.Evidenced + t.ImplementedNoEvidence) * 100 / denom
		t.CoveragePct = &pct
	}
	return t
}

// analyzeGaps produces the three gap lists from control-level coverage.
// aleMap optionally provides Annualized Loss Expectancy (USD) keyed by control ID,
// sourced from a specimen risk register; zero or absent means no FAIR data.
func analyzeGaps(controls []ControlCoverage, aleMap map[string]float64) ([]CoverageGap, []OwnerGap, []EvidenceGap) {
	var cGaps []CoverageGap
	var oGaps []OwnerGap
	var eGaps []EvidenceGap

	for _, c := range controls {
		switch c.Status {
		case Gap:
			gap := CoverageGap{
				ControlID:   c.ID,
				Title:       c.Title,
				Description: "Control not implemented or insufficient data",
				Priority:    gapPriority(c.Severity),
			}
			if aleMap != nil {
				gap.ALE = aleMap[c.ID]
			}
			cGaps = append(cGaps, gap)
		case ImplementedNoEvidence:
			eGaps = append(eGaps, EvidenceGap{
				ControlID:     c.ID,
				Title:         c.Title,
				WhatExists:    c.Evidence,
				WhatNeeded:    "Documented evidence artifact",
				EffortToClose: "low",
			})
		}
		if c.Owner == "" && c.Status != NotApplicable {
			oGaps = append(oGaps, OwnerGap{
				ControlID:     c.ID,
				Title:         c.Title,
				SuggestedType: "Control owner (technical or process lead)",
			})
		}
	}
	return cGaps, oGaps, eGaps
}

// familyFromGroup resolves the family name from a control's Group ID.
func familyFromGroup(groupID string, catalog *gemara.ControlCatalog) string {
	if groupID == "" {
		return ""
	}
	for _, g := range catalog.Groups {
		if g.Id == groupID {
			return g.Title
		}
	}
	return groupID
}

// soaRow is a single row from a CDG-produced soa.csv.
type soaRow struct {
	ControlID            string
	ImplementationStatus string
	EvidenceRef          string
	Owner                string
}

// loadSOA reads a CDG soa.csv file into a slice of soaRows.
// Column detection is flexible — searches by header name.
func loadSOA(path string) ([]soaRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening SOA CSV: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only CSV, close error is harmless

	r := csv.NewReader(f)
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading SOA CSV headers: %w", err)
	}

	// Find column indices.
	colIdx := func(name string) int {
		name = strings.ToLower(name)
		for i, h := range headers {
			if strings.ToLower(h) == name {
				return i
			}
		}
		return -1
	}
	idCol := colIdx("control_id")
	if idCol < 0 {
		idCol = colIdx("control id")
	}
	if idCol < 0 {
		idCol = 0 // fallback to first column
	}
	statusCol := colIdx("implementation_status")
	if statusCol < 0 {
		statusCol = colIdx("status")
	}
	evidenceCol := colIdx("evidence_ref")
	if evidenceCol < 0 {
		evidenceCol = colIdx("evidence")
	}
	ownerCol := colIdx("owner")

	var rows []soaRow
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading SOA CSV row: %w", err)
		}
		row := soaRow{}
		if idCol < len(record) {
			row.ControlID = record[idCol]
		}
		if statusCol >= 0 && statusCol < len(record) {
			row.ImplementationStatus = record[statusCol]
		}
		if evidenceCol >= 0 && evidenceCol < len(record) {
			row.EvidenceRef = record[evidenceCol]
		}
		if ownerCol >= 0 && ownerCol < len(record) {
			row.Owner = record[ownerCol]
		}
		if row.ControlID != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// soaStatusToCoverage maps CDG soa.csv implementation status strings to
// CoverageStatus values.
func soaStatusToCoverage(s string) CoverageStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "implemented", "evidenced", "applicable", "pass":
		return Evidenced
	case "partial", "in_progress", "in progress", "implemented_no_evidence", "planned":
		return ImplementedNoEvidence
	case "no", "not implemented", "gap", "fail", "not_implemented":
		return Gap
	case "n/a", "not_applicable", "not applicable", "na", "excluded":
		return NotApplicable
	default:
		return Gap
	}
}

// gapPriority derives gap priority from control severity metadata.
// Falls back to "medium" if severity is empty or unrecognized.
func gapPriority(severity string) string {
	switch severity {
	case "critical", "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

// ExportedComputeTotals exposes computeTotals for package-external tests.
func ExportedComputeTotals(families []FamilyCoverage) Totals {
	return computeTotals(families)
}

// ToJSON serializes the CoverageMatrix to indented JSON.
func (m *CoverageMatrix) ToJSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// ToMarkdown produces a markdown coverage matrix report.
func (m *CoverageMatrix) ToMarkdown() string {
	var b strings.Builder

	covStr := "—"
	if m.Totals.CoveragePct != nil {
		covStr = fmt.Sprintf("%d%%", *m.Totals.CoveragePct)
	}

	fmt.Fprintf(&b, "# Control Coverage Matrix — %s\n\n", m.Framework)
	fmt.Fprintf(&b, "**Program:** %s  \n", m.Program)
	fmt.Fprintf(&b, "**Assessment date:** %s  \n", m.AssessmentDate)
	fmt.Fprintf(&b, "**Materials basis:** %s  \n\n", strings.Join(m.MaterialsBasis, ", "))

	t := m.Totals
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "| Metric | Count |\n|---|---|\n")
	fmt.Fprintf(&b, "| Total controls | %d |\n", t.Total)
	fmt.Fprintf(&b, "| Evidenced (✓) | %d |\n", t.Evidenced)
	fmt.Fprintf(&b, "| Implemented / no evidence (~) | %d |\n", t.ImplementedNoEvidence)
	fmt.Fprintf(&b, "| Gap (✗) | %d |\n", t.Gap)
	fmt.Fprintf(&b, "| Not applicable (N/A) | %d |\n", t.NotApplicable)
	fmt.Fprintf(&b, "| **Coverage %%** | **%s** |\n\n", covStr)

	if len(m.Families) > 0 {
		fmt.Fprintf(&b, "## Coverage by Family\n\n")
		fmt.Fprintf(&b, "| Family | Controls | ✓ | ~ | ✗ | N/A | Owner | Coverage %% |\n")
		fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|\n")
		for _, f := range m.Families {
			pctStr := "—"
			if f.CoveragePct != nil {
				pctStr = fmt.Sprintf("%d%%", *f.CoveragePct)
			}
			owner := f.Owner
			if owner == "" {
				owner = "[OWNER NEEDED]"
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %s | %s |\n",
				f.Family, f.Total, f.Evidenced, f.ImplementedNoEvidence,
				f.Gap, f.NotApplicable, owner, pctStr)
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(m.CoverageGaps) > 0 {
		fmt.Fprintf(&b, "## Coverage Gaps\n\n")
		fmt.Fprintf(&b, "| Control | Description | Priority |\n|---|---|---|\n")
		for _, g := range m.CoverageGaps {
			fmt.Fprintf(&b, "| %s — %s | %s | %s |\n", g.ControlID, g.Title, g.Description, g.Priority)
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(m.OwnerGaps) > 0 {
		fmt.Fprintf(&b, "## Owner Gaps\n\n")
		fmt.Fprintf(&b, "| Control | Suggested owner type |\n|---|---|\n")
		for _, g := range m.OwnerGaps {
			fmt.Fprintf(&b, "| %s — %s | %s |\n", g.ControlID, g.Title, g.SuggestedType)
		}
		fmt.Fprintf(&b, "\n")
	}

	if len(m.EvidenceGaps) > 0 {
		fmt.Fprintf(&b, "## Evidence Gaps (Implemented / No Evidence)\n\n")
		fmt.Fprintf(&b, "| Control | What exists | What is needed | Effort |\n|---|---|---|---|\n")
		for _, g := range m.EvidenceGaps {
			fmt.Fprintf(&b, "| %s — %s | %s | %s | %s |\n",
				g.ControlID, g.Title, g.WhatExists, g.WhatNeeded, g.EffortToClose)
		}
		fmt.Fprintf(&b, "\n")
	}

	return b.String()
}
