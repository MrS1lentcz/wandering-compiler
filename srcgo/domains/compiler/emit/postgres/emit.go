// Package postgres is the Postgres implementation of emit.DialectEmitter.
// It renders migration Ops to up + down SQL using the type mapping fixed in
// docs/experiments/iteration-1-models.md. Iteration-1 implements AddTable
// only; DropTable / AddColumn / AlterColumn / RenameColumn / AddIndex /
// DropIndex arrive iteration-by-iteration.
//
// Determinism: every per-op renderer walks slices in declaration order. No
// map iteration touches the output path. The ir.Build stage already sorts
// columns (proto declaration order), indexes (table-declared first, then
// synthesised unique, then synthesised storage), and FKs (column order).
package postgres

import (
	"fmt"
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Emitter is the zero-value-usable Postgres implementation of
// emit.DialectEmitter. No configuration knobs in iter-1 — dialect-specific
// toggles (quoting style, schema prefix, …) arrive when pilot schemas need
// them.
type Emitter struct{}

// Name returns the stable dialect identifier.
func (Emitter) Name() string { return "postgres" }

// EmitOp dispatches on the Op variant.
func (e Emitter) EmitOp(op *planpb.Op) (up string, down string, err error) {
	switch v := op.GetVariant().(type) {
	case *planpb.Op_AddTable:
		return e.emitAddTable(v.AddTable.GetTable())
	default:
		return "", "", fmt.Errorf("postgres: unsupported op variant %T (iteration-1 implements AddTable only)", op.GetVariant())
	}
}

// emitAddTable renders CREATE TABLE + separate CREATE INDEX statements (up)
// and DROP INDEX + DROP TABLE (down). Column and constraint layout follows
// the reference in iteration-1-models.md.
func (e Emitter) emitAddTable(t *irpb.Table) (up string, down string, err error) {
	if t.GetName() == "" {
		return "", "", fmt.Errorf("postgres: AddTable with empty name (builder invariant violated)")
	}

	// protoName → Column for index / FK name resolution.
	colByProto := map[string]*irpb.Column{}
	for _, c := range t.GetColumns() {
		colByProto[c.GetProtoName()] = c
	}

	var upB strings.Builder
	fmt.Fprintf(&upB, "CREATE TABLE %s (\n", t.GetName())

	lines := make([]string, 0, len(t.GetColumns()))
	// One line per column.
	for _, col := range t.GetColumns() {
		line, colErr := renderColumn(t, col, colByProto)
		if colErr != nil {
			return "", "", colErr
		}
		lines = append(lines, line)
	}

	// Composite primary key: only emit as a table-level PRIMARY KEY when more
	// than one PK column exists. Single-column PK is inlined on the column
	// line (see renderColumn).
	if len(t.GetPrimaryKey()) > 1 {
		sqlNames := make([]string, 0, len(t.GetPrimaryKey()))
		for _, p := range t.GetPrimaryKey() {
			c := colByProto[p]
			if c == nil {
				return "", "", fmt.Errorf("postgres: table %s: PK references unknown proto field %q", t.GetName(), p)
			}
			sqlNames = append(sqlNames, c.GetName())
		}
		lines = append(lines, fmt.Sprintf("    PRIMARY KEY (%s)", strings.Join(sqlNames, ", ")))
	}

	// Table-level CHECK constraints — collected after columns for readability
	// and parity with the reference SQL in iteration-1-models.md.
	for _, col := range t.GetColumns() {
		for _, ck := range col.GetChecks() {
			line, ckErr := renderCheck(t.GetName(), col, ck)
			if ckErr != nil {
				return "", "", ckErr
			}
			if line != "" {
				lines = append(lines, "    "+line)
			}
		}
	}
	// Raw CHECK constraints (`(w17.db.table).raw_checks`) — author-supplied
	// SQL expression rendered verbatim inside `CONSTRAINT <name> CHECK (…)`.
	// Declaration order is preserved.
	for _, rc := range t.GetRawChecks() {
		lines = append(lines, fmt.Sprintf("    CONSTRAINT %s CHECK (%s)", rc.GetName(), rc.GetExpr()))
	}

	upB.WriteString(strings.Join(lines, ",\n"))
	upB.WriteString("\n);")

	// Separate CREATE INDEX statements — structured indexes first, then
	// raw-body indexes. Down-migration reverses this combined order.
	idxStmts, idxNames, idxErr := renderIndexes(t, colByProto)
	if idxErr != nil {
		return "", "", idxErr
	}
	for _, ri := range t.GetRawIndexes() {
		kw := "CREATE INDEX"
		if ri.GetUnique() {
			kw = "CREATE UNIQUE INDEX"
		}
		idxStmts = append(idxStmts, fmt.Sprintf("%s %s ON %s %s;", kw, ri.GetName(), t.GetName(), ri.GetBody()))
		idxNames = append(idxNames, ri.GetName())
	}
	if len(idxStmts) > 0 {
		upB.WriteString("\n\n")
		upB.WriteString(strings.Join(idxStmts, "\n"))
	}

	// Down: drop indexes (reverse), then drop table.
	var downB strings.Builder
	for i := len(idxNames) - 1; i >= 0; i-- {
		fmt.Fprintf(&downB, "DROP INDEX IF EXISTS %s;\n", idxNames[i])
	}
	fmt.Fprintf(&downB, "DROP TABLE IF EXISTS %s;", t.GetName())

	return upB.String(), downB.String(), nil
}
