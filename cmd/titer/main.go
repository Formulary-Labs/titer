// titer computes a control coverage matrix from a gemara ControlCatalog and
// optional evidence sources.
//
// Usage:
//
//	titer [flags] --catalog <catalog.yaml>
//
// Outputs coverage percentages by family, owner gaps, and evidence gaps.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Formulary-Labs/substrate/exit"
	"github.com/Formulary-Labs/substrate/format"
	"github.com/Formulary-Labs/substrate/provenance"
	"github.com/Formulary-Labs/titer/coverage"
)

const version = "0.1.0"

func main() {
	var (
		catalogFlag      = flag.String("catalog", "", "Path to gemara ControlCatalog YAML (required)")
		soaFlag          = flag.String("soa", "", "Path to CDG soa.csv file (optional)")
		evidenceFlag     = flag.String("evidence", "", "Path to JSON evidence map {control_id: description} (optional)")
		ownerFlag        = flag.String("owners", "", "Path to JSON owner map {control_id: owner_name} (optional)")
		programFlag      = flag.String("program", "", "Program slug for provenance logging")
		fmtFlag          = flag.String("format", "json", "Output format: json (default), md, csv")
		severityFlag     = flag.String("severity", "", "Filter output to gaps of this severity: high, medium, low")
		versionFlag      = flag.Bool("version", false, "Print version and exit")
		quietFlag        = flag.Bool("quiet", false, "Suppress progress output")
		riskRegisterFlag = flag.String("risk-register", "", "Path to specimen list JSON output for FAIR ALE cross-reference (optional)")
		minExposureFlag  = flag.Float64("min-exposure-usd", 0, "Filter gaps to those with ALE >= this USD threshold (requires --risk-register)")
	)
	flag.Usage = usage
	flag.Parse()

	if *versionFlag {
		fmt.Printf("titer version %s\n", version)
		os.Exit(exit.OK)
	}

	if *catalogFlag == "" {
		fmt.Fprintln(os.Stderr, `{"error": "--catalog is required", "code": 2}`)
		flag.Usage()
		os.Exit(exit.ToolError)
	}

	f, err := format.Parse(*fmtFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error": %q, "code": 2}`+"\n", err.Error())
		os.Exit(exit.ToolError)
	}

	// Load optional evidence and owner maps.
	evidenceMap := map[string]string{}
	if *evidenceFlag != "" {
		if err := loadJSONMap(*evidenceFlag, &evidenceMap); err != nil {
			fmt.Fprintf(os.Stderr, `{"error": "loading evidence map: %v", "code": 2}`+"\n", err)
			os.Exit(exit.ToolError)
		}
	}

	ownerMap := map[string]string{}
	if *ownerFlag != "" {
		if err := loadJSONMap(*ownerFlag, &ownerMap); err != nil {
			fmt.Fprintf(os.Stderr, `{"error": "loading owner map: %v", "code": 2}`+"\n", err)
			os.Exit(exit.ToolError)
		}
	}

	// Load optional risk register for FAIR ALE cross-reference.
	var aleMap map[string]float64
	if *riskRegisterFlag != "" {
		var err error
		aleMap, err = loadALEMap(*riskRegisterFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, `{"error": "loading risk register: %v", "code": 2}`+"\n", err)
			os.Exit(exit.ToolError)
		}
	}

	opts := coverage.Options{
		Program:     *programFlag,
		CatalogPath: *catalogFlag,
		SOAPath:     *soaFlag,
		EvidenceMap: evidenceMap,
		OwnerMap:    ownerMap,
		ALEMap:      aleMap,
	}

	if !*quietFlag {
		fmt.Fprintf(os.Stderr, "[TITER] Computing coverage matrix from %s\n", *catalogFlag)
	}

	matrix, err := coverage.Compute(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error": %q, "code": 2}`+"\n", err.Error())
		os.Exit(exit.ToolError)
	}

	// Apply severity filter if requested.
	if *severityFlag != "" {
		matrix = filterBySeverity(matrix, *severityFlag)
	}

	// Apply FAIR exposure filter if requested.
	if *minExposureFlag > 0 {
		matrix = filterByMinExposure(matrix, *minExposureFlag)
	}

	switch f {
	case format.JSON:
		data, err := matrix.ToJSON()
		if err != nil {
			fmt.Fprintf(os.Stderr, "encoding output: %v\n", err)
			os.Exit(exit.ToolError)
		}
		os.Stdout.Write(data) //nolint:errcheck
		fmt.Println()
	case format.YAML:
		fmt.Fprintln(os.Stderr, `{"error": "YAML format is not supported; use --format json, md, or csv", "code": 2}`)
		os.Exit(exit.ToolError)
	case format.MD:
		fmt.Print(matrix.ToMarkdown())
	case format.CSV:
		printCSV(matrix)
	default:
		data, _ := matrix.ToJSON()
		os.Stdout.Write(data) //nolint:errcheck
	}

	if *programFlag != "" {
		covStr := "unknown"
		if matrix.Totals.CoveragePct != nil {
			covStr = fmt.Sprintf("%d%%", *matrix.Totals.CoveragePct)
		}
		_ = provenance.Write("logs/provenance.jsonl", provenance.Entry{
			Spec:        "functions/control-coverage-spec.md",
			Output:      *catalogFlag,
			OutputType:  "other",
			Program:     *programFlag,
			Purpose:     fmt.Sprintf("titer coverage: %s — %d controls, %s covered, %d gaps", matrix.Framework, matrix.Totals.Total, covStr, matrix.Totals.Gap),
			Reusability: provenance.Instance,
			QualityGate: provenance.Pass,
			Tool:        "titer",
			ToolVersion: version,
		})
	}

	// Exit 1 if any gaps exist.
	if matrix.Totals.Gap > 0 {
		os.Exit(exit.Validation)
	}
	os.Exit(exit.OK)
}

