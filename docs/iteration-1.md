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

- **Proto carriers:** `string`, `int32`, `int64`, `bool`, `double`, `google.protobuf.Timestamp`.
- **Semantic `type` enum (in `(w17.field)`):**
  - Strings: `CHAR`, `TEXT`, `UUID`, `EMAIL`, `URL`, `SLUG`.
  - Numbers: `NUMBER`, `ID`, `COUNTER`, `MONEY`, `PERCENTAGE` (0–100), `RATIO` (0–1).
- **`(w17.db.table)` options:** `name`, `indexes` (single or multi-column, unique or not). No auto-generated fields — every DB column is a declared proto field.
- **`(w17.field)` options:** `type` (required), `pk`, `fk`, `immutable`, `null` (default `false` → NOT NULL + required), `blank` (string-only, default `false` → `CHECK (col <> '')`), `max_len` (for `CHAR`/`SLUG`).
- **`(w17.validate)` options:** `min_len`, `max_len`, `gt`, `gte`, `lt`, `lte`, `pattern`.
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
4. Default values & timestamp ergonomics (whether to introduce field-level
   `default` / `on_update` annotations so explicit timestamp fields are
   usable without hand-written INSERTs).
5. ~~Ent boundary~~ — **resolved**, see Decisions below (D4).

## What "done" looks like

The iteration closes when:

- All seven acceptance criteria pass in CI.
- The pilot project has been migrated and its maintainers have signed off.
- All five original open questions have written answers. #1, #3, and #5 are
  resolved (D5, D1, D4). #2 and #4 remain — both are minor-dimension tuning
  (CHECK default verbosity, timestamp ergonomics) and can close once a pilot
  exercises them.

## Decisions

### D1 — Default nullability (resolves open question #3)

**Decision.** Every field is `NOT NULL` + required by default. The opt-out is
`(w17.field) = { null: true }`, which:

1. drops `NOT NULL` on the SQL column, and
2. emits the field as proto3 `optional` in the internal proto (so field
   presence survives the wire format — without `optional`, a scalar's zero
   value and "not set" would be indistinguishable and the nullable-column
   signal would be lost).

`required` is removed from `(w17.validate)` — it would be redundant with
`null`.

**Rationale.** `null` already answers both questions that a `required` flag
would have answered: whether the DB column is nullable, and whether the
validator rejects missing input. Keeping them as one knob keeps the authoring
surface small and eliminates the `null=false, required=false` combination,
which had no coherent meaning.

**String-specific companion:** `blank: true` (string fields only) allows
empty strings through. `null` and `blank` are orthogonal — `null` is about
"may the value be absent", `blank` is about "if the value is present, may
it be `''`". Default for both is `false`.

### D2 — Semantic type enum (supersedes earlier ad-hoc string/number handling)

**Decision.** `(w17.field)` carries a required `type` enum that refines the
proto carrier into a SQL column and a default set of CHECK constraints.

- String carriers: `CHAR`, `TEXT`, `UUID`, `EMAIL`, `URL`, `SLUG`.
- Number carriers: `NUMBER`, `ID`, `COUNTER`, `MONEY`, `PERCENTAGE` (0–100
  "human" scale), `RATIO` (0–1 mathematical fraction).
- `bool` and `google.protobuf.Timestamp` have no semantic subtype yet.

The mapping to SQL and the implicit CHECK constraints are tabulated in
`docs/experiments/iteration-1-models.md`.

**Rationale.** Django-style: data refinement and basic validation in one
annotation, human-readable, and it avoids scattering the same constraints
across `(w17.validate)` for every email/slug/uuid field. `(w17.validate)`
stacks additional constraints (`gt`, `lte`, `pattern`, …) on top.

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

### D4 — Own IR + differ + per-dialect emitters (resolves open question #5)

**Decision.** The compiler owns its whole migration pipeline. Ent and Atlas
are **not used**. The pipeline has four layers:

1. **IR:** dialect-agnostic Go types — `Schema` / `Table` / `Column` /
   `Check` (tagged union: `LengthCheck`, `BlankCheck`, `RangeCheck`,
   `RegexCheck`) / `Index` / `ForeignKey`. Checks carry semantic intent, not
   SQL strings — the emitter renders them per dialect.
2. **Differ:** `Diff(prev, curr *Schema) *MigrationPlan` → ordered `Op`s
   (`AddTable`, later `DropTable`, `AddColumn`, `AlterColumn`, …).
   Iteration-1 handles only `nil → Schema` (initial migration), which reduces
   to one `AddTable` per table. Alter/rename/type-change ops are added
   iteration-by-iteration as pilot projects surface real needs.
3. **SQL emitter:** per-dialect, behind a `DialectEmitter` interface.
   Iteration-1 ships only the Postgres emitter. Adding MySQL / SQLite later
   is a new file, not a rearchitecture.
4. **Sibling consumers of the plan (later iterations):** back-compat lint
   (e.g., "AddColumn NOT NULL without default on non-empty table"),
   changelog generator, schema visualization. All three operate on
   `MigrationPlan.Ops`, so they are dialect-agnostic for free.

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
