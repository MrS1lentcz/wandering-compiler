# Experiment: Iteration 1 — Model Layer Only

Scope of this experiment is intentionally narrow: **only the model layer**.
No projections, no queries, no services, no events. The goal is to prove one
pipeline end-to-end: a developer writes a proto model, the compiler produces a
SQL migration that can be applied against Postgres.

This corresponds to **Phase 1** in `docs/tech-spec.md`. Everything beyond this
scope is parked in `docs/experiments/_parked/schema-projections.md`.

---

```
=======================================[types.proto](../../../codedesigner/api/proto/types.proto)=========================================
 STAGE 1 — WHAT THE DEVELOPER WRITES  (authoring layer)
================================================================================
```

### `w17/domains/catalog/modules/products/models/product.proto`

```protobuf
syntax = "proto3";
package w17.catalog.products;

import "google/protobuf/timestamp.proto";
import "w17/db.proto";
import "w17/field.proto";
import "w17/pg/field.proto";

enum ProductStatus {
  PRODUCT_STATUS_UNSPECIFIED = 0;
  DRAFT                      = 1;
  ACTIVE                     = 2;
  ARCHIVED                   = 3;
}

message Product {
  option (w17.db.table) = {
    name: "products"
    indexes: [
      { fields: ["category_id", "is_active"] }
    ]
  };

  string id              = 1 [(w17.field) = { type: UUID, pk: true, immutable: true, default_auto: UUID_V4 }];
  string slug            = 2 [(w17.field) = { type: SLUG, max_len: 120, unique: true }];
  string name            = 3 [(w17.field) = { type: CHAR, max_len: 255 }];
  string description     = 4 [(w17.field) = { type: TEXT, blank: true }];
  double price           = 5 [(w17.field) = { type: MONEY, gte: 0 }];
  string tax_rate        = 6 [(w17.field) = { type: DECIMAL, precision: 7, scale: 4 }];
  double discount_rate   = 7 [(w17.field) = { type: RATIO, null: true }];
  string status          = 8 [(w17.field) = { type: CHAR, max_len: 32, choices: "w17.catalog.products.ProductStatus" }];
  string category_id     = 9 [
    (w17.field)     = { type: UUID, fk: "categories.id", null: true, orphanable: true },
    (w17.db.column) = { index: true }
  ];
  int64  stock_quantity  = 10 [(w17.field) = { type: COUNTER, default_int: 0 }];
  bool   is_active       = 11 [(w17.field) = { default_auto: TRUE }];

  // PG-native JSONB metadata column.
  string metadata        = 12 [
    (w17.field)    = { type: TEXT, blank: true },
    (w17.pg.field) = { jsonb: true }
  ];

  // created_at: DB default NOW() at insert. updated_at: DB default NOW() at
  // insert only. Handlers mutating the row MUST set updated_at explicitly —
  // the compiler does not auto-update it. The future mandatory-mutation
  // contract (parked experiment) will verify all write RPCs touch it.
  google.protobuf.Timestamp created_at = 13 [(w17.field) = { type: DATETIME, default_auto: NOW, immutable: true }];
  google.protobuf.Timestamp updated_at = 14 [(w17.field) = { type: DATETIME, default_auto: NOW }];
}
```

Notes on authoring surface in iteration 1:

- **Proto carriers:** `string`, `int32`, `int64`, `bool`, `double`, `google.protobuf.Timestamp`, `google.protobuf.Duration`.
  `bytes`, `jsonb`, `repeated`, `oneof`, and nested messages are **out of scope**.
