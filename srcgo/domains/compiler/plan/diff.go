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
	"errors"
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"

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

	onlyPrev, onlyCurr, both := bucketByFqn(prevTables, currTables)

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

	// Per-table column bucketing for carried-over tables. Sorted by
	// curr's name so the column-level Op stream is deterministic.
	sort.Slice(both, func(i, j int) bool {
		return both[i].Curr.GetName() < both[j].Curr.GetName()
	})

	ops := make([]*planpb.Op, 0, len(dropOrdered)+len(addOrdered)+len(both)*4)
	for _, t := range dropOrdered {
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_DropTable{DropTable: &planpb.DropTable{Table: t}},
		})
	}
	// Table-axis renames first on `both` tables — subsequent
	// column-axis ops reference the post-rename qualifier.
	for _, pair := range both {
		ops = append(ops, tableRenames(pair)...)
	}
	// Column drops first across all carried-over tables, then adds.
	// Inside one table we drop before add per the M1 ordering rule
	// (renumbered replacements work cleanly; rename detection
	// collapses matching pairs).
	for _, pair := range both {
		ops = append(ops, columnDrops(pair)...)
	}
	for _, t := range addOrdered {
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{Table: t}},
		})
	}
	for _, pair := range both {
		ops = append(ops, columnAdds(pair)...)
	}
	// Renames after structural adds — RENAME on a freshly-added
	// column is meaningless (we'd just have added it under the new
	// name); RENAME of a column that survived prev → curr happens
	// here.
	for _, pair := range both {
		ops = append(ops, columnRenames(pair)...)
	}
	// AlterColumn after rename — the alter ctx uses the post-rename
	// name. REFUSE-strategy fact changes propagate as errors.
	var alterErrs []error
	for _, pair := range both {
		alters, err := columnAlters(pair)
		if err != nil {
			alterErrs = append(alterErrs, err)
			continue
		}
		ops = append(ops, alters...)
	}
	if len(alterErrs) > 0 {
		return nil, fmt.Errorf("plan: column-alter refusals: %w", errors.Join(alterErrs...))
	}
	// Index drops + replaces + adds. Indexes can reference columns
	// added in this same migration (column adds emitted earlier),
	// so this block follows column adds.
	for _, pair := range both {
		ops = append(ops, indexOps(pair)...)
	}
	for _, pair := range both {
		ops = append(ops, rawIndexOps(pair)...)
	}
	// FK ops follow indexes — FKs benefit from indexed columns
	// (PG checks FK validity faster against indexed targets).
	for _, pair := range both {
		ops = append(ops, fkOps(pair)...)
	}
	// CHECK ops after FKs (no inter-dependency, but keeps the
	// constraint family grouped at the tail).
	for _, pair := range both {
		ops = append(ops, checkOps(pair)...)
	}
	for _, pair := range both {
		ops = append(ops, rawCheckOps(pair)...)
	}
	// Table comments after all structural changes — COMMENT ON
	// references the post-rename qualifier and post-add columns.
	for _, pair := range both {
		ops = append(ops, tableCommentChanges(pair)...)
	}
	// Namespace move + AlterColumn + FK / CHECK diffs land in
	// subsequent commits.

	return &planpb.MigrationPlan{Ops: ops}, nil
}

// columnAdds returns AddColumn ops for columns whose proto field
// number appears in curr but not prev. Order = curr's declaration
// order (preserves D10 stability — number, not slot).
func columnAdds(pair TablePair) []*planpb.Op {
	prevByNum := numberMap(pair.Prev.GetColumns())
	ctx := tableCtxOf(pair.Curr)
	var ops []*planpb.Op
	for _, c := range pair.Curr.GetColumns() {
		if _, ok := prevByNum[c.GetFieldNumber()]; ok {
			continue
		}
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_AddColumn{AddColumn: &planpb.AddColumn{
				Ctx:    ctx,
				Column: c,
			}},
		})
	}
	return ops
}

