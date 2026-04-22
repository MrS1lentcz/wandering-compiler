// Package ir builds the dialect-agnostic intermediate representation from
// loader output. The IR is defined as proto messages at
// proto/domains/compiler/types/ir.proto (package w17.compiler.ir → Go irpb)
// so sibling tools — differ, SQL emitters, back-compat lint, changelog,
// visual editor — consume it wire-compat without speaking Go. See
// docs/iteration-1.md D4 (rev 2026-04-21) and tech-spec strategic decision
// #8 (proto, not Go structs).
//
// Build enforces every invariant from D2 / D7 / D8 / D9 and aggregates
// errors via errors.Join so one compile run surfaces all problems. Every
// user-facing diagnostic flows through *diag.Error (file:line:col + why/fix).
//
// Parse-stage descriptor handles live on the loader's LoadedFile (Go struct,
// non-serializable); the proto boundary starts here — Build populates
// SourceLocation messages from protoreflect.SourceLocations in place of
// live FieldDescriptor fields.
package ir

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	w17pb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17"
	dbpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/db"
)

// Build converts a loaded .proto into a validated *irpb.Schema.
func Build(lf *loader.LoadedFile) (*irpb.Schema, error) {
	b := &builder{lf: lf, msgByTable: map[string]*loader.LoadedMessage{}}
	schema := &irpb.Schema{}
	b.resolveNamespace(schema)
	if len(b.errs) > 0 {
		// Namespace errors are fatal (every downstream identifier
		// validation runs against the effective name); bail before
		// buildTable sees a half-populated state.
		return nil, errors.Join(b.errs...)
	}
	b.namespaceMode = schema.GetNamespaceMode()
	b.namespace = schema.GetNamespace()
	if b.namespaceMode == irpb.NamespaceMode_NAMESPACE_MODE_PREFIX {
		b.prefix = b.namespace
	}
	for _, msg := range lf.Messages {
		if msg.Table == nil {
			// Messages without (w17.db.table) aren't compiler inputs in
			// iteration-1. (Enums and helper types live in the same file.)
			continue
		}
		tbl := b.buildTable(msg)
		if tbl != nil {
			schema.Tables = append(schema.Tables, tbl)
			b.msgByTable[tbl.GetName()] = msg
		}
	}
	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}
	b.resolveFKs(schema)
	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}
	return schema, nil
}

// resolveNamespace reads (w17.db.module) off the loader, validates it,
// and populates Schema.namespace_mode + Schema.namespace. Called before
// buildTable so the prefix, when active, is available for derived-name
// length validation (table_name, check_name, index_name, enum_type_name
// all overflow differently under a prefix).
//
// Validation:
//   - SCHEMA mode: namespace must be non-empty, valid identifier, and
//     not a reserved PG system schema (`pg_*`, `information_schema`).
//   - PREFIX mode: namespace must be non-empty and a valid identifier.
//     No artificial max-length cap beyond NAMEDATALEN — the derived
//     `<prefix>_<table>_<col>_<variant>` validation in buildTable
//     catches overflow honestly.
//   - NONE (default): nothing to validate.
func (b *builder) resolveNamespace(schema *irpb.Schema) {
	mod := b.lf.Module
	if mod == nil {
		return
	}
	switch ns := mod.GetNamespace().(type) {
	case *dbpb.Module_Schema:
		name := ns.Schema
		if name == "" {
			b.err(diag.Atf(b.lf.File, "(w17.db.module).schema is empty").
				WithWhy("declaring the schema oneof variant without a value leaves the module in an ambiguous state — prefer to drop the option entirely for default-schema behaviour, or set a real schema name").
				WithFix(`either remove option (w17.db.module) from this file, or set it to { schema: "<name>" }`))
			return
		}
		if why := validateIdentifier(name); why != "" {
			b.err(diag.Atf(b.lf.File, "(w17.db.module).schema: %s", why).
				WithWhy("the schema name lands in PG `search_path` and every qualified identifier; it must survive NAMEDATALEN and not clash with reserved keywords").
				WithFix("pick a snake_case identifier under 63 bytes that isn't a PG reserved keyword"))
			return
		}
		if isReservedPgSchema(name) {
			b.err(diag.Atf(b.lf.File, "(w17.db.module).schema %q is a reserved PostgreSQL system schema", name).
				WithWhy("PostgreSQL reserves `pg_*`, `information_schema`, and `pg_toast` for system catalogs / toast tables / introspection; creating user tables in those schemas is legal but breaks pg_dump / role assumptions and is universally discouraged").
				WithFix(`rename the schema — avoid the "pg_" prefix and "information_schema". Typical picks: "reporting", "auth", "billing"`))
			return
		}
		schema.NamespaceMode = irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA
		schema.Namespace = name
	case *dbpb.Module_Prefix:
		name := ns.Prefix
		if name == "" {
			b.err(diag.Atf(b.lf.File, "(w17.db.module).prefix is empty").
				WithWhy("declaring the prefix oneof variant without a value leaves the module in an ambiguous state").
				WithFix(`either remove option (w17.db.module) from this file, or set it to { prefix: "<name>" }`))
			return
		}
		if why := validateIdentifier(name); why != "" {
			b.err(diag.Atf(b.lf.File, "(w17.db.module).prefix: %s", why).
				WithWhy("the prefix prepends onto every SQL identifier in this module (table names, derived check / index / type names); it must itself be a valid PG identifier").
				WithFix("pick a short snake_case identifier — typical picks are 2-8 chars like `auth`, `catalog`, `billing`"))
			return
		}
		schema.NamespaceMode = irpb.NamespaceMode_NAMESPACE_MODE_PREFIX
		schema.Namespace = name
	}
}

type builder struct {
	lf         *loader.LoadedFile
	errs       []error
	msgByTable map[string]*loader.LoadedMessage
	// Module-level namespace (D19). Populated once by resolveNamespace
	// from loader's LoadedFile.Module; copied onto every Table via
	// buildTable so emitters see the info Op-local. `prefix` is a
	// convenience alias: non-empty iff namespaceMode == PREFIX. Kept
	// separately so hot-path helpers (applyPrefix in derived-name
	// construction) don't re-check the mode.
	namespaceMode irpb.NamespaceMode
	namespace     string
	prefix        string
}

func (b *builder) err(e *diag.Error) { b.errs = append(b.errs, e) }

