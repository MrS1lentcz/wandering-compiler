// Postgres emitters for ALTER TABLE-family Ops introduced in iter-2 M1.
// Each renderer is symmetric: up SQL takes the schema from prev to curr;
// down SQL inverts. The plan-level Emit wrapper composes them.
package postgres

import (
	"fmt"
	"strings"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// emitAddColumn renders ALTER TABLE ... ADD COLUMN ...; plus any
// per-column CHECK constraints + COMMENT ON COLUMN as separate
// statements, plus a CREATE TYPE prelude for string-carrier SEM_ENUM
// columns. Down inverts via DROP COLUMN (+ DROP TYPE for ENUMs;
// PG drops dependent CHECK constraints automatically when the column
// drops).
func (e Emitter) emitAddColumn(ac *planpb.AddColumn, usage *emit.Usage) (string, string, error) {
	col := ac.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: AddColumn with nil column")
	}
	ctx := ac.GetCtx()
	tbl := tableShellFromCtx(ctx, col)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
	qual := qualifiedTable(tbl)
	colByProto := map[string]*irpb.Column{col.GetProtoName(): col}

	colLine, err := renderColumn(tbl, col, colByProto, usage)
	if err != nil {
		return "", "", fmt.Errorf("postgres: AddColumn %s.%s: %w", ctx.GetTableName(), col.GetProtoName(), err)
	}
	// renderColumn is designed for CREATE TABLE body and pads with
	// leading spaces; ALTER TABLE ADD COLUMN takes the bare line.
	colLine = strings.TrimLeft(colLine, " \t")
	// SEM_ENUM on a string carrier produces a column line referencing
	// the derived type name (`<table>_<col>`) bare. Under SCHEMA
	// namespace the type lives in the same schema as the table;
	// substitute the schema-qualified form so ALTER TABLE in a session
	// without `<namespace>` on search_path still resolves the type.
	if col.GetType() == irpb.SemType_SEM_ENUM && col.GetCarrier() == irpb.Carrier_CARRIER_STRING &&
		tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA && tbl.GetNamespace() != "" {
		bare := pgEnumTypeName(tbl.GetName(), col.GetName())
		qualified := tbl.GetNamespace() + "." + bare
		colLine = strings.Replace(colLine, " "+bare+" ", " "+qualified+" ", 1)
		colLine = strings.Replace(colLine, " "+bare+";", " "+qualified+";", 1)
	}

	enumCreate, enumDrop := renderEnumTypeStatements(tbl, col)
	if enumCreate != "" {
		usage.Use(emit.CapEnumType)
	}

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
		usage.Use(emit.CapCommentOn)
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
func (e Emitter) emitRenameTable(rt *planpb.RenameTable, usage *emit.Usage) (string, string, error) {
	from, to := rt.GetFromName(), rt.GetToName()
	if from == "" || to == "" {
		return "", "", fmt.Errorf("postgres: RenameTable missing from/to name (from=%q to=%q)", from, to)
	}
	if from == to {
		return "", "", fmt.Errorf("postgres: RenameTable no-op (from=%q to=%q)", from, to)
	}
	ctx := rt.GetCtx()
	if ctx.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
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

// emitSetTableNamespace renders the move-table-between-namespaces
// strategy per the iter-2 alter-strategies table:
//
//	SCHEMA ↔ SCHEMA (same name): ALTER TABLE <from_qual> SET SCHEMA <to_ns>
//	PREFIX ↔ PREFIX:             ALTER TABLE <from_name> RENAME TO <to_name>
//	NONE → SCHEMA:               ALTER TABLE <from_name> SET SCHEMA <to_ns>
//	SCHEMA → NONE:               ALTER TABLE <from_qual> SET SCHEMA public
//	NONE → PREFIX:               RENAME (with prefix-baked new name)
//	PREFIX → NONE:               RENAME (to bare name)
//	SCHEMA ↔ PREFIX (cross-mode): chain SET SCHEMA + RENAME
//
// Down inverts. PG metadata-only operations, data-preserving in
// all cases.
func (e Emitter) emitSetTableNamespace(stn *planpb.SetTableNamespace, usage *emit.Usage) (string, string, error) {
	from, to := stn.GetFromMode(), stn.GetToMode()
	if modeUsesSchema(from) || modeUsesSchema(to) {
		usage.Use(emit.CapSchemaQualified)
	}
	fromName, toName := stn.GetTableNameFrom(), stn.GetTableNameTo()
	fromNs, toNs := stn.GetFromNamespace(), stn.GetToNamespace()

	fromQual := qualifyName(from, fromNs, fromName)
	toQual := qualifyName(to, toNs, toName)

	// Pure RENAME (any time the namespace mode is PREFIX-or-NONE on
	// both sides and the schema slot doesn't apply).
	if !modeUsesSchema(from) && !modeUsesSchema(to) {
		if fromName == toName {
			return "", "", fmt.Errorf("postgres: SetTableNamespace produced no-op RENAME (from=%q to=%q)", fromName, toName)
		}
		up := fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", fromName, toName)
		down := fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", toName, fromName)
		return up, down, nil
	}

	// Pure SET SCHEMA (both sides SCHEMA OR one side NONE/PREFIX
	// with same SQL identifier).
	if modeUsesSchema(from) && modeUsesSchema(to) && fromName == toName {
		// Inline qualifier (PG syntax: ALTER TABLE <schema>.<name> SET SCHEMA <new_schema>).
		// to_ns is the new schema; from_qual carries the old schema.
		up := fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", fromQual, toNs)
		down := fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", toQual, fromNs)
		return up, down, nil
	}
	if modeUsesSchema(from) && !modeUsesSchema(to) && fromName == toName {
		// SCHEMA → NONE/PREFIX (same identifier). SET SCHEMA public.
		up := fmt.Sprintf("ALTER TABLE %s SET SCHEMA public;", fromQual)
		down := fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", toName, fromNs)
		return up, down, nil
	}
	if !modeUsesSchema(from) && modeUsesSchema(to) && fromName == toName {
		// NONE/PREFIX → SCHEMA (same identifier).
		up := fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", fromName, toNs)
		down := fmt.Sprintf("ALTER TABLE %s SET SCHEMA public;", toQual)
		return up, down, nil
	}

	// Combined: schema move + rename (PREFIX with prefix change crossing
	// SCHEMA mode, etc.). Two-statement chain. Down inverts both.
	up := schemaMoveRenameChain(from, fromName, fromNs, to, toName, toNs)
	down := schemaMoveRenameChain(to, toName, toNs, from, fromName, fromNs)
	return up, down, nil
}

// schemaMoveRenameChain handles the "schema changed AND name changed"
// transitions by chaining SET SCHEMA + RENAME TO (or vice versa).
// Order: rename first to a temp identifier in the source namespace,
// then SET SCHEMA, then rename to the final identifier — but in
// practice for the common case PG accepts a single statement: when
// the source and destination are both schema-qualified (same name
// across schemas) we can just SET SCHEMA, then RENAME if needed.
func schemaMoveRenameChain(fromMode irpb.NamespaceMode, fromName, fromNs string,
	toMode irpb.NamespaceMode, toName, toNs string) string {
	fromQual := qualifyName(fromMode, fromNs, fromName)
	stmts := []string{}
	if modeUsesSchema(fromMode) && modeUsesSchema(toMode) {
		// Two SCHEMAs + name change: SET SCHEMA then RENAME.
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", fromQual, toNs))
		intermediateQual := qualifyName(toMode, toNs, fromName)
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", intermediateQual, toName))
		return strings.Join(stmts, "\n")
	}
	if modeUsesSchema(fromMode) {
		// SCHEMA → NONE/PREFIX with name change: SET SCHEMA public + RENAME.
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s SET SCHEMA public;", fromQual))
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", fromName, toName))
		return strings.Join(stmts, "\n")
	}
	// NONE/PREFIX → SCHEMA with name change: RENAME + SET SCHEMA.
	stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s RENAME TO %s;", fromName, toName))
	stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s SET SCHEMA %s;", toName, toNs))
	return strings.Join(stmts, "\n")
}

func modeUsesSchema(m irpb.NamespaceMode) bool {
	return m == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA
}

func qualifyName(mode irpb.NamespaceMode, ns, name string) string {
	if mode == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA && ns != "" {
		return ns + "." + name
	}
	return name
}

// emitSetTableComment renders COMMENT ON TABLE ... IS '<text>';.
// Empty `to` drops the comment via `IS NULL`. Symmetric: down
// restores the prev value (also via `IS NULL` if prev was empty).
func (e Emitter) emitSetTableComment(stc *planpb.SetTableComment, usage *emit.Usage) (string, string, error) {
	usage.Use(emit.CapCommentOn)
	tbl := tableShellFromCtx(stc.GetCtx(), nil)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
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

// emitAlterColumn walks the FactChange list and renders one ALTER
// TABLE statement per fact, separated by newlines. Down inverts the
// list: each FactChange's symmetric inverse, in REVERSE order so a
// down rollback unwinds in the order applied.
func (e Emitter) emitAlterColumn(ac *planpb.AlterColumn, usage *emit.Usage) (string, string, error) {
	if ac.GetColumnName() == "" {
		return "", "", fmt.Errorf("postgres: AlterColumn with empty column_name")
	}
	tbl := tableShellFromCtx(ac.GetCtx(), nil)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
	qual := qualifiedTable(tbl)
	colName := ac.GetColumnName()

	var ups, downs []string
	for _, fc := range ac.GetChanges() {
		up, down, err := renderFactChange(qual, colName, fc, ac.GetColumn(), ac.GetPrevColumn(), usage)
		if err != nil {
			return "", "", fmt.Errorf("postgres: AlterColumn %s.%s: %w", ac.GetCtx().GetTableName(), colName, err)
		}
		if up != "" {
			ups = append(ups, up)
		}
		if down != "" {
			downs = append(downs, down)
		}
	}
	// Reverse downs so rollback unwinds in apply-reverse order.
	for i, j := 0, len(downs)-1; i < j; i, j = i+1, j-1 {
		downs[i], downs[j] = downs[j], downs[i]
	}
	return strings.Join(ups, "\n"), strings.Join(downs, "\n"), nil
}

// renderFactChange dispatches one FactChange variant to its emit.
// Each branch returns (up, down) statements as fully-terminated SQL.
// `column` + `prevColumn` supply the full post-/pre-change IR Column
// for variants that need richer context than their own from/to values
// (GeneratedExpr's column-type prefix, DbType's effective-type
// derivation when the other side is UNSPECIFIED).
func renderFactChange(qualTable, colName string, fc *planpb.FactChange, column, prevColumn *irpb.Column, usage *emit.Usage) (string, string, error) {
	switch v := fc.GetVariant().(type) {
	case *planpb.FactChange_Nullable:
		up, down := renderNullableChange(qualTable, colName, v.Nullable)
		return up, down, nil
	case *planpb.FactChange_DefaultValue:
		return renderDefaultChange(qualTable, colName, v.DefaultValue, column, prevColumn, usage)
	case *planpb.FactChange_MaxLen:
		up, down := renderMaxLenChange(qualTable, colName, v.MaxLen)
		return up, down, nil
	case *planpb.FactChange_NumericPrecision:
		usage.Use(emit.CapNumeric)
		up, down := renderNumericPrecisionChange(qualTable, colName, v.NumericPrecision)
		return up, down, nil
	case *planpb.FactChange_DbType:
		return renderDbTypeChange(qualTable, colName, v.DbType, column, prevColumn, usage)
	case *planpb.FactChange_TypeChange:
		return renderTypeChange(qualTable, colName, v.TypeChange, usage)
	case *planpb.FactChange_Unique:
		up, down := renderUniqueChange(qualTable, colName, v.Unique)
		return up, down, nil
	case *planpb.FactChange_GeneratedExpr:
		return renderGeneratedExprChange(qualTable, colName, v.GeneratedExpr, column, prevColumn, usage)
	case *planpb.FactChange_Comment:
		usage.Use(emit.CapCommentOn)
		up, down := renderColumnCommentChange(qualTable, colName, v.Comment)
		return up, down, nil
	case *planpb.FactChange_EnumValues:
		usage.Use(emit.CapEnumType)
		up, down := renderEnumValuesChange(qualTable, colName, v.EnumValues, column, prevColumn)
		return up, down, nil
	case *planpb.FactChange_PrimaryKey:
		up, down := renderPrimaryKeyChange(qualTable, colName, v.PrimaryKey)
		return up, down, nil
	case *planpb.FactChange_AllowedExtensions:
		// Path-family allowed-extensions changes ride along the
		// regex-CHECK on this column. The CHECK regeneration shows
		// up via the structured ChecksChange path (drop+add the
		// derived `<table>_<col>_format` constraint). Emit a
		// no-DDL marker comment so the FactChange isn't lost in
		// the plan dump.
		return fmt.Sprintf("-- wc: allowed_extensions on %s changed; CHECK rebuild emitted via ChecksChange", colName),
			fmt.Sprintf("-- wc: allowed_extensions on %s changed; CHECK rebuild emitted via ChecksChange", colName), nil
	case *planpb.FactChange_PgOptions:
		// pg.required_extensions diffs only affect the deploy-side
		// extension manifest (M4); no DDL impact at the column axis.
		return fmt.Sprintf("-- wc: pg required_extensions on %s changed; manifest tracking lands in M4", colName),
			fmt.Sprintf("-- wc: pg required_extensions on %s changed; manifest tracking lands in M4", colName), nil
	}
	return "", "", fmt.Errorf("FactChange variant %T not yet implemented", fc.GetVariant())
}

// renderNullableChange — DIRECT strategy. SET / DROP NOT NULL.
// Down inverts. PG fails the SET NOT NULL apply if any row is NULL
// (deploy client pre-checks per the platform contract).
func renderNullableChange(qual, col string, ch *planpb.NullableChange) (string, string) {
	if ch.GetTo() {
		// to = nullable → DROP NOT NULL. Down restores NOT NULL.
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", qual, col),
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", qual, col)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", qual, col),
		fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", qual, col)
}

// renderDefaultChange — column-default lifecycle. Three paths:
//
//   - D38 identity add (!from && to=AUTO_IDENTITY): optional prev
//     DROP DEFAULT + ADD GENERATED BY DEFAULT AS IDENTITY + setval
//     seed. Down inverts via DROP IDENTITY + optional prev SET DEFAULT.
//   - D38 identity drop (from=AUTO_IDENTITY && !to): DROP IDENTITY
//     (+ optional new SET DEFAULT). Down rebuilds ADD GENERATED +
//     setval seed.
//   - Plain SET / DROP DEFAULT for literal + auto_kind transitions.
//     defaultExpr receives the real post-change column so sem-type-
//     sensitive autos (AUTO_NOW → NOW() / CURRENT_DATE / CURRENT_TIME)
//     pick the correct expression per the column's SemType.
func renderDefaultChange(qual, col string, ch *planpb.DefaultChange, column, prevColumn *irpb.Column, usage *emit.Usage) (string, string, error) {
	fromIdentity := isIdentityDefaultIR(ch.GetFrom())
	toIdentity := isIdentityDefaultIR(ch.GetTo())

	switch {
	case !fromIdentity && toIdentity:
		return renderIdentityAdd(qual, col, ch.GetFrom(), prevColumn, usage)
	case fromIdentity && !toIdentity:
		return renderIdentityDrop(qual, col, ch.GetTo(), column, usage)
	}

	upStmt, err := defaultStmt(qual, col, "SET DEFAULT", ch.GetTo(), column, usage)
	if err != nil {
		return "", "", err
	}
	downStmt, err := defaultStmt(qual, col, "SET DEFAULT", ch.GetFrom(), prevColumn, usage)
	if err != nil {
		return "", "", err
	}
	if upStmt == "" {
		upStmt = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qual, col)
	}
	if downStmt == "" {
		downStmt = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qual, col)
	}
	return upStmt, downStmt, nil
}

// defaultStmt builds one `ALTER TABLE … ALTER COLUMN … SET DEFAULT
// <expr>;` for a non-nil Default; returns "" for nil so caller can
// substitute DROP DEFAULT. The `col` snapshot is the column's post-
// (or pre-)change state — defaultExpr needs SemType for sem-type-
// sensitive auto expressions (AUTO_NOW on DATETIME vs DATE vs TIME).
func defaultStmt(qual, colName, action string, def *irpb.Default, col *irpb.Column, usage *emit.Usage) (string, error) {
	if def == nil || def.GetVariant() == nil {
		return "", nil
	}
	ctx := col
	if ctx == nil {
		ctx = &irpb.Column{Name: colName}
	}
	expr, err := defaultExpr(ctx, def, usage)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s %s;", qual, colName, action, expr), nil
}

// isIdentityDefaultIR — true when d is an AUTO_IDENTITY default.
// Mirrors the plan/diff.go helper; kept local to emit so the
// dispatcher doesn't pull an import chain for a 3-line predicate.
func isIdentityDefaultIR(d *irpb.Default) bool {
	if d == nil {
		return false
	}
	auto, ok := d.GetVariant().(*irpb.Default_Auto)
	return ok && auto.Auto == irpb.AutoKind_AUTO_IDENTITY
}

// renderIdentityAdd — D38. Transitions a column to AUTO_IDENTITY.
// If prev had any non-identity default, DROP DEFAULT precedes ADD
// GENERATED (PG rejects mixing the two). setval seeds the sequence
// past current MAX so new INSERTs don't collide with legacy values.
// The sequence name is resolved at apply time via
// pg_get_serial_sequence, so the template works regardless of PG's
// internal seq naming (varies by PG version + table namespace).
//
// Down: DROP IDENTITY + optional restore of the prior default.
func renderIdentityAdd(qual, col string, from *irpb.Default, prevColumn *irpb.Column, usage *emit.Usage) (string, string, error) {
	usage.Use(emit.CapIdentityColumn)
	var up, down []string
	if from != nil && from.GetVariant() != nil {
		up = append(up, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qual, col))
	}
	up = append(up,
		fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s ADD GENERATED BY DEFAULT AS IDENTITY;", qual, col),
		fmt.Sprintf("SELECT setval(pg_get_serial_sequence(%s, %s), COALESCE((SELECT MAX(%s) FROM %s), 1), true);",
			sqlStringLiteral(qual), sqlStringLiteral(col), col, qual),
	)

	down = append(down, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP IDENTITY;", qual, col))
	if from != nil && from.GetVariant() != nil {
		restore, err := defaultStmt(qual, col, "SET DEFAULT", from, prevColumn, usage)
		if err != nil {
			return "", "", fmt.Errorf("identity_add down: restore prev default: %w", err)
		}
		if restore != "" {
			down = append(down, restore)
		}
	}
	return strings.Join(up, "\n"), strings.Join(down, "\n"), nil
}

// renderIdentityDrop — D38. Transitions a column off AUTO_IDENTITY.
// DROP IDENTITY is metadata-only; existing column values are kept.
// If curr carries a replacement default, SET DEFAULT follows.
//
// Down: recreate the IDENTITY (ADD GENERATED + setval from current
// MAX); if the curr had a replacement default, drop it first so the
// ADD GENERATED doesn't collide.
func renderIdentityDrop(qual, col string, to *irpb.Default, column *irpb.Column, usage *emit.Usage) (string, string, error) {
	usage.Use(emit.CapIdentityColumn)
	var up, down []string
	up = append(up, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP IDENTITY;", qual, col))
	if to != nil && to.GetVariant() != nil {
		setDef, err := defaultStmt(qual, col, "SET DEFAULT", to, column, usage)
		if err != nil {
			return "", "", fmt.Errorf("identity_drop up: set replacement default: %w", err)
		}
		if setDef != "" {
			up = append(up, setDef)
		}
	}

	if to != nil && to.GetVariant() != nil {
		down = append(down, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qual, col))
	}
	down = append(down,
		fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s ADD GENERATED BY DEFAULT AS IDENTITY;", qual, col),
		fmt.Sprintf("SELECT setval(pg_get_serial_sequence(%s, %s), COALESCE((SELECT MAX(%s) FROM %s), 1), true);",
			sqlStringLiteral(qual), sqlStringLiteral(col), col, qual),
	)
	return strings.Join(up, "\n"), strings.Join(down, "\n"), nil
}

// renderMaxLenChange — DIRECT strategy. ALTER COLUMN TYPE VARCHAR(N)
// works for both widen and narrow; PG rejects narrow at apply if any
// row exceeds N. Down restores the previous width.
//
// max_len == 0 renders as bare VARCHAR (unbounded — equivalent to
// TEXT in PG). Covers the transition TEXT → VARCHAR(N) and back,
// where the "from" side has no length constraint.
func renderMaxLenChange(qual, col string, ch *planpb.MaxLenChange) (string, string) {
	to := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qual, col, varcharTypeSQL(ch.GetTo()))
	from := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qual, col, varcharTypeSQL(ch.GetFrom()))
	return to, from
}

