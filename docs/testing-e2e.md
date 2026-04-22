# End-to-End Testing — Framework + Scope

Companion to `docs/iteration-1-coverage.md` (which covers unit /
golden / apply-roundtrip tests). **E2E** here means scenarios that
exercise the compiler + generated migration **against a real
target database in various real-world states** — right PG
version, wrong PG version, missing extension, concurrent apply,
degraded permissions, and other "operator reality" conditions
that unit tests and the simple up→down→up apply roundtrip don't
cover.

**Status (2026-04-22):** scaffold + coverage list — no runtime
harness yet. Full implementation parks with the hosted platform /
deploy-client iteration (see
`docs/experiments/_parked/migration-delivery.md` + iter-2
backlog). Reason: most of these scenarios are about what happens
*between* the compiler and the target DB — the deploy client is
where pre-flight checks belong, and building the E2E harness
before the deploy client exists would bake in assumptions we're
going to reshape.

---

## What the existing test layers cover

| Layer | Scope | Command |
|---|---|---|
| Unit tests | Per-function invariants, table-driven carrier×sem matrix, helper output | `cd srcgo && go test ./...` |
| Golden fixtures (M8) | Proto → IR → plan → SQL byte-for-byte stability across 26 shapes | `go test ./domains/compiler/ -run TestGoldens` |
| Apply roundtrip (M9) | Every golden runs up → down → up against ephemeral `postgres:18-alpine` with `hstore` + `citext` + `pg_trgm` extensions pre-loaded and `reporting` schema pre-created | `make test-apply` |

Coverage after the D23+ hardening sweep: **~88.5%** across core
packages (ir, emit/postgres, loader, plan, writer, naming, diag,
application).

---

## What E2E scenarios need to cover (iter-2+ work)

### 1. Target-version matrix