func (b *builder) buildTable(msg *loader.LoadedMessage) *irpb.Table {
	if msg.Table.GetName() == "" {
		b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).name is empty", msg.Desc.Name()).
			WithWhy("the SQL table name is never auto-derived from the proto message name (D6 — explicit over implicit)").
			WithFix(`add option (w17.db.table) = { name: "snake_case_plural" };`))
		return nil
	}
	// Validate the EFFECTIVE name that will land in PG. In PREFIX
	// namespace mode (D19) the effective form is `<prefix>_<name>`; in
	// SCHEMA or NONE modes the effective form is the bare name (schema
	// qualification lives in a separate slot and doesn't concatenate
	// into identifier bytes). Running the check on the effective name
	// catches NAMEDATALEN overflow under prefix mode at IR time
	// instead of at apply time.
	effectiveName := applyPrefix(b.prefix, msg.Table.GetName())
	if why := validateIdentifier(effectiveName); why != "" {
		b.err(diag.Atf(msg.Desc, "message %q: %s", msg.Desc.Name(), why).
			WithWhy("Postgres rejects (or silently truncates) identifiers that exceed 63 bytes or collide with reserved keywords — caught here so the failure never reaches apply time").
			WithFix("rename the table via (w17.db.table).name to a shorter / non-reserved identifier (snake_case, plural)"))
		return nil
	}
	tbl := &irpb.Table{
		// Name is the effective SQL name that will land in PG. In PREFIX
		// mode the prefix is baked in here so every downstream
		// consumer (derived index / check / type names, emit-time FK
		// references, raw_index / raw_check name collision namespace)
		// sees one canonical identifier. SCHEMA / NONE modes leave
		// Name bare and the emitter qualifies with `<ns>.<Name>`.
		Name:          effectiveName,
		MessageFqn:    string(msg.Desc.FullName()),
		Location:      sourceLocation(msg.Desc),
		NamespaceMode: b.namespaceMode,
		Namespace:     b.namespace,
	}

	for _, f := range msg.Fields {
		col := b.buildColumn(f)
		if col != nil {
			tbl.Columns = append(tbl.Columns, col)
			if col.GetPk() {
				tbl.PrimaryKey = append(tbl.PrimaryKey, col.GetProtoName())
			}
		}
	}

	// Parse FK references from (w17.db.column). Target resolution runs
	// later in resolveFKs. deletion_rule wins over the null-based
	// inference; if unspecified, nullable columns infer ORPHAN (SET NULL)
	// and non-nullable columns infer CASCADE.
	for _, col := range tbl.Columns {
		f := findLoadedField(msg, col.GetProtoName())
		if f == nil || f.Column == nil || f.Column.GetFk() == "" {
			continue
		}
		ref, ok := parseFKRef(f.Column.GetFk())
		if !ok {
			b.err(diag.Atf(f.Desc, `field %q: fk must be "<table>.<column>", got %q`, col.GetProtoName(), f.Column.GetFk()).
				WithWhy("iteration-1 supports only same-file references in the short form — cross-module package paths arrive later").
				WithFix(`set (w17.db.column) = { fk: "categories.id" } (two segments, table and column, separated by a single dot)`))
			continue
		}
		action, actionErr := resolveFKAction(f, col)
		if actionErr != nil {
			b.err(actionErr)
			continue
		}
		// D19 — FK target is written bare by the author (e.g.
		// `fk: "customers.id"`); apply the module prefix here so the
		// IR-side FK references the same post-prefix name as the
		// target Table.Name. SCHEMA / NONE modes leave it bare; the
		// emitter qualifies SCHEMA references at render time.
		tbl.ForeignKeys = append(tbl.ForeignKeys, &irpb.ForeignKey{
			Column:       col.GetProtoName(),
			TargetTable:  applyPrefix(b.prefix, ref.table),
			TargetColumn: ref.column,
			OnDelete:     action,
		})
	}

	// Table-level indexes.
	for i, idx := range msg.Table.GetIndexes() {
		if len(idx.GetFields()) == 0 {
			b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] has no fields", msg.Desc.Name(), i).
				WithWhy("an index with zero columns has nothing to index on").
				WithFix("supply at least one field name in the `fields:` list"))
			continue
		}
		for _, fname := range idx.GetFields() {
			if findLoadedField(msg, fname) == nil {
				b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] references unknown field %q", msg.Desc.Name(), i, fname).
					WithWhy("every index column must refer to a declared proto field on the same message").
					WithFix(fmt.Sprintf("either declare field %q on the message, or remove it from this index's `fields:` list", fname)))
			}
		}
		for _, fname := range idx.GetInclude() {
			if findLoadedField(msg, fname) == nil {
				b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] INCLUDE references unknown field %q", msg.Desc.Name(), i, fname).
					WithWhy("every INCLUDE column must refer to a declared proto field on the same message").
					WithFix(fmt.Sprintf("either declare field %q on the message, or remove it from `include:`", fname)))
			}
		}
		tbl.Indexes = append(tbl.Indexes, &irpb.Index{
			// Author-supplied name gets the module prefix so it shares
			// the same post-prefix identifier namespace as derived
			// names. In SCHEMA / NONE modes applyPrefix is identity.
			Name:    applyPrefix(b.prefix, idx.GetName()),
			Fields:  append([]string(nil), idx.GetFields()...),
			Unique:  idx.GetUnique(),
			Include: append([]string(nil), idx.GetInclude()...),
		})
	}

	// Synthesise UNIQUE INDEXes for (w17.field).unique columns. PK columns
	// are skipped — every SQL dialect auto-indexes the PRIMARY KEY, and a
	// duplicate unique index would clutter the migration and pg_indexes.
	for _, col := range tbl.Columns {
		if !col.GetUnique() || col.GetPk() {
			continue
		}
		if hasSingleColUniqueIndex(tbl.Indexes, col.GetProtoName()) {
			continue
		}
		tbl.Indexes = append(tbl.Indexes, &irpb.Index{
			Fields: []string{col.GetProtoName()},
			Unique: true,
		})
	}
	// Synthesise plain storage indexes for (w17.db.column).index columns.
	for _, col := range tbl.Columns {
		if !col.GetStorageIndex() {
			continue
		}
		if hasSingleColIndex(tbl.Indexes, col.GetProtoName()) {
			continue
		}
		tbl.Indexes = append(tbl.Indexes, &irpb.Index{
			Fields: []string{col.GetProtoName()},
		})
	}

	// Copy escape-hatch raw CHECK / INDEX bodies from (w17.db.table) into
	// IR. Raw bodies are opaque to the compiler (SQL pass-through), but
	// their identifier names go through the same length/reserved/collision
	// validation as every other emitted identifier.
	for _, rc := range msg.Table.GetRawChecks() {
		// Author-supplied raw_check / raw_index names get the module
		// prefix so they share the same post-prefix identifier
		// namespace as derived names. In SCHEMA / NONE modes
		// applyPrefix is identity.
		tbl.RawChecks = append(tbl.RawChecks, &irpb.RawCheck{
			Name: applyPrefix(b.prefix, rc.GetName()),
			Expr: rc.GetExpr(),
		})
	}
	for _, ri := range msg.Table.GetRawIndexes() {
		tbl.RawIndexes = append(tbl.RawIndexes, &irpb.RawIndex{
			Name:   applyPrefix(b.prefix, ri.GetName()),
			Unique: ri.GetUnique(),
			Body:   ri.GetBody(),
		})
	}

	// Finalise index names up front: derivation lives in IR (not in the
	// emitter) so collision detection and identifier-length validation
	// are possible at IR time. For explicit indexes the author name wins;
	// for synths the <table>_<cols>_<{u,}idx> shape is computed here.
	// Raw indexes carry an author-supplied name and join the same
	// collision namespace. Validate every name (length + reserved) and
	// reject duplicates across all three sources.
	colSQLNameByProto := map[string]string{}
	for _, c := range tbl.Columns {
		colSQLNameByProto[c.GetProtoName()] = c.GetName()
	}
	idxNameSeen := map[string]string{} // name → origin (for diag)
	recordIdx := func(name, origin string, dupFix string) {
		if why := validateIdentifier(name); why != "" {
			b.err(diag.Atf(msg.Desc, "message %q: %s name %s", msg.Desc.Name(), origin, why).
				WithWhy("index names must fit Postgres NAMEDATALEN (63 bytes) and avoid reserved keywords; derived names inherit the table/column lengths, so a long table × multi-col index is a common offender").
				WithFix(`shorten the table / column names, or pass an explicit shorter name via (w17.db.table).indexes[].name (or .raw_indexes[].name)`))
			return
		}
		if prev, dup := idxNameSeen[name]; dup {
			b.err(diag.Atf(msg.Desc, "message %q: %s name %q collides with %s", msg.Desc.Name(), origin, name, prev).
				WithWhy("two CREATE INDEX statements with the same name fail at apply — Postgres has per-schema unique index names. Explicit `(w17.db.table).indexes[].name`, synth'd UNIQUE from `(w17.field).unique`, storage-index from `(w17.db.column).index`, and `(w17.db.table).raw_indexes[].name` all share one namespace").
				WithFix(dupFix))
			return
		}
		idxNameSeen[name] = origin
	}
	for i, idx := range tbl.Indexes {
		if idx.Name == "" {
			sqlCols := make([]string, 0, len(idx.GetFields()))
			for _, f := range idx.GetFields() {
				sqlCols = append(sqlCols, colSQLNameByProto[f])
			}
			idx.Name = derivedIndexName(tbl.GetName(), sqlCols, idx.GetUnique())
		}
		recordIdx(idx.Name, fmt.Sprintf("index[%d]", i),
			"either rename the explicit index to something unique, or drop the redundant synth (remove `unique: true` / `(w17.db.column).index` on the field the explicit index already covers)")
	}
	for i, ri := range tbl.RawIndexes {
		if ri.GetName() == "" {
			b.err(diag.Atf(msg.Desc, "message %q: raw_indexes[%d].name is empty", msg.Desc.Name(), i).
				WithWhy("raw indexes carry no field list to derive a name from — the compiler can't invent one").
				WithFix(`set (w17.db.table).raw_indexes[` + fmt.Sprintf("%d", i) + `].name to a descriptive identifier (convention: <table>_<cols>_<method>)`))
			continue
		}
		if ri.GetBody() == "" {
			b.err(diag.Atf(msg.Desc, "message %q: raw_indexes[%d] %q has empty body", msg.Desc.Name(), i, ri.GetName()).
				WithWhy("an empty body would emit `CREATE INDEX … ON <table>` with nothing after — PG rejects at apply").
				WithFix(`supply the body after ON <table>, e.g. "USING gin (search_tsv)" or "(lower(email)) WHERE deleted_at IS NULL"`))
			continue
		}
		recordIdx(ri.GetName(), fmt.Sprintf("raw_indexes[%d]", i),
			"rename the raw index to avoid collision with an existing derived / explicit index name")
	}

	// Validate CHECK constraint names.
	// Derived: `<table>_<col>_<variant>` — variant suffixes are fixed and
	// non-reserved; only length-check. Cross-column / cross-variant
	// collisions are impossible (attachChecks emits at most one of each
	// variant per column) but raw-check names share the namespace.
	ckNameSeen := map[string]string{}
	for _, c := range tbl.Columns {
		for j, ck := range c.GetChecks() {
			name := derivedCheckName(tbl.GetName(), c.GetName(), ck)
			if why := validateIdentifier(name); why != "" {
				b.err(diag.Atf(msg.Desc, "message %q: column %q check[%d] name %s", msg.Desc.Name(), c.GetProtoName(), j, why).
					WithWhy("constraint names are <table>_<column>_<variant> — long table/column names overflow Postgres NAMEDATALEN (63 bytes) and silently truncate, risking collisions in pg_constraint").
					WithFix("shorten the table / column name to make room for the variant suffix (blank/len/range/format/choices)"))
				continue
			}
			ckNameSeen[name] = fmt.Sprintf("column %q check[%d]", c.GetProtoName(), j)
		}
	}
	for i, rc := range tbl.RawChecks {
		if rc.GetName() == "" {
			b.err(diag.Atf(msg.Desc, "message %q: raw_checks[%d].name is empty", msg.Desc.Name(), i).
				WithWhy("the compiler can't invent a constraint name for an opaque expression").
				WithFix(`set (w17.db.table).raw_checks[` + fmt.Sprintf("%d", i) + `].name to a descriptive identifier (convention: <table>_<what>)`))
			continue
		}
		if rc.GetExpr() == "" {
			b.err(diag.Atf(msg.Desc, "message %q: raw_checks[%d] %q has empty expr", msg.Desc.Name(), i, rc.GetName()).
				WithWhy("an empty expr would emit `CHECK ()` — PG rejects at apply").
				WithFix(`supply the SQL expression that goes inside CHECK(...), e.g. "start_date <= end_date"`))
			continue
		}
		if why := validateIdentifier(rc.GetName()); why != "" {
			b.err(diag.Atf(msg.Desc, "message %q: raw_checks[%d] name %s", msg.Desc.Name(), i, why).
				WithWhy("CHECK constraint names must fit Postgres NAMEDATALEN (63 bytes) and avoid reserved keywords").
				WithFix("shorten or rename the raw check"))
			continue
		}
		if prev, dup := ckNameSeen[rc.GetName()]; dup {
			b.err(diag.Atf(msg.Desc, "message %q: raw_checks[%d] name %q collides with %s", msg.Desc.Name(), i, rc.GetName(), prev).
				WithWhy("CHECK names are unique per table in pg_constraint; raw-check names share the namespace with derived <table>_<column>_<variant> names").
				WithFix("rename the raw check, or remove the per-field option that synthesised the colliding derived name"))
			continue
		}
		ckNameSeen[rc.GetName()] = fmt.Sprintf("raw_checks[%d]", i)
	}

	// D17 — validate PG ENUM type identifier (<table>_<col>) for every
	// string-carrier SEM_ENUM column so length / reserved-keyword
	// failures surface at IR time instead of apply. Mirrors the CHECK
	// constraint name validation above.
	for _, c := range tbl.Columns {
		if c.GetType() != irpb.SemType_SEM_ENUM || c.GetCarrier() != irpb.Carrier_CARRIER_STRING {
			continue
		}
		typeName := fmt.Sprintf("%s_%s", tbl.GetName(), c.GetName())
		if why := validateIdentifier(typeName); why != "" {
			b.err(diag.Atf(msg.Desc, "message %q: column %q ENUM type name %s", msg.Desc.Name(), c.GetProtoName(), why).
				WithWhy("PG derives the ENUM type name from <table>_<column>; long table / column names overflow NAMEDATALEN (63 bytes) and silently truncate — a reserved-keyword collision would fail at CREATE TYPE apply time").
				WithFix("shorten the table or column name, or override the column via (w17.db.column) = { name: \"alt\" }"))
		}
	}

	return tbl
}

