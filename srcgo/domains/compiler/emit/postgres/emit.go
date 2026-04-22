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

// emitAddTable renders CREATE TABLE + separate CREATE INDEX statements
// (up) and DROP INDEX + DROP TABLE (down). Orchestrates the five
// emit stages in order: ENUM types prelude (D17), CREATE TABLE body,
// CREATE INDEX tail, COMMENT ON tail (D22), then down-migration.
// Column and constraint layout follows the reference in
// iteration-1-models.md.
func (e Emitter) emitAddTable(t *irpb.Table) (up string, down string, err error) {
	if t.GetName() == "" {
		return "", "", fmt.Errorf("postgres: AddTable with empty name (builder invariant violated)")
	}
	colByProto := map[string]*irpb.Column{}
	for _, c := range t.GetColumns() {
		colByProto[c.GetProtoName()] = c
	}
	qualTable := qualifiedTable(t)
	enumTypes := collectPgEnumTypes(t)

	var upB strings.Builder
	writeEnumTypePrelude(&upB, t, enumTypes)
	if err := writeCreateTable(&upB, t, colByProto, qualTable); err != nil {
		return "", "", err
	}
	idxNames, err := writeIndexStatements(&upB, t, colByProto, qualTable)
	if err != nil {
		return "", "", err
	}
	writeCommentStatements(&upB, t, qualTable)
	return upB.String(), renderTableDown(t, qualTable, idxNames, enumTypes), nil
}

// writeEnumTypePrelude (D17) prepends `CREATE TYPE <table>_<col> AS ENUM
// (names…)` for every string-carrier SEM_ENUM column before CREATE TABLE.
// Declaration order preserved so goldens stay deterministic. Under SCHEMA
// namespace (D19) the type lives in the same schema as the table; under
// PREFIX mode the type name was already prefixed in IR.
func writeEnumTypePrelude(b *strings.Builder, t *irpb.Table, enumTypes []pgEnumType) {
	for _, et := range enumTypes {
		fmt.Fprintf(b, "CREATE TYPE %s AS ENUM (%s);\n\n", qualifiedIdentifier(t, et.name), strings.Join(et.quotedValues, ", "))
	}
}

// writeCreateTable emits `CREATE TABLE <qual_name> (…);` with column
// lines, composite PK, derived CHECKs, and raw CHECK constraints in the
// established order. Returns an error when any column / check renderer
// refuses the IR as invalid (should be caught at IR build time; this is
// a defense-in-depth boundary).
func writeCreateTable(b *strings.Builder, t *irpb.Table, colByProto map[string]*irpb.Column, qualTable string) error {
	fmt.Fprintf(b, "CREATE TABLE %s (\n", qualTable)
	lines := make([]string, 0, len(t.GetColumns()))
	for _, col := range t.GetColumns() {
		line, err := renderColumn(t, col, colByProto)
		if err != nil {
			return err
		}
		lines = append(lines, line)
	}
	// Composite primary key: only emit as a table-level PRIMARY KEY
	// when more than one PK column exists. Single-column PK is inlined
	// on the column line (see renderColumn).
	if len(t.GetPrimaryKey()) > 1 {
		pkLine, err := renderCompositePrimaryKey(t, colByProto)
		if err != nil {
			return err
		}
		lines = append(lines, pkLine)
	}
	// Table-level CHECK constraints — collected after columns for
	// readability and parity with the reference SQL in
	// iteration-1-models.md.
	for _, col := range t.GetColumns() {
		for _, ck := range col.GetChecks() {
			line, err := renderCheck(t.GetName(), col, ck)
			if err != nil {
				return err
			}
			if line != "" {
				lines = append(lines, "    "+line)
			}
		}
	}
	// Raw CHECK constraints (`(w17.db.table).raw_checks`) — author-
	// supplied SQL expression rendered verbatim inside `CONSTRAINT
	// <name> CHECK (…)`. Declaration order is preserved.
	for _, rc := range t.GetRawChecks() {
		lines = append(lines, fmt.Sprintf("    CONSTRAINT %s CHECK (%s)", rc.GetName(), rc.GetExpr()))
	}
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n);")
	return nil
}

// renderCompositePrimaryKey resolves the SQL column names for a
// multi-column PK and returns the `PRIMARY KEY (...)` line. Errors when a
// PK proto name doesn't resolve to a column — an IR invariant violation
// caught defensively here.
func renderCompositePrimaryKey(t *irpb.Table, colByProto map[string]*irpb.Column) (string, error) {
	sqlNames := make([]string, 0, len(t.GetPrimaryKey()))
	for _, p := range t.GetPrimaryKey() {
		c := colByProto[p]
		if c == nil {
			return "", fmt.Errorf("postgres: table %s: PK references unknown proto field %q", t.GetName(), p)
		}
		sqlNames = append(sqlNames, c.GetName())
	}
	return fmt.Sprintf("    PRIMARY KEY (%s)", strings.Join(sqlNames, ", ")), nil
}

// writeIndexStatements appends the structured + raw CREATE INDEX
// statements after CREATE TABLE. Returns the index names in emission
// order so the down-migration can drop them in reverse.
func writeIndexStatements(b *strings.Builder, t *irpb.Table, colByProto map[string]*irpb.Column, qualTable string) ([]string, error) {
	idxStmts, idxNames, err := renderIndexes(t, colByProto)
	if err != nil {
		return nil, err
	}
	for _, ri := range t.GetRawIndexes() {
		kw := "CREATE INDEX"
		if ri.GetUnique() {
			kw = "CREATE UNIQUE INDEX"
		}
		// Raw index body references columns directly (author wrote the
		// `ON <body>` tail); the table reference is our responsibility.
		// Index name is bare per PG CREATE INDEX syntax (index lives in
		// the table's schema automatically).
		idxStmts = append(idxStmts, fmt.Sprintf("%s %s ON %s %s;", kw, ri.GetName(), qualTable, ri.GetBody()))
		idxNames = append(idxNames, ri.GetName())
	}
	if len(idxStmts) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(idxStmts, "\n"))
	}
	return idxNames, nil
}

