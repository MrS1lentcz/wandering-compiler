# Iteration 1 — M1 revision: merged vocabulary + Django-parity expansion

**Status: SPEC, not yet implemented.** This document is the implementation
contract for a rework of M1 decided on 2026-04-20. The original M1 shipped a
split `(w17.field)` + `(w17.validate)` vocabulary (see git `0526bcf`). The
rework merges them, adds a new `(w17.db.column)` extension for storage-layer
concerns, expands temporal types, and resolves iteration-1 open question #4
(defaults).

After the rework lands:
- Decisions here are folded into `docs/iteration-1.md` (D2 update + new D7).
- The pilot example in `docs/experiments/iteration-1-models.md` is updated.
- This file can be archived (kept for history) — it is not a permanent
  source of truth.

---

## Decisions recap

| Tag | Change |
|---|---|
| **A** | Merge `(w17.validate)` into `(w17.field)`. Delete `proto/w17/validate.proto`. |
| **B** | `unique: bool` on `(w17.field)` (data semantic). NEW `(w17.db.column)` extension on `FieldOptions`: `index: bool`, `name: string` (column name override). |
| **C (new D7)** | `default` as `oneof { default_string, default_int, default_double, default_auto }` on `(w17.field)`. No `default_bool` — bool default flows via `AutoDefault.TRUE/FALSE`. No `on_update` anywhere — too-magical, handlers set it explicitly (see parked mandatory-mutation-contract). |
| **D** | `(w17.db.table).indexes[]` items grow `name: string` (override) + `include: []string` (covering columns). No new extension. |
| **E** | Add SemTypes `DATE`, `TIME`, `DATETIME` on `google.protobuf.Timestamp` carrier and `INTERVAL` on `google.protobuf.Duration` carrier. IR builder enforces carrier × SemType table. |

Core rationale: field-level = **data semantics**; `w17.db.*` = **storage
choices**. Django-inspired coverage where cheap; larger features (F-based
cross-field refs, CheckConstraint, JSONB, INET, SMALLINT, partial indexes,
opclasses, GIN/GIST) stay parked or scheduled for iter-2+ — see bottom of
this doc.

---

## Target proto files

### `proto/w17/field.proto` (full rewrite)

```protobuf
syntax = "proto3";

package w17;

import "google/protobuf/descriptor.proto";

option go_package = "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17;w17pb";

// Field declares the DB / data semantics of a proto field. Previously split
// across (w17.field) + (w17.validate) — merged 2026-04-20 (the split was
// artificial: max_len appeared on both, CHECK-vs-app enforcement is a target
// concern, not a source concern). See docs/iteration-1.md D7 and
// docs/iteration-1-m1-rev.md.
//
// Usage:
//   string slug = 2 [(w17.field) = { type: SLUG, max_len: 120, unique: true }];
//   double price = 5 [(w17.field) = { type: MONEY, gte: 0 }];
//   google.protobuf.Timestamp created_at = 10
//     [(w17.field) = { type: DATETIME, default_auto: NOW }];
extend google.protobuf.FieldOptions {
  Field field = 51001;
}

message Field {
  // Semantic subtype. Required for every carrier except bool; for Timestamp
  // must be DATE / TIME / DATETIME; for Duration defaults to INTERVAL.
  Type type = 1;

  // Primary key.
  bool pk = 2;

  // Foreign key, "<table>.<column>" form. Iteration-1 supports same-file
  // references only.
  string fk = 3;

  // Annotation for future iterations; not enforced in SQL in iter-1.
  bool immutable = 4;

  // Opt-out of NOT NULL. When true the column is nullable AND the generated
  // internal proto emits the field as proto3 `optional`.
  bool null = 5;

  // String-only. When true allows empty string (default CHECK col <> '').
  // Orthogonal to null.
  bool blank = 6;

  // Data-level uniqueness. Renders as UNIQUE INDEX in SQL. Orthogonal to the
  // storage-level (w17.db.column).index flag (which is a pure optimisation).
  bool unique = 7;

  // --- Length bounds (string carriers) ---
  // max_len is required for CHAR and SLUG and drives VARCHAR(N) sizing. For
  // other string types it is an optional upper bound. Counted as Unicode
  // code points (char_length).
  int32 max_len = 8;
  optional int32 min_len = 9;

  // --- Numeric bounds ---
  // gt / lt strict, gte / lte inclusive. Can coexist (e.g. gte + lt).
  // `optional` so 0 vs unset is distinguishable.
  optional double gt  = 10;
  optional double gte = 11;
  optional double lt  = 12;
  optional double lte = 13;

  // --- Pattern override (string carriers) ---
  // Replaces the default regex implied by `type` (e.g. SLUG's `^[a-z0-9-]+$`).
  // Empty string = no override.
  string pattern = 14;

  // --- Default value at insert time ---
  // Exactly one of the branches may be set. No default_bool: bool defaults
  // go via AutoDefault.TRUE / FALSE for single-channel consistency.
  oneof default {
    string      default_string = 20;
    int64       default_int    = 21;
    double      default_double = 22;
    AutoDefault default_auto   = 23;
  }
}

// Type — semantic subtype refining the proto carrier into a SQL column type
// and a default constraint set. Authoritative carrier × SemType table is in
// docs/iteration-1.md D2 (see update in this revision).
enum Type {
  TYPE_UNSPECIFIED = 0;

  // String carriers (carrier: string)
  CHAR  = 1;
  TEXT  = 2;
  UUID  = 3;
  EMAIL = 4;
  URL   = 5;
  SLUG  = 6;

  // Numeric carriers (carrier: int32 / int64 / double — see D2 table)
  NUMBER     = 10;
  ID         = 11;
  COUNTER    = 12;
  MONEY      = 13;
  PERCENTAGE = 14;
  RATIO      = 15;

  // Temporal (carrier: google.protobuf.Timestamp)
  DATE     = 20;
  TIME     = 21;
  DATETIME = 22;

  // Duration (carrier: google.protobuf.Duration)
  INTERVAL = 30;
}

// AutoDefault — dynamically resolved default value. Emitters render each
// variant per dialect. IR builder rejects invalid carrier × AutoDefault
// combinations (see D7 table).
enum AutoDefault {
  AUTO_DEFAULT_UNSPECIFIED = 0;

  // Temporal — resolved per Type:
  //   DATE     → CURRENT_DATE
  //   TIME     → CURRENT_TIME
  //   DATETIME → NOW() / CURRENT_TIMESTAMP
  NOW = 1;

  // UUID generation — valid on string carrier with type UUID.
  UUID_V4 = 10;
  UUID_V7 = 11;

  // JSON literals — valid on string carrier (stored as '[]' / '{}') and on
  // JSONB carrier when JSONB lands (iter-2+). Reserved now for shape
  // stability.
  EMPTY_JSON_ARRAY  = 20;
  EMPTY_JSON_OBJECT = 21;

  // Bool — the only way to set a bool default.
  TRUE  = 30;
  FALSE = 31;
}
```

