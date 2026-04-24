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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// DiffResult carries the differ's output: the structural MigrationPlan
// (Ops that compose the migration) plus any ReviewFindings — decision
// points the classifier couldn't auto-resolve (carrier changes, PK
// flips, custom_type swaps, enum FQN changes, enum value removal,
// element-carrier reshape). Findings don't block Diff itself; the
// caller's policy (engine.Plan + ResolutionSource) decides whether
// they gate downstream emit.
//
// A Finding's axis-related Op is *omitted* from Plan. Emit never sees
// an Op whose decision is pending. Resolved findings splice their
// effect back in at the engine.Plan layer (step 6).
type DiffResult struct {
	Plan     *planpb.MigrationPlan
	Findings []*planpb.ReviewFinding
}

// GetOps proxies to the embedded MigrationPlan so callers that only
// care about the Op stream can read it directly without unwrapping.
// Nil-safe: a nil DiffResult returns nil.
func (r *DiffResult) GetOps() []*planpb.Op {
	if r == nil {
		return nil
	}
	return r.Plan.GetOps()
}

// Diff computes the migration plan from prev → curr. Both inputs may be
// nil: nil prev = initial migration (every curr table emits AddTable),
// nil curr = full teardown (every prev table emits DropTable).
//
// When `cls` is non-nil, axes that would have produced a REFUSE error
// now emit a ReviewFinding instead (the classifier determines strategy +
// rationale). When `cls` is nil, REFUSE axes fall back to today's plain
// errors — useful for tests that haven't wired a classifier.
//
// Op order:
//  1. Drops first: DropTable in reverse FK-topological order (referencer
//     before referencee) so a table isn't dropped while another still
//     references it.
//  2. Adds: AddTable in FK-topological order (referenced before
//     referencer; iter-1's invariant).
//  3. Carried-over-table fact changes (table-level + column + index +
//     FK + check) — wired iteration-by-iteration as the M1 build progresses.
func Diff(prev, curr *irpb.Schema, cls *classifier.Classifier) (*DiffResult, error) {
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
	// Table-axis namespace moves + renames first on `both` tables —
	// subsequent column-axis ops reference the post-move qualifier.
	for _, pair := range both {
		ops = append(ops, tableNamespaceMoves(pair)...)
	}
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
	// name. When a classifier is injected, decision-requiring fact
	// changes (carrier/pk/custom_type/enum-fqn/enum-remove/element)
	// surface as ReviewFindings and the matching Op is *omitted*; the
	// engine layer (step 6) decides whether to splice it back post-
	// Resolution. When classifier is nil, those axes preserve today's
	// plain-error behaviour for tests that haven't wired one.
	var alterErrs []error
	var findings []*planpb.ReviewFinding
	for _, pair := range both {
		alters, fs, err := columnAlters(pair, cls)
		if err != nil {
			alterErrs = append(alterErrs, err)
			continue
		}
		ops = append(ops, alters...)
		findings = append(findings, fs...)
	}
	if len(alterErrs) > 0 {
		return nil, fmt.Errorf("plan: column-alter: %w", joinErrs(alterErrs))
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

	return &DiffResult{
		Plan:     &planpb.MigrationPlan{Ops: ops},
		Findings: findings,
	}, nil
}

// joinErrs merges a slice of errors into one, using errors.Join
// semantics. Extracted so the Diff body stays tight and testable.
func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	// Go 1.20+ errors.Join preserves individual error chains.
	out := errs[0]
	for _, e := range errs[1:] {
		out = fmt.Errorf("%w; %v", out, e)
	}
	return out
}

