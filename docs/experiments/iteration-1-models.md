# Experiment: Iteration 1 — Model Layer Only

Scope of this experiment is intentionally narrow: **only the model layer**.
No projections, no queries, no services, no events. The goal is to prove one
pipeline end-to-end: a developer writes a proto model, the compiler produces a
SQL migration that can be applied against Postgres.

This corresponds to **Phase 1** in `docs/tech-spec.md`. Everything beyond this
scope is parked in `docs/experiments/_parked/schema-projections.md`.

---

```
================================================================================
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
    name:        "products"
    timestamps:  true          // generates created_at, updated_at columns
    soft_delete: true          // generates deleted_at column
    indexes: [
      { fields: ["slug"],                      unique: true },
      { fields: ["category_id", "is_active"]                }
    ]
  };

  string id              = 1 [(w17.field)    = { pk: true, immutable: true }];
  string slug            = 2 [(w17.validate) = { min_len: 1, max_len: 120 }];
  string name            = 3 [(w17.validate) = { min_len: 1, max_len: 255 }];
  string description     = 4;
  double price_cents     = 5 [(w17.validate) = { min: 0 }];
  string category_id     = 6 [(w17.field)    = { fk: "categories.id" }];
  int32  stock_quantity  = 7 [(w17.validate) = { min: 0 }];
  bool   is_active       = 8;
}
```

Notes on authoring surface in iteration 1:

- **Types:** `string`, `int32`, `int64`, `bool`, `double`, `google.protobuf.Timestamp`.
  `decimal`, `bytes`, `jsonb`, `repeated`, `oneof`, and nested messages are **out of scope**.
- **`w17.db.table`:** `name`, `timestamps`, `soft_delete`, `indexes` (single or multi-column, unique or not).
- **`w17.field`:** `pk` (primary key), `fk` (target as `"<table>.<column>"` string — no cross-domain/module wiring yet), `immutable` (for docs/validation only in iteration 1; enforcement comes with services).
- **`w17.validate`:** `min_len`, `max_len`, `min`, `max`, `required`.
- **Out of scope:** projections, references into `common/`, cross-module FKs resolved via package paths, default values, check constraints.

---

```
================================================================================
 STAGE 2 — WHAT THE COMPILER PRODUCES INTERNALLY  (internal proto)
================================================================================

 The authoring layer above is never fed to `protoc` directly. The compiler
 first expands it into plain proto under `gen/proto/` — stripped of `w17.*`
 annotations, with auto-added fields (timestamps, soft_delete) materialized.
 This is the "internal proto" the user earlier described.
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
  string                      id              = 1;
  string                      slug            = 2;
  string                      name            = 3;
  string                      description     = 4;
  double                      price_cents     = 5;
  string                      category_id     = 6;
  int32                       stock_quantity  = 7;
  bool                        is_active       = 8;

  // Added by (w17.db.table).timestamps = true
  google.protobuf.Timestamp   created_at      = 100;
  google.protobuf.Timestamp   updated_at      = 101;

  // Added by (w17.db.table).soft_delete = true
  google.protobuf.Timestamp   deleted_at      = 102;
}
```

Observations:

- `w17.*` extensions are gone — what remains is standard proto any `protoc` plugin handles.
- `timestamps` and `soft_delete` columns are explicit in the internal proto, not hidden behind options.
- Field numbers for auto-added columns start at 100 to avoid collision with user fields.
- Validation metadata is not carried into the internal proto at this stage — it lives in a side-channel consumed by the SQL generator (iteration 1 does not yet generate Go validators).

---

```
================================================================================
 STAGE 3 — SQL MIGRATION  (primary deliverable of iteration 1)
================================================================================
```

### `migrations/20260420_120000_create_products.up.sql`

```sql
CREATE TABLE products (
    id               TEXT          PRIMARY KEY,
    slug             VARCHAR(120)  NOT NULL,
    name             VARCHAR(255)  NOT NULL,
    description      TEXT          NOT NULL DEFAULT '',
    price_cents      DOUBLE PRECISION NOT NULL,
    category_id      TEXT          NOT NULL REFERENCES categories(id),
    stock_quantity   INTEGER       NOT NULL,
    is_active        BOOLEAN       NOT NULL,

    created_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ   NULL,

    CONSTRAINT products_slug_len CHECK (char_length(slug) BETWEEN 1 AND 120),
    CONSTRAINT products_name_len CHECK (char_length(name) BETWEEN 1 AND 255),
    CONSTRAINT products_price_min CHECK (price_cents >= 0),
    CONSTRAINT products_stock_min CHECK (stock_quantity >= 0)
);

CREATE UNIQUE INDEX products_slug_uidx
    ON products (slug);

CREATE INDEX products_category_id_is_active_idx
    ON products (category_id, is_active);

-- updated_at trigger
CREATE OR REPLACE FUNCTION products_set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER products_set_updated_at_trg
    BEFORE UPDATE ON products
    FOR EACH ROW EXECUTE FUNCTION products_set_updated_at();
```