### `proto/w17/db.proto` (extended)

```protobuf
syntax = "proto3";

package w17.db;

import "google/protobuf/descriptor.proto";

option go_package = "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/db;dbpb";

// (w17.db.table) — message-level DB configuration.
//
// Usage:
//   message Product {
//     option (w17.db.table) = {
//       name: "products",
//       indexes: [
//         { fields: ["category_id", "is_active"] }
//       ]
//     };
//     ...
//   }
extend google.protobuf.MessageOptions {
  Table table = 51000;
}

// (w17.db.column) — field-level STORAGE overrides. Data semantics
// (type, constraints, uniqueness, defaults) live on (w17.field).
// (w17.db.column) is reserved for choices that don't affect the data
// contract: storage indexes, column-name overrides for legacy adoption,
// future tablespace / storage parameters.
//
// Usage:
//   string email = 3 [
//     (w17.field)     = { type: EMAIL, unique: true },
//     (w17.db.column) = { index: true, name: "email_address" }
//   ];
extend google.protobuf.FieldOptions {
  Column column = 51002;
}

message Table {
  string name = 1;
  repeated Index indexes = 2;
}

message Index {
  repeated string fields = 1;
  bool unique = 2;

  // Optional name override. Default: auto-derived from table + fields.
  string name = 3;

  // Postgres INCLUDE — covering-index columns. Emitters that don't support
  // INCLUDE must error if non-empty.
  repeated string include = 4;
}

message Column {
  // Single-field non-unique storage index. Sugar for a single-col entry in
  // (w17.db.table).indexes. Does NOT imply UNIQUE — use (w17.field).unique
  // for uniqueness (data semantic, always represented as a UNIQUE INDEX).
  bool index = 1;

  // SQL column name override. Default = proto field name 1:1. Rare — mostly
  // for adopting existing schemas with non-proto naming conventions.
  string name = 2;
}
```

### `proto/w17/validate.proto`

**DELETE.** All its options are now on `(w17.field)`.

---

## Validation tables (M2 concern, documented here)

### Carrier × Type