func varcharTypeSQL(maxLen int32) string {
	if maxLen <= 0 {
		return "VARCHAR"
	}
	return fmt.Sprintf("VARCHAR(%d)", maxLen)
}

// renderColumnCommentChange — DIRECT strategy. COMMENT ON COLUMN …
// IS …; empty values render as IS NULL (PG sentinel for "no
// comment"). Down restores prev.
func renderColumnCommentChange(qual, col string, ch *planpb.CommentChange) (string, string) {
	up := fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", qual, col, commentLiteral(ch.GetTo()))
	down := fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", qual, col, commentLiteral(ch.GetFrom()))
	return up, down
}

// renderNumericPrecisionChange — DIRECT. ALTER COLUMN TYPE
// NUMERIC(p, s). PG accepts widen + narrow at the syntax level;
// narrow with overflowing data fails apply.
func renderNumericPrecisionChange(qual, col string, ch *planpb.NumericPrecisionChange) (string, string) {
	to := numericTypeSQL(ch.GetToPrecision(), ch.ToScale)
	from := numericTypeSQL(ch.GetFromPrecision(), ch.FromScale)
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qual, col, to),
		fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qual, col, from)
}

func numericTypeSQL(precision int32, scale *int32) string {
	if precision <= 0 {
		// No precision constraint — render bare NUMERIC (PG's
		// arbitrary-precision form). Covers the INT family →
		// NUMERIC transition where the INT side carries no
		// precision.
		return "NUMERIC"
	}
	if scale != nil {
		return fmt.Sprintf("NUMERIC(%d, %d)", precision, *scale)
	}
	return fmt.Sprintf("NUMERIC(%d)", precision)
}

