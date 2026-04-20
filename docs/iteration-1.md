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

1. An internal (clean) proto under `gen/proto/` with `w17.*` annotations
   expanded into regular proto fields.
2. A pair of plain-SQL migration files (`*.up.sql` + `*.down.sql`) that create
   the table, indexes, constraints, and triggers against Postgres.
3. An ent schema under `gen/ent/` used exclusively for migration generation —
   never imported by runtime code.

The pilot example is shown in `docs/experiments/iteration-1-models.md`.

## In scope

- **Types:** `string`, `int32`, `int64`, `bool`, `double`, `google.protobuf.Timestamp`.
- **`(w17.db.table)` options:** `name`, `timestamps`, `soft_delete`,
  `indexes` (single or multi-column, unique or not).
- **`(w17.field)` options:** `pk`, `fk`, `immutable`.
- **`(w17.validate)` options:** `min_len`, `max_len`, `min`, `max`, `required`.
- **Output layer:** Postgres 14+ SQL. Tested against a real Postgres instance
  (SQLite acceptable for local dev loops only).
- **Determinism:** same input always produces byte-identical output.

## Out of scope (deferred)

- Projections (`w17.schema.projection`) — moved to a later iteration.
- Query DSL, storage gRPC, standard gRPC, facade APIs, events.
- Rich types: `decimal`, `bytes`, `jsonb`, `repeated`, `oneof`, nested messages.
- Cross-module FKs resolved via package paths (iteration 1 uses plain
  `"<table>.<column>"` strings).
- Cross-domain references via `common/`.
- `immutable` runtime enforcement (the annotation is recorded for future
  iterations but not enforced in SQL).
- Schema-evolution detection beyond what `ent` gives us out of the box.
- UI metadata, admin generation, JS/TS clients, docker/k8s scaffolding.

## Acceptance criteria

1. `wc generate --iteration-1 path/to/product.proto` emits `gen/proto/`,
   `gen/ent/`, and `migrations/<ts>_create_products.{up,down}.sql`.
2. The generated migration applies cleanly against a fresh Postgres 14
   instance via `psql -f`.
3. The generated migration rolls back cleanly via the `.down.sql`.
4. Running the generator twice on unchanged input produces **byte-identical**
   output files (no timestamps or nondeterministic IDs in the content).
5. A golden-file test suite covers the pilot `product.proto` and at least two
   additional shapes: a table with no indexes, and a table with multi-column
   unique constraints.
6. One pilot project (chosen from `docs/conventions-global/`) replaces its
   hand-written migration for one table with the generated one, without any
   behavioral regression.

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

1. Migration naming scheme (timestamp vs. incremental vs. hash-chain).
2. Validation surface in SQL (`CHECK` constraints vs. application-layer only).
3. Default nullability (proposal: NOT NULL unless opted out).
4. Default values table (zero values vs. explicit `(w17.field) = { default }`).
5. Ent boundary (full ent+Atlas pipeline vs. own diff against stored snapshot).

## What "done" looks like

The iteration closes when:

- All six acceptance criteria pass in CI.
- The pilot project has been migrated and its maintainers have signed off.
- The five open questions have written answers in this document (moved from
  "open" to a "Decisions" section below).

## Decisions

*(empty — to be filled as open questions are resolved)*
