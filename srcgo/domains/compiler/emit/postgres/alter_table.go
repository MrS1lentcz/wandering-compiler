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

// emitAlterColumn walks the FactChange list and renders one ALTER
// TABLE statement per fact, separated by newlines. Down inverts the
// list: each FactChange's symmetric inverse, in REVERSE order so a
// down rollback unwinds in the order applied.
func (e Emitter) emitAlterColumn(ac *planpb.AlterColumn) (string, string, error) {
	if ac.GetColumnName() == "" {
		return "", "", fmt.Errorf("postgres: AlterColumn with empty column_name")
	}
	tbl := tableShellFromCtx(ac.GetCtx(), nil)
	qual := qualifiedTable(tbl)
	colName := ac.GetColumnName()

	var ups, downs []string
	for _, fc := range ac.GetChanges() {
		up, down, err := renderFactChange(qual, colName, fc)
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
// Variants whose strategy is DIRECT use plain ALTER COLUMN; variants
// covered by sub-clauses inside a single ALTER TABLE coalesce in a
// future optimisation pass (one ALTER TABLE per fact today).
func renderFactChange(qualTable, colName string, fc *planpb.FactChange) (string, string, error) {
	switch v := fc.GetVariant().(type) {
	case *planpb.FactChange_Nullable:
		up, down := renderNullableChange(qualTable, colName, v.Nullable)
		return up, down, nil
	case *planpb.FactChange_DefaultValue:
		return renderDefaultChange(qualTable, colName, v.DefaultValue)
	case *planpb.FactChange_MaxLen:
		up, down := renderMaxLenChange(qualTable, colName, v.MaxLen)
		return up, down, nil
	case *planpb.FactChange_NumericPrecision:
		up, down := renderNumericPrecisionChange(qualTable, colName, v.NumericPrecision)
		return up, down, nil
	case *planpb.FactChange_DbType:
		up, down := renderDbTypeChange(qualTable, colName, v.DbType)
		return up, down, nil
	case *planpb.FactChange_Unique:
		up, down := renderUniqueChange(qualTable, colName, v.Unique)
		return up, down, nil
	case *planpb.FactChange_GeneratedExpr:
		up, down := renderGeneratedExprChange(qualTable, colName, v.GeneratedExpr)
		return up, down, nil
	case *planpb.FactChange_Comment:
		up, down := renderColumnCommentChange(qualTable, colName, v.Comment)
		return up, down, nil
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

// renderDefaultChange — DIRECT strategy. SET / DROP DEFAULT. The
// default expression rendering re-uses iter-1's defaultExpr against
// a synthetic Column carrying the relevant fields. Returns an error
// if defaultExpr refuses (defensive against IR slip).
func renderDefaultChange(qual, col string, ch *planpb.DefaultChange) (string, string, error) {
	upStmt, err := defaultStmt(qual, col, "SET DEFAULT", ch.GetTo())
	if err != nil {
		return "", "", err
	}
	downStmt, err := defaultStmt(qual, col, "SET DEFAULT", ch.GetFrom())
	if err != nil {
		return "", "", err
	}
	if upStmt == "" {
		// to = unset → DROP DEFAULT
		upStmt = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qual, col)
	}
	if downStmt == "" {
		downStmt = fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", qual, col)
	}
	return upStmt, downStmt, nil
}

// defaultStmt builds one `ALTER TABLE … ALTER COLUMN … SET DEFAULT
// <expr>;` for a non-nil Default; returns "" for nil so caller can
// substitute DROP DEFAULT.
func defaultStmt(qual, col, action string, def *irpb.Default) (string, error) {
	if def == nil || def.GetVariant() == nil {
		return "", nil
	}
	synthetic := &irpb.Column{Name: col}
	expr, err := defaultExpr(synthetic, def)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s %s;", qual, col, action, expr), nil
}

// renderMaxLenChange — DIRECT strategy. ALTER COLUMN TYPE VARCHAR(N)
// works for both widen and narrow; PG rejects narrow at apply if any
// row exceeds N. Down restores the previous width.
func renderMaxLenChange(qual, col string, ch *planpb.MaxLenChange) (string, string) {
	to := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE VARCHAR(%d);", qual, col, ch.GetTo())
	from := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE VARCHAR(%d);", qual, col, ch.GetFrom())
	return to, from
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
	if scale != nil {
		return fmt.Sprintf("NUMERIC(%d, %d)", precision, *scale)
	}
	return fmt.Sprintf("NUMERIC(%d)", precision)
}

// renderDbTypeChange — USING. ALTER COLUMN TYPE <new> USING col::<new>.
// PG fails apply if the cast doesn't apply to live rows. Compatible
// pairs (TEXT↔VARCHAR / JSON↔JSONB / etc.) cast cleanly; incompatible
// pairs (INTEGER → TEXT) fail at apply time which is the right
// outcome — deploy client + UI surface the data issue per platform
// contract.
func renderDbTypeChange(qual, col string, ch *planpb.DbTypeChange) (string, string) {
	toType := dbTypeKeyword(ch.GetTo())
	fromType := dbTypeKeyword(ch.GetFrom())
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s;", qual, col, toType, col, toType),
		fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s;", qual, col, fromType, col, fromType)
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

// renderGeneratedExprChange — DROP+ADD when add or change. DIRECT
// (DROP EXPRESSION on PG 13+) when remove. Iter-2 M1 supports all
// three since the iter-1 IR carries the prev-side expression for
// reconstruction.
func renderGeneratedExprChange(qual, col string, ch *planpb.GeneratedExprChange) (string, string) {
	from, to := ch.GetFrom(), ch.GetTo()
	switch {
	case from == "" && to != "":
		// Add: PG can't add GENERATED to existing column. Drop+add.
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, to),
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", qual, col)
	case from != "" && to == "":
		// Remove: DROP EXPRESSION (PG 13+).
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP EXPRESSION;", qual, col),
			// Down restores via drop+re-add (no inverse for DROP EXPRESSION).
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, from)
	default:
		// Change: drop+add the column with the new expression.
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, to),
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;\nALTER TABLE %s ADD COLUMN %s GENERATED ALWAYS AS (%s) STORED;",
				qual, col, qual, col, from)
	}
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
