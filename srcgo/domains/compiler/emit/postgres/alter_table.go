// Postgres emitters for ALTER TABLE-family Ops introduced in iter-2 M1.
// Each renderer is symmetric: up SQL takes the schema from prev to curr;
// down SQL inverts. The plan-level Emit wrapper composes them.
package postgres

import (
	"fmt"
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// emitAddColumn renders ALTER TABLE ... ADD COLUMN ...; plus any
// per-column CHECK constraints + COMMENT ON COLUMN as separate
// statements, plus a CREATE TYPE prelude for string-carrier SEM_ENUM
// columns. Down inverts via DROP COLUMN (+ DROP TYPE for ENUMs;
// PG drops dependent CHECK constraints automatically when the column
// drops).
func (e Emitter) emitAddColumn(ac *planpb.AddColumn) (string, string, error) {
	col := ac.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: AddColumn with nil column")
	}
	ctx := ac.GetCtx()
	tbl := tableShellFromCtx(ctx, col)
	qual := qualifiedTable(tbl)
	colByProto := map[string]*irpb.Column{col.GetProtoName(): col}

	colLine, err := renderColumn(tbl, col, colByProto)
	if err != nil {
		return "", "", fmt.Errorf("postgres: AddColumn %s.%s: %w", ctx.GetTableName(), col.GetProtoName(), err)
	}
	// renderColumn is designed for CREATE TABLE body and pads with
	// leading spaces; ALTER TABLE ADD COLUMN takes the bare line.
	colLine = strings.TrimLeft(colLine, " \t")

	enumCreate, enumDrop := renderEnumTypeStatements(tbl, col)

	var upB strings.Builder
	upB.WriteString(enumCreate)
	fmt.Fprintf(&upB, "ALTER TABLE %s ADD COLUMN %s;", qual, colLine)
	for _, ck := range col.GetChecks() {
		ckLine, err := renderCheck(tbl.GetName(), col, ck)
		if err != nil {
			return "", "", fmt.Errorf("postgres: AddColumn %s.%s check: %w", ctx.GetTableName(), col.GetProtoName(), err)
		}
		if ckLine == "" {
			continue
		}
		fmt.Fprintf(&upB, "\nALTER TABLE %s ADD %s;", qual, ckLine)
	}
	if c := col.GetComment(); c != "" {
		fmt.Fprintf(&upB, "\nCOMMENT ON COLUMN %s.%s IS %s;", qual, col.GetName(), sqlStringLiteral(c))
	}

	var downB strings.Builder
	fmt.Fprintf(&downB, "ALTER TABLE %s DROP COLUMN %s;", qual, col.GetName())
	if enumDrop != "" {
		downB.WriteString("\n")
		downB.WriteString(enumDrop)
	}
	return upB.String(), downB.String(), nil
}

// emitRenameTable renders ALTER TABLE ... RENAME TO <new>. Symmetric:
// down swaps. PG metadata-only operation, data-preserving. Schema
// qualification: the old name is qualified with the same namespace
// the new name lives in (RENAME doesn't move schemas — that's
// SetTableNamespace's job).
func (e Emitter) emitRenameTable(rt *planpb.RenameTable) (string, string, error) {
	from, to := rt.GetFromName(), rt.GetToName()
	if from == "" || to == "" {
		return "", "", fmt.Errorf("postgres: RenameTable missing from/to name (from=%q to=%q)", from, to)
	}
	if from == to {
		return "", "", fmt.Errorf("postgres: RenameTable no-op (from=%q to=%q)", from, to)
	}
	ctx := rt.GetCtx()
	tblFrom := tableShellFromCtx(&planpb.TableCtx{
		TableName:     from,
		NamespaceMode: ctx.GetNamespaceMode(),
		Namespace:     ctx.GetNamespace(),
	}, nil)
	tblTo := tableShellFromCtx(&planpb.TableCtx{
		TableName:     to,
		NamespaceMode: ctx.GetNamespaceMode(),
		Namespace:     ctx.GetNamespace(),
	}, nil)
	qualFrom := qualifiedTable(tblFrom)
	qualTo := qualifiedTable(tblTo)
	up := fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", qualFrom, to)
	down := fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", qualTo, from)
	return up, down, nil
}

