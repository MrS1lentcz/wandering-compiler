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
import "w17/validate.proto";

message Product {
  option (w17.db.table) = {
    name: "products"
    indexes: [
      { fields: ["slug"],                      unique: true },
      { fields: ["category_id", "is_active"]                }
    ]
  };

  string id              = 1  [(w17.field) = { type: UUID, pk: true, immutable: true }];
  string slug            = 2  [(w17.field) = { type: SLUG, max_len: 120 }];
  string name            = 3  [(w17.field) = { type: CHAR, max_len: 255 }];
  string description     = 4  [(w17.field) = { type: TEXT, blank: true }];
  double price           = 5  [(w17.field) = { type: MONEY }, (w17.validate) = { gte: 0 }];
  double discount_rate   = 6  [(w17.field) = { type: RATIO, null: true }];
  string category_id     = 7  [(w17.field) = { type: UUID, fk: "categories.id" }];
  int64  stock_quantity  = 8  [(w17.field) = { type: COUNTER }];
  bool   is_active       = 9;
}
```

Notes on authoring surface in iteration 1:

- **Proto carriers:** `string`, `int32`, `int64`, `bool`, `double`, `google.protobuf.Timestamp`.
  `bytes`, `jsonb`, `repeated`, `oneof`, and nested messages are **out of scope**.
- **`w17.db.table`:** `name`, `indexes` (single or multi-column, unique or not). **No auto-generated fields.** Every DB column must correspond to a declared proto field — if a table needs `created_at`/`updated_at`, the developer declares those as explicit `google.protobuf.Timestamp` fields. Soft-delete is an application-level concern (archive tables, tombstone flags, separate audit schemas — the compiler has no opinion) and is not provided as a table option.
- **`w17.field`:**
  - `type` (required, enum — semantic subtype; picks SQL column type and implicit constraints, see table below).
  - `pk` (primary key), `fk` (`"<table>.<column>"` string — no cross-domain/module wiring yet), `immutable` (for docs in iteration 1; enforcement comes with services).
  - `null: true` — opt-out of `NOT NULL`. Column becomes nullable in DB, field becomes `optional` in internal proto (so presence survives), validator stops requiring a value. Default is `null: false` → NOT NULL + required.
  - `blank: true` — allow empty string for string types. Default is `blank: false` → `CHECK (col <> '')`. `blank` is orthogonal to `null` (null = "value may be missing"; blank = "if present, may be empty").
  - `max_len` — only meaningful for `CHAR` and `SLUG`; drives `VARCHAR(N)` sizing.
- **`w17.validate`:** `min_len`, `max_len` (length for strings beyond what `type` implies), `gt`, `gte`, `lt`, `lte` (numeric bounds), `pattern` (regex, overrides the type's default regex).
- **Out of scope:** projections, references into `common/`, cross-module FKs resolved via package paths, auto-generated default values beyond zero-values and `COUNTER` → 0, field-level `default`/`on_update` annotations (needed before timestamps are ergonomic to declare — parked as a follow-up design question, see Open questions below).

### Semantic type enum

Semantic `type` is the Django-inspired "data refinement + basic constraints in one" knob. It controls the SQL column type and emits implicit CHECK constraints. Additional `w17.validate` rules stack on top.

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
| `NUMBER`     | `int32`/`int64`/`double` | `INTEGER`/`BIGINT`/`DOUBLE PRECISION` | none (bounds via `w17.validate`) |
| `ID`         | `int32`/`int64`   | `INTEGER`/`BIGINT`    | `CHECK (col >= 0)`                            |
| `COUNTER`    | `int64`           | `BIGINT`              | `CHECK (col >= 0)`; default `0`               |
| `MONEY`      | `double`          | `NUMERIC(19, 4)`      | none (bounds via `w17.validate`). Currency code is a separate field. Wire format is lossy `double`; use int64-cents pattern if you need exact transport. |
| `PERCENTAGE` | `double`          | `NUMERIC(5, 2)`       | `CHECK (col BETWEEN 0 AND 100)` — "human" 0–100 scale |
| `RATIO`      | `double`          | `NUMERIC(5, 4)`       | `CHECK (col BETWEEN 0 AND 1)` — mathematical 0–1 fraction |

**Other types:**

- `bool` carrier, no semantic subtype — maps to `BOOLEAN`.
- `google.protobuf.Timestamp` carrier, no semantic subtype yet — maps to `TIMESTAMPTZ`. (Date-only, local-datetime, and duration variants are deferred to a later iteration.)

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
    id               UUID             PRIMARY KEY,
    slug             VARCHAR(120)     NOT NULL,
    name             VARCHAR(255)     NOT NULL,
    description      TEXT             NOT NULL,
    price            NUMERIC(19, 4)   NOT NULL,
    discount_rate    NUMERIC(5, 4)    NULL,
    category_id      UUID             NOT NULL REFERENCES categories(id),
    stock_quantity   BIGINT           NOT NULL DEFAULT 0,
    is_active        BOOLEAN          NOT NULL,

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

CREATE INDEX products_category_id_is_active_idx
    ON products (category_id, is_active);
```