func (b *builder) buildColumn(lf *loader.LoadedField) *irpb.Column {
	desc := lf.Desc
	protoName := string(desc.Name())

	carrier, carrierOK := protoKindToCarrier(desc)
	if !carrierOK {
		b.err(diag.Atf(desc, "field %q: carrier %s is not supported in iteration-1", protoName, describeKind(desc)).
			WithWhy("iteration-1 accepts string, int32, int64, bool, double, google.protobuf.Timestamp and google.protobuf.Duration as DB-column carriers; other kinds (bytes, repeated, oneof, nested messages) are parked for later iterations").
			WithFix("change the field's proto type to one of the supported carriers, or drop the (w17.field) annotation if the field isn't a DB column"))
		return nil
	}

	col := &irpb.Column{
		Name:      protoName,
		ProtoName: protoName,
		Location:  sourceLocation(desc),
		Carrier:   carrier,
	}

	// Pull data-level options from (w17.field), if present. (w17.field) is
	// optional on every carrier — D14 per-carrier defaults kick in when
	// the type is unset (e.g. int32 → NUMBER, string → TEXT, Timestamp →
	// DATETIME). Authors reach for (w17.field) only when the default
	// doesn't fit.
	fieldOpt := lf.Field

	// Storage-level options from (w17.db.column).
	if lf.Column != nil {
		if override := lf.Column.GetName(); override != "" {
			col.Name = override
		}
		col.StorageIndex = lf.Column.GetIndex()
		col.GeneratedExpr = lf.Column.GetGeneratedExpr()
	}

	// The resolved SQL column name (proto name by default, overridden via
	// (w17.db.column).name) must survive Postgres's NAMEDATALEN cap and
	// avoid reserved keywords. Checked here rather than after the column
	// is appended so the diag anchors on the owning proto field.
	if why := validateIdentifier(col.Name); why != "" {
		b.err(diag.Atf(desc, "field %q: %s", protoName, why).
			WithWhy("Postgres rejects (or silently truncates) identifiers that exceed 63 bytes or collide with reserved keywords — caught here so the failure never reaches apply time").
			WithFix(`rename the column — either use a shorter proto field name, or override via (w17.db.column) = { name: "alt_name" }`))
		return nil
	}

	// Populate element info for collection carriers (map / list) BEFORE
	// sem-type resolution — LIST sem validation needs the element carrier.
	if carrier == irpb.Carrier_CARRIER_MAP || carrier == irpb.Carrier_CARRIER_LIST {
		if err := b.populateElement(col, desc, carrier); err != nil {
			b.err(err)
			return nil
		}
	}

	// Carrier → SemType validation (D2 table + D14 per-carrier defaults).
	semType := irpb.SemType_SEM_UNSPECIFIED
	if fieldOpt != nil {
		semType = protoTypeToSem(fieldOpt.GetType())
	}
	// D17: a bare scalar proto-enum field (e.g. `Status state = 1;`) auto-
	// infers SEM_ENUM on its int32 wire-type carrier. Matches the D14
	// zero-config philosophy — authors opt out with an explicit type only
	// when they want different storage. Gated on carrier to skip
	// `repeated Status` (list element happens to have EnumKind but the
	// column-level carrier is LIST — that path falls through to SEM_AUTO).
	if semType == irpb.SemType_SEM_UNSPECIFIED && desc.Kind() == protoreflect.EnumKind && carrier == irpb.Carrier_CARRIER_INT32 {
		semType = irpb.SemType_SEM_ENUM
	}
	if semType == irpb.SemType_SEM_UNSPECIFIED {
		semType = defaultSemTypeFor(carrier)
	}
	if err := validateCarrierSemType(desc, carrier, semType); err != nil {
		b.err(err)
		return nil
	}
	// For LIST carrier: the sem type must be valid on the element's
	// scalar carrier (repeated string + type: URL → element carrier
	// STRING must accept SEM_URL). SEM_AUTO is always valid. Message
	// elements ignore element sem — storage is always JSONB.
	if carrier == irpb.Carrier_CARRIER_LIST && semType != irpb.SemType_SEM_AUTO {
		if col.GetElementIsMessage() {
			b.err(diag.Atf(desc, "field %q: repeated Message field cannot carry an element sem type (got %s) — storage is always JSON for message elements", protoName, displaySemType(semType)).
				WithWhy("per-element sem types (URL, EMAIL, …) only apply to scalar elements where the DB can store an element-typed array; proto messages serialise as JSON blobs").
				WithFix("drop type: from (w17.field), or type: AUTO to mark intent explicitly"))
			return nil
		}
		if err := validateCarrierSemType(desc, col.GetElementCarrier(), semType); err != nil {
			b.err(err.WithWhy("list carrier validates type against the element carrier (e.g. repeated string + type: URL checks URL on string carrier)"))
			return nil
		}
	}
	col.Type = semType

	// Nullability, PK, uniqueness, immutability.
	if fieldOpt != nil {
		col.Nullable = fieldOpt.GetNull()
		col.Pk = fieldOpt.GetPk()
		col.Unique = fieldOpt.GetUnique() || col.Pk // PK implies UNIQUE (D2 note).
		col.Immutable = fieldOpt.GetImmutable()
	}

	// deletion_rule validity — must accompany fk on the same column.
	// Catches `(w17.db.column) = { deletion_rule: BLOCK }` without `fk:` —
	// the rule has no referenced parent to act on.
	if lf.Column != nil && lf.Column.GetDeletionRule() != dbpb.DeletionRule_DELETION_RULE_UNSPECIFIED && lf.Column.GetFk() == "" {
		b.err(diag.Atf(desc, "field %q: deletion_rule set without fk", protoName).
			WithWhy("deletion_rule declares what happens to this row when its *parent* row is deleted — meaningless without an fk pointing at a parent").
			WithFix(`either add fk: "<table>.<column>" on (w17.db.column), or remove deletion_rule`))
	}

	// max_len: required for CHAR / SLUG, has a preset default for
	// EMAIL / URL, and is forbidden on string sem types whose storage
	// isn't VARCHAR (UUID, JSON, IP, TSEARCH, DECIMAL) or on non-string
	// carriers (numeric / temporal / bool / bytes).
	if fieldOpt != nil {
		col.MaxLen = fieldOpt.GetMaxLen()
	}
	// max_len applies to string carriers at the column level, AND to
	// list carriers at the ELEMENT level (repeated string + type: URL +
	// max_len: 500 sizes VARCHAR(500)[]).
	maxLenCarrier := carrier
	maxLenSem := semType
	if carrier == irpb.Carrier_CARRIER_LIST {
		maxLenCarrier = col.GetElementCarrier()
		// For AUTO elements, no per-element sem — skip max_len sizing.
		if semType == irpb.SemType_SEM_AUTO {
			maxLenCarrier = irpb.Carrier_CARRIER_UNSPECIFIED
		}
	}
	if maxLenCarrier == irpb.Carrier_CARRIER_STRING {
		switch maxLenSem {
		case irpb.SemType_SEM_CHAR, irpb.SemType_SEM_SLUG:
			if col.MaxLen <= 0 {
				b.err(diag.Atf(desc, "field %q: type %s requires max_len", protoName, displaySemType(maxLenSem)).
					WithWhy("CHAR / SLUG render as VARCHAR(N) — without N the column type has no fixed size").
					WithFix("add max_len to (w17.field), e.g. max_len: 80 for short names, 255 for titles"))
			}
		case irpb.SemType_SEM_EMAIL:
			if col.MaxLen <= 0 {
				col.MaxLen = 320
			}
		case irpb.SemType_SEM_URL:
			if col.MaxLen <= 0 {
				col.MaxLen = 2048
			}
		case irpb.SemType_SEM_TEXT:
			// optional upper bound.
		default:
			if col.MaxLen != 0 {
				b.err(diag.Atf(desc, "field %q: max_len is not valid on type %s", protoName, displaySemType(maxLenSem)).
					WithWhy("max_len drives VARCHAR(N) sizing + char_length CHECKs; UUID / JSON / IP / TSEARCH / DECIMAL map to non-VARCHAR SQL types where max_len has no meaning").
					WithFix("drop max_len, or change type: to CHAR / SLUG / TEXT / EMAIL / URL (all VARCHAR-shaped)"))
			}
		}
	} else if col.MaxLen != 0 {
		b.err(diag.Atf(desc, "field %q: max_len is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
			WithWhy("max_len controls char_length on string columns; numeric / temporal / bool / bytes / map columns have no length dimension").
			WithFix("drop max_len from (w17.field), or change the proto field to a string carrier (or repeated string)"))
	}

	// DECIMAL precision/scale.
	if fieldOpt != nil {
		col.Precision = fieldOpt.GetPrecision()
		if fieldOpt.Scale != nil {
			scale := fieldOpt.GetScale()
			col.Scale = &scale
		}
	}
	if semType == irpb.SemType_SEM_DECIMAL {
		if col.GetPrecision() <= 0 {
			b.err(diag.Atf(desc, "field %q: type DECIMAL requires precision", protoName).
				WithWhy("DECIMAL renders as NUMERIC(precision, scale) — precision is the total number of significant digits and has no safe default").
				WithFix("add precision (and optionally scale) to (w17.field), e.g. { type: DECIMAL, precision: 12, scale: 4 }"))
		}
		if col.Scale != nil && *col.Scale < 0 {
			b.err(diag.Atf(desc, "field %q: DECIMAL scale must be >= 0", protoName).
				WithWhy("negative scale is meaningless for NUMERIC").
				WithFix("drop scale or set it to a non-negative integer"))
		}
		if col.Scale != nil && *col.Scale > col.GetPrecision() {
			b.err(diag.Atf(desc, "field %q: DECIMAL scale (%d) exceeds precision (%d)", protoName, *col.Scale, col.GetPrecision()).
				WithWhy("scale counts digits after the decimal point and cannot exceed total digits").
				WithFix(fmt.Sprintf("raise precision to at least %d, or lower scale to at most %d", *col.Scale, col.GetPrecision())))
		}
	} else {
		if col.GetPrecision() != 0 || col.Scale != nil {
			b.err(diag.Atf(desc, "field %q: precision/scale only apply to type DECIMAL (got %s)", protoName, displaySemType(semType)).
				WithWhy("MONEY/PERCENTAGE/RATIO are fixed-shape presets; other types have no precision/scale dimension").
				WithFix("drop precision/scale, or change the field to type: DECIMAL"))
		}
	}

	// Default value — resolve the oneof and validate against carrier/type.
	if fieldOpt != nil {
		def, err := b.resolveDefault(desc, fieldOpt, carrier, semType)
		if err != nil {
			b.err(err)
		}
		col.Default = def
	}

	// Pk / unique don't compose cleanly with collection carriers — PG
	// allows array PKs / UNIQUE INDEX on arrays technically, but the
	// resulting semantics (equality on whole arrays, index bloat) are
	// rarely what the author wants. Reject up front; iter-2 can revisit
	// if a pilot surfaces a real need.
	if carrier == irpb.Carrier_CARRIER_MAP || carrier == irpb.Carrier_CARRIER_LIST {
		if fieldOpt != nil {
			if fieldOpt.GetPk() {
				b.err(diag.Atf(desc, "field %q: pk not supported on %s carrier", protoName, displayCarrier(carrier)).
					WithWhy("primary keys on map / array columns have degenerate semantics (whole-collection equality, index bloat)").
					WithFix("use a scalar PK (int64 ID with default_auto: IDENTITY is the canonical shape)"))
			}
			if fieldOpt.GetUnique() {
				b.err(diag.Atf(desc, "field %q: unique not supported on %s carrier", protoName, displayCarrier(carrier)).
					WithWhy("UNIQUE INDEX on a collection column checks whole-collection equality, which is almost never the author's intent").
					WithFix("drop unique, or use (w17.db.table).raw_indexes to spell the exact index shape you need (e.g. GIN on the element)"))
			}
		}
	}

	// String-only / numeric-only option validation.
	if fieldOpt != nil {
		// Element-level CHECK synths (blank, length-via-min, regex, choices)
		// can't be expressed as PG CHECK on array / map columns (CHECK
		// constraints don't allow subqueries or forall-element operators).
		// Reject the explicit-author variants here so intent never silently
		// drops; max_len stays allowed because it drives VARCHAR(N)[]
		// storage, not a CHECK.
		if carrier == irpb.Carrier_CARRIER_MAP || carrier == irpb.Carrier_CARRIER_LIST {
			if fieldOpt.MinLen != nil || fieldOpt.GetBlank() || fieldOpt.GetPattern() != "" || fieldOpt.GetChoices() != "" {
				b.err(diag.Atf(desc, "field %q: min_len / blank / pattern / choices are not supported on %s carrier", protoName, displayCarrier(carrier)).
					WithWhy("PG CHECK constraints can't iterate over array / map elements — there's no `forall element` operator without a subquery. Per-element validation needs triggers or application-level checks; raw_checks can spell a whole-column CHECK if that suffices").
					WithFix("drop the string-only option, or move the check to (w17.db.table).raw_checks with a PG-specific body (e.g. cardinality() <= N)"))
			}
		} else if carrier != irpb.Carrier_CARRIER_STRING {
			if fieldOpt.MinLen != nil {
				b.err(diag.Atf(desc, "field %q: min_len is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("min_len controls char_length on strings; other carriers have no length").
					WithFix("drop min_len, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetBlank() {
				b.err(diag.Atf(desc, "field %q: blank is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("blank relaxes the implicit `col <> ''` CHECK on strings; non-string columns have no such CHECK").
					WithFix("drop blank, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetPattern() != "" {
				b.err(diag.Atf(desc, "field %q: pattern is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("pattern emits a regex CHECK; regex only applies to strings").
					WithFix("drop pattern, or change the proto field to a string carrier"))
			}
			// D17: choices: IS permitted on int + SEM_ENUM (it names the
			// enum whose numeric values drive CHECK IN). Reject only when
			// the column isn't a numeric-ENUM column.
			if fieldOpt.GetChoices() != "" && semType != irpb.SemType_SEM_ENUM {
				b.err(diag.Atf(desc, "field %q: choices is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("choices emits `CHECK col IN ('A','B',…)` matched against enum *value names*, which are strings").
					WithFix("drop choices, or change the proto field to a string carrier"))
			}
		}
		// Numeric for range purposes = INT32 / INT64 / DOUBLE or the DECIMAL
		// sub-case (string carrier + SEM_DECIMAL, which emits NUMERIC(p,s)).
		// iteration-1.md D2 permits range bounds on DECIMAL ("bounds are
		// carried via double and are precision-limited") — the earlier
		// carrier-only guard rejected that legitimate combination.
		numericForRange := carrier == irpb.Carrier_CARRIER_INT32 ||
			carrier == irpb.Carrier_CARRIER_INT64 ||
			carrier == irpb.Carrier_CARRIER_DOUBLE ||
			(carrier == irpb.Carrier_CARRIER_STRING && semType == irpb.SemType_SEM_DECIMAL)
		if !numericForRange {
			if fieldOpt.Gt != nil || fieldOpt.Gte != nil || fieldOpt.Lt != nil || fieldOpt.Lte != nil {
				b.err(diag.Atf(desc, "field %q: gt/gte/lt/lte require a numeric carrier or type: DECIMAL (got carrier=%s, type=%s)", protoName, displayCarrier(carrier), displaySemType(semType)).
					WithWhy("the range CHECK emits a numeric comparison; it's undefined for non-numeric types").
					WithFix("drop the bound, or change the proto field to int32/int64/double or string+type: DECIMAL"))
			}
		}
	}

	// Postgres dialect passthrough — must be populated BEFORE attachChecks
	// so the synth layer can see when PG storage is redirected via
	// `custom_type` and skip string-only synths that would fail at apply on
	// the overridden column type. Post-D13: curated flags (jsonb / inet /
	// tsvector / hstore) live as core Types or map-carrier AUTO dispatch;
	// this passthrough is narrow.
	if lf.PgField != nil {
		col.Pg = &irpb.PgOptions{
			CustomType:         lf.PgField.GetCustomType(),
			RequiredExtensions: append([]string(nil), lf.PgField.GetRequiredExtensions()...),
		}
	}

	// (w17.db.column).db_type — storage override (D14). Orthogonal to
	// field.Type: data semantic stays on field.Type, storage shape comes
	// from db_type. Validated for carrier compatibility; conflicts with
	// custom_type (pick one override path).
	if lf.Column != nil {
		col.DbType = dbTypeToIR(lf.Column.GetDbType())
	}
	if col.GetDbType() != irpb.DbType_DB_TYPE_UNSPECIFIED {
		if col.GetPg() != nil && col.GetPg().GetCustomType() != "" {
			b.err(diag.Atf(desc, "field %q: (w17.db.column).db_type conflicts with (w17.pg.field).custom_type", protoName).
				WithWhy("db_type and custom_type are two different storage-override paths: db_type is the enumerated cross-dialect surface, custom_type is the opaque PG-specific escape hatch. Setting both is ambiguous — the emitter would have to pick one silently").
				WithFix("pick one: db_type for known types (TEXT, JSONB, CITEXT, …), or custom_type for types the enum doesn't cover (pgvector, PostGIS, custom DOMAINs)"))
		}
		if !dbTypeCompatibleWithCarrier(col.GetDbType(), carrier) {
			b.err(diag.Atf(desc, "field %q: db_type %s is not valid on a %s carrier", protoName, displayDbType(col.GetDbType()), displayCarrier(carrier)).
				WithWhy("each DbType maps to a class of compatible carriers — text-shaped types require string, numeric types require int/double, BYTEA requires bytes, TIMESTAMP requires google.protobuf.Timestamp, etc. Mismatched pairs would produce SQL the proto wire can't populate").
				WithFix(fmt.Sprintf("change the proto carrier to one that matches db_type: %s, or drop db_type and let field.Type preset pick storage", displayDbType(col.GetDbType()))))
		}
		if col.GetDbType() == irpb.DbType_DBT_VARCHAR && col.GetMaxLen() <= 0 {
			b.err(diag.Atf(desc, "field %q: db_type: VARCHAR requires (w17.field).max_len", protoName).
				WithWhy("VARCHAR(N) has no column-type-driven size without N; the emitter can't pick a default").
				WithFix("add max_len to (w17.field), or pick db_type: TEXT for unbounded text"))
		}
		if col.GetDbType() == irpb.DbType_DBT_NUMERIC && col.GetPrecision() <= 0 {
			b.err(diag.Atf(desc, "field %q: db_type: NUMERIC requires (w17.field).precision", protoName).
				WithWhy("NUMERIC(p, s) has no defaults — precision is the total significant-digit count and has no safe fallback").
				WithFix("add precision (and optionally scale) to (w17.field), or pick a fixed-shape type like MONEY"))
		}
	}

	// (w17.pg.field).custom_type compatibility — enforce the "TEXT-only"
	// contract so author intent never silently drops. custom_type is the
	// remaining storage override after D13 lifted jsonb / inet / tsvector
	// to core Types; sem types other than TEXT carry their own storage
	// semantics that would contradict the override. Explicit string-only
	// CHECK options (min_len, max_len, pattern, choices, explicit blank)
	// emit synths attachChecks must skip on a non-string SQL column —
	// reporting the conflict here keeps the author from losing intent
	// silently.
	if col.GetPg() != nil && pgOverridesStorage(col.GetPg()) {
		switch {
		case carrier != irpb.Carrier_CARRIER_STRING:
			b.err(diag.Atf(desc, "field %q: (w17.pg.field).custom_type is only allowed on string-carrier columns in iter-1 (got %s)", protoName, displayCarrier(carrier)).
				WithWhy("numeric / bool / temporal / bytes carriers have a deterministic (carrier, type) → SQL mapping — they don't need an escape hatch; allowing an override here would let two contradictory storage choices silently race").
				WithFix("drop (w17.pg.field).custom_type, or change the proto field to a string carrier + type: TEXT"))
		case semType != irpb.SemType_SEM_TEXT:
			b.err(diag.Atf(desc, "field %q: (w17.pg.field).custom_type requires type: TEXT (got %s)", protoName, displaySemType(semType)).
				WithWhy("sem types other than TEXT carry their own storage — CHAR/SLUG → VARCHAR(N); UUID → UUID; EMAIL/URL → VARCHAR(default); DECIMAL → NUMERIC; JSON → JSONB; IP → INET; TSEARCH → TSVECTOR. Combining them with custom_type silently drops the sem-driven storage and CHECKs").
				WithFix("change type to TEXT for the custom_type escape hatch path, or drop custom_type and let the curated Type pick storage"))
		default:
			if fieldOpt != nil && (fieldOpt.MinLen != nil || fieldOpt.GetMaxLen() > 0 || fieldOpt.GetPattern() != "" || fieldOpt.GetChoices() != "" || fieldOpt.GetBlank()) {
				b.err(diag.Atf(desc, "field %q: min_len / max_len / pattern / choices / blank are incompatible with (w17.pg.field).custom_type", protoName).
					WithWhy("these options synthesise string-only CHECKs (char_length, <>''/regex, IN (...)) that don't type-check against the overridden SQL column").
					WithFix("drop the string-only options, or drop the custom_type override — pick one path"))
			}
		}
	}

	// (w17.db.column).generated_expr — GENERATED ALWAYS AS (<expr>) STORED
	// on PG (D18). The computed value IS the value; any author-supplied
	// way to provide a value (default_*, proto-level PK, FK reference)
	// would fight the expression. Reject all three combinations here
	// rather than at apply time so the failure carries file:line:col and
	// a fix. Uniqueness and nullability remain allowed — PG lets you put
	// UNIQUE on a STORED generated column, and NULL/NOT NULL is
	// independent of how the value is produced.
	if col.GetGeneratedExpr() != "" {
		if col.GetDefault() != nil {
			b.err(diag.Atf(desc, "field %q: generated_expr is incompatible with default_*", protoName).
				WithWhy("a generated column is computed from its expression on every INSERT/UPDATE — PG rejects any DEFAULT clause on GENERATED ALWAYS AS columns because the two would compete for the initial value").
				WithFix("drop default_string / default_int / default_double / default_auto from (w17.field), or drop generated_expr from (w17.db.column)"))
		}
		if col.GetPk() {
			b.err(diag.Atf(desc, "field %q: generated_expr is incompatible with pk: true", protoName).
				WithWhy("Postgres does not allow STORED generated columns as primary keys — the PK must be a plain column you can write to directly").
				WithFix("drop pk from (w17.field), or drop generated_expr; pick a non-generated column as the primary key"))
		}
		if lf.Column != nil && lf.Column.GetFk() != "" {
			b.err(diag.Atf(desc, "field %q: generated_expr is incompatible with fk", protoName).
				WithWhy("a FOREIGN KEY on a generated column makes the referential integrity contract depend on a computed value — PG rejects it because the on-delete/on-update machinery can't act on a column the author doesn't own").
				WithFix(`drop fk from (w17.db.column), or drop generated_expr; model the FK on a plain column and derive the generated one from it`))
		}
	}

	// Build Checks from the surviving facts. CHECK-name length validation
	// (derivedCheckName fits into NAMEDATALEN) happens in buildTable once
	// the table name is available — per-column we can't spell the full
	// constraint name yet.
	//
	// Collection carriers (MAP / LIST) skip CHECK synthesis entirely —
	// PG CHECK constraints can't express per-element predicates without
	// subqueries, and whole-column predicates on collections are almost
	// always the wrong tool. Authors who need them reach for
	// (w17.db.table).raw_checks with dialect-specific SQL.
	if fieldOpt != nil && carrier != irpb.Carrier_CARRIER_MAP && carrier != irpb.Carrier_CARRIER_LIST {
		b.attachChecks(col, fieldOpt, carrier, semType, desc)
	}

	// D17 — SEM_ENUM side-data: resolve the enum descriptor, populate
	// column metadata, and synth CHECK IN (numbers) on int carriers.
	// Runs after attachChecks so the ENUM constraint sorts last (matches
	// the string-`choices:` output position inside attachChecks).
	if semType == irpb.SemType_SEM_ENUM {
		if err := b.resolveEnumColumn(col, desc, fieldOpt, carrier); err != nil {
			b.err(err)
			return nil
		}
	}

	return col
}

// resolveEnumColumn populates SEM_ENUM side-data on a column (enum FQN +
// non-sentinel names / numbers) and, for integer carriers, attaches a
// CHECK IN (numbers) constraint. Called after the carrier × sem
// validation so the three legitimate paths are distinguishable here:
//
//  1. proto-enum field (e.g. `Status state = 1;`) — FQN from descriptor;
//     `choices:` option optional but must match if set.
//  2. string / int carrier with explicit `type: ENUM` — `choices:` is
//     required (the compiler has no descriptor to derive the FQN from).
//  3. proto-enum field with explicit `type: ENUM` — same as (1) with
//     redundant (but permitted) annotation.
func (b *builder) resolveEnumColumn(col *irpb.Column, desc protoreflect.FieldDescriptor, opt *w17pb.Field, carrier irpb.Carrier) *diag.Error {
	protoName := string(desc.Name())
	// Collection carriers aren't in scope for ENUM dispatch in iter-1 —
	// emitting `<table>_<col> AS ENUM (…)` for an array / map element
	// requires prepending CREATE TYPE and array-typing the column, which
	// needs plumbing the derived type name through pgArrayOf. Reject
	// explicit `type: ENUM` on LIST/MAP here; auto-infer is already
	// gated to scalar carriers above.
	if carrier != irpb.Carrier_CARRIER_STRING && carrier != irpb.Carrier_CARRIER_INT32 && carrier != irpb.Carrier_CARRIER_INT64 {
		return diag.Atf(desc, "field %q: type ENUM is not supported on %s carrier (iter-1)", protoName, displayCarrier(carrier)).
			WithWhy("iter-1 ENUM dispatches to string (CREATE TYPE AS ENUM) or int (CHECK IN numbers); collection-of-enum storage (arrays of a dedicated type) is parked until the collection iteration that revisits per-element dispatch").
			WithFix("change the proto field to a scalar (string / int32 / int64 / proto-enum), or drop type: ENUM and use raw_checks for collection-level membership constraints")
	}
	var fqn string
	if desc.Kind() == protoreflect.EnumKind {
		fqn = string(desc.Enum().FullName())
		if opt != nil && opt.GetChoices() != "" && opt.GetChoices() != fqn {
			return diag.Atf(desc, "field %q: choices %q disagrees with proto-enum field's own enum %q", protoName, opt.GetChoices(), fqn).
				WithWhy("a proto-enum field carries its enum reference in the descriptor; an explicit `choices:` with a different FQN would silently override one of the two — pick one source of truth").
				WithFix(fmt.Sprintf(`drop choices: (enum is inferred as %q), or change the field's proto type to match choices:`, fqn))
		}
	} else {
		if opt == nil || opt.GetChoices() == "" {
			return diag.Atf(desc, "field %q: type ENUM on %s carrier requires choices", protoName, displayCarrier(carrier)).
				WithWhy("the compiler needs a proto enum to resolve value names (for PG CREATE TYPE storage) and numbers (for int-carrier CHECK IN); without a descriptor handle on the field itself, choices: is the only way to name one").
				WithFix(`add choices: "<package>.<EnumName>" to (w17.field), e.g. choices: "catalog.v1.ProductStatus"`)
		}
		fqn = opt.GetChoices()
	}

	names, numbers, resolveErr := b.resolveEnumMembers(desc, fqn)
	if resolveErr != nil {
		return resolveErr
	}
	col.EnumFqn = fqn
	col.EnumNames = names
	col.EnumNumbers = numbers

	// int-carrier SEM_ENUM (both explicit-type + proto-enum-field paths
	// land here): CHECK IN (1, 2, …). The string-carrier path goes the
	// CREATE TYPE route — PG's ENUM type enforces membership, no CHECK
	// needed at the IR layer.
	if carrier == irpb.Carrier_CARRIER_INT32 || carrier == irpb.Carrier_CARRIER_INT64 {
		col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Choices{Choices: &irpb.ChoicesCheck{
			EnumFqn: fqn,
			Numbers: numbers,
		}}})
	}

	return nil
}

func (b *builder) attachChecks(col *irpb.Column, opt *w17pb.Field, carrier irpb.Carrier, semType irpb.SemType, origin protoreflect.FieldDescriptor) {
	// stringStorage covers both the sem-type axis (UUID / DECIMAL map to
	// non-string SQL types) and the PG-passthrough axis (jsonb / inet /
	// tsvector / hstore / custom_type redirect storage regardless of sem
	// type). All string-only CHECK synths gate on it — blank, length,
	// regex, choices — because PG rejects those operators on non-string
	// column types at apply time.
	stringStorage := carrier == irpb.Carrier_CARRIER_STRING && columnStoresAsString(col, semType)

	// LengthCheck — omitted when the final SQL storage is VARCHAR(N) since
	// the column type already enforces the upper bound. MinLen always
	// produces a CHECK when present (VARCHAR has no minimum).
	//
	// "Final storage" considers db_type override (D14): when db_type is
	// set to VARCHAR, subsumed; when set to TEXT / CITEXT, NOT subsumed
	// (so CHAR sem + db_type TEXT still emits the length CHECK). When
	// db_type unset, fall back to sem-based subsumption (CHAR / SLUG /
	// EMAIL / URL are VARCHAR-backed by D13).
	if stringStorage {
		var maxSubsumedByType bool
		if col.GetDbType() != irpb.DbType_DB_TYPE_UNSPECIFIED {
			maxSubsumedByType = col.GetDbType() == irpb.DbType_DBT_VARCHAR
		} else {
			maxSubsumedByType = semType == irpb.SemType_SEM_CHAR ||
				semType == irpb.SemType_SEM_SLUG ||
				semType == irpb.SemType_SEM_EMAIL ||
				semType == irpb.SemType_SEM_URL
		}
		hasMin := opt.MinLen != nil
		hasMax := opt.GetMaxLen() > 0 && !maxSubsumedByType
		if hasMin || hasMax {
			lc := &irpb.LengthCheck{}
			if hasMin {
				min := opt.GetMinLen()
				lc.Min = &min
			}
			if hasMax {
				max := opt.GetMaxLen()
				lc.Max = &max
			}
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Length{Length: lc}})
		}
		// BlankCheck — added unless author opted into blank or column is nullable.
		if !opt.GetBlank() && !col.GetNullable() {
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Blank{Blank: &irpb.BlankCheck{}}})
		}
	}

	// RangeCheck — applies to numeric carriers; storage override via
	// `(w17.pg.field).custom_type` is assumed to still be numeric-comparable
	// (author's responsibility — nothing downstream re-checks). Not gated
	// on stringStorage since ranges are numeric-only anyway.
	if opt.Gt != nil || opt.Gte != nil || opt.Lt != nil || opt.Lte != nil {
		rc := &irpb.RangeCheck{Gt: opt.Gt, Gte: opt.Gte, Lt: opt.Lt, Lte: opt.Lte}
		col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Range{Range: rc}})
	}

	// RegexCheck — pattern override takes precedence over type-implied.
	// Both gate on stringStorage: `col ~ 'pat'` against a JSONB / INET /
	// UUID column fails to parse / type-check.
	if stringStorage {
		if opt.GetPattern() != "" {
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Regex{Regex: &irpb.RegexCheck{
				Pattern: opt.GetPattern(),
				Source:  irpb.RegexSource_REGEX_FROM_PATTERN,
			}}})
		} else if regex := defaultRegexFor(semType); regex != "" {
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Regex{Regex: &irpb.RegexCheck{
				Pattern: regex,
				Source:  irpb.RegexSource_REGEX_FROM_TYPE,
			}}})
		}
	}

	// ChoicesCheck — resolve the enum FQN to its value names. Gated on
	// stringStorage (`col IN ('A','B')` requires string equality semantics
	// on the SQL column).
	if stringStorage && opt.GetChoices() != "" {
		values, resolveErr := b.resolveEnumValues(origin, opt.GetChoices())
		if resolveErr != nil {
			b.err(resolveErr)
		} else {
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Choices{Choices: &irpb.ChoicesCheck{
				EnumFqn: opt.GetChoices(),
				Values:  values,
			}}})
		}
	}

	// Percentage/Ratio: emit implicit domain constraints when no author bounds conflict.
	switch semType {
	case irpb.SemType_SEM_PERCENTAGE:
		if opt.Gte == nil && opt.Gt == nil && opt.Lt == nil && opt.Lte == nil {
			zero := 0.0
			hundred := 100.0
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{Gte: &zero, Lte: &hundred}}})
		}
	case irpb.SemType_SEM_RATIO:
		if opt.Gte == nil && opt.Gt == nil && opt.Lt == nil && opt.Lte == nil {
			zero := 0.0
			one := 1.0
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{Gte: &zero, Lte: &one}}})
		}
	}
}

