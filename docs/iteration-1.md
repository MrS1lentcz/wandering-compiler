# Iteration 1 — Model Layer

Contract for the first iteration of wandering-compiler. Scope-reduction
document: what is in, what is explicitly out, and how we know the iteration
is done.

This iteration corresponds to **Phase 1** in `docs/tech-spec.md`. Later phases
are deliberately deferred; their open questions live in the parked experiment
at `docs/experiments/_parked/schema-projections.md`.

## Goal

A developer writes **one** `.proto` file describing a single DB model. The
compiler produces:

1. An internal (clean) proto under `gen/proto/` — the same field set as the
   authoring proto, stripped of `w17.*` annotations (with `null: true` fields
   emitted as proto3 `optional`).
2. A pair of plain-SQL migration files (`*.up.sql` + `*.down.sql`) written to
   `out/migrations/` (gitignored — migrations are **not** checked into the
   user's repo; see D6). Apply cleanly to Postgres 14+.

Internally the compiler builds a dialect-agnostic IR (see Stage 4 in
`docs/experiments/iteration-1-models.md`). The SQL emitter is pluggable per
dialect; iteration-1 ships only the Postgres emitter, but the architecture
must leave room for MySQL / SQLite / … later — see D4 below.

The long-term delivery model (hosted migration platform + deploy client,
migrations-as-artifacts with history/approval/audit) is parked in
`docs/experiments/_parked/migration-delivery.md`. Iteration-1 does not build
any of it — it just produces the SQL that model will eventually deliver.

The pilot example is shown in `docs/experiments/iteration-1-models.md`.

## In scope

- **Proto carriers:** `string`, `int32`, `int64`, `bool`, `double`, `google.protobuf.Timestamp`, `google.protobuf.Duration`.
- **Semantic `type` enum (in `(w17.field)`):**
  - Strings: `CHAR`, `TEXT`, `UUID`, `EMAIL`, `URL`, `SLUG`.
  - Numbers: `NUMBER`, `ID`, `COUNTER`, `MONEY`, `PERCENTAGE` (0–100), `RATIO` (0–1).
  - Arbitrary-precision decimal: `DECIMAL` (carrier: `string` — lossless wire; requires `precision`, optional `scale`).
  - Temporal: `DATE`, `TIME`, `DATETIME` on Timestamp carrier; `INTERVAL` on Duration carrier.
- **`(w17.db.table)` options:** `name`, `indexes` (single or multi-column, unique or not; with optional `name` override and `include` covering columns). No auto-generated fields — every DB column is a declared proto field.
- **`(w17.field)` options (merged vocabulary):** `type` (required for every carrier except bool), `pk`, `fk`, `orphanable` (FK survives parent delete — see D8), `immutable`, `null` (default `false` → NOT NULL + required), `blank` (string-only, default `false` → `CHECK (col <> '')`), `unique` (data-level uniqueness → UNIQUE INDEX), `max_len` / `min_len` (string carriers), `gt` / `gte` / `lt` / `lte` (numeric carriers), `pattern` (string carriers, regex override), `choices` (FQN of a proto enum — CHECK IN (…) — see D8), `precision` / `scale` (DECIMAL), and a `default` oneof — see D7.
- **`(w17.db.column)` options (field-level storage overrides):** `index` (single-field non-unique storage index), `name` (SQL column-name override). Orthogonal to `(w17.field).unique`: `unique` is a data semantic, `index` is a pure optimisation.
- **`(w17.pg.field)` options (Postgres dialect namespace — see D9):** `jsonb`, `inet`, `tsvector`, `hstore`, plus the `custom_type` + `required_extensions` escape hatch for dialect extensions the vocabulary doesn't cover yet (pgvector, PostGIS, custom DOMAINs).
- **Output layer:** Postgres 14+ SQL via the PG dialect emitter. Tested against a real Postgres instance (SQLite acceptable for local dev loops only). The emitter sits behind a dialect interface — additional dialects (MySQL, SQLite-as-production, …) are additive, not disruptive.
- **Intermediate representation:** own dialect-agnostic IR (`Schema` / `Table` / `Column` / `Check` as tagged union / `Index` / `ForeignKey`) + trivial differ (`nil → Schema` yields `AddTable` ops). See D4 and Stage 4 in `iteration-1-models.md`.
- **Determinism:** same input always produces byte-identical output.

## Out of scope (deferred)

- Projections (`w17.schema.projection`) — moved to a later iteration.
- Query DSL, storage gRPC, standard gRPC, facade APIs, events.
- Rich types: `bytes`, `jsonb`, `repeated`, `oneof`, nested messages.
- Auto-generated timestamp/soft-delete columns. Every DB column must be a declared proto field; timestamp ergonomics (`default: NOW`, `on_update: NOW`) are parked as a follow-up — see Open questions.
- Cross-module FKs resolved via package paths (iteration 1 uses plain
  `"<table>.<column>"` strings).
- Cross-domain references via `common/`.
- `immutable` runtime enforcement (the annotation is recorded for future
  iterations but not enforced in SQL).
- Alter/diff operations beyond `AddTable`. Iteration-1 only supports the initial migration (previous schema = empty); `DropColumn`, `AlterColumn`, `RenameColumn`, `AlterIndex`, cross-table FK cycles, and backfill planning are deferred to iteration-2+ and added when a pilot actually needs them.
- Back-compat lint, schema visualization, and changelog generation. The IR is shaped to carry them, but iteration-1 ships only the SQL emitter.
- Additional dialects (MySQL, SQLite-as-production, MS SQL, …). The dialect interface exists from day one; additional emitters are later-iteration work.
- **Hosted migration platform** (migration storage, approval workflow, audit trail, review UI) and the **deploy client** that pulls migrations at apply time. Iteration-1 outputs SQL to `out/` locally; the platform & client are parked in `docs/experiments/_parked/migration-delivery.md`.
- Applied-state tracking on the target DB (e.g. `wc_schema_history` table). Iteration-1 migrations are applied by hand via `psql -f`.
- UI metadata, admin generation, JS/TS clients, docker/k8s scaffolding.

## Acceptance criteria

1. `wc generate --iteration-1 path/to/product.proto` emits `gen/proto/` and
   `out/migrations/0001_create_products.{up,down}.sql`. `out/` is gitignored
   — generated SQL is not a source artifact (D6). No `gen/ent/` — ent/Atlas
   are out (D4).
2. The generated migration applies cleanly against a fresh Postgres 14
   instance via `psql -f`.
3. The generated migration rolls back cleanly via the `.down.sql`.
4. Running the generator twice on unchanged input produces **byte-identical**
   output files (no timestamps or nondeterministic IDs in the content).
5. A golden-file test suite covers the pilot `product.proto` and at least two
   additional shapes: a table with no indexes, and a table with multi-column
   unique constraints.
6. The SQL emitter is invoked through a `DialectEmitter` interface. A stub
   second dialect (even one that panics with "not implemented") exists in the
   codebase to prove the interface is real and not Postgres-shaped by accident.
7. One pilot project (chosen from `docs/conventions-global/`) replaces its
   hand-written migration for one table with the generated one, without any
   behavioral regression. For iteration-1 the pilot applies the generated
   SQL manually (the platform & deploy client arrive later — see
   `_parked/migration-delivery.md`). The pilot's hand-written `migrations/`
   folder goes away once the generator covers all its tables.

## Deliverable

- Generator binary `wc` capable of the above.
- `docs/experiments/iteration-1-models.md` updated with whatever shape
  decisions were finalized during implementation.
- Short migration guide: how an existing project adopts iteration-1 output
  for a single table.

## Open questions to resolve during the iteration

These five questions are blockers and must be answered before the iteration
can close. Full detail in
`docs/experiments/iteration-1-models.md` under "Open questions".

1. ~~Migration naming scheme~~ — **resolved**, see Decisions below (D5).
2. Validation surface in SQL — **not whether**, but **how it's configured**.
   Generator flag (`--check-constraints=full|length-only|off`) lets the project
   trade DB-side enforcement for write throughput. Default value is open.
3. ~~Default nullability~~ — **resolved**, see Decisions below (D1).
4. ~~Default values & timestamp ergonomics~~ — **resolved**, see Decisions
   below (D7). Field-level `default` is a oneof of explicit literals
   (`default_string` / `default_int` / `default_double`) plus
   `default_auto: AutoDefault` for dynamically resolved values
   (`NOW`, `UUID_V4`, `UUID_V7`, `TRUE`, `FALSE`, `EMPTY_JSON_*`).
   No `on_update` — handlers set it explicitly (future enforcement via the
   parked mandatory-mutation-contract experiment).
5. ~~Ent boundary~~ — **resolved**, see Decisions below (D4).

## What "done" looks like

The iteration closes when:

- All seven acceptance criteria pass in CI.
- The pilot project has been migrated and its maintainers have signed off.
- All five original open questions have written answers. #1, #3, #4, and #5
  are resolved (D5, D1, D7, D4). #2 remains — minor-dimension tuning (CHECK
  default verbosity) that can close once a pilot exercises it.