func filterBySeverity(m *coverage.CoverageMatrix, severity string) *coverage.CoverageMatrix {
	var filtered []coverage.CoverageGap
	for _, g := range m.CoverageGaps {
		if g.Priority == severity {
			filtered = append(filtered, g)
		}
	}
	m.CoverageGaps = filtered
	return m
}

// filterByMinExposure retains only gaps whose ALE >= minUSD.
// Gaps with no ALE data (ALE == 0) are excluded when a threshold is set,
// because they cannot be verified to meet the financial bar.
func filterByMinExposure(m *coverage.CoverageMatrix, minUSD float64) *coverage.CoverageMatrix {
	var filtered []coverage.CoverageGap
	for _, g := range m.CoverageGaps {
		if g.ALE >= minUSD {
			filtered = append(filtered, g)
		}
	}
	m.CoverageGaps = filtered
	return m
}

// loadALEMap reads a specimen list JSON output and builds a controlID → ALE map.
// The specimen JSON format is: {"total": N, "risks": [...]} where each risk has
// "control_id" and "ale" fields.
func loadALEMap(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading risk register %q: %w", path, err)
	}
	var payload struct {
		Risks []struct {
			ControlID string  `json:"control_id"`
			ALE       float64 `json:"ale"`
		} `json:"risks"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parsing risk register JSON: %w", err)
	}
	aleMap := make(map[string]float64, len(payload.Risks))
	for _, r := range payload.Risks {
		if r.ControlID != "" && r.ALE > 0 {
			// Use the highest ALE when multiple risks map to the same control.
			if existing := aleMap[r.ControlID]; r.ALE > existing {
				aleMap[r.ControlID] = r.ALE
			}
		}
	}
	return aleMap, nil
}

func printCSV(m *coverage.CoverageMatrix) {
	fmt.Println("control_id,title,family,status,owner,evidence")
	for _, c := range m.Controls {
		fmt.Printf("%q,%q,%q,%q,%q,%q\n",
			c.ID, c.Title, c.Family, c.Status, c.Owner, c.Evidence)
	}
}

func loadJSONMap(path string, dst *map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func usage() {
	fmt.Fprintln(os.Stderr, `titer — control coverage matrix

Usage:
  titer [flags] --catalog <catalog.yaml>

Flags:
  --catalog string           Path to gemara ControlCatalog YAML (required)
  --soa string               Path to CDG soa.csv file (optional)
  --evidence string          Path to JSON evidence map {control_id: description}
  --owners string            Path to JSON owner map {control_id: owner_name}
  --program string           Program slug for provenance logging
  --format string            Output format: json (default), md, csv
  --severity string          Filter gaps by priority: high, medium, low
  --risk-register string     Path to specimen list JSON for FAIR ALE cross-reference
  --min-exposure-usd float   Filter gaps by minimum ALE in USD (requires --risk-register)
  --quiet                    Suppress progress output
  --version                  Print version and exit

Exit codes:
  0  No gaps (all controls evidenced or implemented)
  1  Coverage gaps exist
  2  Tool error

Examples:
  titer --catalog catalog.yaml --format md
  titer --catalog catalog.yaml --soa soa.csv --program iso42001
  titer --catalog catalog.yaml --severity high --format md
  titer --catalog catalog.yaml --risk-register data/iso42001/risks.json --min-exposure-usd 100000

Pipe:
  assay --framework iso27001 --product docs/ | titer --catalog catalog.yaml`)
}