| Carrier | Type must be |
|---|---|
| `bool` | `TYPE_UNSPECIFIED` (SemType forbidden) |
| `string` | one of `CHAR, TEXT, UUID, EMAIL, URL, SLUG` — required |
| `int32` | one of `NUMBER, ID, COUNTER` — required |
| `int64` | one of `NUMBER, ID, COUNTER` — required |
| `double` | one of `NUMBER, MONEY, PERCENTAGE, RATIO` — required |
| `google.protobuf.Timestamp` | one of `DATE, TIME, DATETIME` — required |
| `google.protobuf.Duration` | `INTERVAL` — unspecified permitted (infer INTERVAL) |

Additional field-level rules:

- `max_len` required iff `type ∈ {CHAR, SLUG}`; forbidden otherwise.
- `min_len`, `pattern`, `blank` valid only for string carrier.
- `gt`, `gte`, `lt`, `lte` valid only for numeric carrier.
- `pk` implies `unique` implicitly (no need to set both).
- `fk` is `"<table>.<column>"`; same-file target required in iter-1.

### Type × AutoDefault

| AutoDefault | Valid on |
|---|---|
| `NOW` | Timestamp carrier (type = DATE / TIME / DATETIME) |
| `UUID_V4`, `UUID_V7` | string carrier + type = UUID |
| `EMPTY_JSON_ARRAY`, `EMPTY_JSON_OBJECT` | string carrier + type = TEXT (CHAR permitted if max_len fits) |
| `TRUE`, `FALSE` | bool carrier |

Literal branch validity:

- `default_string` — string carrier only.
- `default_int` — int32 / int64 carriers.
- `default_double` — double carrier.

---

## File-by-file action list

**Create:**