## Decisions

### D1 — Default nullability (resolves open question #3)

**Decision.** Every field is `NOT NULL` + required by default. The opt-out is
`(w17.field) = { null: true }`, which:

1. drops `NOT NULL` on the SQL column, and
2. emits the field as proto3 `optional` in the internal proto (so field
   presence survives the wire format — without `optional`, a scalar's zero
   value and "not set" would be indistinguishable and the nullable-column
   signal would be lost).

`required` is not part of `(w17.field)` — it would be redundant with
`null`. (Historically considered on the now-removed `(w17.validate)` as well.)

**Rationale.** `null` already answers both questions that a `required` flag
would have answered: whether the DB column is nullable, and whether the
validator rejects missing input. Keeping them as one knob keeps the authoring
surface small and eliminates the `null=false, required=false` combination,
which had no coherent meaning.

**String-specific companion:** `blank: true` (string fields only) allows
empty strings through. `null` and `blank` are orthogonal — `null` is about
"may the value be absent", `blank` is about "if the value is present, may
it be `''`". Default for both is `false`.

### D2 — Semantic type enum (supersedes earlier ad-hoc string/number handling; expanded by M1 rev2, 2026-04-20)

**Decision.** `(w17.field)` carries a `type` enum that refines the proto
carrier into a SQL column and a default set of CHECK constraints. `type`
is required for every carrier except `bool` (bool has no subtype).