// buildColumnFinding constructs a ReviewFinding for a decision-required
// column axis. ID is a deterministic sha256 of (message FQN, axis,
// prev wire, curr wire) so resolutions survive re-runs idempotently
// per D30. Severity = BLOCK by default (DROP_AND_CREATE + CUSTOM_MIGRATION
// always need explicit user opt-in). Options list mirrors the cell's
// proposed strategy plus the always-available DROP_AND_CREATE /
// CUSTOM_MIGRATION escape hatches.
func buildColumnFinding(table *irpb.Table, prev, curr *irpb.Column, axis string, cell classifier.Cell) *planpb.ReviewFinding {
	id := findingID(table.GetMessageFqn(), axis, prev, curr)
	severity := planpb.Severity_BLOCK
	// Proposed strategy = what classifier says; Options includes all
	// user-selectable strategies (proposed + universal opt-ins).
	options := []planpb.Strategy{cell.Strategy}
	switch cell.Strategy {
	case planpb.Strategy_CUSTOM_MIGRATION:
		options = append(options, planpb.Strategy_DROP_AND_CREATE)
	case planpb.Strategy_DROP_AND_CREATE:
		options = append(options, planpb.Strategy_CUSTOM_MIGRATION)
	default:
		options = append(options,
			planpb.Strategy_DROP_AND_CREATE,
			planpb.Strategy_CUSTOM_MIGRATION,
		)
	}
	return &planpb.ReviewFinding{
		Id: id,
		Column: &planpb.ColumnRef{
			TableFqn:    table.GetMessageFqn(),
			TableName:   table.GetName(),
			FieldNumber: curr.GetFieldNumber(),
			ColumnName:  curr.GetName(),
		},
		Axis:      axis,
		Proposed:  cell.Strategy,
		Options:   options,
		Rationale: cell.Rationale,
		Severity:  severity,
		Context: &planpb.FindingContext{
			Kind: &planpb.FindingContext_Column{
				Column: &planpb.ColumnContext{Prev: prev, Curr: curr},
			},
			PrevSummary: columnSummary(prev),
			CurrSummary: columnSummary(curr),
		},
	}
}

