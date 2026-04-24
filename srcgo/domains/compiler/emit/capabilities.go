package emit

// Feature-capability catalog. Each emitter publishes a set of
// capabilities its generated SQL may reference, keyed by a stable
// short name (cap ID). For each capability the catalog records:
//
//   - MinVersion: the minimum dialect version that supports the
//     feature natively (empty string = any supported version).
//   - Extensions: dialect extensions that must be loaded (empty
//     slice = none).
//
// Two paths that consume the catalog:
//
//  1. Iter-1.x — static reference. Tests + docs + the platform /
//     deploy client (future) can enumerate the PG features the
//     emitter knows about, what version each needs, and which ones
//     require extensions. `DialectCapabilities.Requirement(cap)`
//     answers this directly.
//
//  2. Iter-2+ — active gating. Emitters accept target-DB config
//     (version, loaded extensions) and validate every feature
//     actually used against it during generation. Schemas that
//     depend on features the target doesn't support fail at IR
//     time with a diagnostic naming the capability + remediation
//     (upgrade, install extension, or avoid the feature). Requires
//     emitter-side usage tracking (`collectUsage(cap, where)`) that
//     iter-1.6 leaves as a stub; the catalog is the shape it writes
//     against.
//
// The cap IDs are strings (not enums) so new features can land
// without proto/enum churn. They follow one of three shapes for
// predictability:
//
//   UPPER_CASE        — SQL type or SQL-spec feature (JSONB, UUID,
//                       ARRAY, INCLUDE_INDEX).
//   lower_case()      — dialect function (gen_random_uuid, uuidv7,
//                       jsonb_path_ops).
//   snake_case        — dialect extension (hstore, citext, pg_trgm).

// Requirement captures what a target DB must provide for a feature
// to apply. Both fields are optional: a feature with neither
// requirement set is universally available in every supported
// version of the dialect.
type Requirement struct {
	// MinVersion is the dialect version string, dialect-specific in
	// form but parseable as dotted-decimal ("18.0", "14", "9.4").
	// Emitters compare targets via SemVer-style numeric comparison
	// with implicit trailing zeros.
	MinVersion string

	// Extensions is the sorted list of dialect extensions required
	// before the feature applies. Emitters / deploy clients are
	// responsible for loading them; the compiler doesn't emit
	// `CREATE EXTENSION` into migration bodies (per D6 / D9 / D11
	// — extension installation is the platform's job).
	Extensions []string
}

// DialectCapabilities is the small inspection interface each SQL
// emitter implements. Pairs with DialectEmitter via composition (no
// explicit composite interface — Go's structural typing handles it).
//
// **Mandatory for SQL dialects, optional for non-SQL.** Postgres,
// MySQL, SQLite, etc. MUST implement this so engine.buildManifest
// can populate Manifest.RequiredExtensions from cap usage. Each SQL
// emitter pins compile-time conformance via
// `var _ emit.DialectCapabilities = Emitter{}` next to its
// Requirement method — forgetting the catalog won't make it past
// `go build`. Non-SQL dialects (redis and other whole-model KV
// stores) may opt out: the catalog has no meaningful surface for
// them, and engine.buildManifest treats a missing impl as
// "contributes no extensions" (the cap list still flows from
// Use() calls).
type DialectCapabilities interface {
	// Name is the stable short identifier of the dialect
	// ("postgres", "mysql", "sqlite", …). Matches DialectEmitter.Name.
	Name() string

	// Requirement returns (req, true) for a cap the dialect knows
	// about, (_, false) for an unknown cap. Unknown caps are a
	// compiler bug (the emitter referenced a capability the catalog
	// doesn't list) — callers surface this as a diagnostic.
	Requirement(cap string) (Requirement, bool)
}

// Canonical capability identifiers. Stable strings so new dialects
// can implement the same keys and downstream tooling can enumerate
// a uniform set. Not every dialect supports every cap — unsupported
// caps are absent from that dialect's Requirement map (not marked
// with a sentinel).