### `out/migrations/0001_create_products.down.sql`

```sql
DROP INDEX IF EXISTS products_category_id_is_active_idx;
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
| `type: COUNTER`                             | `BIGINT NOT NULL DEFAULT 0` + `CHECK (col >= 0)`|
| `type: MONEY`                               | `NUMERIC(19, 4)`                                |
| `type: PERCENTAGE`                          | `NUMERIC(5, 2)` + `CHECK (col BETWEEN 0 AND 100)` |
| `type: RATIO`                               | `NUMERIC(5, 4)` + `CHECK (col BETWEEN 0 AND 1)` |
| `bool` carrier                              | `BOOLEAN`                                       |
| `google.protobuf.Timestamp`                 | `TIMESTAMPTZ`                                   |
| `(w17.field) = { pk: true }`                | `PRIMARY KEY`                                   |
| `(w17.field) = { fk: "t.c" }`               | `REFERENCES t(c)`                               |
| `(w17.field) = { null: true }`              | drop `NOT NULL`; emit proto3 `optional`         |
| `(w17.field) = { blank: false }` (default)  | `CHECK (col <> '')` on string types             |
| `(w17.validate) = { gt/gte/lt/lte/pattern }`| `CHECK` constraint                              |
| `indexes: [{ unique: true, fields }]`       | `CREATE UNIQUE INDEX`                           |
| `indexes: [{ fields }]`                     | `CREATE INDEX`                                  |

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
    Name      string       // proto field name, 1:1
    Carrier   ProtoCarrier // STRING | INT32 | INT64 | BOOL | DOUBLE | TIMESTAMP
    SemType   SemanticType // UUID | CHAR | TEXT | EMAIL | URL | SLUG
                           // | NUMBER | ID | COUNTER | MONEY | PERCENTAGE | RATIO
                           // | (none — for bool / timestamp carriers)
    MaxLen    int          // only for CHAR / SLUG
    Null      bool         // default false → NOT NULL
    Blank     bool         // default false; string-only
    PK        bool
    Immutable bool         // iteration-1: annotation only, not enforced
}

type Index struct {
    Name   string   // stable, derived from table + fields
    Fields []string
    Unique bool
}

type ForeignKey struct {
    Column    string
    RefTable  string
    RefColumn string
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
   survives the wire format. `required` is no longer part of `(w17.validate)`
   — it would be redundant with `null`.
4. **Default values & timestamp ergonomics.** Iteration 1 emits no column
   `DEFAULT` other than `COUNTER` → `0`. Every other field must be supplied
   at INSERT time. Concretely: a developer who declares an explicit
   `google.protobuf.Timestamp created_at` field gets a `TIMESTAMPTZ NOT NULL`
   column with no default, which is awkward. The follow-up design question
   is whether to introduce field-level `(w17.field) = { default: NOW, on_update: NOW }`
   (Django `auto_now_add` / `auto_now` equivalents) — without that, hand-written
   timestamp fields are strictly worse than the old `timestamps: true` shortcut
   that was deliberately removed. Decision deferred until we have one real
   table in a pilot project that needs timestamps.
5. **Ent boundary.** Do we use ent's full migration tooling (schema diff → Atlas → SQL),
   or do we bypass ent and write our own diff against a stored snapshot? Ent gives
   us maturity for free; bypass gives us full control and removes a heavy dependency.

These five questions are the concrete agenda for the next iteration-1 discussion.