// findingID — deterministic hash of (message FQN, axis, prev wire,
// curr wire). Same inputs → same ID; lets resolutions survive
// re-runs (D30 idempotence rule).
func findingID(msgFqn, axis string, prev, curr *irpb.Column) string {
	h := sha256.New()
	h.Write([]byte(msgFqn))
	h.Write([]byte{0})
	h.Write([]byte(axis))
	h.Write([]byte{0})
	if prevBytes, err := proto.Marshal(prev); err == nil {
		h.Write(prevBytes)
	}
	h.Write([]byte{0})
	if currBytes, err := proto.Marshal(curr); err == nil {
		h.Write(currBytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// columnSummary produces a one-liner humans can read in ReviewFinding
// previews. Not the source of truth for context (ColumnContext carries
// the full Column proto); this is a convenience for UI / CLI printing.
func columnSummary(c *irpb.Column) string {
	if c == nil {
		return "<nil>"
	}
	parts := []string{fmt.Sprintf("%s %s", c.GetCarrier(), c.GetDbType())}
	if c.GetNullable() {
		parts = append(parts, "NULL")
	} else {
		parts = append(parts, "NOT NULL")
	}
	if c.GetMaxLen() != 0 {
		parts = append(parts, fmt.Sprintf("max_len=%d", c.GetMaxLen()))
	}
	if c.GetPk() {
		parts = append(parts, "PK")
	}
	return joinStrings(parts, " ")
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
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
// Decision-required facts (carrier, pk, custom_type, element_*,
// enum-fqn, enum-remove) emit ReviewFindings when a classifier is
// provided; the matching Op is omitted (emit never sees it). When
// cls is nil, those axes fall back to plain errors for backward
// compat with pre-D30 tests.
func columnAlters(pair TablePair, cls *classifier.Classifier) ([]*planpb.Op, []*planpb.ReviewFinding, error) {
	prevByNum := numberMap(pair.Prev.GetColumns())
	ctx := tableCtxOf(pair.Curr)
	var ops []*planpb.Op
	var findings []*planpb.ReviewFinding
	var refusals []error
	for _, currCol := range pair.Curr.GetColumns() {
		prevCol, both := prevByNum[currCol.GetFieldNumber()]
		if !both {
			continue
		}
		changes, fs, err := buildFactChanges(pair.Curr, prevCol, currCol, cls)
		if err != nil {
			refusals = append(refusals, err)
			continue
		}
		findings = append(findings, fs...)
		if len(changes) == 0 {
			continue
		}
		ops = append(ops, &planpb.Op{Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
			Ctx:         ctx,
			FieldNumber: currCol.GetFieldNumber(),
			ColumnName:  currCol.GetName(),
			Changes:     changes,
			Column:      currCol,
			PrevColumn:  prevCol,
		}}})
	}
	if len(refusals) > 0 {
		return nil, nil, joinErrs(refusals)
	}
	return ops, findings, nil
}

// buildFactChanges walks every fact axis on a Column pair and
// emits a FactChange entry per axis whose value differs. When a
// classifier is supplied, decision-required axes (carrier / pk /
// custom_type / element reshape / enum fqn / enum remove) produce
// ReviewFindings instead of errors; the Op for that column is
// omitted (findings[0].column + axis tells engine.Plan what to
// do). When cls is nil, those axes error as before.
//
// Ordering in the returned slice is fact-class declaration order
// here so emit produces deterministic SQL.
func buildFactChanges(carrierTable *irpb.Table, prev, curr *irpb.Column, cls *classifier.Classifier) ([]*planpb.FactChange, []*planpb.ReviewFinding, error) {
	var findings []*planpb.ReviewFinding

	if prev.GetCarrier() != curr.GetCarrier() {
		if cls == nil {
			return nil, nil, fmt.Errorf("column %q (#%d): proto carrier change %v→%v is REFUSE-strategy (drop the field and add a new one with a fresh number)",
				curr.GetName(), curr.GetFieldNumber(), prev.GetCarrier(), curr.GetCarrier())
		}
		cell := cls.Carrier(prev.GetCarrier(), curr.GetCarrier())
		findings = append(findings, buildColumnFinding(carrierTable, prev, curr, "carrier_change", cell))
		return nil, findings, nil
	}
	if prev.GetPk() != curr.GetPk() {
		axisCase := "enable"
		if prev.GetPk() {
			axisCase = "disable"
		}
		if cls == nil {
			return nil, nil, fmt.Errorf("column %q (#%d): primary-key flip is REFUSE-strategy (PK change is table-rebuild territory; author writes an explicit migration)",
				curr.GetName(), curr.GetFieldNumber())
		}
		cell := cls.Constraint("pk", axisCase)
		findings = append(findings, buildColumnFinding(carrierTable, prev, curr, "pk_flip", cell))
		return nil, findings, nil
	}
	if prev.GetElementCarrier() != curr.GetElementCarrier() ||
		prev.GetElementIsMessage() != curr.GetElementIsMessage() {
		if cls == nil {
			return nil, nil, fmt.Errorf("column %q (#%d): collection element reshape is REFUSE-strategy",
				curr.GetName(), curr.GetFieldNumber())
		}
		cell := cls.Constraint("element_reshape", "any")
		findings = append(findings, buildColumnFinding(carrierTable, prev, curr, "element_reshape", cell))
		return nil, findings, nil
	}
	// D36 — detection runs on alias identity (post-registry resolution)
	// + falls back to sql_type comparison for legacy fields that may
	// not carry an alias yet (empty custom_type on both sides → no-op).
	prevAlias := prev.GetPg().GetCustomTypeAlias()
	currAlias := curr.GetPg().GetCustomTypeAlias()
	prevPgCT := prev.GetPg().GetCustomType()
	currPgCT := curr.GetPg().GetCustomType()
	aliasChanged := prevAlias != currAlias
	sqlTypeChanged := prevPgCT != currPgCT
	if aliasChanged || sqlTypeChanged {
		if cls == nil {
			return nil, nil, fmt.Errorf("column %q (#%d): (w17.pg.field).custom_type change %q→%q is REFUSE-strategy (custom_type is author-owned)",
				curr.GetName(), curr.GetFieldNumber(), prevAlias, currAlias)
		}
		cell := cls.Constraint("pg_custom_type", "any")
		findings = append(findings, buildColumnFinding(carrierTable, prev, curr, "pg_custom_type", cell))
		return nil, findings, nil
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
	if numericChanged(prev, curr) {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_NumericPrecision{NumericPrecision: &planpb.NumericPrecisionChange{
			FromPrecision: prev.GetPrecision(),
			FromScale:     scalePtr(prev),
			ToPrecision:   curr.GetPrecision(),
			ToScale:       scalePtr(curr),
		}}})
	}
	if prev.GetDbType() != curr.GetDbType() {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_DbType{DbType: &planpb.DbTypeChange{
			From: prev.GetDbType(), To: curr.GetDbType(),
		}}})
	}
	// Unique flag changes ride on the Index bucket — iter-1 synthesises
	// a UNIQUE INDEX into Table.Indexes at IR build time, so a flag
	// flip surfaces as an Index add/drop there. Emitting a UniqueChange
	// here too would double-add / double-drop the same constraint.
	if prev.GetGeneratedExpr() != curr.GetGeneratedExpr() {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_GeneratedExpr{GeneratedExpr: &planpb.GeneratedExprChange{
			From: prev.GetGeneratedExpr(), To: curr.GetGeneratedExpr(),
		}}})
	}
	if prev.GetComment() != curr.GetComment() {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_Comment{Comment: &planpb.CommentChange{
			From: prev.GetComment(), To: curr.GetComment(),
		}}})
	}
	enumChange, enumFinding, err := enumValuesFactChange(carrierTable, prev, curr, cls)
	if err != nil {
		return nil, nil, err
	}
	if enumFinding != nil {
		findings = append(findings, enumFinding)
		return nil, findings, nil
	}
	if enumChange != nil {
		out = append(out, enumChange)
	}
	if !equalStringSlices(prev.GetAllowedExtensions(), curr.GetAllowedExtensions()) {
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_AllowedExtensions{AllowedExtensions: &planpb.AllowedExtensionsChange{
			From: prev.GetAllowedExtensions(),
			To:   curr.GetAllowedExtensions(),
		}}})
	}
	if !proto.Equal(prev.GetPg(), curr.GetPg()) {
		// Custom_type already handled above; only required_extensions
		// can differ here. Manifest-only impact (no DDL); FactChange carried
		// for downstream consumers (M4 capability tracking).
		out = append(out, &planpb.FactChange{Variant: &planpb.FactChange_PgOptions{PgOptions: &planpb.PgOptionsChange{
			From: prev.GetPg(), To: curr.GetPg(),
		}}})
	}
	return out, findings, nil
}