### `migrations/20260420_120000_create_products.down.sql`

```sql
DROP TRIGGER IF EXISTS products_set_updated_at_trg ON products;
DROP FUNCTION IF EXISTS products_set_updated_at();
DROP INDEX IF EXISTS products_category_id_is_active_idx;
DROP INDEX IF EXISTS products_slug_uidx;
DROP TABLE IF EXISTS products;
```

Mapping rules applied:

| Authoring concept                     | SQL output                                         |
|---------------------------------------|----------------------------------------------------|
| `string` (no `max_len`)               | `TEXT NOT NULL DEFAULT ''`                         |
| `string` with `max_len: N`            | `VARCHAR(N) NOT NULL` + `CHECK (char_length >= min_len)` |
| `int32`, `int64`, `double`, `bool`    | `INTEGER`, `BIGINT`, `DOUBLE PRECISION`, `BOOLEAN` |
| `google.protobuf.Timestamp`           | `TIMESTAMPTZ`                                      |
| `(w17.field) = { pk: true }`          | `PRIMARY KEY`                                      |
| `(w17.field) = { fk: "t.c" }`         | `REFERENCES t(c)`                                  |
| `(w17.validate) = { min, max }`       | `CHECK` constraint                                 |
| `timestamps: true`                    | `created_at`, `updated_at` columns + trigger        |
| `soft_delete: true`                   | `deleted_at TIMESTAMPTZ NULL` column               |
| `indexes: [{ unique: true, fields }]` | `CREATE UNIQUE INDEX`                              |
| `indexes: [{ fields }]`               | `CREATE INDEX`                                     |

Every generated row is deterministic: same proto input always yields the same SQL
byte-for-byte. This is a hard acceptance criterion (see `docs/iteration-1.md`).

---

```
================================================================================
 STAGE 4 — ENT MODEL  (secondary deliverable, only for migration tooling)
================================================================================

 Ent is used ONLY to drive migration generation and diffing. It is not part of
 the read/write runtime path — queries (future iteration) will populate proto
 messages directly from SQL. Therefore the ent schema here is minimal and
 never edited by the developer.
================================================================================
```

### `gen/ent/schema/product.go`

```go
// Code generated by w17. DO NOT EDIT.
package schema

import (
    "entgo.io/ent"
    "entgo.io/ent/schema"
    "entgo.io/ent/schema/field"
    "entgo.io/ent/schema/index"
)

type Product struct{ ent.Schema }

func (Product) Annotations() []schema.Annotation {
    return []schema.Annotation{
        // soft-delete + timestamps handled by w17 ent extension
    }
}

func (Product) Fields() []ent.Field {
    return []ent.Field{
        field.String("id").Immutable(),
        field.String("slug").MinLen(1).MaxLen(120),
        field.String("name").MinLen(1).MaxLen(255),
        field.Text("description").Default(""),
        field.Float("price_cents").Min(0),
        field.String("category_id"),
        field.Int32("stock_quantity").Min(0),
        field.Bool("is_active"),
    }
}

func (Product) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("slug").Unique(),
        index.Fields("category_id", "is_active"),
    }
}
```

The ent schema is **write-only from the compiler's side**. It exists only so
we can lean on `ent/migrate` for diff + plan. Once iteration 2 starts (Query
DSL → SQL), no runtime code will import ent.

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
3. **`required` semantics.** Proto3 has no `required`; everything is implicitly
   optional. `(w17.validate) = { required: true }` is the only way to declare
   NOT NULL. Default if no annotation: NOT NULL or NULL? Proposal: **NOT NULL**.
4. **Default values.** Strings default to `''`, numbers to `0`, bools to `false` —
   matches proto3 zero values. `created_at` / `updated_at` get `NOW()`. Is this the
   right table of defaults, or should developer opt in via `(w17.field) = { default: ... }`?
5. **Ent boundary.** Do we use ent's full migration tooling (schema diff → Atlas → SQL),
   or do we bypass ent and write our own diff against a stored snapshot? Ent gives
   us maturity for free; bypass gives us full control and removes a heavy dependency.

These five questions are the concrete agenda for the next iteration-1 discussion.
