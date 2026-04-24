//go:build e2e

package e2e

// runCell executes one classifier cell end-to-end against a
// running PG container. The sequence mirrors `make test-apply`
// alter fixtures but is produced in-process:
//
//  1. Generate prev-only initial migration via engine.Plan(nil, prev)
//  2. Apply prev up → DB is now in the "before" state
//  3. Probe engine.Plan(prev, curr) to learn the Finding ID
//  4. Build the cell's Resolution (SAFE / LOSSLESS_USING / CUSTOM_MIGRATION)
//  5. Final engine.Plan(prev, curr, resolutions) → Migration has SQL
//  6. Apply diff up → DB in "after" state
//  7. Apply diff down → DB back to "before"
//  8. Apply prev down → DB empty
//
// Any SQL apply failure fails the test. Rollback on every path
// (even on failure mid-sequence) is the caller's responsibility
// — tests use fresh DB names per cell so no cleanup needed.

import (
	"fmt"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Cell is one matrix entry the harness runs. Axis + Name appear
// in sub-test names; Strategy drives the Resolution builder.
type Cell struct {
	// Axis: "carrier" / "dbtype" / "constraint".
	Axis string
	// Name: "BOOL→STRING" style — used for sub-test labels.
	Name string
	// Strategy from the YAML cell. Drives the Resolution shape.
	Strategy planpb.Strategy
	// Prev + Curr IR schemas synthesised for this cell.
	Prev, Curr *irpb.Schema
	// CustomSQL body for CUSTOM_MIGRATION cells — a stub that
	// applies cleanly on the synthetic table. Empty for other
	// strategies.
	CustomSQL string
	// SkipReason: non-empty → sub-test skipped with this reason.
	// Used for cells whose synthesizer isn't supported yet.
	SkipReason string
}

// Run executes one cell against a container.
func (c Cell) Run(t *testing.T, cont *PGContainer, cls *classifier.Classifier) {
	if c.SkipReason != "" {
		t.Skip(c.SkipReason)
	}
	db := fmt.Sprintf("e2e_%s_%s",
		sanitize(c.Axis), sanitize(c.Name))
	if err := cont.CreateDB(db); err != nil {
		t.Fatalf("create db %s: %v", db, err)
	}

	pgEmitter := func(*irpb.Connection) (emit.DialectEmitter, error) {
		return postgres.Emitter{}, nil
	}

	// Phase 1: prev migration — initial "before" state.
	prevPlan, err := engine.Plan(nil, c.Prev, cls, nil, pgEmitter)
	if err != nil {
		t.Fatalf("engine.Plan(nil, prev): %v", err)
	}
	if len(prevPlan.Migrations) != 1 {
		t.Fatalf("prev plan expected 1 migration, got %d", len(prevPlan.Migrations))
	}
	if err := cont.Apply(db, prevPlan.Migrations[0].GetUpSql()); err != nil {
		t.Fatalf("apply prev.up: %v", err)
	}

	// Phase 2: probe to learn the Finding ID (for cells that
	// produce one). No Findings = in-axis auto-applied.
	probe, err := engine.Plan(c.Prev, c.Curr, cls, nil, pgEmitter)
	if err != nil {
		t.Fatalf("engine.Plan(prev, curr) probe: %v", err)
	}

	// Phase 3: build resolutions for any Findings.
	var resolutions []*planpb.Resolution
	for _, f := range probe.Findings {
		resolutions = append(resolutions, c.buildResolution(f))
	}

	// Phase 4: final plan with resolutions applied.
	final, err := engine.Plan(c.Prev, c.Curr, cls, resolutions, pgEmitter)
	if err != nil {
		t.Fatalf("engine.Plan(prev, curr, resolutions): %v", err)
	}
	if len(final.Findings) != 0 {
		t.Fatalf("expected all findings resolved, still have %d", len(final.Findings))
	}
	if len(final.Migrations) != 1 {
		t.Fatalf("final plan expected 1 migration, got %d", len(final.Migrations))
	}
	up := final.Migrations[0].GetUpSql()
	down := final.Migrations[0].GetDownSql()
	if up == "" {
		t.Fatalf("final migration has empty up SQL — engine failed to inject Ops for strategy %s", c.Strategy)
	}

	// Phase 5: apply the diff roundtrip.
	if err := cont.Apply(db, up); err != nil {
		t.Fatalf("apply diff.up:\n%s\nerr: %v", up, err)
	}
	if err := cont.Apply(db, down); err != nil {
		t.Fatalf("apply diff.down:\n%s\nerr: %v", down, err)
	}

	// Phase 6: apply prev.down for full teardown.
	if err := cont.Apply(db, prevPlan.Migrations[0].GetDownSql()); err != nil {
		t.Fatalf("apply prev.down: %v", err)
	}
}

// buildResolution constructs the Resolution that matches the
// cell's Strategy. For CUSTOM_MIGRATION, the CustomSQL field
// supplies the stub body; for everything else the proposed
// strategy from the Finding is accepted verbatim.
func (c Cell) buildResolution(f *planpb.ReviewFinding) *planpb.Resolution {
	r := &planpb.Resolution{
		FindingId: f.GetId(),
		Strategy:  c.Strategy,
		Actor:     "e2e-harness",
	}
	if c.Strategy == planpb.Strategy_CUSTOM_MIGRATION {
		r.CustomSql = c.CustomSQL
	}
	return r
}

// sanitize keeps DB names filesystem-/psql-safe. Lowercase only
// so PG's unquoted-identifier folding matches our explicit string
// (otherwise `CREATE DATABASE foo_BAR` creates `foo_bar` but the
// follow-up `psql -d foo_BAR` looks up the unfolded name).
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '_':
			out = append(out, ch)
		case ch >= 'A' && ch <= 'Z':
			out = append(out, ch+('a'-'A'))
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