// renderDbTypeChange — USING. ALTER COLUMN TYPE <new> USING col::<new>.
// When the FactChange carries DBT_UNSPECIFIED on one side (preset
// storage default), derive the effective type from the column snapshot
// via columnType() so both sides of the ALTER carry real SQL types.
func renderDbTypeChange(qual, col string, ch *planpb.DbTypeChange, curr, prev *irpb.Column, usage *emit.Usage) (string, string, error) {
	toType, err := dbTypeOrEffective(ch.GetTo(), curr, qualToTable(qual), usage)
	if err != nil {
		return "", "", fmt.Errorf("DbType to: %w", err)
	}
	fromType, err := dbTypeOrEffective(ch.GetFrom(), prev, qualToTable(qual), usage)
	if err != nil {
		return "", "", fmt.Errorf("DbType from: %w", err)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s;", qual, col, toType, col, toType),
		fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s;", qual, col, fromType, col, fromType), nil
}

// dbTypeOrEffective returns the bare dialect keyword for a DbType
// when explicit; falls back to columnType() on the Column snapshot
// when the enum is UNSPECIFIED (preset storage). Keeps the ALTER
// TYPE clause well-formed across all (UNSPECIFIED↔explicit) and
// (explicit↔explicit) transitions.
func dbTypeOrEffective(t irpb.DbType, col *irpb.Column, tableName string, usage *emit.Usage) (string, error) {
	if t != irpb.DbType_DB_TYPE_UNSPECIFIED {
		recordDbTypeCap(usage, t)
		return dbTypeKeyword(t), nil
	}
	if col == nil {
		return "", fmt.Errorf("UNSPECIFIED DbType with no column snapshot")
	}
	return columnType(tableName, col, usage)
}

// renderTypeChange — D33 cross-carrier ALTER TABLE ... TYPE clause.
// Column type strings are rendered through the standard columnType
// dispatch (so cap usage lights up the same way as AddTable/
// AddColumn would). USING expressions come pre-rendered by engine
// from the classifier template; empty using = plain ALTER TABLE
// TYPE without USING.
func renderTypeChange(qual, col string, ch *planpb.TypeChange, usage *emit.Usage) (string, string, error) {
	tableName := qualToTable(qual)
	toType, err := columnType(tableName, ch.GetToColumn(), usage)
	if err != nil {
		return "", "", fmt.Errorf("TypeChange to_column: %w", err)
	}
	fromType, err := columnType(tableName, ch.GetFromColumn(), usage)
	if err != nil {
		return "", "", fmt.Errorf("TypeChange from_column: %w", err)
	}
	up := renderAlterColumnType(qual, col, toType, ch.GetUsingUp())
	down := renderAlterColumnType(qual, col, fromType, ch.GetUsingDown())
	return up, down, nil
}

// renderAlterColumnType formats one ALTER TABLE ... ALTER COLUMN
// ... TYPE <type> [USING <expr>]; statement. Empty using omits
// the clause — PG attempts implicit cast, fails at apply if
// incompatible (which is the correct signal).
func renderAlterColumnType(qual, col, typeSQL, using string) string {
	if using == "" {
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", qual, col, typeSQL)
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s;",
		qual, col, typeSQL, using)
}

// recordDbTypeCap mirrors pgColumnFromDbType's cap-tagging for the
// ALTER TYPE path (dbTypeKeyword returns just the keyword and skips
// the column-scoped usage recording). Keeping them separate avoids
// threading usage through the single-arg keyword helper, which tests
// use for golden rendering without a live collector.
func recordDbTypeCap(usage *emit.Usage, t irpb.DbType) {
	switch t {
	case irpb.DbType_DBT_CITEXT:
		usage.Use(emit.CapExtCitext)
	case irpb.DbType_DBT_JSON:
		usage.Use(emit.CapJSON)
	case irpb.DbType_DBT_JSONB:
		usage.Use(emit.CapJSONB)
	case irpb.DbType_DBT_HSTORE:
		usage.Use(emit.CapExtHstore)
	case irpb.DbType_DBT_INET:
		usage.Use(emit.CapINET)
	case irpb.DbType_DBT_CIDR:
		usage.Use(emit.CapCIDR)
	case irpb.DbType_DBT_MACADDR:
		usage.Use(emit.CapMACADDR)
	case irpb.DbType_DBT_TSVECTOR:
		usage.Use(emit.CapTSVECTOR)
	case irpb.DbType_DBT_UUID:
		usage.Use(emit.CapUUID)
	case irpb.DbType_DBT_DOUBLE_PRECISION:
		usage.Use(emit.CapDoublePrecision)
	case irpb.DbType_DBT_NUMERIC:
		usage.Use(emit.CapNumeric)
	case irpb.DbType_DBT_DATE:
		usage.Use(emit.CapDate)
	case irpb.DbType_DBT_TIME:
		usage.Use(emit.CapTime)
	case irpb.DbType_DBT_TIMESTAMP:
		usage.Use(emit.CapTimestamp)
	case irpb.DbType_DBT_TIMESTAMPTZ:
		usage.Use(emit.CapTimestampTZ)
	case irpb.DbType_DBT_INTERVAL:
		usage.Use(emit.CapInterval)
	case irpb.DbType_DBT_BYTEA, irpb.DbType_DBT_BLOB:
		usage.Use(emit.CapBYTEA)
	case irpb.DbType_DBT_BOOLEAN:
		usage.Use(emit.CapBoolean)
	}
}

// dbTypeKeyword renders the bare PG type keyword for a DbType enum
// value. For VARCHAR / NUMERIC the caller is responsible for adding
// the parenthesised modifier (max_len / precision); these are emitted
// via the dedicated NumericPrecision / MaxLen FactChanges.
func dbTypeKeyword(t irpb.DbType) string {
	switch t {
	case irpb.DbType_DBT_TEXT:
		return "TEXT"
	case irpb.DbType_DBT_VARCHAR:
		return "VARCHAR"
	case irpb.DbType_DBT_CITEXT:
		return "CITEXT"
	case irpb.DbType_DBT_JSON:
		return "JSON"
	case irpb.DbType_DBT_JSONB:
		return "JSONB"
	case irpb.DbType_DBT_HSTORE:
		return "HSTORE"
	case irpb.DbType_DBT_INET:
		return "INET"
	case irpb.DbType_DBT_CIDR:
		return "CIDR"
	case irpb.DbType_DBT_MACADDR:
		return "MACADDR"
	case irpb.DbType_DBT_TSVECTOR:
		return "TSVECTOR"
	case irpb.DbType_DBT_UUID:
		return "UUID"
	case irpb.DbType_DBT_SMALLINT:
		return "SMALLINT"
	case irpb.DbType_DBT_INTEGER:
		return "INTEGER"
	case irpb.DbType_DBT_BIGINT:
		return "BIGINT"
	case irpb.DbType_DBT_REAL:
		return "REAL"
	case irpb.DbType_DBT_DOUBLE_PRECISION:
		return "DOUBLE PRECISION"
	case irpb.DbType_DBT_NUMERIC:
		return "NUMERIC"
	case irpb.DbType_DBT_DATE:
		return "DATE"
	case irpb.DbType_DBT_TIME:
		return "TIME"
	case irpb.DbType_DBT_TIMESTAMP:
		return "TIMESTAMP"
	case irpb.DbType_DBT_TIMESTAMPTZ:
		return "TIMESTAMPTZ"
	case irpb.DbType_DBT_INTERVAL:
		return "INTERVAL"
	case irpb.DbType_DBT_BYTEA:
		return "BYTEA"
	case irpb.DbType_DBT_BLOB:
		return "BYTEA"
	case irpb.DbType_DBT_BOOLEAN:
		return "BOOLEAN"
	}
	return ""
}

// renderUniqueChange — DROP+ADD. PG has no ALTER COLUMN for unique
// flips. Constraint name follows iter-1's derived `<table>_<col>_uidx`
// convention so it round-trips against existing iter-1 inline indexes.
func renderUniqueChange(qual, col string, ch *planpb.UniqueChange) (string, string) {
	name := uniqueConstraintName(qualToTable(qual), col)
	add := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);", qual, name, col)
	drop := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s;", qual, name)
	if ch.GetTo() {
		return add, drop
	}
	return drop, add
}

