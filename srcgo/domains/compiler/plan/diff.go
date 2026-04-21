// Package plan is the differ layer: given two *irpb.Schema snapshots (prev,
// curr) it produces an ordered *planpb.MigrationPlan of Ops. Per-dialect
// emitters and sibling consumers (back-compat lint, changelog, visual editor,
// platform UI) read the plan wire-compat without speaking Go. See
// docs/iteration-1.md D4 (rev 2026-04-21) and tech-spec strategic decision #8.
//
// Iteration-1 only handles the initial-migration case (prev == nil): the
// differ walks curr.Tables in stable name order and emits one AddTable op
// per table. DropTable / AddColumn / AlterColumn / RenameColumn / AddIndex
// / DropIndex land iteration-by-iteration as pilot schemas surface real
// alter-diff needs.
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

	tables := append([]*irpb.Table(nil), curr.GetTables()...)
	sort.Slice(tables, func(i, j int) bool {
		return tables[i].GetName() < tables[j].GetName()
	})

	ops := make([]*planpb.Op, 0, len(tables))
	for _, t := range tables {
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{Table: t}},
		})
	}
	return &planpb.MigrationPlan{Ops: ops}, nil
}
