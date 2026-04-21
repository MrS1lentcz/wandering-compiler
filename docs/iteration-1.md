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