// columnDrops returns DropColumn ops for columns whose proto field
// number appears in prev but not curr. Carries the prev-side Column
// so down can re-create it. Uses curr's table context (the live
// shape during the migration; the table itself isn't being dropped).
func columnDrops(pair TablePair) []*planpb.Op {
	currByNum := numberMap(pair.Curr.GetColumns())
	ctx := tableCtxOf(pair.Curr)
	var ops []*planpb.Op
	for _, c := range pair.Prev.GetColumns() {
		if _, ok := currByNum[c.GetFieldNumber()]; ok {
			continue
		}
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{
				Ctx:    ctx,
				Column: c,
			}},
		})
	}
	return ops
}

// columnAlters returns AlterColumn ops for both-present columns
// whose facts differ. Identity = proto field number (D10); name
// changes are handled separately by columnRenames. Each emitted
// AlterColumn carries the FactChange list the emitter walks to
// produce ALTER TABLE statements per the iteration-2 strategy
// table.
//
// REFUSE-strategy facts (carrier, pk, custom_type, element_*,
// generated_expr — only when the change is structural) trigger
// a *diag.Error returned to the caller. Caller (Diff) accumulates
// + returns; emit never sees a refusal.
func columnAlters(pair TablePair) ([]*planpb.Op, error) {
	prevByNum := numberMap(pair.Prev.GetColumns())
	ctx := tableCtxOf(pair.Curr)
	var ops []*planpb.Op
	var refusals []error
	for _, currCol := range pair.Curr.GetColumns() {
		prevCol, both := prevByNum[currCol.GetFieldNumber()]
		if !both {
			continue
		}
		changes, err := buildFactChanges(pair.Curr, prevCol, currCol)
		if err != nil {
			refusals = append(refusals, err)
			continue
		}
		if len(changes) == 0 {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
			Ctx:         ctx,
			FieldNumber: currCol.GetFieldNumber(),
			ColumnName:  currCol.GetName(),
			Changes:     changes,
		}}})
	}
	if len(refusals) > 0 {
		return nil, errors.Join(refusals...)
	}
	return ops, nil
}

// buildFactChanges walks every fact axis on a Column pair and
// emits a FactChange entry per axis whose value differs. Returns
// an error for REFUSE-strategy axes per iteration-2 alter-strategies
// (carrier change, pk flip, element_* reshape, pg.custom_type change).
// Ordering in the returned slice is fact-class declaration order
// here so emit produces deterministic SQL.
func buildFactChanges(carrierTable *irpb.Table, prev, curr *irpb.Column) ([]*planpb.FactChange, error) {
	if prev.GetCarrier() != curr.GetCarrier() {
		return nil, fmt.Errorf("column %q (#%d): proto carrier change %v→%v is REFUSE-strategy (drop the field and add a new one with a fresh number)",
			curr.GetName(), curr.GetFieldNumber(), prev.GetCarrier(), curr.GetCarrier())
	}
	if prev.GetPk() != curr.GetPk() {
		return nil, fmt.Errorf("column %q (#%d): primary-key flip is REFUSE-strategy (PK change is table-rebuild territory; author writes an explicit migration)",
			curr.GetName(), curr.GetFieldNumber())
	}
	if prev.GetElementCarrier() != curr.GetElementCarrier() ||
		prev.GetElementIsMessage() != curr.GetElementIsMessage() {
		return nil, fmt.Errorf("column %q (#%d): collection element reshape is REFUSE-strategy",
			curr.GetName(), curr.GetFieldNumber())
	}
	prevPgCT := prev.GetPg().GetCustomType()
	currPgCT := curr.GetPg().GetCustomType()
	if prevPgCT != currPgCT {
		return nil, fmt.Errorf("column %q (#%d): (w17.pg.field).custom_type change %q→%q is REFUSE-strategy (custom_type is author-owned)",
			curr.GetName(), curr.GetFieldNumber(), prevPgCT, currPgCT)
	}

	var out []*planpb.FactChange
	if prev.GetNullable() != curr.GetNullable() {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_Nullable{Nullable: &planpb.NullableChange{
			From: prev.GetNullable(), To: curr.GetNullable(),
		}}})
	}
	if !proto.Equal(prev.GetDefault(), curr.GetDefault()) {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_DefaultValue{DefaultValue: &planpb.DefaultChange{
			From: prev.GetDefault(), To: curr.GetDefault(),
		}}})
	}
	if prev.GetMaxLen() != curr.GetMaxLen() {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{
			From: prev.GetMaxLen(), To: curr.GetMaxLen(),
		}}})
	}
	if prev.GetComment() != curr.GetComment() {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_Comment{Comment: &planpb.CommentChange{
			From: prev.GetComment(), To: curr.GetComment(),
		}}})
	}
	// Numeric precision / scale, db_type, generated_expr, enum_values,
	// allowed_extensions, unique change variants land in the
	// follow-up commit that wires their emit branches.
	return out, nil
}