The capability catalog (D16) declares `MinVersion` per feature.
E2E validates the compiler + apply behaviour across:

  - **PG 14** (iter-1 floor per AC #2)
  - **PG 15** — prior-major
  - **PG 16** — current major minus two
  - **PG 17** — current major minus one
  - **PG 18** — current (what `make test-apply` uses today)

Each fixture runs through every version image. Expected outcomes
split per capability: e.g. `uuidv7()` applies natively on PG 18,
needs `pg_uuidv7` extension on PG 14–17 (and so a different code
path from the deploy client).

### 2. Extension availability

Every `(w17.pg.field).required_extensions` declares a dependency
that's not in the core PG distribution. E2E should cover:

  - Extension **available** and installed → apply succeeds.
  - Extension **missing** → apply surfaces PG's error with a
    clean diagnostic (not a stack trace). Exercises
    `CREATE EXTENSION IF NOT EXISTS` idempotence.
  - Extension installed **in the wrong schema** (`hstore` in
    `public` vs `extensions`) — search_path resolution boundary
    case.

Extensions in scope for iter-1 output: `hstore` (map<string,
string>), `citext` (db_type: CITEXT), `pg_trgm` (opclass
gin_trgm_ops), `pg_uuidv7` (uuidv7() on PG 14–17), `btree_gin` /
`btree_gist` (mixed index-type queries — future).

### 3. Deprecated / removed features

PG occasionally drops features or changes semantics. Covers:

  - Features that **changed** between versions (e.g. HASH index
    WAL-logging since PG 10 — pre-10 apply would silently lose
    data on crash).
  - Features that **were removed** (Berkeley DB bindings,
    server-side Python 2, …). Unlikely to affect us but the
    matrix should surface it.
  - Features **deprecated with planned removal** (e.g. `xml2`
    contrib, `contrib/spi`) — warn the operator before they
    break.

### 4. Concurrent / partial-apply conditions

  - Another migration applies concurrently → lock collision; PG
    retries / the deploy client resolves.
  - Apply interrupted mid-script → transactional DDL means
    everything rolls back (PG feature); E2E verifies the
    post-rollback state matches pre-apply.
  - `CREATE INDEX CONCURRENTLY` escape hatch (iter-2+ via
    raw_indexes or a dedicated flag) — cannot wrap in BEGIN;
    how do we surface that to authors?

### 5. Permission degradation

  - Role **without CREATE TABLE** on the target schema.
  - Role **without CREATE EXTENSION** on the target DB (common
    in managed PG like RDS).
  - Role **without CREATE SCHEMA** (D19 requires the schema to
    pre-exist).

Each scenario asserts: compiler output unchanged (still emits
the declarative DDL), apply fails with a clean role-permission
error, deploy client surfaces which GRANT the operator needs.

### 6. Cross-schema FKs (D19 multi-domain — iter-2+)

When cross-module FK syntax lands, E2E covers:

  - FK from module `orders` to module `auth` across schemas →
    `REFERENCES auth.users(id)`.
  - Missing GRANT SELECT on referenced schema → apply error.
  - Schema rename in flight → referential integrity.

### 7. Data survival through alter-diff (iter-2 M1)

The big one. Alter-diff operations that preserve data:

  - Add nullable column → no data loss.
  - Drop column → data gone (intentional); confirm rollback
    doesn't resurrect.
  - Rename column (detected via proto field number per D10) →
    data preserved.
  - Change column type with compatible cast → USING clause
    required.
  - Change column type with incompatible cast → refuse or
    require explicit `using_expr:` escape hatch.
  - Widen VARCHAR(80) → VARCHAR(200) → safe.
  - Narrow VARCHAR(200) → VARCHAR(80) → data-dependent; refuse
    or require explicit `truncate: true`.
  - Add CHECK constraint to populated table → fails with
    existing violating rows; operator either cleans data or
    adds `NOT VALID` + `VALIDATE CONSTRAINT` two-step.

### 8. Dialect portability (iter-2+ MySQL / SQLite emitters)

Same authoring proto → emit for each dialect → apply to each
dialect → assert equivalent semantics.

---

## Harness approach (sketch)

Go test harness that orchestrates Docker via `testcontainers-go`
or similar. Each E2E case:

1. Boots a PG container with specified image version +
   extensions + permission profile.
2. Optionally runs `setup.sql` for preconditions (e.g. seed
   data before an alter-diff scenario).
3. Invokes `wc generate` to produce the migration.
4. Applies the migration (or asserts it fails with a specific
   message).
5. Runs `assertions.sql` — queries that verify post-apply
   state (row counts, column types, index existence, etc.).
6. Optionally runs down-migration + asserts rollback state.

Fixture layout proposal:

```
srcgo/domains/compiler/testdata-e2e/
  scenario-<name>/
    input.proto                 # authoring surface
    target.yaml                 # pg_version, extensions, role
    setup.sql                   # optional preconditions
    expected-apply-ok.sql       # post-apply assertions (when success)
    expected-apply-error.txt    # expected error substring (when failure)
    README.md                   # scenario narrative (why this case matters)
```

Harness: one Go test per scenario, resolved via
`discoverScenarios("testdata-e2e")` parallel to the existing
`TestGoldens` shape.

---

## Why defer the harness build

Three reasons:

  1. **Deploy client is where pre-flight checks belong.** Most
     E2E scenarios (extension presence, role permissions,
     version compatibility) are things the deploy client
     should check before the migration reaches the DB. Building
     the compiler-side harness first would duplicate the
     logic — we want that work to live in the deploy client,
     and the E2E harness to exercise *that layer*.

  2. **Alter-diff is iter-2 M1.** The biggest value of E2E —
     data survival through schema changes — can't land until
     alter-diff does. Running E2E only against `AddTable` ops
     is a tiny fraction of the scenarios worth covering.

  3. **Capability catalog is already the source of truth.**
     D16 + per-dialect catalogs encode the version /
     extension matrix declaratively. Iter-2's "local schema
     validator" (see iter-2-backlog and the project memory)
     replays migrations against the IR + catalog without
     Docker — cheaper and more complete for most of what E2E
     would check anyway. Real-DB E2E becomes the
     **integration** layer, not the **validation** layer.

---

## Iter-1 vs iter-2+ split

**Iter-1 (done):** apply-roundtrip harness in `Makefile` covers
up→down→up against one PG version, one extension set, one
schema. Exhaustive on fixture shapes; narrow on target-DB
variance. Good enough until alter-diff + deploy client arrive.

**Iter-2+ (planned):**
  - **M1 (alter-diff):** cover data-survival scenarios first —
    they're the highest-stakes E2E class.
  - **M-deploy:** deploy client + local schema validator — most
    target-DB variance handled here, E2E just confirms the
    validator matches real-DB behaviour.
  - **M-multi-dialect:** once MySQL / SQLite emitters exist,
    E2E becomes the tri-dialect compatibility test (same proto
    → three SQL outputs → three apply results, cross-checked).

---

## Quick reference

| Command | Purpose |
|---|---|
| `cd srcgo && go test ./... -cover` | Full unit + golden + apply-roundtrip with per-package coverage |
| `go test ./... -coverpkg=./domains/compiler/... -coverprofile=/tmp/cov.out && go tool cover -func=/tmp/cov.out \| tail` | Cross-package coverage profile + summary |
| `make test-apply` | Ephemeral PG 18 apply roundtrip over every fixture |
| — (future) `make test-e2e` | Matrix of scenarios × PG versions × extensions × roles |
