//go:build e2e

package e2e

// Matrix runner — top-level orchestrator that starts one PG
// container per version in pgVersions() and iterates every Cell
// as a sub-test.
//
// First wave (this commit): 3 representative carrier cells
// proving the harness works end-to-end. Expansion to all 110
// carrier cells + dbtype + constraint happens in follow-up
// commits once the skeleton is verified.

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func TestMatrix_Carrier_Representative(t *testing.T) {
	cls := loadClassifier(t)
	cells := representativeCarrierCells()

	for _, version := range pgVersions() {
		t.Run("pg"+version, func(t *testing.T) {
			cont, err := StartPG(version)
			if err != nil {
				t.Fatalf("StartPG(%s): %v", version, err)
			}
			t.Cleanup(cont.Stop)
			for _, cell := range cells {
				cell := cell
				t.Run(cell.Axis+"_"+cell.Name, func(t *testing.T) {
					cell.Run(t, cont, cls)
				})
			}
		})
	}
}

// representativeCarrierCells returns a 3-cell sample covering
// the three strategy dispatch paths engine.injectStrategyOps
// handles:
//
//   - BOOL→STRING  = LOSSLESS_USING (renders `target::text` USING)
//   - INT32→INT64  = LOSSLESS_USING (renders `target::bigint` USING)
//   - STRING→BOOL  = CUSTOM_MIGRATION (engine splices user SQL)
//
// Once the full matrix lands, these specific cells stay as-is —
// they're the canonical smoke test before expanding.
func representativeCarrierCells() []Cell {
	return []Cell{
		{
			Axis:     "carrier",
			Name:     carrierLabel(irpb.Carrier_CARRIER_BOOL, irpb.Carrier_CARRIER_STRING),
			Strategy: planpb.Strategy_LOSSLESS_USING,
			Prev:     carrierSchema(irpb.Carrier_CARRIER_BOOL, irpb.SemType_SEM_UNSPECIFIED),
			Curr:     carrierSchema(irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_CHAR),
		},
		{
			Axis:     "carrier",
			Name:     carrierLabel(irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64),
			Strategy: planpb.Strategy_LOSSLESS_USING,
			Prev:     carrierSchema(irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_NUMBER),
			Curr:     carrierSchema(irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_NUMBER),
		},
		{
			Axis:     "carrier",
			Name:     carrierLabel(irpb.Carrier_CARRIER_STRING, irpb.Carrier_CARRIER_BOOL),
			Strategy: planpb.Strategy_CUSTOM_MIGRATION,
			Prev:     carrierSchema(irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_CHAR),
			Curr:     carrierSchema(irpb.Carrier_CARRIER_BOOL, irpb.SemType_SEM_UNSPECIFIED),
			// Stub SQL: drop the column + re-add as BOOL. In a real
			// author scenario this would be a data-preserving
			// CASE-WHEN mapping — the harness just needs SQL that
			// applies cleanly on the synthetic table (empty rows).
			CustomSQL: `ALTER TABLE t DROP COLUMN target;
ALTER TABLE t ADD COLUMN target BOOLEAN;`,
		},
	}
}

// loadClassifier loads the production YAML matrix from docs/
// relative to the repo root. Resolves the path via runtime.Caller
// so the test is CWD-independent.
func loadClassifier(t *testing.T) *classifier.Classifier {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "classification")
	c, err := classifier.Load(dir)
	if err != nil {
		t.Fatalf("classifier.Load: %v", err)
	}
	return c
}
