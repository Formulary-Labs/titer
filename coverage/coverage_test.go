package coverage_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Formulary-Labs/titer/coverage"
)

const testCatalogPath = "../../probe/testdata/good-control-catalog.yaml"

func TestCompute_validCatalog(t *testing.T) {
	if _, err := os.Stat(testCatalogPath); err != nil {
		t.Skip("test catalog not found — skipping")
	}

	opts := coverage.Options{
		Program:     "test-program",
		CatalogPath: testCatalogPath,
	}

	matrix, err := coverage.Compute(opts)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	if matrix.Totals.Total == 0 {
		t.Error("total controls must be > 0")
	}
	if len(matrix.Controls) == 0 {
		t.Error("controls list must not be empty")
	}
	// Without evidence, all controls should be gaps.
	if matrix.Totals.Gap != matrix.Totals.Total {
		t.Errorf("without evidence, all %d controls should be gaps, got %d", matrix.Totals.Total, matrix.Totals.Gap)
	}
}

func TestCompute_withEvidenceMap(t *testing.T) {
	if _, err := os.Stat(testCatalogPath); err != nil {
		t.Skip("test catalog not found — skipping")
	}

	// First, get control IDs from the catalog.
	matrix0, err := coverage.Compute(coverage.Options{
		Program:     "test",
		CatalogPath: testCatalogPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix0.Controls) == 0 {
		t.Skip("no controls to test with")
	}

	firstID := matrix0.Controls[0].ID
	opts := coverage.Options{
		Program:     "test-program",
		CatalogPath: testCatalogPath,
		EvidenceMap: map[string]string{
			firstID: "Section 4.2.1 of product documentation",
		},
	}

	matrix, err := coverage.Compute(opts)
	if err != nil {
		t.Fatalf("Compute with evidence error: %v", err)
	}
	if matrix.Totals.Evidenced != 1 {
		t.Errorf("expected 1 evidenced control, got %d", matrix.Totals.Evidenced)
	}
}

func TestCoveragePctFormula(t *testing.T) {
	families := []coverage.FamilyCoverage{
		{Total: 10, Evidenced: 3, ImplementedNoEvidence: 2, Gap: 4, NotApplicable: 1},
	}
	totals := coverage.ExportedComputeTotals(families)
	if totals.CoveragePct == nil {
		t.Fatal("CoveragePct must not be nil")
	}
	if *totals.CoveragePct != 55 {
		t.Errorf("CoveragePct = %d, want 55", *totals.CoveragePct)
	}
}

func TestToMarkdown_containsTable(t *testing.T) {
	if _, err := os.Stat(testCatalogPath); err != nil {
		t.Skip("test catalog not found — skipping")
	}
	matrix, err := coverage.Compute(coverage.Options{
		Program:     "test",
		CatalogPath: testCatalogPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	md := matrix.ToMarkdown()
	if !strings.Contains(md, "# Control Coverage Matrix") {
		t.Error("markdown missing title")
	}
	if !strings.Contains(md, "Coverage Gaps") {
		t.Error("markdown missing Coverage Gaps section")
	}
}