// columnRenames returns RenameColumn ops for both-present columns
// whose proto field number is stable but whose `name` (after
// (w17.db.column).name override) changed. D10 makes this free —
// the rename collapses what would otherwise be a Drop + Add
// false positive into one ALTER ... RENAME COLUMN. Order = curr
// declaration order. Excludes columns whose number isn't on both
// sides (those flow through columnAdds / columnDrops).
func columnRenames(pair TablePair) []*planpb.Op {
	prevByNum := numberMap(pair.Prev.GetColumns())
	ctx := tableCtxOf(pair.Curr)
	var ops []*planpb.Op
	for _, c := range pair.Curr.GetColumns() {
		prevCol, ok := prevByNum[c.GetFieldNumber()]
		if !ok {
			continue
		}
		if prevCol.GetName() == c.GetName() {
			continue
		}
		ops = append(ops, &planpb.Op{
			Variant: &planpb.Op_RenameColumn{RenameColumn: &planpb.RenameColumn{
				Ctx:         ctx,
				FieldNumber: c.GetFieldNumber(),
				FromName:    prevCol.GetName(),
				ToName:      c.GetName(),
			}},
		})
	}
	return ops
}

// numberMap indexes columns by their proto field number. Returns an
// empty map for an empty input — D10 identity collapses naturally.
func numberMap(cols []*irpb.Column) map[int32]*irpb.Column {
	out := make(map[int32]*irpb.Column, len(cols))
	for _, c := range cols {
		out[c.GetFieldNumber()] = c
	}
	return out
}

// tableCtxOf builds the qualifier-fact bundle every column-level /
// index / FK / CHECK op needs. Pulls FQN, name, namespace mode +
// value off the table; emit consumes it without touching Schema.
func tableCtxOf(t *irpb.Table) *planpb.TableCtx {
	return &planpb.TableCtx{
		MessageFqn:     t.GetMessageFqn(),
		TableName:      t.GetName(),
		NamespaceMode:  t.GetNamespaceMode(),
		Namespace:      t.GetNamespace(),
	}
}