// resolveDefault parses the (w17.field).default oneof and validates
// carrier/type compatibility per D7.
func (b *builder) resolveDefault(desc protoreflect.FieldDescriptor, opt *w17pb.Field, carrier irpb.Carrier, semType irpb.SemType) (*irpb.Default, *diag.Error) {
	switch d := opt.GetDefault().(type) {
	case nil:
		return nil, nil
	case *w17pb.Field_DefaultString:
		if carrier != irpb.Carrier_CARRIER_STRING {
			return nil, diag.Atf(desc, "field %q: default_string requires a string carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("default_string emits a string literal — non-string columns can't accept it").
				WithFix("use default_int / default_double / default_auto for non-string carriers, or change the proto field to string")
		}
		return &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: d.DefaultString}}, nil
	case *w17pb.Field_DefaultInt:
		if carrier != irpb.Carrier_CARRIER_INT32 && carrier != irpb.Carrier_CARRIER_INT64 {
			return nil, diag.Atf(desc, "field %q: default_int requires an integer carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("default_int emits an integer literal").
				WithFix("use default_double for double carriers, or default_string for strings")
		}
		return &irpb.Default{Variant: &irpb.Default_LiteralInt{LiteralInt: d.DefaultInt}}, nil
	case *w17pb.Field_DefaultDouble:
		if carrier != irpb.Carrier_CARRIER_DOUBLE {
			return nil, diag.Atf(desc, "field %q: default_double requires a double carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("default_double emits a floating-point literal").
				WithFix("use default_int for integer carriers, default_string for strings")
		}
		return &irpb.Default{Variant: &irpb.Default_LiteralDouble{LiteralDouble: d.DefaultDouble}}, nil
	case *w17pb.Field_DefaultAuto:
		kind := protoAutoToKind(d.DefaultAuto)
		if err := validateAutoDefault(desc, kind, carrier, semType); err != nil {
			return nil, err
		}
		return &irpb.Default{Variant: &irpb.Default_Auto{Auto: kind}}, nil
	default:
		return nil, diag.Atf(desc, "field %q: unknown default branch %T", desc.Name(), d).
			WithWhy("this is a compiler bug — the default oneof grew a branch the IR builder doesn't recognise").
			WithFix("please file an issue with the failing .proto attached")
	}
}

// resolveEnumValues walks a field's parent file + transitive imports for a
// fully-qualified enum name and returns the ordered value-name slice with the
// mandatory *_UNSPECIFIED zero sentinel stripped.
func (b *builder) resolveEnumValues(origin protoreflect.FieldDescriptor, fqn string) ([]string, *diag.Error) {
	names, _, err := b.resolveEnumMembers(origin, fqn)
	return names, err
}

// resolveEnumMembers returns both non-sentinel names and numbers for a
// fully-qualified enum. Shared between the string-`choices:` CHECK path
// (names only) and SEM_ENUM dispatch (D17 — uses both names for PG
// CREATE TYPE storage and numbers for int-carrier CHECK IN).
func (b *builder) resolveEnumMembers(origin protoreflect.FieldDescriptor, fqn string) ([]string, []int64, *diag.Error) {
	enum := findEnum(origin.ParentFile(), protoreflect.FullName(fqn))
	if enum == nil {
		return nil, nil, diag.Atf(origin, "field %q: choices enum %q not found", origin.Name(), fqn).
			WithWhy("choices takes a fully-qualified proto enum name; the IR builder walked the current file and its imports and could not locate it").
			WithFix(`verify the FQN (package + enum name, e.g. "catalog.v1.ProductStatus") and make sure the defining .proto is imported`)
	}
	values := enum.Values()
	names := make([]string, 0, values.Len())
	numbers := make([]int64, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if v.Number() == 0 {
			// Proto3 convention: 0-value is *_UNSPECIFIED / sentinel.
			continue
		}
		names = append(names, string(v.Name()))
		numbers = append(numbers, int64(v.Number()))
	}
	if len(names) == 0 {
		return nil, nil, diag.Atf(origin, "field %q: choices enum %q has no non-zero values", origin.Name(), fqn).
			WithWhy("every declared enum value had number 0 (the sentinel); a CHECK IN () would match nothing").
			WithFix("add at least one real value to the enum (e.g. DRAFT = 1)")
	}
	return names, numbers, nil
}

// resolveFKs verifies every FK target table/column exists among compiled tables.
func (b *builder) resolveFKs(schema *irpb.Schema) {
	byName := map[string]*irpb.Table{}
	for _, t := range schema.Tables {
		byName[t.GetName()] = t
	}
	for _, t := range schema.Tables {
		msg := b.msgByTable[t.GetName()]
		for _, fk := range t.ForeignKeys {
			// Descriptor of the FK-owning field, for file:line:col anchoring.
			var desc protoreflect.Descriptor
			if msg != nil {
				if f := findLoadedField(msg, fk.GetColumn()); f != nil {
					desc = f.Desc
				}
			}
			target, ok := byName[fk.GetTargetTable()]
			if !ok {
				b.err(diag.Atf(desc, "field %q: fk target table %q not defined in this file", fk.GetColumn(), fk.GetTargetTable()).
					WithWhy("iteration-1 resolves fk references within the single compiled proto — cross-file fk resolution lands in iter-2").
					WithFix(fmt.Sprintf("add a message annotated with (w17.db.table).name = %q to this file, or correct the fk reference", fk.GetTargetTable())))
				continue
			}
			if !hasColumn(target, fk.GetTargetColumn()) {
				b.err(diag.Atf(desc, "field %q: fk target column %q not found on table %q", fk.GetColumn(), fk.GetTargetColumn(), fk.GetTargetTable()).
					WithWhy("the fk references a column that doesn't exist on the target table — a broken FK would fail at apply time").
					WithFix(fmt.Sprintf("verify the column name (case-sensitive, proto-field name) on message for table %q, or correct the fk reference", fk.GetTargetTable())))
				continue
			}
			if !fkTargetColumnIsUnique(target, fk.GetTargetColumn()) {
				b.err(diag.Atf(desc, "field %q: fk target %q.%q has no uniqueness constraint — Postgres rejects FKs at apply unless the target column is single-col PK or carries a single-col UNIQUE index", fk.GetColumn(), fk.GetTargetTable(), fk.GetTargetColumn()).
					WithWhy("on a composite PK each individual column is part of the compound key but not uniquely indexed on its own; FKs pointing at one of those columns fail at apply with `no unique constraint matching given keys for referenced table`").
					WithFix(fmt.Sprintf("either add `unique: true` to the target field %q.%q so the IR synthesises a single-col UNIQUE index, or reference a column that already has one (composite-key FKs are not supported in iteration-1)", fk.GetTargetTable(), fk.GetTargetColumn())))
			}
		}
	}
}

// populateElement fills col.ElementCarrier / col.ElementIsMessage for
// collection carriers (CARRIER_MAP, CARRIER_LIST). For maps: inspects
// fd.MapKey / fd.MapValue and enforces the iter-1.6 "key must be
// string" invariant. For lists: treats fd as its own element kind.
//
// Message elements carry CARRIER_UNSPECIFIED + ElementIsMessage=true —
// emitters see this and dispatch to JSONB regardless of dialect.
// Scalar elements carry their resolved Carrier; `repeated Timestamp`
// for example populates ElementCarrier=CARRIER_TIMESTAMP so the PG
// emitter can render TIMESTAMPTZ[].
func (b *builder) populateElement(col *irpb.Column, desc protoreflect.FieldDescriptor, carrier irpb.Carrier) *diag.Error {
	switch carrier {
	case irpb.Carrier_CARRIER_MAP:
		key := desc.MapKey()
		val := desc.MapValue()
		// iter-1.6: key must be string (covers 99% of real-world maps;
		// arbitrary-key dispatch across dialects is a future iteration).
		keyCarrier, keyOk := protoKindToScalarCarrier(key)
		if !keyOk || keyCarrier != irpb.Carrier_CARRIER_STRING {
			return diag.Atf(desc, "field %q: map key must be string (got %s)", desc.Name(), key.Kind()).
				WithWhy("iter-1.6 dispatches maps based on value type (HSTORE for string values, JSONB otherwise) — non-string keys aren't expressible in HSTORE and would need extra JSONB shape conventions").
				WithFix("change the map to `map<string, V>`; non-string keys land in a later iteration")
		}
		if val.Kind() == protoreflect.MessageKind {
			// map<string, SomeMsg> — value is a proto message.
			name := val.Message().FullName()
			if name != "google.protobuf.Timestamp" && name != "google.protobuf.Duration" {
				col.ElementCarrier = irpb.Carrier_CARRIER_UNSPECIFIED
				col.ElementIsMessage = true
				return nil
			}
			// Timestamp / Duration values are treated as scalars (they map
			// to well-defined PG types).
		}
		valCarrier, valOk := protoKindToScalarCarrier(val)
		if !valOk {
			return diag.Atf(desc, "field %q: unsupported map value kind %s", desc.Name(), val.Kind()).
				WithWhy("value types outside the iter-1 carrier set (scalar primitives + Timestamp + Duration + Message) aren't dispatchable").
				WithFix("change the value type to string / int32 / int64 / double / bool / bytes / Timestamp / Duration / a Message (will render as JSONB)")
		}
		col.ElementCarrier = valCarrier
		col.ElementIsMessage = false
		return nil

	case irpb.Carrier_CARRIER_LIST:
		if desc.Kind() == protoreflect.MessageKind {
			name := desc.Message().FullName()
			if name != "google.protobuf.Timestamp" && name != "google.protobuf.Duration" {
				col.ElementCarrier = irpb.Carrier_CARRIER_UNSPECIFIED
				col.ElementIsMessage = true
				return nil
			}
		}
		elemCarrier, ok := protoKindToScalarCarrier(desc)
		if !ok {
			return diag.Atf(desc, "field %q: unsupported repeated element kind %s", desc.Name(), desc.Kind()).
				WithWhy("element types outside the iter-1 carrier set aren't dispatchable").
				WithFix("change the element type to string / int32 / int64 / double / bool / bytes / Timestamp / Duration / a Message (will render as JSONB)")
		}
		col.ElementCarrier = elemCarrier
		col.ElementIsMessage = false
		return nil
	}
	return nil
}

// resolveFKAction turns a (w17.db.column).deletion_rule + (w17.field).null
// combination into the concrete irpb.FKAction emitted into the plan. The
// rule wins when set; otherwise infer from null (null:true → ORPHAN,
// else CASCADE). Each explicit rule has a validation gate — ORPHAN
// needs null:true (you can't SET NULL a NOT NULL column), RESET needs a
// default_* value (PG would leave the column in a no-default-available
// state when the parent vanishes).
func resolveFKAction(f *loader.LoadedField, col *irpb.Column) (irpb.FKAction, *diag.Error) {
	rule := dbpb.DeletionRule_DELETION_RULE_UNSPECIFIED
	if f.Column != nil {
		rule = f.Column.GetDeletionRule()
	}
	nullable := col.GetNullable()

	switch rule {
	case dbpb.DeletionRule_DELETION_RULE_UNSPECIFIED:
		if nullable {
			return irpb.FKAction_FK_ACTION_SET_NULL, nil
		}
		return irpb.FKAction_FK_ACTION_CASCADE, nil

	case dbpb.DeletionRule_CASCADE:
		return irpb.FKAction_FK_ACTION_CASCADE, nil

	case dbpb.DeletionRule_ORPHAN:
		if !nullable {
			return irpb.FKAction_FK_ACTION_UNSPECIFIED, diag.Atf(f.Desc, "field %q: deletion_rule: ORPHAN requires null: true", col.GetProtoName()).
				WithWhy("ORPHAN sets the child FK to NULL when the parent is deleted — a NOT NULL column would violate its own constraint during the SET NULL step").
				WithFix(`either set null: true on (w17.field), or pick deletion_rule: CASCADE (child deleted with parent) / BLOCK (refuse parent delete) / RESET (child FK → default_*)`)
		}
		return irpb.FKAction_FK_ACTION_SET_NULL, nil

	case dbpb.DeletionRule_BLOCK:
		return irpb.FKAction_FK_ACTION_RESTRICT, nil

	case dbpb.DeletionRule_RESET:
		if col.GetDefault() == nil {
			return irpb.FKAction_FK_ACTION_UNSPECIFIED, diag.Atf(f.Desc, "field %q: deletion_rule: RESET requires a (w17.field).default_* value", col.GetProtoName()).
				WithWhy("RESET maps to ON DELETE SET DEFAULT — PG needs a value to write back into the FK column; without default_* the SET DEFAULT clause has nothing to set").
				WithFix(`add default_int / default_string / default_auto to (w17.field), or pick deletion_rule: ORPHAN (FK → NULL, requires null: true) / BLOCK (refuse parent delete) / CASCADE (child deleted with parent)`)
		}
		return irpb.FKAction_FK_ACTION_SET_DEFAULT, nil
	}
	return irpb.FKAction_FK_ACTION_UNSPECIFIED, diag.Atf(f.Desc, "field %q: unknown deletion_rule %s", col.GetProtoName(), rule).
		WithWhy("this is a compiler bug — deletion_rule enum grew a variant the IR builder doesn't handle").
		WithFix("please file an issue")
}

// fkTargetColumnIsUnique returns true when the target column is addressable
// as a PG FK target: single-col PK, OR covered by a single-col UNIQUE index
// (table-level or synth'd from `(w17.field).unique`). Composite-PK member
// columns return false — PG rejects FKs pointing at them at apply time.
func fkTargetColumnIsUnique(target *irpb.Table, colProtoName string) bool {
	// Single-col PK is the common fast path.
	if len(target.GetPrimaryKey()) == 1 && target.GetPrimaryKey()[0] == colProtoName {
		return true
	}
	// Table-level or synthesised single-col UNIQUE index.
	for _, idx := range target.GetIndexes() {
		if !idx.GetUnique() {
			continue
		}
		fields := idx.GetFields()
		if len(fields) == 1 && fields[0] == colProtoName {
			return true
		}
	}
	return false
}

// --- small helpers ---

func sourceLocation(d protoreflect.Descriptor) *irpb.SourceLocation {
	if d == nil {
		return nil
	}
	file := d.ParentFile()
	if file == nil {
		return nil
	}
	sl := &irpb.SourceLocation{File: file.Path()}
	loc := file.SourceLocations().ByDescriptor(d)
	if loc.StartLine > 0 || loc.StartColumn > 0 {
		sl.Line = int32(loc.StartLine + 1)
		sl.Col = int32(loc.StartColumn + 1)
	}
	return sl
}

func protoKindToCarrier(fd protoreflect.FieldDescriptor) (irpb.Carrier, bool) {
	if fd.ContainingOneof() != nil {
		return irpb.Carrier_CARRIER_UNSPECIFIED, false
	}
	// Maps must be checked BEFORE IsList because proto maps are also
	// reported as repeated (synthetic entry messages).
	if fd.IsMap() {
		return irpb.Carrier_CARRIER_MAP, true
	}
	if fd.IsList() {
		return irpb.Carrier_CARRIER_LIST, true
	}
	return protoKindToScalarCarrier(fd)
}

// protoKindToScalarCarrier maps a single proto Kind to its scalar
// Carrier. Shared between column-level dispatch (scalar fields) and
// element-level dispatch (list elements, map values).
func protoKindToScalarCarrier(fd protoreflect.FieldDescriptor) (irpb.Carrier, bool) {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return irpb.Carrier_CARRIER_BOOL, true
	case protoreflect.StringKind:
		return irpb.Carrier_CARRIER_STRING, true
	case protoreflect.BytesKind:
		return irpb.Carrier_CARRIER_BYTES, true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return irpb.Carrier_CARRIER_INT32, true
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return irpb.Carrier_CARRIER_INT64, true
	case protoreflect.DoubleKind:
		return irpb.Carrier_CARRIER_DOUBLE, true
	case protoreflect.EnumKind:
		// D17: proto enum fields travel on the int32 wire type — dispatch
		// to the int32 carrier so SEM_ENUM side-data (CHECK IN numbers)
		// can attach without a dedicated carrier.
		return irpb.Carrier_CARRIER_INT32, true
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Timestamp":
			return irpb.Carrier_CARRIER_TIMESTAMP, true
		case "google.protobuf.Duration":
			return irpb.Carrier_CARRIER_DURATION, true
		}
	}
	return irpb.Carrier_CARRIER_UNSPECIFIED, false
}