- `docs/experiments/_parked/mandatory-mutation-contract.md` — parked
  experiment (see full text below, "Parked experiment: mandatory mutation
  contract").

**Edit:**

1. `proto/w17/field.proto` — replace whole file with the spec above.
2. `proto/w17/db.proto` — add `Column` message, add second `extend
   google.protobuf.FieldOptions` block, add `name` + `include` on `Index`.
3. `docs/iteration-1.md` — update D2 (Type enum expanded, validation table
   inline); add D7 (defaults decision, AutoDefault enum, oneof shape); note
   D2 supersedes the earlier string/number-only enum.
4. `docs/experiments/iteration-1-models.md` — update the Product pilot
   example to exercise the new vocabulary (see full example below, "Updated
   pilot example"). Update Stage-4 IR shape section to reflect the merged
   `Column` struct with `Default` tagged union.
5. `docs/iteration-1-impl.md` — update M1 milestone to reference merged
   vocabulary; update status line; keep M1 counting as served by the rev.
6. `srcgo/domains/compiler/loader/testdata/vocab_fixture.proto` — add cases
   for: `unique`, `(w17.db.column)`, `default_auto: NOW` on DATETIME,
   `default_auto: UUID_V4` on UUID, `default_string`, `Index.name` +
   `Index.include`.
7. `srcgo/domains/compiler/loader/vocab_test.go` — add assertions matching
   the fixture; drop the `(w17.validate)` path.
8. `Makefile` — remove `proto/w17/validate.proto` from the `schemagen` arg
   list.

**Delete:**

- `proto/w17/validate.proto` — obsolete after A.
- Generated `srcgo/pb/w17/validate.pb.go` gets removed on next `make
  schemagen` run (gitignored; no git action needed).

**Regenerate + verify:**

```
make schemagen
go test ./domains/compiler/loader/...    # from srcgo/
go vet ./...                             # from srcgo/
```

Expected result: vocab test green, build clean, no `validate.pb.go` in
`srcgo/pb/w17/`.

---

## Updated pilot example (for iteration-1-models.md Stage 1)

```protobuf
syntax = "proto3";
package w17.catalog.products;

import "google/protobuf/timestamp.proto";
import "w17/db.proto";
import "w17/field.proto";

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
  double discount_rate   = 6 [(w17.field) = { type: RATIO, null: true }];
  string category_id     = 7 [
    (w17.field)     = { type: UUID, fk: "categories.id" },
    (w17.db.column) = { index: true }
  ];
  int64  stock_quantity  = 8 [(w17.field) = { type: COUNTER }];
  bool   is_active       = 9 [(w17.field) = { default_auto: TRUE }];

  // created_at: DB default NOW() at insert. updated_at: DB default NOW() at
  // insert only. Handlers mutating the row MUST set updated_at explicitly —
  // the compiler does not auto-update it. The future mandatory-mutation
  // contract (parked experiment) will verify all write RPCs touch it.
  google.protobuf.Timestamp created_at = 10 [(w17.field) = { type: DATETIME, default_auto: NOW, immutable: true }];
  google.protobuf.Timestamp updated_at = 11 [(w17.field) = { type: DATETIME, default_auto: NOW }];
}
```

The SQL example (Stage 3) needs re-rendering to match — single-field UNIQUE
index for `slug` (was via multi-index), storage `category_id` index (from
`db.column.index`), UUID-generation default on `id`, NOW() defaults on
created_at / updated_at. Left as implementation task.

---

## Parked experiment: mandatory mutation contract

**File:** `docs/experiments/_parked/mandatory-mutation-contract.md`
**Content (create verbatim):**

```markdown
# Parked experiment — Mandatory mutation contract

Parked 2026-04-20. Requires the query DSL (iter-2+ minimum); write this
up as a design sketch so the idea doesn't evaporate.

## Motivation

Auto-update side-effects (Django `auto_now=True`, DB `ON UPDATE NOW()`
triggers, ORM `save()` hooks) are cheap to add and expensive to escape.
Archive copies, backfill jobs, admin corrections, data migrations —
every one of those is a case where the developer explicitly does not
want the "magic" to fire, and every one of them fights the framework
instead of cooperating with it.

wandering-compiler's generated storage layer has a property Django does
not: every mutation to every table is a declared storage RPC. We know
statically which methods mutate which fields. That means we can move
from "automatic-with-escape" to "explicit-with-validation":

1. Schema declares a mutation contract per table — "every write RPC must
   update these fields".
2. Compiler walks every storage RPC's DQL (query DSL) body.
3. Any RPC that writes the table but misses a contracted field → build
   error with the RPC location.
4. RPCs that legitimately skip the update declare an explicit exemption
   on themselves; the exemption is visible at the call site.

This buys us (a) no silent side-effects, (b) a static audit of "which
write paths keep updated_at honest" visible in the platform UI, (c) a
cheap hook for domain-specific invariants beyond timestamps: optimistic
version counters, audit trails, row-level cache epochs, soft-delete
timestamps.

## Sketch shape

On the table annotation:

    option (w17.db.table) = {
      name: "articles"
      update_contract: [
        { field: "updated_at", on: ANY_WRITE },
        { field: "version",    on: ANY_WRITE, mode: INCREMENT }
      ]
    };

On a storage RPC that legitimately skips:

    rpc ArchiveArticle(ArchiveArticleRequest) returns (ArchiveArticleResponse) {
      option (w17.rpc) = {
        update_contract_exempt: ["articles.updated_at", "articles.version"]
      };
    }

## Open questions (resolve in the iteration that builds this)

- Exemption scope: per-field or whole-table? Start per-field.
- Granularity of "write": any UPDATE, or any "real" mutation (ignoring
  idempotent no-ops)? Probably any UPDATE — simpler and no one writes
  a no-op update on purpose.
- Exemption visibility: surface in platform UI as a list of "dangerous"
  RPCs so reviewers see them.
- Cross-table writes (`UPDATE joins`): probably future; start with
  single-table UPDATE/DELETE.

## Not this iteration

Iter-1 has no query DSL, so there is nothing to check. This experiment
lands when we have storage RPCs to analyse — iter-2 at the earliest.

No work starts here until an actual project needs it; it is not a gating
item for any iteration.
```

---

## Commit strategy

Single atomic commit. The rework touches proto, generated code, docs,
tests — partial intermediate states don't make sense (`make schemagen`
would fail between steps). Suggested commit title:

```
M1 rev2: merge vocabulary, add (w17.db.column), expand types + defaults
```

Body: reference this doc (`docs/iteration-1-m1-rev.md`) and the iter-1.md
D7 addition. Note that `(w17.validate)` is gone and `(w17.db.column)` is
new.

---

## What is NOT in this rev (parked / scheduled)

Do not expand scope during implementation. These items are deliberately
deferred:

- `DECIMAL` with generic `precision` / `scale` (MONEY/PERCENTAGE/RATIO
  remain as fixed-shape sugar).
- `SMALLINT` / int16 carrier (needs proto carrier discussion).
- `INET`, JSONB, BINARY carriers (JSONB + BINARY already parked in
  iter-1 "Out of scope").
- `choices: [string]` → CHECK IN (...) — useful, low-risk, but out of
  this rev to keep scope tight. Schedule for iter-2 unless a pilot
  needs it first.
- Index method (btree/gin/gist/brin), opclasses, partial `condition`,
  tablespace.
- Model-level `constraints = [CheckConstraint(...), UniqueConstraint(
  fields, condition)]` — needs F-based expressions, out of iter-1.
- F-based cross-field expressions — future experiment doc, not now.
- Mandatory mutation contract — parked experiment (created by this
  rev).
- Default values referring to other fields (needs F-based).

If a pilot surfaces a real need for any of the above, add it as a new
iteration milestone; do not sneak it into this rev.