// checkOps returns Add / Drop / Replace ops for structured CHECKs
// across both-present columns of one `both` pair. Identity = the
// CHECK variant kind on a given column (each column carries at
// most one of each variant kind — len/blank/range/regex/choices —
// because that's what attachChecks emits in iter-1). Set-diff per
// (column number, variant kind):
//   onlyPrev → DropCheck
//   onlyCurr → AddCheck
//   both with proto.Equal differences → ReplaceCheck
//
// Columns added or dropped at this migration carry their checks
// with them via AddColumn / DropColumn — they don't surface as
// separate AddCheck / DropCheck ops.
func checkOps(pair TablePair) []*planpb.Op {
	ctx := tableCtxOf(pair.Curr)
	prevByNum := numberMap(pair.Prev.GetColumns())

	var ops []*planpb.Op
	for _, currCol := range pair.Curr.GetColumns() {
		prevCol, both := prevByNum[currCol.GetFieldNumber()]
		if !both {
			continue // AddColumn carries the new column's checks
		}
		prevByKind := checkMap(prevCol)
		currByKind := checkMap(currCol)
		// Drops first.
		for _, prevCk := range prevCol.GetChecks() {
			kind := checkVariantKind(prevCk)
			if _, ok := currByKind[kind]; ok {
				continue
			}
			ops = append(ops, &planpb.Op{Variant: &planpb.Op_DropCheck{DropCheck: &planpb.DropCheck{
				Ctx: ctx, Column: currCol, Check: prevCk,
			}}})
		}
		for _, currCk := range currCol.GetChecks() {
			kind := checkVariantKind(currCk)
			prevCk, both := prevByKind[kind]
			if !both {
				ops = append(ops, &planpb.Op{Variant: &planpb.Op_AddCheck{AddCheck: &planpb.AddCheck{
					Ctx: ctx, Column: currCol, Check: currCk,
				}}})
				continue
			}
			if proto.Equal(prevCk, currCk) {
				continue
			}
			ops = append(ops, &planpb.Op{Variant: &planpb.Op_ReplaceCheck{ReplaceCheck: &planpb.ReplaceCheck{
				Ctx: ctx, Column: currCol, From: prevCk, To: currCk,
			}}})
		}
	}
	return ops
}

// rawCheckOps mirrors checkOps for raw_check entries. Identity =
// RawCheck.Name. Body comparison is byte-equal — opaque (D11).
func rawCheckOps(pair TablePair) []*planpb.Op {
	ctx := tableCtxOf(pair.Curr)
	prevByName := rawCheckMap(pair.Prev.GetRawChecks())
	currByName := rawCheckMap(pair.Curr.GetRawChecks())

	var ops []*planpb.Op
	for _, rc := range pair.Prev.GetRawChecks() {
		if _, ok := currByName[rc.GetName()]; ok {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_DropRawCheck{DropRawCheck: &planpb.DropRawCheck{
			Ctx: ctx, Check: rc,
		}}})
	}
	for _, rc := range pair.Curr.GetRawChecks() {
		prevRC, both := prevByName[rc.GetName()]
		if !both {
			ops = append(ops, &planpb.Op{Variant: &planpb.Op_AddRawCheck{AddRawCheck: &planpb.AddRawCheck{
				Ctx: ctx, Check: rc,
			}}})
			continue
		}
		if proto.Equal(prevRC, rc) {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_ReplaceRawCheck{ReplaceRawCheck: &planpb.ReplaceRawCheck{
			Ctx: ctx, From: prevRC, To: rc,
		}}})
	}
	return ops
}

// checkMap indexes a column's structured checks by their variant
// kind ("len" / "blank" / "range" / "regex" / "choices") so the
// per-column set-diff can compare like-with-like.
func checkMap(col *irpb.Column) map[string]*irpb.Check {
	out := make(map[string]*irpb.Check, len(col.GetChecks()))
	for _, ck := range col.GetChecks() {
		out[checkVariantKind(ck)] = ck
	}
	return out
}

// checkVariantKind returns a stable string name for the Check
// variant. Keep aligned with renderCheckBody's suffix in the
// postgres emitter — the suffix names the constraint, so identity
// must match it.
func checkVariantKind(ck *irpb.Check) string {
	switch ck.GetVariant().(type) {
	case *irpb.Check_Length:
		return "len"
	case *irpb.Check_Blank:
		return "blank"
	case *irpb.Check_Range:
		return "range"
	case *irpb.Check_Regex:
		return "format"
	case *irpb.Check_Choices:
		return "choices"
	}
	return ""
}

func rawCheckMap(rcs []*irpb.RawCheck) map[string]*irpb.RawCheck {
	out := make(map[string]*irpb.RawCheck, len(rcs))
	for _, r := range rcs {
		out[r.GetName()] = r
	}
	return out
}