- **String carriers** (`string`): `CHAR`, `TEXT`, `UUID`, `EMAIL`, `URL`, `SLUG`.
- **Number carriers** (`int32` / `int64` / `double`): `NUMBER`, `ID`, `COUNTER`, `MONEY`, `PERCENTAGE` (0–100 "human" scale), `RATIO` (0–1 mathematical fraction).
- **Temporal carriers**:
  - `google.protobuf.Timestamp` → required one of `DATE`, `TIME`, `DATETIME`.
  - `google.protobuf.Duration` → `INTERVAL` (unspecified is permitted and inferred).
- **`bool`** carrier has no semantic subtype.

The mapping to SQL and the implicit CHECK constraints are tabulated in
`docs/experiments/iteration-1-models.md`. Everything else that used to live
on `(w17.validate)` (`min_len`, `max_len`, `gt`, `gte`, `lt`, `lte`,
`pattern`) is now part of `(w17.field)` directly — the validate/field split
was dropped in M1 rev2 (see `docs/iteration-1-m1-rev.md`).

**Carrier × Type (authoritative):**

| Carrier | `type` must be |
|---|---|
| `bool` | `TYPE_UNSPECIFIED` (subtype forbidden) |
| `string` | one of `CHAR, TEXT, UUID, EMAIL, URL, SLUG` — required |
| `int32` | one of `NUMBER, ID, COUNTER` — required |
| `int64` | one of `NUMBER, ID, COUNTER` — required |
| `double` | one of `NUMBER, MONEY, PERCENTAGE, RATIO` — required |
| `string` (DECIMAL sub-case) | `DECIMAL` (requires `precision`; `scale` optional, default 0) |
| `google.protobuf.Timestamp` | one of `DATE, TIME, DATETIME` — required |
| `google.protobuf.Duration` | `INTERVAL` — unspecified permitted (infer) |