// enumValuesFactChange handles SEM_ENUM column evolution. Returns
// nil when the names list is unchanged. When a classifier is
// provided, enum FQN change and enum value removal surface as
// ReviewFindings; the FactChange return stays nil in those cases so
// emit skips the column. "Added only" → EnumValuesChange with the
// added subset (SAFE; no finding).
func enumValuesFactChange(carrierTable *irpb.Table, prev, curr *irpb.Column, cls *classifier.Classifier) (*planpb.FactChange, *planpb.ReviewFinding, error) {
	if curr.GetType() != irpb.SemType_SEM_ENUM || prev.GetType() != irpb.SemType_SEM_ENUM {
		return nil, nil, nil
	}
	if prev.GetEnumFqn() != curr.GetEnumFqn() {
		if cls == nil {
			return nil, nil, fmt.Errorf("column %q (#%d): enum FQN change %q→%q is REFUSE-strategy (different proto enum entirely; drop the column and re-add)",
				curr.GetName(), curr.GetFieldNumber(), prev.GetEnumFqn(), curr.GetEnumFqn())
		}
		cell := cls.Constraint("enum_values", "fqn_change")
		return nil, buildColumnFinding(carrierTable, prev, curr, "enum_fqn_change", cell), nil
	}
	prevSet := stringSet(prev.GetEnumNames())
	currSet := stringSet(curr.GetEnumNames())
	var removed []string
	for n := range prevSet {
		if _, ok := currSet[n]; !ok {
			removed = append(removed, n)
		}
	}
	if len(removed) > 0 {
		sort.Strings(removed)
		if cls == nil {
			return nil, nil, fmt.Errorf("column %q (#%d): enum value removal %v is REFUSE-strategy (PG can't drop enum values; drop the column or recreate the type)",
				curr.GetName(), curr.GetFieldNumber(), removed)
		}
		cell := cls.Constraint("enum_values", "remove")
		return nil, buildColumnFinding(carrierTable, prev, curr, "enum_values_remove", cell), nil
	}
	var addedNames []string
	var addedNumbers []int64
	for i, n := range curr.GetEnumNames() {
		if _, ok := prevSet[n]; ok {
			continue
		}
		addedNames = append(addedNames, n)
		if i < len(curr.GetEnumNumbers()) {
			addedNumbers = append(addedNumbers, curr.GetEnumNumbers()[i])
		}
	}
	if len(addedNames) == 0 {
		return nil, nil, nil
	}
	return &planpb.FactChange{Variant: &planpb.FactChange_EnumValues{EnumValues: &planpb.EnumValuesChange{
		AddedNames:   addedNames,
		AddedNumbers: addedNumbers,
	}}}, nil, nil
}

