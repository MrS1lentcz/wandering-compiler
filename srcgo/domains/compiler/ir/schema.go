package ir

import "google.golang.org/protobuf/reflect/protoreflect"

// Schema is a full validated authoring input, ready for the differ and
// emitters. Iteration-1: one message -> one Schema with one Table.
type Schema struct {
	Tables []*Table
}

// Table mirrors a single proto message annotated with (w17.db.table).
type Table struct {
	Name    string // SQL table name
	Message protoreflect.MessageDescriptor
	Columns []*Column
	Indexes []*Index

	// Flattened views onto Columns for emitter convenience.
	PrimaryKey  []*Column
	ForeignKeys []*ForeignKey
}

// Column is one SQL column. Most proto-level checks (min_len/max_len on
// non-sized types, numeric bounds, pattern overrides, choices, blank)
// resolve into Checks during ir.Build; the column keeps only the facts
// needed to render its DDL type and direct column modifiers.
type Column struct {
	Name      string // SQL name (after (w17.db.column).name override)
	ProtoName string // original proto field name (for indexes / FK refs)
	Field     protoreflect.FieldDescriptor

	Carrier   Carrier
	Type      SemType
	Nullable  bool
	PK        bool
	Unique    bool // data-level (UNIQUE INDEX). Pure storage index lives in StorageIndex.
	Immutable bool // recorded for future iterations; no SQL emission in iter-1.

	// Size-driving fields. Retained on Column (not folded into LengthCheck)
	// because emitters pick the column DDL type from these before any
	// CHECK constraint renders.
	MaxLen    int32 // CHAR/SLUG -> VARCHAR(N); other strings -> optional upper bound in LengthCheck
	Precision int32 // DECIMAL — required
	Scale     int32 // DECIMAL — optional (0 == "not set")
	HasScale  bool

	// Default value at INSERT time. Nil = no default.
	Default Default

	// Postgres dialect options (jsonb / inet / tsvector / hstore / custom
	// type escape hatch). Nil when no (w17.pg.field) annotation.
	Pg *PgOptions

	// Non-unique storage index — sugar for a single-col entry in table.indexes.
	StorageIndex bool

	// Checks attached to this column (length bounds, blank, ranges, regex,
	// choices). Pre-resolved at build time; emitters type-switch on each.
	Checks []Check
}

// PgOptions is a verbatim passthrough of (w17.pg.field). Interpretation is
// the PG emitter's job.
type PgOptions struct {
	JSONB              bool
	Inet               bool
	TSVector           bool
	HStore             bool
	CustomType         string
	RequiredExtensions []string
}

// Index is a table-level index (either author-declared via
// (w17.db.table).indexes or synthesised from (w17.field).unique /
// (w17.db.column).index). Uniqueness drives UNIQUE INDEX emission;
// Include is Postgres-only (covering-index columns).
type Index struct {
	Name    string // auto-derived by the naming package when empty
	Fields  []string
	Unique  bool
	Include []string
}

// ForeignKey captures a resolved FK. Target is the parsed "<table>.<column>"
// reference; OnDelete is already derived from orphanable + null per D8 so
// emitters do not re-implement the rule.
type ForeignKey struct {
	Column   *Column
	Target   FKRef
	OnDelete FKAction
}

// FKRef is the parsed form of (w17.field).fk.
type FKRef struct {
	Table  string
	Column string
}

// Default is the tagged union over the (w17.field).default oneof.
// Variants: AutoDefault (NOW / UUID_V4 / UUID_V7 / EMPTY_JSON_* / TRUE /
// FALSE / IDENTITY) and the literal branches (string / int / double).
type Default interface {
	isDefault()
}

// AutoDefault kinds — mirrors w17pb.AutoDefault but kept independent of the
// proto enum (same reason as Carrier: IR is dialect + source independent).
type AutoKind int

const (
	AutoUnspecified AutoKind = iota
	AutoNow
	AutoUUIDv4
	AutoUUIDv7
	AutoEmptyJSONArray
	AutoEmptyJSONObject
	AutoTrue
	AutoFalse
	AutoIdentity
)

func (k AutoKind) String() string {
	switch k {
	case AutoNow:
		return "NOW"
	case AutoUUIDv4:
		return "UUID_V4"
	case AutoUUIDv7:
		return "UUID_V7"
	case AutoEmptyJSONArray:
		return "EMPTY_JSON_ARRAY"
	case AutoEmptyJSONObject:
		return "EMPTY_JSON_OBJECT"
	case AutoTrue:
		return "TRUE"
	case AutoFalse:
		return "FALSE"
	case AutoIdentity:
		return "IDENTITY"
	}
	return "<unspecified>"
}

type AutoDefault struct{ Kind AutoKind }
type LiteralString struct{ Value string }
type LiteralInt struct{ Value int64 }
type LiteralDouble struct{ Value float64 }

func (AutoDefault) isDefault()   {}
func (LiteralString) isDefault() {}
func (LiteralInt) isDefault()    {}
func (LiteralDouble) isDefault() {}