func describeKind(fd protoreflect.FieldDescriptor) string {
	if fd.IsList() {
		return "repeated " + fd.Kind().String()
	}
	if fd.IsMap() {
		return "map"
	}
	if fd.Kind() == protoreflect.MessageKind {
		return string(fd.Message().FullName())
	}
	return fd.Kind().String()
}

func suggestedTypeFor(c irpb.Carrier) string {
	switch c {
	case irpb.Carrier_CARRIER_STRING:
		return "CHAR, max_len: 255"
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		return "NUMBER"
	case irpb.Carrier_CARRIER_DOUBLE:
		return "NUMBER"
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return "DATETIME"
	case irpb.Carrier_CARRIER_DURATION:
		return "INTERVAL"
	}
	return "CHAR"
}

// defaultSemTypeFor maps each carrier to its zero-config default SemType
// (D14). Authors opt into a specific sub-type via (w17.field).type only
// when the preset they want differs from the default.
//
//	string     → TEXT (unbounded text; CHAR/SLUG/etc. are opt-in)
//	int32/64   → NUMBER (generic integer; ID / COUNTER are opt-in)
//	double     → NUMBER (generic double; MONEY / PERCENTAGE / RATIO opt-in)
//	Timestamp  → DATETIME (timezone-aware; DATE / TIME are opt-in)
//	Duration   → INTERVAL (the only option)
//	bool       → UNSPECIFIED (no SemType refinement exists)
//	bytes      → UNSPECIFIED (JSON is the only opt-in)
//	map / list → AUTO (dialect picks storage based on element info)
func defaultSemTypeFor(carrier irpb.Carrier) irpb.SemType {
	switch carrier {
	case irpb.Carrier_CARRIER_STRING:
		return irpb.SemType_SEM_TEXT
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64, irpb.Carrier_CARRIER_DOUBLE:
		return irpb.SemType_SEM_NUMBER
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return irpb.SemType_SEM_DATETIME
	case irpb.Carrier_CARRIER_DURATION:
		return irpb.SemType_SEM_INTERVAL
	case irpb.Carrier_CARRIER_MAP, irpb.Carrier_CARRIER_LIST:
		return irpb.SemType_SEM_AUTO
	}
	return irpb.SemType_SEM_UNSPECIFIED
}