- **`w17.db.table`:** `name`, `indexes` (single or multi-column, unique or not; each index may set `name` override and `include` covering columns). **No auto-generated fields.** Every DB column must correspond to a declared proto field — if a table needs `created_at`/`updated_at`, the developer declares those as explicit `google.protobuf.Timestamp` fields with `default_auto: NOW`. Soft-delete is an application-level concern (archive tables, tombstone flags, separate audit schemas — the compiler has no opinion) and is not provided as a table option.
- **`w17.field` (merged vocabulary — data semantics):**
  - `type` (required for every carrier except `bool`; picks SQL column type and implicit constraints, see table below).
  - `pk` (primary key), `fk` (`"<table>.<column>"` string — no cross-domain/module wiring yet), `immutable` (for docs in iteration 1; enforcement comes with services).
  - `orphanable` (optional bool, FK-only) — property describing whether the row can outlive its parent. `true` → `SET NULL` on parent delete (needs `null: true`); `false` → `CASCADE`; unset → inferred from `null`. See D8.
  - `null: true` — opt-out of `NOT NULL`. Column becomes nullable in DB, field becomes `optional` in internal proto (so presence survives), validator stops requiring a value. Default is `null: false` → NOT NULL + required.
  - `blank: true` — allow empty string for string types. Default is `blank: false` → `CHECK (col <> '')`. `blank` is orthogonal to `null` (null = "value may be missing"; blank = "if present, may be empty").
  - `unique: true` — data-level uniqueness. Renders as `CREATE UNIQUE INDEX`. Orthogonal to the storage-only `(w17.db.column).index` flag.
  - `max_len` / `min_len` — length bounds for string carriers; `max_len` is required for `CHAR` / `SLUG` and drives `VARCHAR(N)` sizing.
  - `gt` / `gte` / `lt` / `lte` — numeric bounds (optional, so `0` is distinguishable from unset).
  - `pattern` — regex, overrides the type's default regex (e.g. `SLUG`).
  - `choices` — FQN of a proto enum reachable from this file; emits `CHECK (col IN ('VAL1', …))` with the enum's value *names*. Cross-file permitted. See D8.
  - `precision` / `scale` — `type: DECIMAL` only; total digits + digits after the decimal point.
  - `default` oneof — see D7. Literal branches `default_string` / `default_int` / `default_double`, or `default_auto: AutoDefault` for dynamically resolved defaults (`NOW`, `UUID_V4/V7`, `TRUE/FALSE`, `EMPTY_JSON_*`, `IDENTITY` for auto-increment PKs).
- **`w17.db.column` (storage-only overrides, orthogonal to data semantics):**
  - `index: true` — single-field non-unique storage index (sugar for a single-column entry in `(w17.db.table).indexes`). Does **not** imply `UNIQUE`.
  - `name` — SQL column-name override (rare; mostly for adopting existing schemas with non-proto naming conventions).
- **`w17.pg.field` (Postgres dialect namespace — see D9):**
  - `jsonb` / `inet` / `tsvector` / `hstore` — curated PG-native types; each replaces the SQL column type that `(w17.field).type` would otherwise pick.
  - `custom_type` — raw SQL column type passthrough (escape hatch for pgvector, PostGIS, custom DOMAINs). When set, the PG emitter bypasses the semantic-type dispatch for this column.
  - `required_extensions` — Postgres extensions the emitter must `CREATE EXTENSION IF NOT EXISTS` before table creation.
- **Out of scope:** projections, references into `common/`, cross-module FKs resolved via package paths, `on_update` auto-mutation side-effects (deliberately rejected — see D7 + parked mandatory-mutation-contract experiment).

### Semantic type enum

Semantic `type` is the Django-inspired "data refinement + basic constraints in one" knob. It controls the SQL column type and emits implicit CHECK constraints. Additional bounds (`gt`/`gte`/`lt`/`lte`/`min_len`/`max_len`/`pattern`) stack on top via other `(w17.field)` attributes — they live on the same extension since M1 rev2.

**String types** (proto carrier: `string`):

| `type` value | SQL column                  | Implicit constraints                                                    |
|-------------:|----------------------------|-------------------------------------------------------------------------|
| `CHAR`       | `VARCHAR(max_len)`         | `max_len` required; `CHECK (char_length(col) <= max_len)`; `<> ''` unless `blank: true` |
| `TEXT`       | `TEXT`                     | `<> ''` unless `blank: true`                                            |
| `UUID`       | `UUID` (PG native)         | format enforced by column type                                          |
| `EMAIL`      | `VARCHAR(320)`             | `CHECK` with loose email regex; `<> ''` unless `blank: true`            |
| `URL`        | `TEXT`                     | `<> ''` unless `blank: true` (regex validation at app layer)            |
| `SLUG`       | `VARCHAR(max_len)`         | `max_len` required; `CHECK` with `^[a-z0-9-]+$`                         |