// uniqueConstraintName mirrors iter-1's derived UNIQUE-INDEX name for
// a single-column unique. iter-1 uses `<table>_<col>_uidx` (suffix
// derived in ir.Build); we re-derive for alter-diff identity.
func uniqueConstraintName(table, col string) string {
	return fmt.Sprintf("%s_%s_uidx", table, col)
}

// qualToTable strips the schema-qualifier prefix from a qualified
// identifier so derived constraint names stay stable across NONE /
// SCHEMA modes (the constraint name doesn't include the schema —
// PG keeps constraints in the same namespace as their owning table).
func qualToTable(qual string) string {
	if i := strings.LastIndex(qual, "."); i >= 0 {
		return qual[i+1:]
	}
	return qual
}

// renderPrimaryKeyChange — D39. Single-column PK enable/disable.
// The constraint name follows PG's auto-naming rule: <table>_pkey.
// Engine guarantees single-column scope upstream (multi-column PK
// swaps hard-error in injectPkFlip), so ADD PRIMARY KEY targets
// just this column.
func renderPrimaryKeyChange(qual, col string, ch *planpb.PrimaryKeyChange) (string, string) {
	constraint := qualToTable(qual) + "_pkey"
	addStmt := fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s);", qual, col)
	dropStmt := fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", qual, constraint)
	switch {
	case !ch.GetFrom() && ch.GetTo():
		return addStmt, dropStmt
	case ch.GetFrom() && !ch.GetTo():
		return dropStmt, addStmt
	}
	return "", ""
}