// emitSetTableComment renders COMMENT ON TABLE ... IS '<text>';.
// Empty `to` drops the comment via `IS NULL`. Symmetric: down
// restores the prev value (also via `IS NULL` if prev was empty).
func (e Emitter) emitSetTableComment(stc *planpb.SetTableComment) (string, string, error) {
	tbl := tableShellFromCtx(stc.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	up := fmt.Sprintf("COMMENT ON TABLE %s IS %s;", qual, commentLiteral(stc.GetTo()))
	down := fmt.Sprintf("COMMENT ON TABLE %s IS %s;", qual, commentLiteral(stc.GetFrom()))
	return up, down, nil
}

// commentLiteral renders a comment value: empty = NULL (PG's "no
// comment" sentinel), otherwise SQL string literal with quotes
// doubled by sqlStringLiteral.
func commentLiteral(v string) string {
	if v == "" {
		return "NULL"
	}
	return sqlStringLiteral(v)
}

// emitRenameColumn renders ALTER TABLE ... RENAME COLUMN <from> TO
// <to>. Symmetric: down swaps. PG ALTER ... RENAME COLUMN is a
// metadata-only operation (no rewrite), data-preserving.
func (e Emitter) emitRenameColumn(rc *planpb.RenameColumn) (string, string, error) {
	from, to := rc.GetFromName(), rc.GetToName()
	if from == "" || to == "" {
		return "", "", fmt.Errorf("postgres: RenameColumn missing from/to name (from=%q to=%q)", from, to)
	}
	if from == to {
		return "", "", fmt.Errorf("postgres: RenameColumn no-op (from=%q to=%q)", from, to)
	}
	tbl := tableShellFromCtx(rc.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	up := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", qual, from, to)
	down := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", qual, to, from)
	return up, down, nil
}

// emitDropColumn renders ALTER TABLE ... DROP COLUMN ...; (+ DROP TYPE
// for ENUMs). Down is the inverse — re-creates the column the same way
// emitAddColumn would.
func (e Emitter) emitDropColumn(dc *planpb.DropColumn) (string, string, error) {
	col := dc.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: DropColumn with nil column")
	}
	ctx := dc.GetCtx()
	tbl := tableShellFromCtx(ctx, col)
	qual := qualifiedTable(tbl)

	var upB strings.Builder
	fmt.Fprintf(&upB, "ALTER TABLE %s DROP COLUMN %s;", qual, col.GetName())
	if col.GetType() == irpb.SemType_SEM_ENUM && col.GetCarrier() == irpb.Carrier_CARRIER_STRING {
		typeName := pgEnumTypeName(tbl.GetName(), col.GetName())
		fmt.Fprintf(&upB, "\nDROP TYPE IF EXISTS %s;", qualifiedIdentifier(tbl, typeName))
	}

	addUp, _, err := e.emitAddColumn(&planpb.AddColumn{Ctx: ctx, Column: col})
	if err != nil {
		return "", "", err
	}
	return upB.String(), addUp, nil
}

// tableShellFromCtx synthesises a minimal *irpb.Table carrying the
// fields the column / qualifier renderers consult: name, namespace
// mode + value, and the single Column being operated on. Pass col=nil
// for ops that don't operate on a specific column (e.g. RenameColumn,
// which only needs the table qualifier). The PK + FK lists are
// intentionally empty — adding / dropping an FK or participating in
// PK changes are separate Op variants in the alter-diff plan.
func tableShellFromCtx(ctx *planpb.TableCtx, col *irpb.Column) *irpb.Table {
	t := &irpb.Table{
		Name:          ctx.GetTableName(),
		MessageFqn:    ctx.GetMessageFqn(),
		NamespaceMode: ctx.GetNamespaceMode(),
		Namespace:     ctx.GetNamespace(),
	}
	if col != nil {
		t.Columns = []*irpb.Column{col}
	}
	return t
}

// renderEnumTypeStatements derives the CREATE TYPE / DROP TYPE pair
// for a column that needs PG ENUM storage (string carrier + SEM_ENUM).
// Returns ("", "") for any other column. The CREATE statement is
// fully terminated + trailing newline so it can prepend ADD COLUMN
// directly; the DROP statement has no terminator-trailing — caller
// composes spacing.
func renderEnumTypeStatements(tbl *irpb.Table, col *irpb.Column) (create string, drop string) {
	if col.GetType() != irpb.SemType_SEM_ENUM || col.GetCarrier() != irpb.Carrier_CARRIER_STRING {
		return "", ""
	}
	typeName := pgEnumTypeName(tbl.GetName(), col.GetName())
	quoted := make([]string, 0, len(col.GetEnumNames()))
	for _, n := range col.GetEnumNames() {
		quoted = append(quoted, sqlStringLiteral(n))
	}
	create = fmt.Sprintf("CREATE TYPE %s AS ENUM (%s);\n\n", qualifiedIdentifier(tbl, typeName), strings.Join(quoted, ", "))
	drop = fmt.Sprintf("DROP TYPE IF EXISTS %s;", qualifiedIdentifier(tbl, typeName))
	return create, drop
}