// fkOps returns AddForeignKey / DropForeignKey / ReplaceForeignKey
// ops for one `both` pair. Identity = derived constraint name
// `<table>_<col>_fkey` (matches PG's auto-derived convention so
// existing alter-diff plans can drop iter-1 inline-FK constraints
// by their PG-given name). Set-diff over the constraint-name space:
//   onlyPrev → DropForeignKey
//   onlyCurr → AddForeignKey
//   both with proto.Equal differences (target table / column /
//     deletion_rule changed) → ReplaceForeignKey
func fkOps(pair TablePair) []*planpb.Op {
	ctx := tableCtxOf(pair.Curr)
	prevByName := fkMap(pair.Prev)
	currByName := fkMap(pair.Curr)

	var ops []*planpb.Op
	for _, fk := range pair.Prev.GetForeignKeys() {
		name := fkConstraintName(pair.Prev.GetName(), fk.GetColumn())
		if _, ok := currByName[name]; ok {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_DropForeignKey{DropForeignKey: &planpb.DropForeignKey{
			Ctx:            ctx,
			Fk:             fk,
			ConstraintName: name,
			Columns:        pair.Prev.GetColumns(),
		}}})
	}
	for _, fk := range pair.Curr.GetForeignKeys() {
		name := fkConstraintName(pair.Curr.GetName(), fk.GetColumn())
		prevFK, both := prevByName[name]
		if !both {
			ops = append(ops, &planpb.Op{Variant: &planpb.Op_AddForeignKey{AddForeignKey: &planpb.AddForeignKey{
				Ctx:            ctx,
				Fk:             fk,
				ConstraintName: name,
				Columns:        pair.Curr.GetColumns(),
			}}})
			continue
		}
		if proto.Equal(prevFK, fk) {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_ReplaceForeignKey{ReplaceForeignKey: &planpb.ReplaceForeignKey{
			Ctx:            ctx,
			From:           prevFK,
			To:             fk,
			ConstraintName: name,
			Columns:        pair.Curr.GetColumns(),
		}}})
	}
	return ops
}

// fkMap indexes a table's FKs by their derived constraint name.
func fkMap(t *irpb.Table) map[string]*irpb.ForeignKey {
	out := make(map[string]*irpb.ForeignKey, len(t.GetForeignKeys()))
	for _, fk := range t.GetForeignKeys() {
		out[fkConstraintName(t.GetName(), fk.GetColumn())] = fk
	}
	return out
}

// fkConstraintName derives the FK constraint name on the convention PG
// uses when none is explicit: `<table>_<col>_fkey`. Used both for
// generating alter-diff op identities and for emitting matching
// CONSTRAINT clauses on AddForeignKey statements.
func fkConstraintName(table, col string) string {
	return table + "_" + col + "_fkey"
}

// indexOps returns AddIndex / DropIndex / ReplaceIndex ops for the
// structured indexes of one `both` pair. Identity = Index.Name (set
// by ir.Build's derivation pass; never empty in valid IR). Set-diff
// over the name space:
//   onlyPrev → DropIndex   (down recreates via the prev-side Index)
//   onlyCurr → AddIndex
//   both     → if proto.Equal(prev, curr) no-op; else ReplaceIndex
//
// Raw-index variants use the same shape but compare opaque bodies
// (D11 escape-hatch identity-on-name).
func indexOps(pair TablePair) []*planpb.Op {
	ctx := tableCtxOf(pair.Curr)
	prevByName := indexMap(pair.Prev.GetIndexes())
	currByName := indexMap(pair.Curr.GetIndexes())

	var ops []*planpb.Op
	// Drops first — DROP INDEX before CREATE INDEX of the same name
	// (in the Replace case the differ emits both).
	for _, idx := range pair.Prev.GetIndexes() {
		if _, ok := currByName[idx.GetName()]; ok {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_DropIndex{DropIndex: &planpb.DropIndex{
			Ctx:     ctx,
			Index:   idx,
			Columns: pair.Prev.GetColumns(),
		}}})
	}
	for _, idx := range pair.Curr.GetIndexes() {
		prevIdx, both := prevByName[idx.GetName()]
		if !both {
			ops = append(ops, &planpb.Op{Variant: &planpb.Op_AddIndex{AddIndex: &planpb.AddIndex{
				Ctx:     ctx,
				Index:   idx,
				Columns: pair.Curr.GetColumns(),
			}}})
			continue
		}
		if proto.Equal(prevIdx, idx) {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_ReplaceIndex{ReplaceIndex: &planpb.ReplaceIndex{
			Ctx:     ctx,
			From:    prevIdx,
			To:      idx,
			Columns: pair.Curr.GetColumns(),
		}}})
	}
	return ops
}