// Core SQL features (SQL:2003 / SQL:2016 and below).
const (
	// JSON / JSONB family.
	CapJSON  = "JSON"
	CapJSONB = "JSONB"

	// Temporal types.
	CapDate        = "DATE"
	CapTime        = "TIME"
	CapTimestamp   = "TIMESTAMP"
	CapTimestampTZ = "TIMESTAMPTZ"
	CapInterval    = "INTERVAL"

	// Numeric.
	CapNumeric         = "NUMERIC"
	CapDoublePrecision = "DOUBLE_PRECISION"

	// Identifier / binary / bool.
	CapUUID    = "UUID"
	CapBYTEA   = "BYTEA"
	CapBoolean = "BOOLEAN"

	// Array (scalar[]) — PG native; others fall back to JSON.
	CapArray = "ARRAY"

	// Dedicated enumerated type (PG `CREATE TYPE … AS ENUM`; MySQL
	// `ENUM(...)` inline on column; SQLite TEXT + CHECK IN names).
	CapEnumType = "ENUM_TYPE"

	// Schema-qualified identifiers (PG `schema.table`). Module-level
	// namespace delivered by D19. PG has universal support (every
	// version); MySQL's "schema = database" quirk makes it unavailable
	// there; SQLite has no schema concept. Name-prefix mode (D19's
	// sibling strategy) is dialect-agnostic and doesn't need a cap —
	// any emitter that accepts an identifier accepts `<prefix>_<name>`.
	CapSchemaQualified = "SCHEMA_QUALIFIED"

	// COMMENT ON TABLE / COLUMN IS '...' — SQL:1999 feature, universal
	// on PG, supported by MySQL (as inline `COMMENT '...'` syntax on
	// column and table definitions — different surface, same effect),
	// not available on stock SQLite (comments live in sqlite_master
	// only for the CREATE statement text).
	CapCommentOn = "COMMENT_ON"

	// Index access methods (D23). BTREE is universal and needs no cap
	// (it's the default every dialect assumes). The others declare
	// per-dialect min-version / extension requirements.
	CapGinIndex    = "GIN_INDEX"
	CapGistIndex   = "GIST_INDEX"
	CapBrinIndex   = "BRIN_INDEX"
	CapSpgistIndex = "SPGIST_INDEX"
	CapHashIndex   = "HASH_INDEX"

	// Indexes + constraints.
	CapIncludeIndex       = "INCLUDE_INDEX"        // covering index
	CapPartialIndex       = "PARTIAL_INDEX"        // CREATE INDEX … WHERE
	CapExpressionIndex    = "EXPRESSION_INDEX"     // CREATE INDEX … ((expr))
	CapIdentityColumn     = "IDENTITY_COLUMN"      // GENERATED BY DEFAULT AS IDENTITY
	CapGeneratedColumn    = "GENERATED_COLUMN"     // GENERATED ALWAYS AS (expr) STORED
	CapOnDeleteRestrict   = "ON_DELETE_RESTRICT"
	CapOnDeleteSetDefault = "ON_DELETE_SET_DEFAULT"

	// Transactional DDL — BEGIN/COMMIT around CREATE TABLE etc.
	CapTransactionalDDL = "TRANSACTIONAL_DDL"
)

// Postgres-specific types.
const (
	CapINET     = "INET"
	CapCIDR     = "CIDR"
	CapMACADDR  = "MACADDR"
	CapTSVECTOR = "TSVECTOR"
	CapTSQUERY  = "TSQUERY"
)

// Postgres-specific dialect functions.
const (
	CapFnGenRandomUUID = "gen_random_uuid()"
	CapFnUUIDv7        = "uuidv7()"
	CapOpJsonbPathOps  = "jsonb_path_ops"
	CapOpGinTrgmOps    = "gin_trgm_ops"
)

// Postgres contrib extensions (and well-known third-party).
const (
	CapExtHstore       = "hstore"
	CapExtCitext       = "citext"
	CapExtPgTrgm       = "pg_trgm"
	CapExtPgJsonschema = "pg_jsonschema"
	CapExtPgUUIDv7     = "pg_uuidv7"
)