func protoTypeToSem(t w17pb.Type) irpb.SemType {
	switch t {
	case w17pb.Type_CHAR:
		return irpb.SemType_SEM_CHAR
	case w17pb.Type_TEXT:
		return irpb.SemType_SEM_TEXT
	case w17pb.Type_UUID:
		return irpb.SemType_SEM_UUID
	case w17pb.Type_EMAIL:
		return irpb.SemType_SEM_EMAIL
	case w17pb.Type_URL:
		return irpb.SemType_SEM_URL
	case w17pb.Type_SLUG:
		return irpb.SemType_SEM_SLUG
	case w17pb.Type_JSON:
		return irpb.SemType_SEM_JSON
	case w17pb.Type_IP:
		return irpb.SemType_SEM_IP
	case w17pb.Type_TSEARCH:
		return irpb.SemType_SEM_TSEARCH
	case w17pb.Type_NUMBER:
		return irpb.SemType_SEM_NUMBER
	case w17pb.Type_ID:
		return irpb.SemType_SEM_ID
	case w17pb.Type_COUNTER:
		return irpb.SemType_SEM_COUNTER
	case w17pb.Type_MONEY:
		return irpb.SemType_SEM_MONEY
	case w17pb.Type_PERCENTAGE:
		return irpb.SemType_SEM_PERCENTAGE
	case w17pb.Type_RATIO:
		return irpb.SemType_SEM_RATIO
	case w17pb.Type_DECIMAL:
		return irpb.SemType_SEM_DECIMAL
	case w17pb.Type_DATE:
		return irpb.SemType_SEM_DATE
	case w17pb.Type_TIME:
		return irpb.SemType_SEM_TIME
	case w17pb.Type_DATETIME:
		return irpb.SemType_SEM_DATETIME
	case w17pb.Type_INTERVAL:
		return irpb.SemType_SEM_INTERVAL
	case w17pb.Type_ENUM:
		return irpb.SemType_SEM_ENUM
	}
	return irpb.SemType_SEM_UNSPECIFIED
}