func stringSet(ss []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		out[s] = struct{}{}
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

// numericChanged returns true when precision or scale differs.
// Precision-only IR fields use int32 + optional int32 (proto3
// optional); compare via the wrapper-aware getter.
func numericChanged(prev, curr *irpb.Column) bool {
	if prev.GetPrecision() != curr.GetPrecision() {
		return true
	}
	pP, pPok := scaleOf(prev)
	cP, cPok := scaleOf(curr)
	if pPok != cPok {
		return true
	}
	return pP != cP
}

func scaleOf(c *irpb.Column) (int32, bool) {
	if c.Scale == nil {
		return 0, false
	}
	return c.GetScale(), true
}

// scalePtr returns *int32 ready to attach to NumericPrecisionChange's
// optional fields. Mirrors the proto3 optional-field convention.
func scalePtr(c *irpb.Column) *int32 {
	if c.Scale == nil {
		return nil
	}
	v := c.GetScale()
	return &v
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
// changed while the FQN AND namespace stayed (D24). Pure name change
// only — namespace moves (which may also imply a baked-prefix name
// change) flow through tableNamespaceMoves instead so we don't
// double-emit.
func tableRenames(pair TablePair) []*planpb.Op {
	if pair.Prev.GetName() == pair.Curr.GetName() {
		return nil
	}
	if namespaceChanged(pair.Prev, pair.Curr) {
		return nil // SetTableNamespace handles
	}
	return []*planpb.Op{{Variant: &planpb.Op_RenameTable{RenameTable: &planpb.RenameTable{
		Ctx:      tableCtxOf(pair.Curr),
		FromName: pair.Prev.GetName(),
		ToName:   pair.Curr.GetName(),
	}}}}
}

// tableNamespaceMoves returns SetTableNamespace ops for `both` pairs
// whose (NamespaceMode, Namespace) changed. Carries from + to mode +
// namespace + table-name pair so the emitter can route per the
// alter-strategies table (SCHEMA↔SCHEMA → SET SCHEMA, PREFIX↔PREFIX
// → RENAME TO with new-prefixed name, cross-mode → chain).
func tableNamespaceMoves(pair TablePair) []*planpb.Op {
	if !namespaceChanged(pair.Prev, pair.Curr) {
		return nil
	}
	return []*planpb.Op{{Variant: &planpb.Op_SetTableNamespace{SetTableNamespace: &planpb.SetTableNamespace{
		MessageFqn:     pair.Curr.GetMessageFqn(),
		TableNameFrom:  pair.Prev.GetName(),
		TableNameTo:    pair.Curr.GetName(),
		FromMode:       pair.Prev.GetNamespaceMode(),
		FromNamespace:  pair.Prev.GetNamespace(),
		ToMode:         pair.Curr.GetNamespaceMode(),
		ToNamespace:    pair.Curr.GetNamespace(),
	}}}}
}

func namespaceChanged(prev, curr *irpb.Table) bool {
	return prev.GetNamespaceMode() != curr.GetNamespaceMode() ||
		prev.GetNamespace() != curr.GetNamespace()
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