// renderEnumValuesChange handles every direction of enum-type
// evolution. Three branches, picked in priority order:
//
//   - Removed values present (D37 enum_values_remove or D40
//     enum_fqn_change with shrinking value set) — PG has no DROP
//     VALUE, so the rebuild is the only path: CREATE TYPE <t>_new
//     AS ENUM (<curr full values>); ALTER COLUMN USING cast; DROP
//     TYPE old; RENAME new → old. The new type is seeded from
//     curr's full enum_names list, so any concurrently-added values
//     are folded in atomically. Down rebuilds with prev's full list.
//     Cast fails at apply if a row still carries a removed value —
//     user confirmed that risk via --decide needs_confirm.
//   - Added values only (in-axis SAFE for plain enum extension, or
//     D40 enum_fqn_change with growing value set) — ALTER TYPE
//     ADD VALUE per added name. PG has no inverse for ADD VALUE in
//     a single transaction, so down emits a comment marker.
//   - Both empty (D40 enum_fqn_change with identical value sets) —
//     proto-side FQN swap that's a database-side no-op. Emit a
//     comment marker so the migration record reflects the intent
//     without any DDL.
func renderEnumValuesChange(qual, col string, ch *planpb.EnumValuesChange, curr, prev *irpb.Column) (string, string) {
	typeName := pgEnumTypeName(qualToTable(qual), col)
	qualType := typeName
	if i := strings.LastIndex(qual, "."); i >= 0 {
		qualType = qual[:i+1] + typeName
	}

	if len(ch.GetRemovedNames()) > 0 {
		return renderEnumRebuild(qual, col, typeName, qualType, curr, prev)
	}

	if len(ch.GetAddedNames()) == 0 {
		marker := fmt.Sprintf("-- wc: enum FQN change on %s: PG type %s unchanged (identical value set);"+
			" no DDL emitted", col, qualType)
		return marker, marker
	}

	var ups, downs []string
	for _, name := range ch.GetAddedNames() {
		ups = append(ups, fmt.Sprintf("ALTER TYPE %s ADD VALUE %s;", qualType, sqlStringLiteral(name)))
		downs = append(downs, fmt.Sprintf("-- wc: cannot drop ENUM value %s from %s on rollback (PG limitation; manual cleanup required)", sqlStringLiteral(name), qualType))
	}
	return strings.Join(ups, "\n"), strings.Join(downs, "\n")
}