Additional field-level rules enforced by the IR builder (M2):

- `max_len` required iff `type ∈ {CHAR, SLUG}`; forbidden otherwise.
- `min_len`, `pattern`, `blank`, `choices` valid only for string carrier.
- `gt`, `gte`, `lt`, `lte` valid only for numeric carrier (including DECIMAL — bounds are carried via double and are precision-limited by `double`'s range; acceptable for practical validation, not for arbitrary-scale decimals).
- `precision` / `scale` valid only for `type: DECIMAL`; `precision > 0`, `0 <= scale <= precision`.
- `pk` implies `unique` implicitly (no need to set both).
- `fk` is `"<table>.<column>"`; same-file target required in iter-1.
- `orphanable` valid only when `fk` is set. `orphanable: true` requires `null: true` (can't SET NULL a NOT NULL column).
- `choices` is the FQN of a proto enum reachable from this file (cross-file permitted); the carrier must be `string` and `type` is typically `CHAR` (with `max_len` large enough for the longest enum value name) or `TEXT`.

**Rationale.** Django-style: data refinement and basic validation in one
annotation, human-readable. The separate `(w17.validate)` extension was
removed because the split was artificial (`max_len` appeared on both sides;
CHECK-in-DB vs. app-layer enforcement is a target-rendering concern, not a
source-vocabulary concern). Everything is now on `(w17.field)`; the
storage-only flags (`index`, `name` override) live on the new
`(w17.db.column)`.

### D3 — No compiler-generated fields (supersedes `timestamps` / `soft_delete` table options)

**Decision.** `(w17.db.table)` no longer carries `timestamps` or
`soft_delete` options. Every DB column corresponds to an explicitly declared
proto field. Soft-delete is an application-level concern (archive tables,
tombstone flags, audit schemas are all legitimate shapes) and the compiler
does not pick one for the developer. Timestamp fields, if desired, are
declared as ordinary `google.protobuf.Timestamp` fields.

**Rationale.** The value of the compiler lives in the DSL → SQL mapping, not
in injecting hidden columns. Hidden columns make the proto a lie — the
generated internal proto differs from the authored one — and they bake one
particular soft-delete pattern into every table. Explicit declarations keep
the proto honest and the developer in control. The follow-up open question
(#4) is whether to add field-level `default` / `on_update` annotations so
explicit timestamp fields are ergonomic to author.

### D4 — Own IR + differ + per-dialect emitters (resolves open question #5; revised 2026-04-21)

**Decision.** The compiler owns its whole migration pipeline. Ent and Atlas
are **not used**. The pipeline has four layers:

1. **IR:** dialect-agnostic **proto messages** at
   `proto/domains/compiler/types/ir.proto` (private/internal,
   `go_package = ".../srcgo/pb/domains/compiler/types;irpb"`) —
   `Schema` / `Table` / `Column` / `Index` / `ForeignKey` /
   `PgOptions` / `SourceLocation`. Tagged unions are proto `oneof`:
   `Check { oneof variant { LengthCheck … | BlankCheck … | RangeCheck …
   | RegexCheck … | ChoicesCheck … } }` and `Default { oneof variant {
   AutoDefault … | LiteralString … | LiteralInt … | LiteralDouble … }
   }`. Dialect-independent enums (`Carrier`, `SemType`, `FKAction`,
   `AutoKind`) are proto enums. Checks carry semantic intent, not SQL
   strings — the emitter renders them per dialect. `SourceLocation`
   replaces live `protoreflect.FieldDescriptor` storage (descriptors
   aren't serializable; the IR is). See tech-spec strategic decision
   #8 for the cross-cutting "proto, not Go structs" rule.
2. **Differ:** `Diff(prev, curr *irpb.Schema) *planpb.MigrationPlan` →
   ordered `Op`s (`AddTable`, later `DropTable`, `AddColumn`,
   `AlterColumn`, …), also proto (`proto/domains/compiler/types/plan.proto`).
   Iteration-1 handles only `nil → Schema` (initial migration), which
   reduces to one `AddTable` per table. Alter/rename/type-change ops
   are added iteration-by-iteration as pilot projects surface real
   needs.
3. **SQL emitter:** per-dialect, behind a `DialectEmitter` Go interface.
   Emitters consume `*irpb.Schema` + `*planpb.MigrationPlan`; helpers
   are free Go functions over the generated proto types. Iteration-1
   ships only the Postgres emitter. Adding MySQL / SQLite later is a
   new file, not a rearchitecture.
4. **Sibling consumers of the plan (later iterations):** back-compat
   lint (e.g., "AddColumn NOT NULL without default on non-empty
   table"), changelog generator, schema visualization, platform UI.
   All four operate on `MigrationPlan.Ops`, so they are
   dialect-agnostic for free — and because the plan is proto, any of
   them can live in a different process / language without re-serialising.

> **Why proto IR, not Go structs (rev 2026-04-21):** The pre-rev shape
> was hand-rolled Go (`srcgo/domains/compiler/ir/{schema,checks,types}.go`).
> Most fields turned out to be 1:1 mirrors of the authoring proto
> (`Nullable`, `PK`, `MaxLen`, …). More importantly, the compiler is
> itself a gRPC-addressable domain (see project memory "compiler is a
> domain"): visual editor, platform UI, future import-from-DB, and
> sibling consumers (lint, changelog) all need to read/emit IR. Proto
> gives them wire-compat in any language and cleaner tagged unions than
> Go interfaces. See tech-spec strategic decision #8.

**Rationale.**

1. **We would need an IR anyway.** Back-compat lint, visualization, and
   changelogs cannot be produced by parsing emitted SQL back into meaning —
   the semantic information is lost by the time SQL is rendered. Owning the
   IR means every downstream tool operates on structured data from the start.

2. **Django-style dialect portability.** The project does not want to lock
   itself to Postgres. Atlas abstracts schema structure across dialects but
   its `check { expr = "..." }` blocks are SQL strings, re-binding the
   schema to one dialect. Our kind-tagged `Check` union renders to
   `char_length(col) <= N` on PG and `CHAR_LENGTH(col) <= N` on MySQL from
   the same IR node.

3. **Iteration-1 does not pay for alter-diff complexity.** The hard parts of
   ent/Atlas (rename vs drop+add, type-change planning, backfill) are real
   — but iteration-1 only needs `AddTable`. We pay for complexity when the
   pilot needs it, not upfront.

4. **One fewer external dependency.** Ent pulls in a Go module + code
   generator; Atlas is a separate binary / service. Our users see one
   tool (`wc`) and one set of generated files.

**Cost accepted.** Diffing non-trivial schema changes correctly is
historically painful (column renames, FK cycles, nullability transitions with
backfill). We pay this cost iteration by iteration instead of outsourcing it
to ent/Atlas. Acceptance criterion #6 exists to enforce that the dialect
interface is real: a stub second emitter must compile against the same
`DialectEmitter` contract as the Postgres one, catching PG-shaped leaks
while iteration-1 is still small.

### D5 — Migration naming: `<NNNN>_<slug>.sql` (resolves open question #1)

**Decision.** Migration filenames are 4-digit zero-padded sequence + `_` +
slug derived from the ops, e.g. `0001_create_products.up.sql` and
`0001_create_products.down.sql`. No timestamp. Slug is generated from the
`MigrationPlan.Ops`:

- Single `AddTable{products}` → `create_products`.
- Single `AddIndex{idx_products_slug}` → `add_index_products_slug`.
- Multi-op migration → concatenate first two op slugs, truncated; operator
  can override via a PR-level metadata hook (arrives with the platform in
  `_parked/migration-delivery.md`).

**Rationale.**

1. **Lex-sort = numeric sort.** Zero-padding keeps `ls`, glob `*.sql`,
   `psql -f`, and UI listings ordered without a custom sorter.
2. **No timestamp because generation is atomic.** Iteration-1 generates
   migrations as a single-writer event per merge; with the platform
   (component 2 in `_parked/migration-delivery.md`), generation is
   centralized and collision-free. Timestamp carries no information that
   git commit metadata or platform audit trail don't already have.
3. **4 digits is enough.** 9999 migrations at 100/year is 100 years. Projects
   that approach the limit squash old migrations into a consolidated snapshot
   (same pattern Django / Rails / Alembic use); the ceiling is hit far after
   other cleanup concerns bite.
4. **Slug costs nothing, helps review.** Even when migrations don't live in
   git, a human-readable filename makes platform UI listings, log lines, and
   error messages legible at a glance.

### D6 — Migrations are platform artifacts, not source-committed files

**Decision.** The compiler writes SQL to `out/migrations/` (gitignored). The
user's git repository contains only the authoring proto. Generated SQL is
never checked in. The long-term delivery path is the hosted migration
platform (parked in `docs/experiments/_parked/migration-delivery.md`).

**What iteration-1 does:** writes SQL to `out/` locally; the pilot project
applies it manually via `psql -f` for the duration of the pilot.

**What the platform will do (later phase):** ingest the emitted SQL +
`MigrationPlan` IR, attach history / approval metadata / audit trail,
expose a review UI, and serve migrations to the deploy client at apply time.

**Rationale.**

1. **PR review focus.** Reviewers should read the proto change, not 800
   lines of mechanically generated SQL that ship downstream of it.
2. **Audit belongs outside git.** "Who approved migration 0042 for prod on
   2026-05-17" is an authorization event with tenant / environment / approver
   metadata. Git is the wrong system of record.
3. **Single source of truth.** Proto defines the schema; SQL is a derivative.
   Storing the derivative alongside the source invites drift.
4. **Terraform analogy.** Declarative state → computed plan → review in UI
   → apply. The model is deliberate.

**Caveat.** Until the platform ships, iteration-1 and its pilots operate
without audit trail, approval workflow, or applied-state tracking. These
gaps are known and expected; they are not iteration-1 problems.

### D7 — Field-level defaults (resolves open question #4; added by M1 rev2, 2026-04-20)

**Decision.** `(w17.field)` carries a `default` oneof:

    oneof default {
      string      default_string = 20;
      int64       default_int    = 21;
      double      default_double = 22;
      AutoDefault default_auto   = 23;
    }

Explicit-literal branches (`default_string`, `default_int`, `default_double`)
are emitted as SQL `DEFAULT <literal>`. `default_auto: AutoDefault` covers
dynamically resolved values that the per-dialect emitter renders:

    enum AutoDefault {
      AUTO_DEFAULT_UNSPECIFIED = 0;
      NOW               = 1;    // temporal: CURRENT_DATE / CURRENT_TIME / NOW()
      UUID_V4           = 10;   // string + type UUID
      UUID_V7           = 11;   // string + type UUID
      EMPTY_JSON_ARRAY  = 20;   // string (and future JSONB) literal '[]'
      EMPTY_JSON_OBJECT = 21;   // string (and future JSONB) literal '{}'
      TRUE              = 30;   // bool carrier
      FALSE             = 31;   // bool carrier
    }

No `default_bool` — bool defaults flow through `AutoDefault.TRUE / FALSE`
for single-channel consistency. No `on_update` anywhere — auto-update
side-effects are too magical to escape once baked in (archive copies,
backfill jobs, admin corrections all fight the framework). Handlers that
mutate a row must set `updated_at` (or equivalent) explicitly; future
enforcement will come from the parked mandatory-mutation-contract
experiment (`docs/experiments/_parked/mandatory-mutation-contract.md`).

**Type × AutoDefault compatibility (IR builder enforces):**

| AutoDefault | Valid on |
|---|---|
| `NOW` | Timestamp carrier (type = DATE / TIME / DATETIME) |
| `UUID_V4`, `UUID_V7` | string carrier + type = UUID |
| `EMPTY_JSON_ARRAY`, `EMPTY_JSON_OBJECT` | string carrier + type = TEXT (CHAR permitted if `max_len` fits); also valid on `(w17.pg.field).jsonb` columns |
| `TRUE`, `FALSE` | bool carrier |
| `IDENTITY` | int32 / int64 carrier + type = ID + pk = true (auto-increment PK) |

Literal-branch carrier compatibility:

- `default_string` — string carrier only.
- `default_int` — int32 / int64 carriers.
- `default_double` — double carrier.

**Rationale.** Django's `default=` + `auto_now_add=True` / `auto_now=True`
is two design choices in one: a *data* decision (what default value) and a
*behaviour* decision (who re-writes the field on every update). Merging
them into one `default` knob broke escape — there was no way to say "use
the default for inserts but let my handler manage updates" without
disabling the default entirely. Splitting `default` (data) from the
parked mandatory-mutation-contract (behaviour) keeps each concern
single-purpose and gives us a statically-audited, per-RPC opt-out for
mutation-side-effects when we build that layer.

`IDENTITY` lives in `AutoDefault` rather than as a new `Type` variant
(`AUTO_ID`) because auto-increment *is* a default-value concern — the
column type is plain `INTEGER` / `BIGINT`, what differs is who supplies
the value at insert. Keeping it in the `default` channel avoids
redundancy (no need to write `pk: true` AND `type: AUTO_ID` — the
carrier + `type: ID` + `pk: true` + `default_auto: IDENTITY` combination
reads top-down like every other default rule).

### D8 — FK orphan behaviour + enumerated choices (added by M1 rev3, 2026-04-20)

**Decision.** Two compact additions to `(w17.field)` that fill the most
common Django-parity gaps without importing its full vocabulary.

**`orphanable: optional bool`** — a *property* of the child row answering
"can it outlive the parent it references?" Only meaningful when `fk` is
set.

- `true`  → yes. Parent delete leaves this row with FK column `SET NULL`. Requires `null: true`.
- `false` → no. Parent delete `CASCADE`-removes this row.
- unset → inferred from `null`: `null: true` → `true`; `null: false` → `false`.

The richer Django vocabulary (`PROTECT`, `RESTRICT`, `SET_DEFAULT`,
`DO_NOTHING`) is explicitly **not** in the schema. "Block a delete
because the child would go stale" is an *application invariant*, not a
data-contract property — it belongs in the platform's static-analysis /
UI layer, where the operator sees the graph of impacted rows before they
click "delete". Baking it into the schema forces every call site to
negotiate with the DB's error behaviour instead of the product's rules.

The field is named `orphanable` (not `on_delete`) on purpose: the
vocabulary describes a *property* of the field, not a trigger-and-action
pair. Reading `orphanable: true` says something about the row; the
consequence follows from the property + nullability. Django's
`on_delete=CASCADE` reads as an instruction tied to an event — we want
the declarative phrasing.

**`choices: string`** — fully-qualified name of a proto enum reachable
from the authoring file (the loader's import graph). The IR builder
resolves the path, reads the enum's value names, and emits
`CHECK (col IN ('VAL1', 'VAL2', …))`. Carrier is `string`; the enum
value *names* are stored, not their numeric tags (stable across renumbers,
grep-friendly in DB backups). The enum itself is an ordinary proto
declaration — no parallel vocabulary for choice values, no duplication.
Cross-file enum references are permitted from day one (unlike `fk`,
which is iter-1 same-file only — enums are pure data shape, no
dependency cycle risk).

**Rationale.**

1. **Django parity where it's cheap.** `on_delete` (minus the cascade
   family) and `choices` are the two most common Django model
   declarations that rev2 couldn't express. Both admit a tight, property-
   shaped form that avoids Django's coupling of trigger + action.
2. **No new enum types for `orphanable`.** A bool + optional covers the
   entire in-scope vocabulary. If future iterations need a third value
   (e.g., `SET_DEFAULT`), a migration to a small enum is straightforward.
3. **`choices` reuses proto enums, doesn't invent a parallel list.**
   `choices: [string]` (Django-style inline list) would force developers
   to restate values in schemas *and* in every consumer — the proto enum
   is already the source of truth for the authoring surface. An IDE
   plugin (future) then gets autocomplete and cross-references for free
   because `choices` is just an enum path.

### D9 — Dialect-specific extension namespace (added by M1 rev3, 2026-04-20)

**Decision.** Each SQL dialect may ship its own proto namespace at
`proto/w17/<dialect>/field.proto` with a `(w17.<dialect>.field)`
extension on `FieldOptions`. The first such namespace is Postgres at
`proto/w17/pg/field.proto` → `(w17.pg.field)`. Authors opt into
dialect-specific features per field; the dialect's emitter reads its own
namespace and ignores others (or errors when non-empty flags are set and
the emitter can't honour them).

**`(w17.pg.field)` ships with two layers:**

1. **Curated PG-native flags** — `jsonb`, `inet`, `tsvector`, `hstore`.
   Each flag replaces the SQL column type that the core
   `(w17.field).type` would otherwise pick, while all other
   `(w17.field)` semantics (`null`, `unique`, CHECK constraints,
   defaults) still apply.
2. **Escape hatch** — `custom_type: string` + `required_extensions:
   [string]`. When `custom_type` is non-empty, the emitter uses that
   string verbatim as the SQL column type and skips the semantic-type
   dispatch. `required_extensions` are rendered as
   `CREATE EXTENSION IF NOT EXISTS "<name>"` before `CREATE TABLE`
   (deduplicated across columns). This is the primary path for pgvector,
   PostGIS, custom DOMAINs, and anything the curated flags don't yet
   cover.

IR changes: `ir.Column` gains a `DialectSpecific
map[DialectKey]proto.Message` — each emitter reads only its own key
("pg", "mysql", …) and passes through everything else untouched. Keeps
the core IR dialect-agnostic; dialect-specific emitters are the only
ones that know about dialect-specific shapes.

**Rationale.**

1. **The escape hatch is mandatory** (CLAUDE.md non-negotiable #3).
   `custom_type` is the field-level version of the doctrine — no generator
   without a documented way to reach around it.
2. **Namespacing per dialect keeps the core small.** Adding pgvector
   support doesn't touch `(w17.field)`. Adding MySQL-specific types
   doesn't touch the PG namespace. Each dialect's vocabulary grows
   independently.
3. **Curated flags first, escape hatch second.** `jsonb: true` is
   preferable to `custom_type: "jsonb"` because the curated flag carries
   semantic intent the compiler can use downstream (JSONB validators,
   `default_auto: EMPTY_JSON_*` compatibility, future projections). The
   escape hatch exists so users are never *blocked*, not so they reach
   for it casually.
4. **Forward-compat with custom extensions.** pgvector, PostGIS,
   pg_trgm opclasses — each of these can start as `custom_type` + an
   entry in `required_extensions`, then get promoted to a curated
   `(w17.pg.vector)` / `(w17.pg.geometry)` extension of its own when a
   real pilot makes the case.
