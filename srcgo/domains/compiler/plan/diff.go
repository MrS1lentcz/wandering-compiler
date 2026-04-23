// Package plan is the differ layer: given two *irpb.Schema snapshots (prev,
// curr) it produces an ordered *planpb.MigrationPlan of Ops. Per-dialect
// emitters and sibling consumers (back-compat lint, changelog, visual editor,
// platform UI) read the plan wire-compat without speaking Go. See
// docs/iteration-2.md M1 Design and tech-spec strategic decision #8.
//
// Iteration-2 M1 walks both schemas in four stages:
//
//  1. Bucket tables by MessageFqn (D24): onlyPrev → DropTable, onlyCurr →
//     AddTable, both → table-fact + column-level diffs.
//  2. Per carried-over table, table-level fact changes (name, namespace,
//     comment) become RenameTable / SetTableNamespace / SetTableComment.
//  3. Per carried-over table, bucket columns by proto field number (D10):
//     onlyPrev → DropColumn, onlyCurr → AddColumn, both → AlterColumn /
//     RenameColumn.
//  4. Per carried-over table, set-diff indexes / FKs / checks / raw_*
//     entries by their identity keys.
//
// This file ships the table-bucket stage. Column / index / FK / CHECK
// stages land iteration-by-iteration as the M1 implementation walks the
// alter-strategy table from iteration-2.md.
package plan

import (
	"fmt"
	"sort"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Diff computes the migration plan from prev → curr. Both inputs may be
// nil: nil prev = initial migration (every curr table emits AddTable),
// nil curr = full teardown (every prev table emits DropTable).
//
// Op order:
//  1. Drops first: DropTable in reverse FK-topological order (referencer
//     before referencee) so a table isn't dropped while another still
//     references it.
//  2. Adds: AddTable in FK-topological order (referenced before
//     referencer; iter-1's invariant).
//  3. Carried-over-table fact changes (table-level + column + index +
//     FK + check) — wired iteration-by-iteration as the M1 build progresses.
func Diff(prev, curr *irpb.Schema) (*planpb.MigrationPlan, error) {
	prevTables := tablesOf(prev)
	currTables := tablesOf(curr)

	onlyPrev, onlyCurr, _ := bucketByFqn(prevTables, currTables)

	dropOrdered, err := topoSortByFK(onlyPrev)
	if err != nil {
		return nil, fmt.Errorf("plan: drop-order: %w", err)
	}
	// Reverse for drops: referencer before referencee.
	for i, j := 0, len(dropOrdered)-1; i < j; i, j = i+1, j-1 {
		dropOrdered[i], dropOrdered[j] = dropOrdered[j], dropOrdered[i]
	}

	addOrdered, err := topoSortByFK(onlyCurr)
	if err != nil {
		return nil, fmt.Errorf("plan: add-order: %w", err)
	}

	ops := make([]*planpb.Op, 0, len(dropOrdered)+len(addOrdered))
	for _, t := range dropOrdered {
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_DropTable{DropTable: &planpb.DropTable{Table: t}},
		})
	}
	for _, t := range addOrdered {
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{Table: t}},
		})
	}
	// Carried-over tables (FQN in both prev and curr): fact + column +
	// index / FK / CHECK diffing happens here in subsequent commits.
	// For now the `both` bucket is intentionally unused — table-fact
	// equality is asserted by the differ's table-level pass landing
	// in the next commit.

	return &planpb.MigrationPlan{Ops: ops}, nil
}

// tablesOf returns the schema's tables, or nil for a nil schema. Single
// helper so the bucketing stage stays nil-safe without scattering checks.
func tablesOf(s *irpb.Schema) []*irpb.Table {
	if s == nil {
		return nil
	}
	return s.GetTables()
}

// bucketByFqn splits prev / curr table sets into three groups by
// MessageFqn (D24 identity key): tables present only in prev, tables
// present only in curr, and tables in both. Each output list preserves
// the corresponding input's order so downstream sorters (topo, lexical
// tiebreak) see deterministic input.
//
// `both` is returned as a slice of {prev, curr} pairs so the caller
// reaches both sides without re-indexing. Empty for the table-add-only
// case (iter-1) and the table-drop-only case (full teardown).
func bucketByFqn(prev, curr []*irpb.Table) (onlyPrev, onlyCurr []*irpb.Table, both []TablePair) {
	prevByFqn := make(map[string]*irpb.Table, len(prev))
	for _, t := range prev {
		prevByFqn[t.GetMessageFqn()] = t
	}
	currByFqn := make(map[string]*irpb.Table, len(curr))
	for _, t := range curr {
		currByFqn[t.GetMessageFqn()] = t
	}

	for _, t := range curr {
		if _, ok := prevByFqn[t.GetMessageFqn()]; ok {
			both = append(both, TablePair{Prev: prevByFqn[t.GetMessageFqn()], Curr: t})
		} else {
			onlyCurr = append(onlyCurr, t)
		}
	}
	for _, t := range prev {
		if _, ok := currByFqn[t.GetMessageFqn()]; !ok {
			onlyPrev = append(onlyPrev, t)
		}
	}
	return onlyPrev, onlyCurr, both
}

// TablePair couples the prev + curr sides of one carried-over table for
// downstream fact / column / index diffing.
type TablePair struct {
	Prev *irpb.Table
	Curr *irpb.Table
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
// TABLE; the constraint is only checked at INSERT time). FK targets
// outside the input set (e.g. when topo-sorting the drop set, an FK
// might point at a table that's staying in `both`) don't create a
// constraint either — only deps within the input matter for ordering
// the input. Multi-table FK cycles within the input are rejected:
// they're explicitly out of scope per docs/iteration-1.md "Not in scope".
func topoSortByFK(input []*irpb.Table) ([]*irpb.Table, error) {
	byName := make(map[string]*irpb.Table, len(input))
	names := make([]string, 0, len(input))
	for _, t := range input {
		byName[t.GetName()] = t
		names = append(names, t.GetName())
	}
	sort.Strings(names)

	state := make(map[string]int, len(input))
	out := make([]*irpb.Table, 0, len(input))

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("FK cycle involving table %q (multi-table FK cycles are out of scope; see iteration-1.md \"Not in scope\")", name)
		}
		state[name] = 1

		t := byName[name]
		dedup := map[string]struct{}{}
		for _, fk := range t.GetForeignKeys() {
			tgt := fk.GetTargetTable()
			if tgt == name {
				continue // self-FK
			}
			if _, ok := byName[tgt]; !ok {
				continue // dep outside input — no ordering constraint within the input
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