func protoAutoToKind(a w17pb.AutoDefault) irpb.AutoKind {
	switch a {
	case w17pb.AutoDefault_NOW:
		return irpb.AutoKind_AUTO_NOW
	case w17pb.AutoDefault_UUID_V4:
		return irpb.AutoKind_AUTO_UUID_V4
	case w17pb.AutoDefault_UUID_V7:
		return irpb.AutoKind_AUTO_UUID_V7
	case w17pb.AutoDefault_EMPTY_JSON_ARRAY:
		return irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY
	case w17pb.AutoDefault_EMPTY_JSON_OBJECT:
		return irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT
	case w17pb.AutoDefault_TRUE:
		return irpb.AutoKind_AUTO_TRUE
	case w17pb.AutoDefault_FALSE:
		return irpb.AutoKind_AUTO_FALSE
	case w17pb.AutoDefault_IDENTITY:
		return irpb.AutoKind_AUTO_IDENTITY
	}
	return irpb.AutoKind_AUTO_UNSPECIFIED
}

// validateCarrierSemType enforces docs/iteration-1.md D2.
func validateCarrierSemType(desc protoreflect.FieldDescriptor, carrier irpb.Carrier, sem irpb.SemType) *diag.Error {
	name := desc.Name()
	switch carrier {
	case irpb.Carrier_CARRIER_BOOL:
		if sem != irpb.SemType_SEM_UNSPECIFIED {
			return diag.Atf(desc, "field %q: bool carrier must not set a semantic type (got %s)", name, displaySemType(sem)).
				WithWhy("bool has exactly one column shape (BOOLEAN) — there is no semantic refinement to pick").
				WithFix("drop type: from (w17.field); for a default value use default_auto: TRUE or FALSE")
		}
	case irpb.Carrier_CARRIER_BYTES:
		switch sem {
		case irpb.SemType_SEM_UNSPECIFIED:
			// Raw binary blob — BYTEA / BLOB — no refinement.
		case irpb.SemType_SEM_JSON:
			// bytes-carrying-JSON: caller holds a []byte, storage is still JSONB.
		default:
			return diag.Atf(desc, "field %q: bytes carrier accepts only type: JSON or no type at all (got %s)", name, displaySemType(sem)).
				WithWhy("bytes maps to BYTEA by default; type: JSON redirects storage to JSONB while keeping the bytes wire type. No other semantic refinement applies").
				WithFix("drop type: from (w17.field), or set type: JSON")
		}
	case irpb.Carrier_CARRIER_STRING:
		switch sem {
		case irpb.SemType_SEM_CHAR, irpb.SemType_SEM_TEXT, irpb.SemType_SEM_UUID, irpb.SemType_SEM_EMAIL, irpb.SemType_SEM_URL, irpb.SemType_SEM_SLUG, irpb.SemType_SEM_DECIMAL,
			irpb.SemType_SEM_JSON, irpb.SemType_SEM_IP, irpb.SemType_SEM_TSEARCH, irpb.SemType_SEM_ENUM:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: string carrier requires a semantic type", name).
				WithWhy("string maps to many SQL types (VARCHAR, TEXT, UUID, JSONB, INET, TSVECTOR) with different constraints; the compiler won't guess").
				WithFix("add one of: CHAR, TEXT, UUID, EMAIL, URL, SLUG, DECIMAL, JSON, IP, TSEARCH, ENUM")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a string carrier", name, displaySemType(sem)).
				WithWhy("the D2 carrier×type table restricts string to CHAR, TEXT, UUID, EMAIL, URL, SLUG, DECIMAL, JSON, IP, TSEARCH, ENUM").
				WithFix("pick one of the string-valid types, or change the carrier")
		}
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		switch sem {
		case irpb.SemType_SEM_NUMBER, irpb.SemType_SEM_ID, irpb.SemType_SEM_COUNTER, irpb.SemType_SEM_ENUM:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: %s carrier requires a semantic type", name, displayCarrier(carrier)).
				WithWhy("int32/int64 can carry NUMBER / ID / COUNTER / ENUM — each emits a different SQL shape (PK vs indexed FK vs bounded counter vs CHECK IN numbers)").
				WithFix("add one of: NUMBER, ID, COUNTER, ENUM")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on an integer carrier", name, displaySemType(sem)).
				WithWhy("the D2 carrier×type table restricts integer carriers to NUMBER, ID, COUNTER, ENUM").
				WithFix("pick an integer-valid type, or change the carrier (e.g. double for MONEY)")
		}
	case irpb.Carrier_CARRIER_DOUBLE:
		switch sem {
		case irpb.SemType_SEM_NUMBER, irpb.SemType_SEM_MONEY, irpb.SemType_SEM_PERCENTAGE, irpb.SemType_SEM_RATIO:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: double carrier requires a semantic type", name).
				WithWhy("double can carry NUMBER / MONEY / PERCENTAGE / RATIO — each emits different constraints (bounds for PERCENTAGE/RATIO, scale for MONEY)").
				WithFix("add one of: NUMBER, MONEY, PERCENTAGE, RATIO; use DECIMAL on a string carrier for exact precision")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a double carrier", name, displaySemType(sem)).
				WithWhy("the D2 carrier×type table restricts double to NUMBER, MONEY, PERCENTAGE, RATIO").
				WithFix("pick a double-valid type, or change the carrier")
		}
	case irpb.Carrier_CARRIER_TIMESTAMP:
		switch sem {
		case irpb.SemType_SEM_DATE, irpb.SemType_SEM_TIME, irpb.SemType_SEM_DATETIME:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: google.protobuf.Timestamp carrier requires a semantic type", name).
				WithWhy("Timestamp can be a DATE, TIME, or DATETIME — each emits a different SQL column type").
				WithFix("add one of: DATE, TIME, DATETIME")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a Timestamp carrier", name, displaySemType(sem)).
				WithWhy("Timestamp is restricted to DATE / TIME / DATETIME per D2").
				WithFix("pick a Timestamp-valid type (DATE, TIME, DATETIME), or change the carrier")
		}
	case irpb.Carrier_CARRIER_DURATION:
		if sem != irpb.SemType_SEM_INTERVAL {
			return diag.Atf(desc, "field %q: Duration carrier must be INTERVAL (got %s)", name, displaySemType(sem)).
				WithWhy("google.protobuf.Duration maps 1:1 to the SQL INTERVAL type — no other refinement is defined in iter-1").
				WithFix("set type: INTERVAL or drop the type: key so it's inferred")
		}
	case irpb.Carrier_CARRIER_MAP:
		if sem != irpb.SemType_SEM_AUTO {
			return diag.Atf(desc, "field %q: map carrier must be AUTO (got %s)", name, displaySemType(sem)).
				WithWhy("map<K,V> dispatches to HSTORE / JSONB / JSON per dialect + value type; iter-1.6 doesn't support per-value sem-type refinement on maps").
				WithFix("drop type: from (w17.field) (AUTO is inferred on map carriers), or type: AUTO to mark the intent explicitly; refine storage via (w17.db.column).db_type if needed")
		}
	case irpb.Carrier_CARRIER_LIST:
		// repeated X: element-level refinement. AUTO (or unset → AUTO) is
		// the default; anything else must be valid on the element carrier.
		// Checked in a dedicated pass that sees element_carrier — the
		// generic carrier×sem matrix below can't reach into elements.
	}
	return nil
}

// validateAutoDefault enforces the Type × AutoDefault table from D7.
func validateAutoDefault(desc protoreflect.FieldDescriptor, kind irpb.AutoKind, carrier irpb.Carrier, sem irpb.SemType) *diag.Error {
	switch kind {
	case irpb.AutoKind_AUTO_NOW:
		if carrier != irpb.Carrier_CARRIER_TIMESTAMP {
			return diag.Atf(desc, "field %q: default_auto: NOW requires a Timestamp carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("NOW resolves to CURRENT_DATE / CURRENT_TIME / CURRENT_TIMESTAMP; only Timestamp columns accept any of those").
				WithFix("change the carrier to google.protobuf.Timestamp (type: DATETIME / DATE / TIME), or remove default_auto")
		}
	case irpb.AutoKind_AUTO_UUID_V4, irpb.AutoKind_AUTO_UUID_V7:
		if carrier != irpb.Carrier_CARRIER_STRING || sem != irpb.SemType_SEM_UUID {
			return diag.Atf(desc, "field %q: default_auto: %s requires string carrier with type UUID (got carrier=%s, type=%s)", desc.Name(), displayAutoKind(kind), displayCarrier(carrier), displaySemType(sem)).
				WithWhy("UUID_V4 / UUID_V7 generate a UUID literal — only columns declared as UUID accept it").
				WithFix("set the field to `string foo = N [(w17.field) = { type: UUID, default_auto: UUID_V4 }];`")
		}
	case irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY, irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT:
		// Post-D13: empty-JSON literals make sense only on type: JSON
		// (either on string or bytes carrier — both store as JSONB).
		// Previously allowed on TEXT / CHAR as a workaround; those paths
		// would emit '[]' / '{}' into a plain text column, which is
		// semantically wrong — the column can't validate JSON shape.
		isStringOrBytes := carrier == irpb.Carrier_CARRIER_STRING || carrier == irpb.Carrier_CARRIER_BYTES
		if !isStringOrBytes || sem != irpb.SemType_SEM_JSON {
			return diag.Atf(desc, "field %q: default_auto: %s requires type: JSON (got carrier=%s, type=%s)", desc.Name(), displayAutoKind(kind), displayCarrier(carrier), displaySemType(sem)).
				WithWhy("empty-JSON defaults emit '[]' / '{}' — semantically JSON literals, meaningful only on JSON-shaped columns (JSONB on PG, JSON on MySQL, TEXT on SQLite)").
				WithFix("set the field to `[(w17.field) = { type: JSON, default_auto: EMPTY_JSON_ARRAY }];` on a string or bytes carrier")
		}
	case irpb.AutoKind_AUTO_TRUE, irpb.AutoKind_AUTO_FALSE:
		if carrier != irpb.Carrier_CARRIER_BOOL {
			return diag.Atf(desc, "field %q: default_auto: %s requires a bool carrier (got %s)", desc.Name(), displayAutoKind(kind), displayCarrier(carrier)).
				WithWhy("TRUE/FALSE are the single channel for bool defaults (there is no default_bool literal branch)").
				WithFix("change the carrier to bool, or use a literal default for non-bool columns")
		}
	case irpb.AutoKind_AUTO_IDENTITY:
		integer := carrier == irpb.Carrier_CARRIER_INT32 || carrier == irpb.Carrier_CARRIER_INT64
		if !integer || sem != irpb.SemType_SEM_ID {
			return diag.Atf(desc, "field %q: default_auto: IDENTITY requires int32/int64 with type: ID (got carrier=%s, type=%s)", desc.Name(), displayCarrier(carrier), displaySemType(sem)).
				WithWhy("IDENTITY renders as `GENERATED BY DEFAULT AS IDENTITY` (PG) / AUTO_INCREMENT (MySQL) — both apply only to integer PK columns declared as ID").
				WithFix("set the field to `int64 id = 1 [(w17.field) = { type: ID, pk: true, default_auto: IDENTITY }];`")
		}
	}
	return nil
}

func findLoadedField(msg *loader.LoadedMessage, protoName string) *loader.LoadedField {
	for _, f := range msg.Fields {
		if string(f.Desc.Name()) == protoName {
			return f
		}
	}
	return nil
}

// fkRef is the parsed form of (w17.field).fk — package-private; the wire-
// shape lives in irpb.ForeignKey.
type fkRef struct {
	table  string
	column string
}

func parseFKRef(s string) (fkRef, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fkRef{}, false
	}
	return fkRef{table: parts[0], column: parts[1]}, true
}