// renderEnumRebuild — D37. 4-statement PG ENUM rebuild, bidirectional.
// The _new suffix is ephemeral within the transaction; final RENAME
// leaves the original type name in place so the column's type doesn't
// drift. Both directions perform the same shape; only the value list
// and bind direction differ.
func renderEnumRebuild(qual, col, typeName, qualType string, curr, prev *irpb.Column) (string, string) {
	newTypeName := typeName + "_new"
	qualNewType := qualType + "_new"
	if i := strings.LastIndex(qualType, "."); i >= 0 {
		qualNewType = qualType[:i+1] + newTypeName
	}

	renderBlock := func(values []string) string {
		quoted := make([]string, 0, len(values))
		for _, n := range values {
			quoted = append(quoted, sqlStringLiteral(n))
		}
		return strings.Join([]string{
			fmt.Sprintf("CREATE TYPE %s AS ENUM (%s);", qualNewType, strings.Join(quoted, ", ")),
			fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::text::%s;", qual, col, qualNewType, col, qualNewType),
			fmt.Sprintf("DROP TYPE %s;", qualType),
			fmt.Sprintf("ALTER TYPE %s RENAME TO %s;", qualNewType, typeName),
		}, "\n")
	}
	up := renderBlock(curr.GetEnumNames())
	down := renderBlock(prev.GetEnumNames())
	return up, down
}

