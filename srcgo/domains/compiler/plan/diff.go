// Package plan is the differ layer: given two *irpb.Schema snapshots (prev,
// curr) it produces an ordered *planpb.MigrationPlan of Ops. Per-dialect
// emitters and sibling consumers (back-compat lint, changelog, visual editor,
// platform UI) read the plan wire-compat without speaking Go. See
// docs/iteration-1.md D4 (rev 2026-04-21) and tech-spec strategic decision #8.
//
// Iteration-1 only handles the initial-migration case (prev == nil): the
// differ walks curr.Tables in FK-dependency order (topological; referenced
// tables before referencers, self-refs permitted because PG / most
// dialects accept inline self-FK in CREATE TABLE) and emits one AddTable
// op per table. Ties between mutually-independent tables break by lexical
// name for deterministic output (AC #4). DropTable / AddColumn /
// AlterColumn / RenameColumn / AddIndex / DropIndex land
// iteration-by-iteration as pilot schemas surface real alter-diff needs.
package plan

import (
	"fmt"
	"sort"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Diff computes the migration plan from prev → curr. Iteration-1 requires
// prev == nil; any other input is a programming error (caught in test).
func Diff(prev, curr *irpb.Schema) (*planpb.MigrationPlan, error) {
	if prev != nil {
		return nil, fmt.Errorf("plan.Diff: non-nil prev not supported in iteration-1 (alter-diff lands iteration-by-iteration as pilot schemas need it)")
	}
	if curr == nil {
		return &planpb.MigrationPlan{}, nil
	}

	tables, err := topoSortByFK(curr.GetTables())
	if err != nil {
		return nil, err
	}

	ops := make([]*planpb.Op, 0, len(tables))
	for _, t := range tables {
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{Table: t}},
		})
	}
	return &planpb.MigrationPlan{Ops: ops}, nil
}

// topoSortByFK returns tables in FK-dependency order (referenced tables
// first). Ties — sets of tables none of which depends on another — are
// broken lexically so output is deterministic (AC #4).
//
// Implementation: depth-first traversal with the outer loop iterating
// table names in lexical order. For each table we recursively visit its
// FK targets first (in lexical order among the deps), then emit the
// table itself. Self-FKs are ignored — they don't create an ordering
// constraint (PG accepts inline `REFERENCES <self>(col)` in CREATE
// TABLE; the constraint is only checked at INSERT time). Missing
// targets are impossible here — ir.resolveFKs already rejects them —
// but we error loudly anyway to surface any future IR-layer slip.
// Multi-table FK cycles are rejected: they're explicitly out of scope
// per docs/iteration-1.md "Not in scope" (cross-table FK cycles → iter-2+).
func topoSortByFK(input []*irpb.Table) ([]*irpb.Table, error) {
	byName := make(map[string]*irpb.Table, len(input))
	names := make([]string, 0, len(input))
	for _, t := range input {
		byName[t.GetName()] = t
		names = append(names, t.GetName())
	}
	sort.Strings(names)

	// 0=unvisited, 1=visiting (cycle detection), 2=done.
	state := make(map[string]int, len(input))
	out := make([]*irpb.Table, 0, len(input))

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("plan: FK cycle involving table %q (multi-table FK cycles are out of scope in iteration-1; see iteration-1.md \"Not in scope\")", name)
		}
		state[name] = 1

		t := byName[name]
		// Collect distinct non-self FK targets, then visit in lexical order.
		dedup := map[string]struct{}{}
		for _, fk := range t.GetForeignKeys() {
			tgt := fk.GetTargetTable()
			if tgt == name {
				continue // self-FK: no ordering constraint.
			}
			if _, ok := byName[tgt]; !ok {
				return fmt.Errorf("plan: table %q references unknown table %q (ir.resolveFKs should have caught this)", name, tgt)
			}
			dedup[tgt] = struct{}{}
		}
		deps := make([]string, 0, len(dedup))
		for d := range dedup {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		for _, d := range deps {
			if err := visit(d); err != nil {
				return err
			}
		}

		state[name] = 2
		out = append(out, t)
		return nil
	}

	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}
