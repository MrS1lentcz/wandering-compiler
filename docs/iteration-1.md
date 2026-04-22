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

- **Proto carriers:** `string`, `int32`, `int64`, `bool`, `double`, `bytes`, `google.protobuf.Timestamp`, `google.protobuf.Duration`.
- **Semantic `type` enum (in `(w17.field)`):**
  - Strings: `CHAR`, `TEXT`, `UUID`, `EMAIL`, `URL`, `SLUG`.
  - Numbers: `NUMBER`, `ID`, `COUNTER`, `MONEY`, `PERCENTAGE` (0–100), `RATIO` (0–1).
  - Arbitrary-precision decimal: `DECIMAL` (carrier: `string` — lossless wire; requires `precision`, optional `scale`).
  - Temporal: `DATE`, `TIME`, `DATETIME` on Timestamp carrier; `INTERVAL` on Duration carrier.
- **`(w17.db.table)` options:** `name`, `indexes` (single or multi-column, unique or not; with optional `name` override and `include` covering columns), `raw_checks` + `raw_indexes` (escape hatches — see D11). No auto-generated fields — every DB column is a declared proto field.
- **`(w17.field)` options (data semantics):** `type` (required for every carrier except bool / bytes), `pk`, `immutable`, `null` (default `false` → NOT NULL + required), `blank` (string-only, default `false` → `CHECK (col <> '')`), `unique` (data-level uniqueness → UNIQUE INDEX), `max_len` / `min_len` (string carriers), `gt` / `gte` / `lt` / `lte` (numeric carriers), `pattern` (string carriers, regex override), `choices` (FQN of a proto enum — CHECK IN (…) — see D8), `precision` / `scale` (DECIMAL), and a `default` oneof — see D7.
- **`(w17.db.column)` options (DB-level rules):** `index` (single-field non-unique storage index), `name` (SQL column-name override), `fk` (foreign key `"<table>.<column>"` — same-file only in iter-1), `deletion_rule` (ON DELETE behaviour — CASCADE / ORPHAN / BLOCK / RESET — see D12). `fk` + `deletion_rule` live here (not on `(w17.field)`) because foreign keys and their deletion semantics are DB-engine rules, same category as indexes and CHECK constraints. `(w17.field).unique` stays a data semantic (author declares "this field uniquely identifies the row"), orthogonal to the pure-optimisation `index` flag.
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
7. **(Revised 2026-04-21.)** A combinatorial **grand-tour fixture set**
   exercises every iter-1 vocabulary primitive and its interesting
   pairings, end-to-end through the golden suite (AC #5) *and* the
   apply-roundtrip harness (AC #2, #3). Coverage axes, each exercised
   at least once:

   - Every `(carrier, type)` cell of D2 — `CHAR`, `TEXT`, `UUID`, `EMAIL`,
     `URL`, `SLUG`, `DECIMAL` on `string`; `NUMBER`, `ID`, `COUNTER` on
     integer carriers; `NUMBER`, `MONEY`, `PERCENTAGE`, `RATIO` on
     `double`; `DATE` / `TIME` / `DATETIME` on `Timestamp`; `INTERVAL` on
     `Duration`; `bool` (no subtype).
   - Every `AutoDefault` variant — `NOW`, `UUID_V4`, `UUID_V7`, `TRUE`,
     `FALSE`, `EMPTY_JSON_ARRAY`, `EMPTY_JSON_OBJECT`, `IDENTITY`.
   - Every CHECK variant — Length (`min_len` / `max_len`), Blank, Range
     (`gt` / `gte` / `lt` / `lte`), Regex (type-implied *and* `pattern`
     override), Choices (proto enum FQN).
   - PK shapes — single-col `int64` `IDENTITY`, single-col `UUID` +
     `default_auto: UUID_V7`, composite PK on an m2m join table.
   - Index shapes — single-col derived-name UNIQUE (via
     `(w17.field).unique`), multi-col named UNIQUE (via
     `(w17.db.table).indexes`), non-unique storage index (via
     `(w17.db.column).index`), `INCLUDE` (covering index).
   - FK shapes — `orphanable: true` (SET NULL), `orphanable: false`
     (inferred CASCADE-ish), self-referencing FK, FK into a table with a
     composite PK.
   - Table archetypes — standalone, parent→child, m2m join table,
     self-referential tree, table exercising a `(w17.pg.field)` curated
     flag (`jsonb` / `inet` / `tsvector` / `hstore`), table exercising
     the `custom_type` + `required_extensions` escape hatch (pgvector or
     equivalent).

   Fixtures live in `srcgo/domains/compiler/testdata/` alongside AC #5's
   original three shapes.

   **Why this replaced the original "pilot project adoption" framing.**
   Single-repo adoption is only as strong as that repo's coverage — if
   the pilot lacks m2m, self-FKs, DECIMAL, or PG-specific types, the
   iter-1 sign-off proves only what that particular repo happens to
   exercise, not what the vocabulary can express. A combinatorial
   synthetic matrix makes coverage **explicit**: every primitive is
   named, every pairing is a file in the tree, regressions are visible
   in `git diff`. External-repo adoption becomes an iter-2 concern once
   the hosted platform + deploy client exist to drive it (see
   `_parked/migration-delivery.md` — iter-1 has no applied-state tracking
   anyway, which caps how rigorous a real pilot could be).

## Apply requirements

The generated `.up.sql` / `.down.sql` pair is a plain Postgres migration
script. What the **target database** needs to accept it:

- **Postgres 14+.** AC #2's floor. M9's apply-roundtrip harness runs on
  `postgres:18-alpine`; the emitted DDL stays inside the 14/15/16/17/18
  intersection (no syntax crossing a version gate). CI composition against
  older majors is an iter-2 concern.

- **`uuidv7()` is a built-in on Postgres 18 only.** `default_auto: UUID_V7`
  emits a bare `uuidv7()` call — on PG 14–17 apply fails with
  `function uuidv7() does not exist` unless the user has loaded a
  compatible extension (for example `pg_uuidv7`). `UUID_V4` uses PG's
  built-in `gen_random_uuid()` and works on every supported major. If
  the target deployment is < PG 18, prefer `UUID_V4` or install the
  extension before apply.

- **Extensions are the target's responsibility.** `(w17.pg.field).required_extensions`
  is authoring metadata only — the compiler does not emit `CREATE
  EXTENSION` into migration bodies (parked decision per D6: the hosted
  platform owns extension installation; iter-1 users do it manually
  before `psql -f`-ing the migration). Curated flags that need
  contribs: `hstore`. Everything else `(w17.pg.field)` currently
  exposes (`jsonb`, `inet`, `tsvector`, MACADDR via `custom_type`) is
  built into stock Postgres. M9's `make test-apply` runs
  `CREATE EXTENSION IF NOT EXISTS hstore` per test DB to stay honest
  about the dependency.

- **Transactional apply — migrations are all-or-nothing.** Every
  emitted `.up.sql` / `.down.sql` is wrapped in
  `BEGIN; … COMMIT;`, so a syntax error, FK conflict, or failed CHECK
  mid-migration rolls back every CREATE TABLE / CREATE INDEX already
  issued in that script. The target DB ends at the pre-apply state
  rather than a half-created mess. Safe for every op iter-1 emits;
  non-transactional exceptions (`CREATE INDEX CONCURRENTLY`, etc.)
  will land as explicit opt-outs when iter-2 needs them.

- **`DROP TABLE` in rollback scripts assumes no external dependencies.**
  Generated `.down.sql` emits `DROP TABLE IF EXISTS`, not
  `DROP TABLE ... CASCADE`. If the user has created views, triggers,
  functions, or FKs outside wc's knowledge that depend on these
  tables, the rollback fails with `cannot drop ... because other
  objects depend on it`. Iter-1 doesn't attempt to discover such
  objects — the intended use is: either run the down immediately
  after its up (before external code catches the tables), or detach
  dependents manually before rolling back.

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
- The grand-tour fixture matrix (AC #7 rev 2026-04-21) is green through
  both the golden suite and the apply-roundtrip harness.
- All five original open questions have written answers. #1, #3, #4, and #5
  are resolved (D5, D1, D7, D4). #2 remains — minor-dimension tuning (CHECK
  default verbosity) that can close once a concrete combo in the matrix
  surfaces a decision point.

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

### Preset Bundles — reference matrix (added 2026-04-21)

Every `(carrier, type)` combination is a **preset bundle** — the author
picks one label and the compiler applies: SQL column type (per dialect),
required / default side-data, auto-synth CHECKs, compatible defaults,
compatible overrides. The table below is the authoritative breakdown.

Iteration-1 ships only the Postgres column; later-iteration dialects
fill the MySQL / SQLite columns.

| Carrier | Type | PG column | MySQL column (iter-2+) | SQLite column (iter-2+) | Auto-CHECKs (when stringStorage) | Required side-data | Default side-data | `default_*` compatible | `default_auto` compatible | `(w17.pg.field).custom_type` compatible |
|---|---|---|---|---|---|---|---|---|---|---|
| `string` | `CHAR` | `VARCHAR(max_len)` | `VARCHAR(max_len)` | `TEXT` | Blank, Length(min_len) | `max_len` | — | `default_string` | — | — |
| `string` | `TEXT` | `TEXT` | `TEXT` | `TEXT` | Blank, Length(min_len, max_len if set) | — | — | `default_string` | — | ✓ |
| `string` | `UUID` | `UUID` | `CHAR(36)` | `TEXT` | — (no blank synth, no regex synth) | — | — | `default_string` | `UUID_V4`, `UUID_V7` | — |
| `string` | `EMAIL` | `VARCHAR(max_len)` | `VARCHAR(max_len)` | `TEXT` | Blank, Regex (email) | — | `max_len: 320` if unset | `default_string` | — | — |
| `string` | `URL` | `VARCHAR(max_len)` | `VARCHAR(max_len)` | `TEXT` | Blank, Regex (http/s) | — | `max_len: 2048` if unset | `default_string` | — | — |
| `string` | `SLUG` | `VARCHAR(max_len)` | `VARCHAR(max_len)` | `TEXT` | Blank, Regex (slug) | `max_len` | — | `default_string` | — | — |
| `string` | `JSON` | `JSONB` | `JSON` | `TEXT` | — (non-string storage) | — | — | `default_string` (JSON literal) | `EMPTY_JSON_ARRAY`, `EMPTY_JSON_OBJECT` | — |
| `string` | `IP` | `INET` | `VARCHAR(45)` | `TEXT` | — (non-string storage) | — | — | `default_string` | — | — |
| `string` | `TSEARCH` | `TSVECTOR` | `TEXT`+FULLTEXT (iter-2) | `TEXT`+FTS5 (iter-2) | — (non-string storage) | — | — | — | — | — |
| `string` | `DECIMAL` | `NUMERIC(precision, scale)` | `DECIMAL(precision, scale)` | `NUMERIC` | — (non-string storage) | `precision` | `scale: 0` | — | — | — |
| `int32`/`int64` | `NUMBER` | `INTEGER` / `BIGINT` | `INT` / `BIGINT` | `INTEGER` | Range (gt/gte/lt/lte if set) | — | — | `default_int` | — | — |
| `int32`/`int64` | `ID` | `INTEGER` / `BIGINT` | `INT` / `BIGINT` | `INTEGER` | — | — | — | `default_int` | `IDENTITY` (pk:true only) | — |
| `int64` | `COUNTER` | `BIGINT` | `BIGINT` | `INTEGER` | Range (if set) | — | — | `default_int` | — | — |
| `double` | `NUMBER` | `DOUBLE PRECISION` | `DOUBLE` | `REAL` | Range (if set) | — | — | `default_double` | — | — |
| `double` | `MONEY` | `NUMERIC(19, 4)` | `DECIMAL(19, 4)` | `NUMERIC` | Range (if set) | — | — | `default_double` | — | — |
| `double` | `PERCENTAGE` | `NUMERIC(5, 2)` | `DECIMAL(5, 2)` | `NUMERIC` | Range(0, 100) implicit + explicit | — | — | `default_double` | — | — |
| `double` | `RATIO` | `NUMERIC(5, 4)` | `DECIMAL(5, 4)` | `NUMERIC` | Range(0, 1) implicit + explicit | — | — | `default_double` | — | — |
| `Timestamp` | `DATE` | `DATE` | `DATE` | `TEXT` | — | — | — | — | `NOW` → `CURRENT_DATE` | — |
| `Timestamp` | `TIME` | `TIME` | `TIME` | `TEXT` | — | — | — | — | `NOW` → `CURRENT_TIME` | — |
| `Timestamp` | `DATETIME` | `TIMESTAMPTZ` | `DATETIME` | `TEXT` | — | — | — | — | `NOW` → `NOW()` | — |
| `Duration` | `INTERVAL` | `INTERVAL` | — (iter-2) | — (iter-2) | — | — | — | — | — | — |
| `bool` | — | `BOOLEAN` | `TINYINT(1)` | `INTEGER` | — | — | — | — | `TRUE`, `FALSE` | — |
| `bytes` | — | `BYTEA` | `BLOB` | `BLOB` | — | — | — | — | — | — |
| `bytes` | `JSON` | `JSONB` | `JSON` | `TEXT` | — | — | — | — | `EMPTY_JSON_ARRAY`, `EMPTY_JSON_OBJECT` | — |
| `string` | `ENUM` | `<table>_<col>` (CREATE TYPE AS ENUM) | `ENUM(...)` (iter-2) | `TEXT` + CHECK IN names (iter-2) | — (type enforces membership) | `choices` | — | `default_string` (enum name literal) | — | — |
| `int32`/`int64` | `ENUM` | `INTEGER` / `BIGINT` | `INT` / `BIGINT` | `INTEGER` | CHECK IN (numbers…) | `choices` (unless proto-enum field) | — | `default_int` | — | — |

**Cross-axis rules** not in the table:

- `(w17.pg.field).custom_type` requires `carrier: string` + `type: TEXT`.
  Any other combination is rejected — custom_type is the escape hatch for
  TEXT-shaped columns whose storage the author wants to override
  verbatim (`CITEXT`, `vector(1536)`, PostGIS geometry, custom DOMAINs).
- `(w17.db.column).fk` requires the target column to be addressable as a
  PG FK target: single-col PK or single-col UNIQUE index. Composite-PK
  member columns are rejected. See D12.
- `(w17.db.column).deletion_rule: ORPHAN` requires `(w17.field).null: true`.
  `RESET` requires any `default_*` variant set. `CASCADE` and `BLOCK`
  have no additional requirements. See D12.
- `(w17.db.table).raw_indexes` and `raw_checks` bodies are opaque SQL —
  compiler validates names (NAMEDATALEN, reserved keywords, collisions)
  but doesn't type-check bodies. See D11.

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

### D5 — Migration naming: `YYYYMMDDTHHMMSSZ.sql` (resolves open question #1; revised 2026-04-21)

**Decision.** Migration filenames are a compact UTC ISO-8601 timestamp of
the generation moment, e.g. `20260421T143015Z.up.sql` and
`20260421T143015Z.down.sql`. No sequence number, no slug, no op-derived
name. The CLI calls `time.Now().UTC()` at generate time and passes it to
`naming.Name(at time.Time) string`; tests inject a frozen clock.

**Rationale.**

1. **D6 puts review in the UI, not the filename.** Migrations are
   platform artifacts — every change is approved / audited / diffed in
   the hosted migration platform (see `_parked/migration-delivery.md`)
   with the full PR context attached. The filename never carries the
   review-relevant signal; slug sympathy was Django baggage that this
   project inherits no benefit from.
2. **Sequence numbers need state the iter-1 CLI doesn't have.** D6
   makes `out/migrations/` gitignored, so "next sequence = count
   existing files" works on one machine and breaks between machines:
   dev A generates `0001_…` and opens a PR; dev B pulls, regenerates,
   and also writes `0001_…` with different content. The platform will
   own sequencing server-side, but until then the CLI has no durable
   counter to read. Timestamps sidestep the problem entirely — every
   generate run produces a unique filename regardless of outside state.
3. **Lex-sort = chrono-sort.** `YYYYMMDDTHHMMSSZ` (fixed-width UTC)
   orders correctly under `ls`, `glob *.sql`, `psql -f *.up.sql`, and
   platform UI listings with no custom sorter.
4. **Collisions need sub-second generate-twice-in-a-row.** For CLI use
   that's effectively impossible (one invocation = one timestamp).
   When the platform centralises generation it picks its own sequencer
   and can use higher precision or a monotonic counter if it ever
   matters.
5. **AC #4 stays about SQL content, not filenames.** Two re-runs with
   the same input produce byte-identical `.up.sql` / `.down.sql` bodies
   — that's what the acceptance criterion protects. Filename freshness
   on re-run is a *feature* (each generate is a new unit of work the
   platform can approve independently), not a determinism violation.

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

### D8 — FK orphan behaviour + enumerated choices (added by M1 rev3, 2026-04-20; superseded by D12 rev 2026-04-21 for the FK axis)

**Decision.** Two compact additions to the authoring vocabulary that
fill the most common Django-parity gaps without importing its full
surface. The FK-behaviour half (`orphanable`) was replaced by D12's
richer `deletion_rule` enum on 2026-04-21 — the section below keeps
the original rationale for history, and the `choices` half stays
unchanged.

**(Historical, superseded.) `orphanable: optional bool`** — a
*property* of the child row answering "can it outlive the parent it
references?" Only meaningful when `fk` is set.

- `true`  → yes. Parent delete leaves this row with FK column `SET NULL`. Requires `null: true`.
- `false` → no. Parent delete `CASCADE`-removes this row.
- unset → inferred from `null`: `null: true` → `true`; `null: false` → `false`.

In M1 rev3 the richer Django vocabulary (`PROTECT`, `RESTRICT`,
`SET_DEFAULT`, `DO_NOTHING`) was explicitly parked as an application
invariant, not a data-contract property. **D12 reversed that call**
after the Django-parity audit showed real schemas that legitimately
need DB-level `RESTRICT` — e.g., "cannot delete Customer while Invoice
rows reference it, regardless of application code path". D12's
`deletion_rule` enum supersedes `orphanable` entirely (skeleton
stage, no back-compat).

The `orphanable` naming discipline (noun-shape, not
hook-shape) carries over: `deletion_rule: CASCADE / ORPHAN / BLOCK /
RESET` — same anti-`on_*` framing, now extended to the full DB-level
palette.

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

### D10 — Proto field numbers are the differ's identity key (resolves iteration-2+ alter-diff strategy)

**Decision.** The differ uses **proto field numbers** — not column
names, not similarity heuristics — as the identity key for deciding
whether two columns across (prev, curr) schemas are "the same column".
A field whose number is present in both snapshots is the *same* column;
any difference in name, type, nullability, defaults, or CHECK constraints
becomes an `AlterColumn` op. A number present in only prev is
`DropColumn`; a number present in only curr is `AddColumn`. Rename
detection is free and unambiguous.

For tables, the equivalent identity key is the proto message FQN
(already carried on `ir.Table.message_fqn`). A message rename changes
the FQN and is semantically `DropTable` + `AddTable` — consistent with
proto's wire contract, where message renames are themselves breaking.

**Rationale.**

1. **Proto field numbers are an inviolable contract.** Users never
   reuse a number, never swap two numbers, never renumber — the proto
   ecosystem enforces it via wire-compat lint and by making violations
   visibly break at runtime. This is a strong baseline we get for free;
   we don't have to re-invent it with column comments, sequence counters,
   or shadow metadata tables.
2. **Ent / Atlas / Django migrate have to guess.** They see a column
   `foo_id` disappear and `parent_id` appear and decide "rename? drop+add?"
   via similarity heuristics that are right most of the time and wrong
   when it matters. The whole class of problem — rename-detection, type-
   change planning when a column is also renamed, backfill shape when
   identity is ambiguous — collapses when identity is in the source (the
   number), not in the ambiguous surface form (the name).
3. **Simpler differ logic.** Alter-diff reduces to a bucketing pass:
   group columns by number across (prev, curr). Both-present with equal
   facts → no-op. Both-present with differing facts → `AlterColumn`.
   Only-prev → `DropColumn`. Only-curr → `AddColumn`. No fuzzy matching,
   no tie-breaking, no rename-threshold knob.
4. **User-facing semantics match user intent.** When the generated
   migration says `AlterColumn`, the author kept the field number and
   changed something else — a deliberate act. When it says
   `DropColumn + AddColumn`, the author removed a number and added a
   new one — also deliberate, and the right framing for any non-trivial
   data migration.

**Relation to D3.** "No compiler-generated fields" (D3) makes this
possible: every DB column is an explicit proto field with an explicit
number. If we injected hidden `created_at` / `updated_at` columns, they'd
lack a number and the differ would need a parallel identity mechanism for
them. The two decisions reinforce each other — D3 keeps the IR honest,
D10 turns that honesty into cheap alter-diff.

### D11 — Table-level escape hatches for CHECK and INDEX (added 2026-04-21)

**Decision.** `(w17.db.table)` carries two opaque-SQL escape hatches
alongside the curated vocabulary:

- **`raw_checks: [ { name, expr } ]`** — table-level CHECK constraints
  that the per-field vocabulary (Length / Blank / Range / Regex /
  Choices) can't spell. `expr` is pass-through SQL rendered inside
  `CONSTRAINT <name> CHECK (<expr>)`.
- **`raw_indexes: [ { name, unique, body } ]`** — index shapes the
  structured `indexes:` field can't spell. `body` is pass-through SQL
  rendered after `CREATE [UNIQUE] INDEX <name> ON <table>`.

The compiler validates `name` (NAMEDATALEN ≤ 63, not a reserved PG
keyword, no collision with any other emitted identifier — derived
synths, explicit indexes, other raw entries) but treats `expr` /
`body` as opaque. Dialect portability is the author's problem — same
contract as `(w17.pg.field).custom_type`.

**Rationale.**

1. **Parity gaps with Django that the curated vocabulary can't
   reasonably close.** Django's `Meta.constraints = [CheckConstraint(…)]`
   accepts arbitrary Q() expressions that reference multiple columns
   or call functions; per-field CHECKs can't. Django's `Index(fields=[…],
   condition=Q(…))` emits partial indexes, `GinIndex` / `GistIndex` /
   `BrinIndex` emit non-btree indexes, `expressions=[F('lower(col)')]`
   emits expression indexes — iter-1's structured `indexes:` has none
   of those knobs. The first real full-text-search use-case in a wc
   project needs a GIN index on tsvector; without an escape hatch,
   the user is blocked.
2. **CLAUDE.md non-negotiable #3 (escape hatches mandatory).** Every
   generator must have a documented fall-back to hand-written SQL.
   `(w17.pg.field).custom_type` covers the column-type axis;
   `raw_checks` / `raw_indexes` cover the table-level constraint and
   index axes. No generator without an escape hatch.
3. **Raw (opaque string) over structured (GinIndex / PartialIndex
   messages) in iter-1.** A structured vocabulary for the common cases
   (GIN, partial, expression) is iter-2 work — the right shape needs
   real usage to pin. A raw escape hatch unblocks *now* and composes
   cleanly with the future structured vocabulary: `raw_indexes` entries
   that stabilise into a pattern graduate into typed message shapes,
   exactly as `custom_type` entries graduate into curated flags per D9.
4. **Alter-diff compatibility.** Raw bodies participate in the differ
   (iter-2+) by identity-on-name — a raw index changes shape → the
   differ emits DROP + CREATE with the new body. No fuzzy semantic
   diffing on opaque SQL; simple, correct, aligned with D10's
   "identity is explicit, not heuristic" ethos.
5. **Iter-1 semantics for DB portability.** Raw bodies live on
   `(w17.db.table)`, the dialect-agnostic vocabulary. That's
   deliberate: cross-column CHECKs (`start <= end`) and partial
   indexes (`WHERE deleted_at IS NULL`) are standard SQL, not PG-
   specific. Things that ARE PG-specific (`USING gin`, `gin_trgm_ops`)
   live inside the opaque body and simply don't apply on other
   dialects — same "author's problem" as `custom_type: "JSONB"`.

**Operator-class + extension caveat.** Raw indexes that use operator
classes (`gin_trgm_ops`, `jsonb_path_ops`) may require PG extensions
the target DB doesn't have by default. The `required_extensions` list
lives on `(w17.pg.field)` and is per-column — table-level raw
indexes have no equivalent surface yet. Iter-1 users annotate one
of the columns the index covers with `required_extensions` as a
workaround; iter-2 may lift `required_extensions` to the table level
for raw-index use cases.

### D19 — Module namespace: schema XOR prefix (added 2026-04-22)

**Decision.** Each compilation module (= proto file in iter-1; = proto
domain directory in iter-2) picks exactly one of two namespacing
strategies, declared in a new file-level option:

```proto
// PG-native schema qualification — every table lives in <name>
option (w17.db.module) = { schema: "reporting" };
// → CREATE TABLE reporting.events (...); REFERENCES reporting.users(id); …

// Name-prefix convention — works on every dialect (including MySQL +
// SQLite which have no PG-style schema concept)
option (w17.db.module) = { prefix: "catalog" };
// → CREATE TABLE catalog_events (...); REFERENCES catalog_users(id); …

// Default (no option) — bare names, land in PG's default schema
// (usually `public`), Django-flat layout
```

The two strategies are **mutually exclusive** (proto `oneof` enforces)
and **module-immutable**: no per-message override. A module that mixes
one table outside its namespace is a code smell we refuse to model —
it would create patterns like `reporting.public_events` or
`catalog_foreign_events` that nobody wants. Iter-2 multi-file
compilation enforces the module rule by rejecting any `.proto` whose
`(w17.db.module)` disagrees with its siblings.

**Why both strategies.**

  - **SCHEMA mode** is PG-native, zero overhead — PG's own namespace
    mechanism keeps indexes / constraints / sequences / types
    auto-scoped. Natural for multi-tenant (per-tenant schema),
    blue/green migration swaps, or logical separation (`auth`,
    `reporting`, `billing` side-by-side in one DB).
  - **PREFIX mode** is dialect-agnostic — MySQL's "schema = database"
    quirk and SQLite's lack of schemas make PG-style qualification
    unavailable there. Prefix mode produces identifiers every dialect
    accepts: `catalog_products` works identically on PG, MySQL,
    SQLite. Django's `app_modelname` convention lands here.

**Invariants enforced at IR time.**

  - SCHEMA mode: name is a valid identifier (NAMEDATALEN, no reserved
    keyword) AND not a PG system schema (`pg_*`, `information_schema`,
    `pg_toast`). Reserved check runs at IR time so authors never
    accidentally shadow pg_catalog.
  - PREFIX mode: name is a valid identifier. No artificial cap —
    overflow is caught naturally on the post-prefix effective
    identifier, which must itself fit 63 bytes.
  - Empty namespace value on either oneof branch = error (author
    picked a strategy but forgot to name it).
  - Derived index / CHECK constraint / PG ENUM type names run through
    the existing NAMEDATALEN validation against the **post-prefix**
    form, so prefix overflow fails loudly with file:line:col instead
    of silently truncating at apply.
  - Author-supplied names (`indexes[].name`, `raw_checks[].name`,
    `raw_indexes[].name`) also get the module prefix applied — "no
    per-message override" applies here too. Author picks the suffix;
    the compiler picks (and enforces) the prefix.

**What the compiler emits.**

  - **SCHEMA mode:** CREATE TABLE `<schema>.<name>`, REFERENCES
    `<schema>.<target>`, DROP TABLE `<schema>.<name>`, DROP INDEX
    `<schema>.<idx_name>`, DROP TYPE `<schema>.<type_name>`. CREATE
    INDEX name itself is bare per PG syntax (index auto-scopes to the
    table's schema). `Table.Name` stays bare in IR so the differ can
    detect namespace changes separately from rename operations in
    iter-2.
  - **PREFIX mode:** every identifier the module owns is
    `<prefix>_<bare>`, baked into IR at build time. Emitter sees
    fully-prefixed identifiers and renders them straight — no
    qualification dispatch at emit time. Uniform across dialects.
  - **NONE mode (default):** bare identifiers, no qualification.

**What the compiler does NOT emit.** `CREATE SCHEMA reporting` is
never part of the migration body. Creating the schema is a
deploy-client / platform job (same logic as PG extensions per D6 /
D9) — the migration only uses the schema, not creates it. Operators
provision the schema + its GRANTs before applying migrations.

**Cross-schema FK.** Same-file in iter-1 means same-namespace —
cross-schema FK is a null case. When iter-2's multi-file / cross-
domain FK lands, PG natively supports `REFERENCES auth.users(id)` so
nothing about the wire format changes; only the `(w17.db.column).fk`
syntax grows to carry the target's module (likely a qualified form
like `"auth.users.id"` or by-module-reference).

**Identity for iter-2 alter-diff.** Table identity for rename /
schema-move detection is deferred: per-table identity key has not
been pinned (D10 only pins per-column). Plausible candidates are
`MessageFqn` (proto-stable across namespace changes) or `(mode, ns,
name)` tuple (name changes are renames; namespace changes are a
schema-level op). The IR captures every fact the differ needs; the
choice lands inside the D23 indexes+constraints overhaul next to
alter-diff.

**Rationale.** Django's `app_label` is prefix-mode (Django has no
schema concept); SQLAlchemy exposes PG schemas directly via
`__table_args__ = {'schema': 'reporting'}`. wc ships both as
orthogonal strategies with a common "module-level, immutable" rule —
author picks per module, compiler applies uniformly. The rule
catches "one table out of the group" as a code smell rather than
silently accepting it.

Capability: `SCHEMA_QUALIFIED` = `{}` in the PG catalog (available on
every supported version). Prefix mode needs no capability — any
emitter accepting identifiers accepts `<prefix>_<name>`.

### D18 — Generated columns: GENERATED ALWAYS AS … STORED (added 2026-04-22)

**Decision.** `(w17.db.column)` gains an opaque `generated_expr: string`
field that, when set, emits the column as
`GENERATED ALWAYS AS (<expr>) STORED`. The body is pass-through SQL —
same contract as `raw_checks.expr` / `raw_indexes.body` — with zero
interpretation by wc. The authoring surface:

```proto
string full_name = 3 [
  (w17.field)     = { type: CHAR, max_len: 200 },
  (w17.db.column) = {
    generated_expr: "first_name || ' ' || last_name"
  }
];
// → full_name VARCHAR(200) NOT NULL GENERATED ALWAYS AS (first_name || ' ' || last_name) STORED
```

**Why STORED, not VIRTUAL.** Iteration-1 targets PostgreSQL, which
implements SQL:2016 STORED only — VIRTUAL generated columns are not
available on PG (as of 18). MySQL offers both; SQLite offers VIRTUAL.
Shipping STORED today keeps the compiler honest about what PG
actually emits; the MySQL / SQLite emitters (iter-2+) can add a
`virtual:` sibling option if pilot schemas need the VIRTUAL shape.
Raw SQL via `(w17.db.table).raw_indexes` covers VIRTUAL-like
shapes today (expression index = deterministic computed value
without column storage).

**Invariants enforced at IR time.**

  - `generated_expr` is incompatible with `default_string` /
    `default_int` / `default_double` / `default_auto` — a
    GENERATED ALWAYS AS column is computed from its expression and
    PG rejects any DEFAULT clause alongside it (the two would
    compete for the initial value).
  - `generated_expr` is incompatible with `pk: true` — PG rejects
    STORED generated columns as primary keys. Authors pick a plain
    column as the PK and derive the generated one from it.
  - `generated_expr` is incompatible with `fk:` on the same column
    — a FOREIGN KEY on a computed column makes the referential
    integrity contract depend on a value the author doesn't own;
    PG rejects it. Model the FK on a plain column + derive the
    generated one from it.
  - `unique` and `null` remain allowed. PG permits UNIQUE on STORED
    generated columns, and NULL / NOT NULL is independent of how
    the value is produced.
  - CHECK synths on the column (blank, length, regex, range,
    choices, raw) still fire. They apply to the *computed* value —
    a useful feature for enforcing invariants on derived data. The
    author is responsible for ensuring the expression can satisfy
    them (e.g. if a CHECK rejects empty strings, the expression
    must not evaluate to `''`).

**What the compiler emits.**

  - Column line: `<name> <TYPE> [NULL|NOT NULL] GENERATED ALWAYS AS (<expr>) STORED`
    — DEFAULT / IDENTITY branches are skipped (IR has already
    rejected the combinations that would otherwise reach the
    emitter). Inline PK / FK branches are unreachable for the same
    reason.
  - Nothing else changes about migrations: generated columns drop
    with the table (no separate cleanup), and the expression is
    captured verbatim inside `CREATE TABLE` rather than as an
    `ALTER TABLE ADD COLUMN`.

**Escape hatches.**

  - Author wants a VIRTUAL-style value (expression computed on
    read, not stored): use `(w17.db.table).raw_indexes` for an
    expression index if the use case is "search by derived value",
    or defer to the iter-2 MySQL / SQLite emitter surface for
    VIRTUAL support.
  - Author wants a DEFAULT that isn't quite an expression (e.g.
    function call on INSERT only): keep the column plain with
    `default_auto: NOW` / `default_auto: UUID_V7` / `default_*`.
    Generated columns mean "ALWAYS", not "ON INSERT".
  - Author wants dialect-specific generated syntax beyond STORED
    (e.g. MySQL's `GENERATED ALWAYS AS (…) VIRTUAL`): iter-1 has
    no VIRTUAL surface; park until multi-dialect emitters land.

**Rationale.** Django ships `GeneratedField` (4.2+) and SQLModel
leans on SQLAlchemy's `Column(Computed(...))` — both collapse the
three main dialects' generated-column shapes into one author-facing
field. wc takes the same shape but keeps the body opaque (SQL
pass-through) rather than modelling a mini-expression DSL:
computed columns are rare enough that a typed expression surface
would be a large abstraction for little payoff, and the raw-SQL
contract matches how `raw_checks` / `raw_indexes` already handle
author-supplied expressions.

Capability: `GENERATED_COLUMN` = `{MinVersion: "12.0"}` in the PG
catalog. PG 11 and earlier have no STORED-generated-column support.

### D17 — ENUM type: carrier-dispatched storage (added 2026-04-22)

**Decision.** `(w17.field).Type` gains a `ENUM` value that maps, per
carrier, onto the author's actual intent:

| Carrier | Storage | Membership enforcement |
|---|---|---|
| `string`  | PG `CREATE TYPE <table>_<col> AS ENUM (names…)` + column of that type | PG ENUM type itself |
| `int32` / `int64` | `INTEGER` / `BIGINT` | `CHECK col IN (numbers…)` |
| proto-enum field (e.g. `Status s = 1;`) | `INTEGER` (proto wire is int32) | `CHECK col IN (numbers…)` — auto-inferred from the descriptor |

Three author paths:

```proto
// Case A — bare proto-enum field. Auto-inferred SEM_ENUM on int32
// carrier; choices resolved from the descriptor.
Status state = 1;
// → INTEGER NOT NULL CHECK (state IN (1, 2, 3))

// Case B — string-backed with PG ENUM storage.
string state = 2 [(w17.field) = {
  type: ENUM,
  choices: "pkg.Status"
}];
// → CREATE TYPE posts_state AS ENUM ('DRAFT','PUBLISHED'); state posts_state NOT NULL

// Case C — explicit int + SEM_ENUM. Same shape as Case A but on an
// int32/int64 field that isn't itself a proto enum.
int64 state = 3 [(w17.field) = {
  type: ENUM,
  choices: "pkg.Status"
}];
// → BIGINT NOT NULL CHECK (state IN (1, 2, 3))
```

**Open question resolved.** The handoff flagged whether a bare
proto-enum field should auto-infer SEM_ENUM (option b) or require an
explicit `type: ENUM` annotation (option a). **D17 ships option b** —
matches the D14 zero-config philosophy and proto's own wire semantics
(a proto enum field travels as int32 + known-good number set). Authors
who want the string+PG ENUM shape opt in explicitly via `string foo = N
[type: ENUM, choices: "…"]`.

**Invariants enforced at IR time.**

  - SEM_ENUM is valid only on string / int32 / int64 carriers. LIST /
    MAP + ENUM is rejected (per-element-of-dedicated-type dispatch
    parks with the collection iteration).
  - String + SEM_ENUM requires `choices:` — the compiler has no
    descriptor handle on a `string` field and can't derive an enum
    reference without it.
  - Proto-enum field + explicit `choices:` must agree on FQN. A
    mismatch rejects rather than silently picking one source of truth.
  - Zero-value (`*_UNSPECIFIED = 0`) is stripped from both the name
    list (CREATE TYPE) and the number list (CHECK IN) per the proto3
    sentinel convention — same behaviour as the existing string-
    `choices:` path.
  - `<table>_<col>` (the derived CREATE TYPE name) is validated for
    NAMEDATALEN + reserved keywords at IR time, not apply time.

**What the compiler emits per axis.**

  - Column type: `<table>_<col>` for string path; `INTEGER` / `BIGINT`
    for int path.
  - CREATE TYPE prepended before CREATE TABLE (one statement per
    string-carrier SEM_ENUM column, declaration order).
  - DROP TYPE appended after DROP TABLE in reverse order — table
    drops first because the column depends on the ENUM type.
  - CHECK IN (numbers) attached on int path via the existing
    `ChoicesCheck` variant extended with a `numbers` field. The
    emitter's `renderChoices` dispatches on whichever of
    `values` / `numbers` is populated.
  - String-only CHECK synths (blank, length, regex) skip on string
    SEM_ENUM — the dedicated type enforces membership by
    construction, redundant CHECKs would bloat pg_constraint.

**Escape hatches.**

  - Author wants raw string column + CHECK IN names (no PG ENUM
    type): use `type: CHAR, choices: "..."` — the existing D2 path.
  - Author wants custom PG storage (domain type, int4range,
    anything): `(w17.pg.field).custom_type` on a TEXT column, or
    `(w17.db.table).raw_checks` for a dialect-specific expression.
  - Author wants to override auto-inferred ENUM on a proto-enum
    field: add explicit `type: NUMBER` — the field resolves as a
    plain integer without the CHECK.

**Rationale.** Django + SQLModel + most ORMs ship a single "enum"
declaration that the DB layer renders as per-dialect optimal storage
(PG native ENUM, MySQL inline ENUM(...), SQLite TEXT+CHECK). The
compiler collapses the three authoring surfaces (bare proto enum,
string+CHECK, int+CHECK) into one type token with carrier-driven
dispatch — users don't have to know dialect ENUM quirks, and proto
enum fields Just Work out of the box.

Capability: `ENUM_TYPE` = `{MinVersion: "8.3"}` in the PG catalog
(CREATE TYPE AS ENUM landed in PG 8.3).

### D16 — Dialect-capability catalog + inspection interface (added 2026-04-21)

**Decision.** Each dialect emitter ships a static catalog of every
capability its generated SQL may reference, keyed by a stable cap ID
string. Each capability declares its target-DB requirements:
minimum dialect version (if any) and required extensions (if any).
A small `DialectCapabilities` interface exposes the catalog to
downstream tooling.

```go
// srcgo/domains/compiler/emit/capabilities.go

type Requirement struct {
    MinVersion string   // dialect version (dotted-decimal) or empty
    Extensions []string // sorted extension names or empty
}

type DialectCapabilities interface {
    Name() string
    Requirement(cap string) (Requirement, bool)
}
```

Cap IDs follow three shape conventions:

  - `UPPER_CASE` — SQL type or feature (JSONB, UUID, ARRAY,
    INCLUDE_INDEX, IDENTITY_COLUMN).
  - `lower_case()` — dialect function (`gen_random_uuid()`,
    `uuidv7()`, `jsonb_path_ops`, `gin_trgm_ops`).
  - `snake_case` — dialect extension (`hstore`, `citext`, `pg_trgm`,
    `pg_jsonschema`).

All cap IDs are string constants in `emit/capabilities.go` so new
features land without proto/enum churn. Catalog entries live per
dialect in `emit/<dialect>/capabilities.go`.

Iter-1 PG catalog covers ~35 entries: every type / feature / function
/ extension the PG emitter currently references + the ones iter-2
will reach for (pg_jsonschema, pg_uuidv7). Unit tests enforce:

  - Every cap constant in `emit/capabilities.go` has an entry in the
    PG catalog (or we'd reference an unknown cap).
  - Every catalog entry is well-formed: MinVersion parseable as
    dotted-decimal, extensions non-empty when declared.
  - Unknown cap lookups return ok=false (contract for "compiler bug:
    emitter references a cap the catalog doesn't know").

**What's wired in iter-1.** Inspection only — the catalog answers
"what does feature X require?" via `Requirement(cap)`. Consumers:
docs, audit tooling, future platform. `Emit()` itself doesn't yet
track which caps each migration actually uses, doesn't accept
target-DB config, doesn't gate emission against that config.

**What's deferred to iter-2** (iteration-2-backlog.md captures the
full writeup):

  - **Usage tracking.** Per-migration collection of "this SQL uses
    JSONB in column X, gin_trgm_ops in index Y, …" as the emitter
    runs. Feeds the manifest.
  - **Requirements manifest.** Alongside `.up.sql` / `.down.sql`
    the generator emits a structured manifest listing every cap
    used + where. Deploy client verifies before apply.
  - **Target-DB config.** `Emitter{TargetVersion, AvailableExtensions}`.
    Emit-time validation: used caps must satisfy target's version +
    available extensions; mismatches fail at `wc generate` with a
    diagnostic naming the cap + remediation (upgrade, install
    extension, or avoid the feature).
  - **CLI flags.** `--target-pg-version=14 --extensions=hstore,citext`.

**Rationale.**

1. **"Ready when users land on older DBs" (the user's phrasing).**
   Iter-1 implicitly targets PG 18 (hardcoded uuidv7 use, INCLUDE
   on PG 11+, IDENTITY on PG 10+). Without a capability layer,
   users on PG 14 get cryptic apply-time errors. The catalog is the
   first brick in the wall that eventually surfaces "feature X
   requires PG 18+ but you declared target PG 14" at design time.

2. **Cap IDs as strings, not enums.** New features arrive through
   adding a constant + catalog entry — no proto enum renumbering,
   no backward-compat gymnastics. The test suite's
   `expectedPgCaps` list is the single place where "what does the
   PG emitter use?" is enumerated for audit.

3. **Per-dialect catalog, shared constants.** Each dialect's catalog
   is independent — PG doesn't know anything about MySQL's version
   numbers, SQLite doesn't care about JSONB. But the cap IDs are
   shared (a cap ID like `JSONB` means the same thing across
   dialects — the feature JSON-binary storage). Downstream tooling
   can ask "across dialects, which ones have JSONB?" uniformly.

4. **Inspection before enforcement.** Iter-1 ships the read surface
   without the write surface. This lets docs + audit tooling + the
   platform start consuming the catalog now, and leaves enforcement
   for iter-2 when target-version config exists.

5. **Catalog-vs-code discipline.** Each feature in the emitter is
   expected to reference its cap ID constant. A sweep that connects
   every emitted SQL construct to a cap ID lands with usage tracking
   (iter-2) — iter-1 pins the catalog shape so that sweep is
   mechanical, not a redesign.

### D15 — Collection carriers (map, repeated) + AUTO dispatch + element typing (added 2026-04-21)

**Decision.** Proto `map<K, V>` and `repeated X` fields become
first-class columns. Two new carriers + one new sem-type value + a
deterministic per-dialect dispatch ladder.

**Carriers:**

- `CARRIER_MAP` — proto `map<K, V>`. Iter-1.6 requires K=string;
  non-string keys are rejected (HSTORE is string-string only, and
  cross-dialect JSONB shape conventions for non-string keys aren't
  pinned).
- `CARRIER_LIST` — proto `repeated X`. X can be any scalar carrier
  (string / int32 / int64 / double / bool / bytes / Timestamp /
  Duration) or a proto message.

**SemType:** `SEM_AUTO` — the explicit "let the dialect pick storage
based on carrier + element info" marker. Valid only on map / list
carriers. Default on those carriers (leaving `type:` unset is
equivalent to `type: AUTO`).

**Dispatch ladder (Postgres iter-1.6):**

| Field shape | PG storage |
|---|---|
| `map<string, string>` | `HSTORE` (requires hstore extension) |
| `map<string, V>` where V is non-string scalar | `JSONB` |
| `map<string, V>` where V is Message | `JSONB` |
| `repeated <scalar>` | `<scalar PG type>[]` (native array) |
| `repeated Timestamp` / `repeated Duration` | `TIMESTAMPTZ[]` / `INTERVAL[]` (native) |
| `repeated Message` | `JSONB` |

For later dialects: native array / map types fall back to JSON where
absent, then TEXT where even JSON isn't available. Same deterministic
ladder.

**Element typing on repeated.** `(w17.field).type` on a repeated
field refines the element's sem-type. `repeated string [type: URL]`
= array of URLs → `VARCHAR(2048)[]` (URL's preset default max_len).
The column-level convention is: on a list carrier, Column.Type IS
the element's sem-type. This matches the author's mental model
(choosing a sub-type picks that type for each element) without
introducing a separate per-element options message.

`max_len` on a repeated field applies to the element's `VARCHAR(N)`
sizing. CHAR / SLUG / EMAIL / URL presets carry over identically.

Element sem refinement is **not** supported on maps — iter-1.6
dispatches maps strictly on key / value carrier types. Element-
typed map values land iter-2+.

**What's rejected at IR time:**

- `pk: true` on collection carriers (array PKs have degenerate
  semantics).
- `unique: true` on collection carriers (UNIQUE on whole-array
  equality is almost never the intent; use `raw_indexes` for the
  specific index shape).
- `min_len` / `blank` / `pattern` / `choices` on collections — these
  would need per-element CHECKs, and PG CHECK constraints can't
  express "forall element" without subqueries. Authors who need them
  reach for `(w17.db.table).raw_checks` with dialect-specific SQL.
- `type:` other than AUTO on maps.
- Element sem type set on `repeated Message` (message elements store
  as JSONB; per-element sem is meaningless there).
- Non-string map keys.

CHECK synthesis skips entirely on collection carriers — no implicit
range (PERCENTAGE / RATIO), no implicit blank, no implicit regex.
Column-level CHECKs on arrays / maps arrive via `raw_checks`.

**Rationale.**

1. **Django / ORM parity.** `ArrayField`, `JSONField`, `HStoreField`
   are day-one features in Django. Without collection carriers,
   iter-1 rejected `repeated`/`map` outright and forced authors to
   hand-roll a JSONB+app-serialised shape — worse storage density,
   no native operators, no native indexes. Post-D15 the common case
   is declarative and native.

2. **AUTO + element typing is more composable than a big enum.**
   An alternative was `DbType: ARRAY_OF_TEXT`, `DbType: ARRAY_OF_INT`,
   etc. — O(N*M) combinatorial enum. AUTO + element-carrier +
   element-sem composes from building blocks already in the IR; the
   dispatch ladder is ~15 lines of emitter code, not a 100-entry
   enum.

3. **Element-level CHECKs parked, not faked.** PG CHECK constraints
   provably can't iterate (no subquery support). Offering author-
   level `min_len` / `pattern` / etc. on list carriers would require
   silently dropping them, which violates the "no silent drops"
   discipline that D13 / D14 established. Rejecting up front + raw
   escape hatch preserves discipline while leaving power-user room.

4. **Composable with D14 `db_type`.** Authors can override AUTO
   dispatch via `db_type: JSONB` on a map to force JSON over HSTORE,
   or via `db_type: JSON` on a list to force JSON over native arrays.
   No new escape hatch needed; the orthogonal storage axis already
   covers this.

5. **Timestamp / Duration as scalar-ish elements.** Proto-level
   they're messages; semantically they're single-valued scalars. PG
   has `TIMESTAMPTZ[]` and `INTERVAL[]` as first-class types. Treating
   them as scalars in the LIST dispatch preserves native storage for
   the common case without complicating the general Message →
   JSONB fallback.

### D14 — Zero-config defaults + data/storage orthogonal axes (added 2026-04-21)

**Decision.** Two coupled changes to the authoring surface:

**1. Per-carrier zero-config defaults.** `(w17.field)` is now optional
on every carrier. When `type:` is unset, the compiler picks the
default `SemType` for the carrier:

| Carrier | Default SemType | Default PG storage |
|---|---|---|
| `string` | `TEXT` | `TEXT` |
| `int32` / `int64` | `NUMBER` | `INTEGER` / `BIGINT` |
| `double` | `NUMBER` | `DOUBLE PRECISION` |
| `Timestamp` | `DATETIME` | `TIMESTAMPTZ` |
| `Duration` | `INTERVAL` | `INTERVAL` |
| `bool` | — (no refinement) | `BOOLEAN` |
| `bytes` | — (no refinement) | `BYTEA` |

A bare `int32 visits = 1;` compiles: emitter produces
`visits INTEGER NOT NULL`. Authors opt into sub-types (ID, COUNTER,
CHAR, SLUG, EMAIL, MONEY, DATE, …) only when the default doesn't fit.

Still explicit (no default inference):

- `pk: true` — per-table decision, not per-field.
- `type: ID` — most int columns aren't identifiers; defaulting to ID
  would force every other field to opt out.
- `max_len` for CHAR / SLUG — the compiler can't invent a size;
  EMAIL / URL have preset defaults (320 / 2048) only because those
  sem types carry conventional maxes.
- `precision` for DECIMAL — no safe default.
- MONEY / PERCENTAGE / RATIO — fixed-shape variants; default NUMBER
  is safer for generic numerics.

**2. `(w17.db.column).db_type` as a storage-override axis, orthogonal
to field.Type.** `field.Type` drives data semantics (CHECKs,
validation, preset storage default); `db_type` drives the final SQL
column type. The author can combine them:

```proto
string bio = 1 [
  (w17.field)     = { type: CHAR, max_len: 6000 },
  (w17.db.column) = { db_type: TEXT }
];
// → column: TEXT NOT NULL
// → CHECK: blank + char_length(bio) <= 6000 (length CHECK is
//   NOT subsumed by the TEXT storage since it's not VARCHAR-backed)
```

```proto
string email = 2 [
  (w17.field)     = { type: EMAIL, unique: true },
  (w17.db.column) = { db_type: CITEXT }
];
// → column: CITEXT NOT NULL
// → CHECK: blank + email format regex still applies
// → UNIQUE INDEX benefits from case-insensitive equality
```

`DbType` is enumerated (~30 values covering common cross-dialect +
PG-native types). Validated at IR time: each value declares which
carriers it's compatible with. Conflict with
`(w17.pg.field).custom_type` is rejected — the two override paths
are distinct:

- `db_type` — enumerated, compiler knows the type, validates carrier
  compatibility, emitter maps per dialect.
- `custom_type` — opaque string, PG-specific, escape hatch for types
  the enum doesn't cover (pgvector, PostGIS geometry, custom DOMAINs,
  dialect-specific extensions).

**Interaction with Length CHECK subsumption.** When `db_type` is set,
the subsumption logic checks the final storage shape rather than
sem-type:

- `db_type: VARCHAR` → `char_length <= max_len` subsumed (column
  type already enforces).
- `db_type: TEXT` / `CITEXT` / other non-VARCHAR → length CHECK
  still emitted.
- `db_type` unset → preset sem-type dispatch (CHAR / SLUG / EMAIL /
  URL subsumed, TEXT / UUID / JSON / IP / TSEARCH / DECIMAL not).

**Interaction with `columnStoresAsString`.** String-only synths
(blank, regex, choices) gate on the final storage, not the sem-type.
`db_type: CITEXT` → string-shaped storage → synths emit.
`db_type: JSONB` → non-string → synths skip.

**Rationale.**

1. **Zero-config for the 80% case.** Django, ent, Prisma all support
   "declare the field, get a reasonable column" — iter-1 required
   explicit `type:` on most carriers, which was friction for the
   common case (generic int column, generic string column, generic
   timestamp). The defaults table captures the conventional choice
   per carrier; anything else is opt-in.

2. **Data / storage orthogonality matches real authoring needs.**
   Authors sometimes want the data-semantic benefits of a preset
   (EMAIL's format CHECK, CHAR's max_len CHECK) with a different
   storage shape (CITEXT for case-insensitive equality, TEXT for
   legacy schemas). Pre-D14 the only path was `custom_type`, which
   loses all preset semantics — CHECK synths, default_auto
   compatibility, differ awareness. `db_type` is the typed middle
   layer: enum-known storage + preset-preserved semantics.

3. **Custom_type stays mandatory per CLAUDE.md non-negotiable #3.**
   DbType can never cover every SQL type across every dialect
   (pgvector, PostGIS, CUBE, custom DOMAINs). Opaque escape hatch
   remains. The two paths are disjoint — conflict-at-same-column is
   an IR-time error.

4. **The Preset Bundles matrix gains a "default" row per carrier.**
   Authors who want to know "what happens when I write
   `int32 foo = 1;`" look up `int32` in the matrix and see NUMBER →
   INTEGER. The matrix is the single source of truth — code and doc
   derive from it.

### D13 — Dialect-specific storage lifted into field.Type (added 2026-04-21)

**Decision.** Three PG-specific curated flags on `(w17.pg.field)` —
`jsonb`, `inet`, `tsvector` — graduated into core `(w17.field).type`
values: `JSON`, `IP`, `TSEARCH`. `hstore` stays for now (iter-1.6 lifts
it via a map-carrier AUTO dispatch), alongside `custom_type` +
`required_extensions` which remain as genuine PG-only escape hatches.

`field.Type` is now the **preset layer** — each value encodes:

1. Storage shape per dialect (JSON → JSONB on PG, JSON on MySQL,
   TEXT on SQLite).
2. Required side-data (CHAR / SLUG still require `max_len`; DECIMAL
   still requires `precision`).
3. Preset defaults (EMAIL → VARCHAR(320); URL → VARCHAR(2048)); author
   override via `max_len` works uniformly across VARCHAR-backed types.
4. Auto-synth CHECK set (blank / length / regex / range / choices), gated
   on storage shape (non-string SQL types skip string-only synths).
5. Compatible `default_*` variants (e.g. `default_auto: EMPTY_JSON_*`
   now requires `type: JSON`).
6. Compatibility with `(w17.pg.field).custom_type` override (only on
   string carrier + `type: TEXT`).

**Rationale.**

1. **Cross-dialect semantics.** JSON / IP / TSEARCH have coherent
   semantics across dialects — "this column holds structured JSON
   data", "this column holds an IP address". The author shouldn't
   have to know that PG uses JSONB while MySQL uses JSON while SQLite
   uses TEXT — the preset chooses. Dialect-specific flags
   (`(w17.pg.field).jsonb`) forced the author to hardcode a
   dialect into the authoring surface, which is the opposite of the
   preset discipline D2 established.

2. **`hstore` stays PG-only because it doesn't have cross-dialect
   equivalent.** HSTORE is a proprietary PG key-value type; no other
   common dialect has a direct analog. The cross-dialect question for
   key-value collections is answered by the iter-1.6 map-carrier AUTO
   dispatch (`map<string,string>` → HSTORE on PG, JSON elsewhere), at
   which point the `hstore` flag can retire.

3. **`custom_type` + `required_extensions` still mandatory
   (per CLAUDE.md non-negotiable #3).** Every generator needs an
   escape hatch for types the curated vocabulary doesn't cover —
   pgvector, PostGIS geometry, MACADDR, custom DOMAINs. D13 shrinks
   the escape-hatch surface but doesn't remove it.

4. **EMAIL / URL grow `max_len` override.** Previously EMAIL was
   hardcoded VARCHAR(320) and URL was TEXT; authors couldn't narrow
   either. Post-D13 both accept `max_len` the same way CHAR/SLUG do,
   with preset defaults (320 / 2048) when unset. Consistent behaviour
   across all VARCHAR-backed types; max_len always means "VARCHAR(N)".

5. **`default_auto: EMPTY_JSON_*` tightened to `type: JSON`.**
   Previously allowed on TEXT/CHAR as a stopgap; those paths emitted
   `'[]'` / `'{}'` into plain text columns where the DB couldn't
   validate JSON shape. Now EMPTY_JSON_* is compatible only with
   `type: JSON` (string or bytes carrier), which ensures JSONB
   storage on PG and JSON on MySQL.

**Scope carved for iter-1.6.** The `AUTO` type value + CARRIER_MAP +
CARRIER_LIST + element-typing for `repeated <scalar>` are a separate
batch — bigger scope (new carriers, new dispatch rules), coherent as
its own mini-iteration.

### D12 — FK relocation + deletion_rule enum (added 2026-04-21, supersedes D8's `orphanable` half)

**Decision.** Two changes to the FK vocabulary:

1. **`fk` moves from `(w17.field)` to `(w17.db.column)`.** A foreign
   key is a *DB-engine rule* in the same family as indexes, CHECK
   constraints, and `(w17.db.column).index` — not a general "what
   shape is this field" semantic. `(w17.field)` shrinks to pure data
   shape (type, null, blank, validators, default, max_len/min_len,
   precision/scale, choices, unique, pk, immutable); `(w17.db.column)`
   carries the DB-rule knobs (`name`, `index`, `fk`, `deletion_rule`).
   The resulting authoring surface reads as two coherent layers
   instead of one kitchen-sink option message.

2. **`orphanable: optional bool` is replaced by `deletion_rule: enum`
   on `(w17.db.column)`.** The Django-parity audit in the iter-1
   polish cycle surfaced real schemas that need DB-level RESTRICT
   (`BLOCK`) and SET DEFAULT (`RESET`); `orphanable`'s CASCADE/SET NULL
   binary was too narrow. The enum values stay in the noun-shape,
   non-hook idiom D8 established:

   ```
   enum DeletionRule {
     DELETION_RULE_UNSPECIFIED = 0;  // inferred: null:true → ORPHAN, else CASCADE
     CASCADE = 1;  // child deleted with parent
     ORPHAN  = 2;  // child's FK becomes NULL (requires null: true) — renamed SET NULL
     BLOCK   = 3;  // parent delete refused (SQL: RESTRICT)
     RESET   = 4;  // child's FK becomes its declared default (SQL: SET DEFAULT; requires default_*)
   }
   ```

   `ORPHAN` preserves the `orphanable` idiom (a property of the child
   row — "does it get orphaned?") now as an enum variant. `BLOCK` and
   `RESET` are verbs of the rule itself, never of an event-handler.
   Under no circumstance does the vocabulary grow an `on_*` prefix.

**Rationale.**

1. **The 2026-04-21 Django-parity audit invalidated D8's "PROTECT is
   app-level" stance.** The argument was sound when we were thinking
   about cascade sequences as application-invariant enforcement — and
   the platform's static analysis will still own the cross-domain
   "should this delete even be allowed" question. But DB-level
   `RESTRICT` is a different guarantee: it survives bypassing the
   application entirely (direct SQL, read replicas with write access,
   ops engineers running `DELETE` during incident response). Schemas
   that rely on this guarantee — invoicing, audit trails, historical
   records — need it at the DB layer, not the app layer. D8 was wrong
   to park it.

2. **`fk` was always a DB concept, never a field concept.** The
   original placement on `(w17.field)` was a convenience, not a design
   call. A field described by a proto has no FK semantics — FKs are
   constraints that only exist at the DB layer. Moving `fk` to
   `(w17.db.column)` makes the layer boundary honest: `(w17.field)` is
   the authoring surface a form builder / API validator / Django
   admin can interpret; `(w17.db.column)` is the surface a migration
   generator interprets. Both layers consume `(w17.field)`; only the
   migration generator consumes `(w17.db.column)`.

3. **Zero-backcompat skeleton stage.** No real users, no published
   fixtures outside the repo, no deployments. The vocabulary change is
   a single-commit rewrite — relocating `fk`, removing `orphanable`,
   naturalising every fixture. Doing this after iter-1 signs off
   would be a painful v2 migration; doing it now is a morning's work.

4. **Validator gates match the enum semantics.** `ORPHAN` rejects
   without `null: true` (can't SET NULL a NOT NULL column). `RESET`
   rejects without a `default_*` value (SET DEFAULT with no default is
   a contradictory clause). `BLOCK` and `CASCADE` have no additional
   requirements. Matches the principle "validate at IR, not at PG
   apply, so the diag points at the proto source".

**Inference (when deletion_rule is unspecified).** To keep the
"reasonable default" feel of the old `orphanable` inference: a nullable
FK column is assumed orphanable (`null: true` → `ORPHAN`); a non-null
FK column is assumed cascading (`null: false` → `CASCADE`). Explicit
`deletion_rule` overrides inference in every direction — you can spell
`null: true, deletion_rule: CASCADE` and get cascade on a nullable FK
if that's what you want.

**Parked implementation work (iter-2+).** `irpb.Column` needs a
`int32 number` field, populated from `protoreflect.FieldDescriptor.Number()`
in `ir.Build`. Iteration-1 does not consume it (differ only handles
`prev == nil`), so the addition is deferred to whichever rev first
tackles non-trivial alter-diff. The IR is proto, so adding the field
later is wire-compat; there's no forcing function to do it upfront.