// renderGeneratedExprChange — DROP+ADD when add or change. DIRECT
// (DROP EXPRESSION on PG 13+) when remove. Column snapshots supply
// the column type (prefix before GENERATED ALWAYS AS).
func renderGeneratedExprChange(qual, col string, ch *planpb.GeneratedExprChange, curr, prev *irpb.Column, usage *emit.Usage) (string, string, error) {
	from, to := ch.GetFrom(), ch.GetTo()
	tableName := qualToTable(qual)
	usage.Use(emit.CapGeneratedColumn)
	switch {
	case from == "" && to != "":
		toType, err := columnType(tableName, curr, usage)
		if err != nil {
			return "", "", fmt.Errorf("GeneratedExpr add: column type: %w", err)
		}
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, toType, to),
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", qual, col), nil
	case from != "" && to == "":
		fromType, err := columnType(tableName, prev, usage)
		if err != nil {
			return "", "", fmt.Errorf("GeneratedExpr remove: column type: %w", err)
		}
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP EXPRESSION;", qual, col),
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, fromType, from), nil
	default:
		toType, err := columnType(tableName, curr, usage)
		if err != nil {
			return "", "", fmt.Errorf("GeneratedExpr change: column type curr: %w", err)
		}
		fromType, err := columnType(tableName, prev, usage)
		if err != nil {
			return "", "", fmt.Errorf("GeneratedExpr change: column type prev: %w", err)
		}
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, toType, to),
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, fromType, from), nil
	}
}