// writeCommentStatements (D22) appends `COMMENT ON TABLE / COLUMN`
// statements for every element with a resolved comment. Emitted after
// CREATE TABLE + indexes so the pg_class / pg_attribute rows exist.
// Deterministic order: table first, then columns in declaration order.
// Down: no explicit reset needed — DROP TABLE removes entries in
// pg_description transitively.
func writeCommentStatements(b *strings.Builder, t *irpb.Table, qualTable string) {
	commentStmts := collectCommentStmts(t, qualTable)
	if len(commentStmts) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(commentStmts, "\n"))
	}
}

// renderTableDown emits the down-migration: drop indexes (reverse),
// drop table, then drop ENUM types (reverse declaration order). Indexes
// live inside / alongside the table so they go first; the ENUM types
// are standalone pg_type objects referenced by table columns, so they
// drop after the table that uses them. DROP INDEX / DROP TYPE carry the
// schema qualifier under SCHEMA namespace (robustness against
// search_path); PREFIX mode had the prefix baked into the identifier at
// IR time.
func renderTableDown(t *irpb.Table, qualTable string, idxNames []string, enumTypes []pgEnumType) string {
	var downB strings.Builder
	for i := len(idxNames) - 1; i >= 0; i-- {
		fmt.Fprintf(&downB, "DROP INDEX IF EXISTS %s;\n", qualifiedIdentifier(t, idxNames[i]))
	}
	fmt.Fprintf(&downB, "DROP TABLE IF EXISTS %s;", qualTable)
	for i := len(enumTypes) - 1; i >= 0; i-- {
		fmt.Fprintf(&downB, "\nDROP TYPE IF EXISTS %s;", qualifiedIdentifier(t, enumTypes[i].name))
	}
	return downB.String()
}

// qualifiedTable renders the table-identifier form used inside CREATE
// TABLE, CREATE INDEX ... ON, REFERENCES, DROP TABLE, and every other
// statement that names the table itself. Three cases:
//
//   NONE   → bare <name>
//   SCHEMA → <namespace>.<name> (PG schema qualification)
//   PREFIX → <name> — the prefix is already baked into Table.Name at IR
//            build time (see applyPrefix in ir.Build); no further work
//            at emit time. Treating PREFIX identically to NONE keeps
//            the identifier-rendering path uniform across every
//            emitter site.
//
// Schema qualification is emitter-private: IR stores the bare name +
// mode + namespace, and the emitter builds the final identifier.
// SCHEMA mode keeps Table.Name bare so the differ can detect
// namespace changes separately from rename operations in iter-2.
func qualifiedTable(t *irpb.Table) string {
	if t.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		return t.GetNamespace() + "." + t.GetName()
	}
	return t.GetName()
}

// qualifiedIdentifier renders the schema-qualified form for non-table
// identifiers that live inside the table's namespace: indexes
// (DROP INDEX schema.idx), PG ENUM types (CREATE TYPE schema.type_name,
// DROP TYPE schema.type_name). PREFIX mode: the identifier arrives
// already prefixed from IR — pass through.
//
// CREATE INDEX is special: PG syntax `CREATE INDEX <name> ON <table>`
// does not take a schema qualifier on the index name (the index
// automatically lands in the schema of <table>). That site uses the
// bare identifier directly; this helper is for DROP INDEX + CREATE /
// DROP TYPE.
func qualifiedIdentifier(t *irpb.Table, bare string) string {
	if t.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		return t.GetNamespace() + "." + bare
	}
	return bare
}

// collectCommentStmts renders COMMENT ON TABLE + COMMENT ON COLUMN
// statements for every resolved non-empty comment on the table.
// Table first, then columns in IR declaration order. Qualified names
// pick up the schema namespace; column comments key off the column
// SQL name (respecting (w17.db.column).name overrides). Apostrophes
// in the comment body are SQL-escaped by doubling (sqlStringLiteral).
func collectCommentStmts(t *irpb.Table, qualTable string) []string {
	var out []string
	if c := t.GetComment(); c != "" {
		out = append(out, fmt.Sprintf("COMMENT ON TABLE %s IS %s;", qualTable, sqlStringLiteral(c)))
	}
	for _, col := range t.GetColumns() {
		if c := col.GetComment(); c != "" {
			out = append(out, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s;", qualTable, col.GetName(), sqlStringLiteral(c)))
		}
	}
	return out
}

// pgEnumType captures the derived CREATE TYPE side-data for a single
// string-carrier SEM_ENUM column — the type identifier and the already-
// quoted value list. Collected once per table so emitAddTable can emit
// the CREATE statements in declaration order and the DROPs in reverse.
type pgEnumType struct {
	name         string
	quotedValues []string
}

func collectPgEnumTypes(t *irpb.Table) []pgEnumType {
	var out []pgEnumType
	for _, col := range t.GetColumns() {
		if col.GetType() != irpb.SemType_SEM_ENUM || col.GetCarrier() != irpb.Carrier_CARRIER_STRING {
			continue
		}
		quoted := make([]string, 0, len(col.GetEnumNames()))
		for _, n := range col.GetEnumNames() {
			quoted = append(quoted, sqlStringLiteral(n))
		}
		out = append(out, pgEnumType{
			name:         pgEnumTypeName(t.GetName(), col.GetName()),
			quotedValues: quoted,
		})
	}
	return out
}