// rawIndexOps mirrors indexOps for raw_index entries. Identity =
// RawIndex.Name. Body comparison is byte-equal — opaque (D11).
func rawIndexOps(pair TablePair) []*planpb.Op {
	ctx := tableCtxOf(pair.Curr)
	prevByName := rawIndexMap(pair.Prev.GetRawIndexes())
	currByName := rawIndexMap(pair.Curr.GetRawIndexes())

	var ops []*planpb.Op
	for _, ri := range pair.Prev.GetRawIndexes() {
		if _, ok := currByName[ri.GetName()]; ok {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_DropRawIndex{DropRawIndex: &planpb.DropRawIndex{
			Ctx:   ctx,
			Index: ri,
		}}})
	}
	for _, ri := range pair.Curr.GetRawIndexes() {
		prevRI, both := prevByName[ri.GetName()]
		if !both {
			ops = append(ops, &planpb.Op{Variant: &planpb.Op_AddRawIndex{AddRawIndex: &planpb.AddRawIndex{
				Ctx:   ctx,
				Index: ri,
			}}})
			continue
		}
		if proto.Equal(prevRI, ri) {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_ReplaceRawIndex{ReplaceRawIndex: &planpb.ReplaceRawIndex{
			Ctx:  ctx,
			From: prevRI,
			To:   ri,
		}}})
	}
	return ops
}

func indexMap(idxs []*irpb.Index) map[string]*irpb.Index {
	out := make(map[string]*irpb.Index, len(idxs))
	for _, i := range idxs {
		out[i.GetName()] = i
	}
	return out
}

func rawIndexMap(ris []*irpb.RawIndex) map[string]*irpb.RawIndex {
	out := make(map[string]*irpb.RawIndex, len(ris))
	for _, r := range ris {
		out[r.GetName()] = r
	}
	return out
}

// tableRenames returns RenameTable ops for `both` pairs whose SQL name
// changed while the FQN stayed (D24): a rename of (w17.db.table).name
// or, in PREFIX mode, a change to the module prefix that re-derives
// the name. Emitted before any column-axis op on the same table so
// subsequent ops reference the new name.
func tableRenames(pair TablePair) []*planpb.Op {
	if pair.Prev.GetName() == pair.Curr.GetName() {
		return nil
	}
	return []*planpb.Op{{Variant: &planpb.Op_RenameTable{RenameTable: &planpb.RenameTable{
		Ctx:      tableCtxOf(pair.Curr),
		FromName: pair.Prev.GetName(),
		ToName:   pair.Curr.GetName(),
	}}}}
}

// tableCommentChanges returns SetTableComment ops for `both` pairs
// whose resolved comment changed (D22). Empty `to` = drop comment via
// COMMENT ON TABLE … IS NULL.
func tableCommentChanges(pair TablePair) []*planpb.Op {
	if pair.Prev.GetComment() == pair.Curr.GetComment() {
		return nil
	}
	return []*planpb.Op{{Variant: &planpb.Op_SetTableComment{SetTableComment: &planpb.SetTableComment{
		Ctx:  tableCtxOf(pair.Curr),
		From: pair.Prev.GetComment(),
		To:   pair.Curr.GetComment(),
	}}}}
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