// emitDropColumn renders ALTER TABLE ... DROP COLUMN ...; (+ DROP TYPE
// for ENUMs). Down is the inverse — re-creates the column the same way
// emitAddColumn would.
func (e Emitter) emitDropColumn(dc *planpb.DropColumn, usage *emit.Usage) (string, string, error) {
	col := dc.GetColumn()
	if col == nil {
		return "", "", fmt.Errorf("postgres: DropColumn with nil column")
	}
	ctx := dc.GetCtx()
	tbl := tableShellFromCtx(ctx, col)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
	qual := qualifiedTable(tbl)

	var upB strings.Builder
	fmt.Fprintf(&upB, "ALTER TABLE %s DROP COLUMN %s;", qual, col.GetName())
	if col.GetType() == irpb.SemType_SEM_ENUM && col.GetCarrier() == irpb.Carrier_CARRIER_STRING {
		typeName := pgEnumTypeName(tbl.GetName(), col.GetName())
		fmt.Fprintf(&upB, "\nDROP TYPE IF EXISTS %s;", qualifiedIdentifier(tbl, typeName))
	}

	// Down rebuilds the column; share the usage collector so caps
	// exposed by the rehydrated shape (JSONB, UUID, …) still land
	// on the manifest — rollback re-applies them.
	addUp, _, err := e.emitAddColumn(&planpb.AddColumn{Ctx: ctx, Column: col}, usage)
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