func hasSingleColUniqueIndex(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if i.GetUnique() && len(i.GetFields()) == 1 && i.GetFields()[0] == field {
			return true
		}
	}
	return false
}

func hasSingleColIndex(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if len(i.GetFields()) == 1 && i.GetFields()[0] == field {
			return true
		}
	}
	return false
}

func hasColumn(t *irpb.Table, protoName string) bool {
	for _, c := range t.GetColumns() {
		if c.GetProtoName() == protoName {
			return true
		}
	}
	return false
}

// findEnum walks a file + transitive imports for a fully-qualified enum name.
// Supports both top-level enums and enums nested inside messages.
func findEnum(root protoreflect.FileDescriptor, fqn protoreflect.FullName) protoreflect.EnumDescriptor {
	seen := map[string]bool{}
	var visit func(f protoreflect.FileDescriptor) protoreflect.EnumDescriptor
	visit = func(f protoreflect.FileDescriptor) protoreflect.EnumDescriptor {
		if seen[f.Path()] {
			return nil
		}
		seen[f.Path()] = true
		if e := findEnumInContainer(f, fqn); e != nil {
			return e
		}
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			if e := visit(imports.Get(i).FileDescriptor); e != nil {
				return e
			}
		}
		return nil
	}
	return visit(root)
}

// enumContainer is the common subset of FileDescriptor and MessageDescriptor
// that exposes both top-level Enums() and nested Messages().
type enumContainer interface {
	Enums() protoreflect.EnumDescriptors
	Messages() protoreflect.MessageDescriptors
}

func findEnumInContainer(c enumContainer, fqn protoreflect.FullName) protoreflect.EnumDescriptor {
	enums := c.Enums()
	for i := 0; i < enums.Len(); i++ {
		e := enums.Get(i)
		if e.FullName() == fqn {
			return e
		}
	}
	msgs := c.Messages()
	for i := 0; i < msgs.Len(); i++ {
		if e := findEnumInContainer(msgs.Get(i), fqn); e != nil {
			return e
		}
	}
	return nil
}

// defaultRegexFor returns the type-implied regex pattern for string semantic
// types that carry one, or "" for types without a default pattern.
//
// UUID is intentionally absent: PG's native UUID column rejects non-UUID
// strings by construction, so a regex CHECK would be pure redundancy. If a
// future dialect lacks a native UUID type and stores it as CHAR(36), the
// emitter for that dialect re-introduces the pattern locally — IR stays
// dialect-neutral.
func defaultRegexFor(sem irpb.SemType) string {
	switch sem {
	case irpb.SemType_SEM_SLUG:
		return `^[a-z0-9]+(?:-[a-z0-9]+)*$`
	case irpb.SemType_SEM_EMAIL:
		// Not RFC 5322 — the "good enough" check every ORM ships.
		return `^[^@\s]+@[^@\s]+\.[^@\s]+$`
	case irpb.SemType_SEM_URL:
		return `^https?://.+$`
	}
	return ""
}

// semTypeStoresAsString returns true when the sem type maps to a string-shaped
// SQL column (VARCHAR / TEXT) across all iter-1 dialects. Returns false for
// sem types that redirect storage to a non-string SQL type:
//   - UUID    → UUID
//   - DECIMAL → NUMERIC
//   - JSON    → JSONB (PG) / JSON (MySQL)
//   - IP      → INET (PG) / VARCHAR(45) (MySQL) — text-shaped but semantically not
//   - TSEARCH → TSVECTOR (PG)
//
// String-only CHECK synths (blank, length, regex, choices) skip on
// non-string storage because the operators don't type-check against these
// columns at apply time.
func semTypeStoresAsString(sem irpb.SemType) bool {
	switch sem {
	case irpb.SemType_SEM_UUID,
		irpb.SemType_SEM_DECIMAL,
		irpb.SemType_SEM_JSON,
		irpb.SemType_SEM_IP,
		irpb.SemType_SEM_TSEARCH,
		irpb.SemType_SEM_ENUM:
		// SEM_ENUM on string carrier routes to PG CREATE TYPE AS ENUM —
		// the dedicated type (not VARCHAR/TEXT) enforces membership, so
		// blank / length / regex / choices synths would be redundant at
		// best and type-check failures at worst.
		return false
	}
	return true
}

// columnStoresAsString combines three axes that can affect storage shape:
//
//  1. Sem-type (UUID/DECIMAL/JSON/IP/TSEARCH → non-string SQL).
//  2. PG passthrough `custom_type` (opaque — assume non-string).
//  3. `(w17.db.column).db_type` override (enumerated — checked via
//     dbTypeStoresAsString).
//
// When any of these redirect storage to a non-string SQL column,
// string-only CHECK synths (blank, length, regex, choices) must skip —
// operators like `<> ''`, `char_length(…)`, `~ 'pat'`, and
// `IN ('a','b')` don't type-check against non-string columns.
//
// Precedence: db_type wins over sem-type (it's the explicit override).
// custom_type wins over both (it's the opaque escape hatch, and IR
// already rejects it co-existing with db_type).
func columnStoresAsString(col *irpb.Column, sem irpb.SemType) bool {
	if pg := col.GetPg(); pg != nil && pgOverridesStorage(pg) {
		return false
	}
	if dbType := col.GetDbType(); dbType != irpb.DbType_DB_TYPE_UNSPECIFIED {
		return dbTypeStoresAsString(dbType)
	}
	return semTypeStoresAsString(sem)
}

func pgOverridesStorage(pg *irpb.PgOptions) bool {
	return pg.GetCustomType() != ""
}

// dbTypeStoresAsString returns true when the db_type maps to a string-
// shaped SQL column (TEXT / VARCHAR / CITEXT). All other DbType values
// map to non-string storage (JSONB, INET, UUID, NUMERIC, TIMESTAMP, …)
// so string-only CHECK synths skip.
func dbTypeStoresAsString(dbType irpb.DbType) bool {
	switch dbType {
	case irpb.DbType_DBT_TEXT, irpb.DbType_DBT_VARCHAR, irpb.DbType_DBT_CITEXT:
		return true
	}
	return false
}

// dbTypeCompatibleWithCarrier enforces the (carrier, db_type) matrix —
// each DbType maps to a class of compatible carriers.
func dbTypeCompatibleWithCarrier(dbType irpb.DbType, carrier irpb.Carrier) bool {
	switch dbType {
	case irpb.DbType_DB_TYPE_UNSPECIFIED:
		return true
	case irpb.DbType_DBT_TEXT, irpb.DbType_DBT_VARCHAR, irpb.DbType_DBT_CITEXT,
		irpb.DbType_DBT_INET, irpb.DbType_DBT_CIDR, irpb.DbType_DBT_MACADDR,
		irpb.DbType_DBT_TSVECTOR, irpb.DbType_DBT_UUID:
		return carrier == irpb.Carrier_CARRIER_STRING
	case irpb.DbType_DBT_JSON, irpb.DbType_DBT_JSONB, irpb.DbType_DBT_HSTORE:
		return carrier == irpb.Carrier_CARRIER_STRING || carrier == irpb.Carrier_CARRIER_BYTES
	case irpb.DbType_DBT_SMALLINT, irpb.DbType_DBT_INTEGER, irpb.DbType_DBT_BIGINT:
		return carrier == irpb.Carrier_CARRIER_INT32 || carrier == irpb.Carrier_CARRIER_INT64
	case irpb.DbType_DBT_REAL, irpb.DbType_DBT_DOUBLE_PRECISION:
		return carrier == irpb.Carrier_CARRIER_DOUBLE
	case irpb.DbType_DBT_NUMERIC:
		return carrier == irpb.Carrier_CARRIER_DOUBLE || carrier == irpb.Carrier_CARRIER_STRING
	case irpb.DbType_DBT_DATE, irpb.DbType_DBT_TIME, irpb.DbType_DBT_TIMESTAMP, irpb.DbType_DBT_TIMESTAMPTZ:
		return carrier == irpb.Carrier_CARRIER_TIMESTAMP
	case irpb.DbType_DBT_INTERVAL:
		return carrier == irpb.Carrier_CARRIER_DURATION
	case irpb.DbType_DBT_BYTEA, irpb.DbType_DBT_BLOB:
		return carrier == irpb.Carrier_CARRIER_BYTES
	case irpb.DbType_DBT_BOOLEAN:
		return carrier == irpb.Carrier_CARRIER_BOOL
	}
	return false
}

// dbTypeToIR maps the authoring-surface (w17.db.column).db_type enum to
// the IR's DbType enum. Values are 1:1 aliases; the separate enums keep
// the IR self-contained without importing the authoring vocabulary.
func dbTypeToIR(t dbpb.DbType) irpb.DbType {
	switch t {
	case dbpb.DbType_TEXT:
		return irpb.DbType_DBT_TEXT
	case dbpb.DbType_VARCHAR:
		return irpb.DbType_DBT_VARCHAR
	case dbpb.DbType_CITEXT:
		return irpb.DbType_DBT_CITEXT
	case dbpb.DbType_JSON:
		return irpb.DbType_DBT_JSON
	case dbpb.DbType_JSONB:
		return irpb.DbType_DBT_JSONB
	case dbpb.DbType_HSTORE:
		return irpb.DbType_DBT_HSTORE
	case dbpb.DbType_INET:
		return irpb.DbType_DBT_INET
	case dbpb.DbType_CIDR:
		return irpb.DbType_DBT_CIDR
	case dbpb.DbType_MACADDR:
		return irpb.DbType_DBT_MACADDR
	case dbpb.DbType_TSVECTOR:
		return irpb.DbType_DBT_TSVECTOR
	case dbpb.DbType_UUID:
		return irpb.DbType_DBT_UUID
	case dbpb.DbType_SMALLINT:
		return irpb.DbType_DBT_SMALLINT
	case dbpb.DbType_INTEGER:
		return irpb.DbType_DBT_INTEGER
	case dbpb.DbType_BIGINT:
		return irpb.DbType_DBT_BIGINT
	case dbpb.DbType_REAL:
		return irpb.DbType_DBT_REAL
	case dbpb.DbType_DOUBLE_PRECISION:
		return irpb.DbType_DBT_DOUBLE_PRECISION
	case dbpb.DbType_NUMERIC:
		return irpb.DbType_DBT_NUMERIC
	case dbpb.DbType_DATE:
		return irpb.DbType_DBT_DATE
	case dbpb.DbType_TIME:
		return irpb.DbType_DBT_TIME
	case dbpb.DbType_TIMESTAMP:
		return irpb.DbType_DBT_TIMESTAMP
	case dbpb.DbType_TIMESTAMPTZ:
		return irpb.DbType_DBT_TIMESTAMPTZ
	case dbpb.DbType_INTERVAL:
		return irpb.DbType_DBT_INTERVAL
	case dbpb.DbType_BYTEA:
		return irpb.DbType_DBT_BYTEA
	case dbpb.DbType_BLOB:
		return irpb.DbType_DBT_BLOB
	case dbpb.DbType_BOOLEAN:
		return irpb.DbType_DBT_BOOLEAN
	}
	return irpb.DbType_DB_TYPE_UNSPECIFIED
}

// displayDbType trims the DBT_ prefix so diagnostics read the way the
// author writes the authoring-surface name (e.g. "JSONB" not
// "DBT_JSONB").
func displayDbType(t irpb.DbType) string {
	return strings.TrimPrefix(t.String(), "DBT_")
}