**Number types** (proto carriers as noted):

| `type` value | Proto carrier      | SQL column            | Implicit constraints                          |
|-------------:|-------------------|-----------------------|-----------------------------------------------|
| `NUMBER`     | `int32`/`int64`/`double` | `INTEGER`/`BIGINT`/`DOUBLE PRECISION` | none (bounds via `(w17.field).gt/gte/lt/lte`) |
| `ID`         | `int32`/`int64`   | `INTEGER`/`BIGINT`    | `CHECK (col >= 0)`                            |
| `COUNTER`    | `int64`           | `BIGINT`              | `CHECK (col >= 0)`; no implicit default (use `default_int: 0`) |
| `MONEY`      | `double`          | `NUMERIC(19, 4)`      | none (bounds via `(w17.field).gt/gte/lt/lte`). Currency code is a separate field. Wire format is lossy `double`; use int64-cents pattern if you need exact transport. |
| `PERCENTAGE` | `double`          | `NUMERIC(5, 2)`       | `CHECK (col BETWEEN 0 AND 100)` — "human" 0–100 scale |
| `RATIO`      | `double`          | `NUMERIC(5, 4)`       | `CHECK (col BETWEEN 0 AND 1)` — mathematical 0–1 fraction |
| `DECIMAL`    | `string`          | `NUMERIC(precision, scale)` | Requires `(w17.field).precision` (> 0); `scale` optional (default 0). String carrier for lossless wire; bounds via `gt/gte/lt/lte` (double-typed, precision-limited by double's range). |

**Temporal types** (proto carriers as noted):

| `type` value | Proto carrier                | SQL column     | Implicit constraints / notes                 |
|-------------:|-----------------------------|----------------|----------------------------------------------|
| `DATE`       | `google.protobuf.Timestamp` | `DATE`         | time component truncated at emit time        |
| `TIME`       | `google.protobuf.Timestamp` | `TIME`         | date component truncated at emit time        |
| `DATETIME`   | `google.protobuf.Timestamp` | `TIMESTAMPTZ`  | stored with TZ; canonical UTC                |
| `INTERVAL`   | `google.protobuf.Duration`  | `INTERVAL`     | unspecified `type` is permitted (inferred)   |

**Other types:**

- `bool` carrier, no semantic subtype — maps to `BOOLEAN`.

### Null and blank semantics, together

Four combinations that the author should understand:

| `null`  | `blank` | DB                      | Validator behavior                                        |
|--------:|--------:|-------------------------|-----------------------------------------------------------|
| `false` | `false` | `NOT NULL`, `<> ''`     | Required, must not be empty. **Default for every field.** |
| `false` | `true`  | `NOT NULL`, no `<> ''`  | Required, empty string OK.                                |
| `true`  | `false` | `NULL` allowed          | Optional; if provided, must not be empty string.          |
| `true`  | `true`  | `NULL` allowed          | Optional; if provided, may be empty.                      |

`blank` applies only to string `type`s; for numeric/bool/timestamp it is ignored.

---

```
================================================================================
 STAGE 2 — WHAT THE COMPILER PRODUCES INTERNALLY  (internal proto)
================================================================================

 The authoring layer above is never fed to `protoc` directly. The compiler
 first emits plain proto under `gen/proto/` — the same field set as the
 authoring proto, stripped of `w17.*` annotations. No fields are added or
 removed. This is the "internal proto" consumed by every downstream tool
 (no custom options required).
================================================================================
```

### `gen/proto/w17/catalog/products/product.proto`

```protobuf
syntax = "proto3";
package w17.catalog.products;

import "google/protobuf/timestamp.proto";

// Generated from w17/domains/catalog/modules/products/models/product.proto
// DO NOT EDIT.
message Product {
  string  id              = 1;
  string  slug            = 2;
  string  name            = 3;
  string  description     = 4;
  double  price           = 5;
  optional double discount_rate = 6;  // null: true → proto3 optional
  string  category_id     = 7;
  int64   stock_quantity  = 8;
  bool    is_active       = 9;
  google.protobuf.Timestamp created_at = 10;
  google.protobuf.Timestamp updated_at = 11;
}
```

Observations:

- `w17.*` extensions are gone — what remains is standard proto any `protoc` plugin handles.
- Field numbers and names are preserved 1:1 from the authoring proto. No fields are injected by the compiler.
- `null: true` fields become proto3 `optional` (so presence survives the wire format — without it, a nullable column and the scalar zero value would collide).
- Validation metadata is not carried into the internal proto at this stage — it lives in a side-channel consumed by the SQL generator (iteration 1 does not yet generate Go validators).

---

```
================================================================================
 STAGE 3 — SQL MIGRATION  (primary deliverable of iteration 1)
================================================================================
```

### `out/migrations/0001_create_products.up.sql`

```sql
CREATE TABLE products (
    id               UUID             PRIMARY KEY  DEFAULT gen_random_uuid(),
    slug             VARCHAR(120)     NOT NULL,
    name             VARCHAR(255)     NOT NULL,
    description      TEXT             NOT NULL,
    price            NUMERIC(19, 4)   NOT NULL,
    discount_rate    NUMERIC(5, 4)    NULL,
    category_id      UUID             NOT NULL REFERENCES categories(id),
    stock_quantity   BIGINT           NOT NULL,
    is_active        BOOLEAN          NOT NULL    DEFAULT TRUE,
    created_at       TIMESTAMPTZ      NOT NULL    DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL    DEFAULT NOW(),

    CONSTRAINT products_slug_len      CHECK (char_length(slug) <= 120),
    CONSTRAINT products_slug_format   CHECK (slug ~ '^[a-z0-9-]+$'),
    CONSTRAINT products_name_len      CHECK (char_length(name) <= 255),
    CONSTRAINT products_name_blank    CHECK (name <> ''),
    CONSTRAINT products_price_min     CHECK (price >= 0),
    CONSTRAINT products_discount_rate CHECK (discount_rate BETWEEN 0 AND 1),
    CONSTRAINT products_stock_min     CHECK (stock_quantity >= 0)
);

CREATE UNIQUE INDEX products_slug_uidx
    ON products (slug);

CREATE INDEX products_category_id_idx
    ON products (category_id);

CREATE INDEX products_category_id_is_active_idx
    ON products (category_id, is_active);
```

### `out/migrations/0001_create_products.down.sql`

```sql
DROP INDEX IF EXISTS products_category_id_is_active_idx;
DROP INDEX IF EXISTS products_category_id_idx;
DROP INDEX IF EXISTS products_slug_uidx;
DROP TABLE IF EXISTS products;
```

Mapping rules applied:

| Authoring concept                           | SQL output                                      |
|---------------------------------------------|-------------------------------------------------|
| `type: CHAR, max_len: N`                    | `VARCHAR(N)` + length & blank CHECKs            |
| `type: TEXT`                                | `TEXT` (+ blank CHECK unless `blank: true`)     |
| `type: UUID`                                | `UUID`                                          |
| `type: EMAIL`                               | `VARCHAR(320)` + format CHECK                   |
| `type: URL`                                 | `TEXT` (+ blank CHECK unless `blank: true`)     |
| `type: SLUG, max_len: N`                    | `VARCHAR(N)` + `^[a-z0-9-]+$` CHECK             |
| `type: NUMBER` (int32/int64/double carrier) | `INTEGER` / `BIGINT` / `DOUBLE PRECISION`       |
| `type: ID`                                  | same as carrier + `CHECK (col >= 0)`            |
| `type: COUNTER`                             | `BIGINT` + `CHECK (col >= 0)` (no implicit default; use `default_int: 0`) |
| `type: MONEY`                               | `NUMERIC(19, 4)`                                |
| `type: PERCENTAGE`                          | `NUMERIC(5, 2)` + `CHECK (col BETWEEN 0 AND 100)` |
| `type: RATIO`                               | `NUMERIC(5, 4)` + `CHECK (col BETWEEN 0 AND 1)` |
| `type: DATE`                                | `DATE` (Timestamp carrier)                      |
| `type: TIME`                                | `TIME` (Timestamp carrier)                      |
| `type: DATETIME`                            | `TIMESTAMPTZ` (Timestamp carrier)               |
| `type: INTERVAL`                            | `INTERVAL` (Duration carrier)                   |
| `type: DECIMAL, precision: P, scale: S`     | `NUMERIC(P, S)` (string carrier)                |
| `bool` carrier                              | `BOOLEAN`                                       |
| `(w17.field) = { pk: true }`                | `PRIMARY KEY`                                   |
| `(w17.field) = { fk: "t.c" }`               | `REFERENCES t(c)` (delete action from `orphanable`) |
| `(w17.field) = { fk, orphanable: true }`    | `ON DELETE SET NULL` (requires `null: true`)    |
| `(w17.field) = { fk, orphanable: false }`   | `ON DELETE CASCADE`                             |
| `(w17.field) = { fk }` (orphanable unset, NOT NULL) | inferred `ON DELETE CASCADE`            |
| `(w17.field) = { fk, null: true }` (orphanable unset) | inferred `ON DELETE SET NULL`         |
| `(w17.field) = { null: true }`              | drop `NOT NULL`; emit proto3 `optional`         |
| `(w17.field) = { blank: false }` (default)  | `CHECK (col <> '')` on string types             |
| `(w17.field) = { unique: true }`            | `CREATE UNIQUE INDEX` (single-column, data-level) |
| `(w17.field) = { gt/gte/lt/lte/pattern }`   | `CHECK` constraint (merged from old `(w17.validate)`) |
| `(w17.field) = { choices: "Path.To.Enum" }` | `CHECK (col IN ('VAL1', 'VAL2', …))` — enum value names |
| `(w17.field) = { default_string: "x" }`     | column `DEFAULT 'x'`                            |
| `(w17.field) = { default_int: N }`          | column `DEFAULT N`                              |
| `(w17.field) = { default_double: X }`       | column `DEFAULT X`                              |
| `(w17.field) = { default_auto: NOW }`       | `DEFAULT NOW()` / `CURRENT_DATE` / `CURRENT_TIME` per type |
| `(w17.field) = { default_auto: UUID_V4 }`   | `DEFAULT gen_random_uuid()`                     |
| `(w17.field) = { default_auto: UUID_V7 }`   | `DEFAULT uuidv7()` (extension required; emitter flags if missing) |
| `(w17.field) = { default_auto: TRUE/FALSE }`| `DEFAULT TRUE` / `DEFAULT FALSE`                |
| `(w17.field) = { default_auto: EMPTY_JSON_* }` | `DEFAULT '[]'` / `DEFAULT '{}'`              |
| `(w17.field) = { default_auto: IDENTITY }`  | `GENERATED BY DEFAULT AS IDENTITY` (PG / Oracle / DB2 / MSSQL); `AUTO_INCREMENT` (MySQL); `AUTOINCREMENT` (SQLite) |
| `(w17.db.column) = { index: true }`         | `CREATE INDEX` (non-unique, single-column)      |
| `(w17.db.column) = { name: "x" }`           | SQL column name override                        |
| `(w17.pg.field) = { jsonb: true }`          | `JSONB` (overrides core type dispatch)          |
| `(w17.pg.field) = { inet/tsvector/hstore }` | `INET` / `TSVECTOR` / `HSTORE` (ditto)          |
| `(w17.pg.field) = { custom_type: "x" }`     | `x` verbatim (escape hatch); bypasses core type |
| `(w17.pg.field) = { required_extensions: ["x"] }` | `CREATE EXTENSION IF NOT EXISTS "x"` prepended to migration (deduplicated) |
| `indexes: [{ unique: true, fields }]`       | `CREATE UNIQUE INDEX`                           |
| `indexes: [{ fields }]`                     | `CREATE INDEX`                                  |
| `indexes: [{ fields, name: "x" }]`          | `CREATE INDEX x` (override auto-derived name)   |
| `indexes: [{ fields, include: [cols] }]`    | `CREATE INDEX … INCLUDE (cols)` (Postgres covering index) |

Every generated row is deterministic: same proto input always yields the same SQL
byte-for-byte. This is a hard acceptance criterion (see `docs/iteration-1.md`).

---

```
================================================================================
 STAGE 4 — INTERMEDIATE REPRESENTATION  (internal; drives diff + emitter)
================================================================================

 The compiler builds its own IR of the schema. Everything downstream — SQL
 emitter, back-compat lint, visualization, changelog generator — operates on
 this IR, not on the authoring proto and not on emitted SQL. The IR is the
 one place semantic types and constraints are resolved into a dialect-agnostic
 shape. Ent and Atlas are NOT used; see D4 in docs/iteration-1.md.
================================================================================
```

### IR shape (Go, internal to the compiler)

```go
// Package ir is the dialect-agnostic schema representation. All downstream
// stages — diff, SQL emitter, lint, viz, changelog — consume this, not proto.
package ir

type Schema struct {
    Tables []*Table
}

type Table struct {
    Name        string
    Columns     []*Column
    Indexes     []*Index
    Checks      []Check        // tagged union, see below
    ForeignKeys []*ForeignKey
}

type Column struct {
    Name       string       // SQL column name (proto field name 1:1 unless overridden via (w17.db.column).name)
    Carrier    ProtoCarrier // STRING | INT32 | INT64 | BOOL | DOUBLE | TIMESTAMP | DURATION
    SemType    SemanticType // UUID | CHAR | TEXT | EMAIL | URL | SLUG
                            // | NUMBER | ID | COUNTER | MONEY | PERCENTAGE | RATIO | DECIMAL
                            // | DATE | TIME | DATETIME | INTERVAL
                            // | (none — for bool carrier)
    MaxLen     int          // only for CHAR / SLUG
    Precision  int          // only for DECIMAL (> 0)
    Scale      int          // only for DECIMAL (0 <= Scale <= Precision)
    Null       bool         // default false → NOT NULL
    Blank      bool         // default false; string-only
    Unique     bool         // data-level uniqueness (w17.field.unique)
    StoreIndex bool         // storage-only index (w17.db.column.index); non-unique
    PK         bool
    Immutable  bool         // iteration-1: annotation only, not enforced
    Choices    *EnumRef     // nil = no choices restriction; non-nil = enum value-name CHECK
    Default    Default      // tagged union, nil = no default; see below

    // DialectSpecific carries per-dialect options the core IR doesn't model
    // (e.g. (w17.pg.field).jsonb / custom_type / required_extensions). Each
    // emitter reads only its own key; others pass through untouched. Keeps
    // the IR dialect-agnostic — see D9.
    DialectSpecific map[DialectKey]proto.Message
}

// Choices / enum resolution — proto enum reference captured after loader
// resolution. Emitters render CHECK (col IN ('…', '…', …)) using Values.
type EnumRef struct {
    FQN    string
    Values []string // enum value names, in declaration order
}

// ForeignKey gains the orphan-disposition field sourced from
// (w17.field).orphanable (or inferred from Column.Null).
type ForeignKey struct {
    Column      string
    RefTable    string
    RefColumn   string
    Orphanable  bool // true → ON DELETE SET NULL; false → ON DELETE CASCADE
}

// Default is a tagged union covering the oneof on (w17.field).
// Emitters render each variant per dialect.
type Default interface{ defaultKind() }

type DefaultString struct{ Value string }
type DefaultInt    struct{ Value int64 }
type DefaultDouble struct{ Value float64 }
type DefaultAuto   struct{ Kind AutoDefaultKind } // NOW | UUID_V4 | UUID_V7 | EMPTY_JSON_ARRAY | EMPTY_JSON_OBJECT | TRUE | FALSE | IDENTITY

type Index struct {
    Name    string   // stable, derived from table + fields unless overridden
    Fields  []string
    Unique  bool
    Include []string // Postgres INCLUDE (covering index); emitters that don't support it error on non-empty
}

// Check is a tagged union. Each variant carries the semantic intent, NOT a
// SQL string — the per-dialect emitter renders each kind to dialect-specific
// SQL (regex operator, length function, etc. differ between PG / MySQL / SQLite).
type Check interface{ checkKind() }

type LengthCheck struct{ Column string; Max int }           // CHAR / SLUG max length
type BlankCheck  struct{ Column string }                    // col <> ''
type RangeCheck  struct{ Column string; Min, Max *float64 } // numeric bounds
type RegexCheck  struct{ Column string; Pattern string }    // SLUG format, EMAIL, user pattern
```

### Diff shape (also internal)

```go
// Package plan builds a MigrationPlan from two Schemas. Iteration-1 only ever
// diffs nil → Schema (initial migration), but the same types carry all later
// alter operations.
package plan

type MigrationPlan struct {
    Ops []Op
}

type Op interface{ opKind() }

// Iteration-1 only emits AddTable. Drop/Alter/Rename ops come with iteration-2+
// when a real pilot project starts altering an existing schema.
type AddTable struct{ Table *ir.Table }
```

### Why this replaces ent / Atlas

1. **Dialect portability without lock-in.** `Check` is a kind-tagged union, not raw SQL — so `LengthCheck` renders to `char_length(col) <= N` on PG and `CHAR_LENGTH(col) <= N` on MySQL. Atlas's HCL `check { expr = "..." }` block can't do this; its expression is a SQL string that ties the schema to a dialect.

2. **Lint, viz, changelog live here.** Back-compat lint (e.g., "adding NOT NULL column without default to non-empty table") runs over `MigrationPlan.Ops`. Changelog renders `AddTable{…}` to human markdown. Visualization walks `Schema.Tables`. None of these are possible on top of emitted SQL without re-parsing it.

3. **Iteration-1 differ is trivial.** `prev == nil` → every `Table` becomes an `AddTable` op. Full alter diffing (rename vs drop+add, type changes, nullability transitions) is deferred to iteration-2 when a real pilot needs it — we add ops as demand appears, not upfront.

4. **No hand-written IR per table.** The IR is built by the proto loader; there is no `gen/ir/` directory a developer would look at. It is runtime-only state inside the compiler binary.

---

```
================================================================================
 OPEN QUESTIONS FOR THIS ITERATION
================================================================================
```

1. **Migration naming scheme.** Timestamp prefix (`20260420_120000_*`) vs. incremental
   number (`0001_*`, `0002_*`) vs. hash-stamped chain? Tradeoff: merge-conflict
   resistance vs. readability vs. ordering guarantees.
2. **Validation surface in SQL.** Should `min_len` / `max_len` be emitted as `CHECK`
   constraints (shown above) or left to application-layer validators only?
   Tradeoff: DB as second line of defense vs. migration complexity.
3. ~~**`required` semantics.**~~ **Resolved** — see Decisions section below.
   Default is `NOT NULL` + required. Opt-out is `(w17.field) = { null: true }`,
   which makes the column nullable and emits proto3 `optional` so presence
   survives the wire format. `required` is not part of `(w17.field)` — it
   would be redundant with `null`. (Historically also absent from the now-
   removed `(w17.validate)`.)
4. ~~**Default values & timestamp ergonomics.**~~ **Resolved** by M1 rev2
   (2026-04-20) — see `iteration-1.md` D7. `(w17.field)` carries a `default`
   oneof: `default_string` / `default_int` / `default_double` for explicit
   literals, `default_auto: AutoDefault` for dynamically resolved values
   (`NOW`, `UUID_V4`, `UUID_V7`, `TRUE`, `FALSE`, `EMPTY_JSON_ARRAY`,
   `EMPTY_JSON_OBJECT`). No `on_update` — handlers mutate `updated_at`-style
   fields explicitly; future static enforcement via the parked
   mandatory-mutation-contract experiment (`_parked/mandatory-mutation-contract.md`).
5. **Ent boundary.** Do we use ent's full migration tooling (schema diff → Atlas → SQL),
   or do we bypass ent and write our own diff against a stored snapshot? Ent gives
   us maturity for free; bypass gives us full control and removes a heavy dependency.

These five questions are the concrete agenda for the next iteration-1 discussion.
