# Session Handoff — Schema-Gap Close-Out Before Alter-Diff

**Saved:** 2026-04-22 (end of long session). Next session resumes from here.
**Last commit:** `1386a83` — D16 dialect-capability catalog.

---

## Where we are

Iter-1 is **functionally closed** — AC #1–#7 green, 15 positive fixtures
pass M8 (goldens) + M9 (apply-roundtrip on PG 18), 26 error fixtures
pass `TestBuildErrors`, decision records D1–D16 all documented.

But before opening **alter-diff (iter-2 M1)** we agreed to close
remaining schema-declaration gaps so the diff has the most complete
IR to work against. **This handoff captures the agreed batch and its
current progress.**

See also:
- `docs/iteration-1-impl.md` — authoritative Status block + full
  milestone-by-milestone history.
- `docs/iteration-1-coverage.md` — Django parity audit + test
  coverage.
- `docs/iteration-2-backlog.md` — iter-2 candidate list (includes
  what's being parked below).

---

## Agreed batch — 4 features to land before alter-diff

Each is a standalone commit for review hygiene. Order reflects
user preference (ENUM first, user's highlighted design).

> **Reorg note (2026-04-22 session).** The original four-feature
> batch (ENUM / generated / schema-qualified / index sort) shipped
> the first two as D17 + D18. The remaining two were reshaped: the
> "schema-qualified" slot grew to cover **namespace as a module-
> level binary choice (schema XOR prefix)** — now D19. The "index
> sort order" slot has been **pulled OUT of this batch** and folded
> into a broader "indexes + constraints dynamic redesign" (new D23)
> that lands right after D22, replacing the piecemeal D20 shape.
> D21 (default table name) + D22 (optional bundle: COMMENT ON +
> path-family presets + MAC_ADDRESS + SMALL_INTEGER) slot in
> between. See the Progress block at the bottom for the canonical
> order — the section below preserves the original D19/D20
> design-detail text for historical context only.

### 1. ENUM type — **NEXT ACTION, not started**

**Design (agreed):**

Dispatch per carrier:

| Carrier | Wire | Storage | CHECK |
|---|---|---|---|
| `string` | enum value name | PG `CREATE TYPE` + column of that type | — (type enforces) |
| `int32`/`int64` | enum value number | `INTEGER` / `BIGINT` | `CHECK col IN (numbers)` |
| proto-enum field (`Status s = 1;`) | varint int32 | INTEGER + CHECK IN numbers (auto-inferred) | auto-inferred from descriptor |

Two user paths:

```proto
// Case A: proto enum field — sem auto-inferred
enum Status { STATUS_UNSPECIFIED = 0; DRAFT = 1; PUBLISHED = 2; }
Status state = 1;
// → carrier INT32, sem ENUM, choices auto = "pkg.Status"
// → INTEGER NOT NULL CHECK (state IN (1, 2))

// Case B: string-backed with PG ENUM storage
string state = 2 [(w17.field) = { type: ENUM, choices: "pkg.Status" }];
// → carrier STRING, sem ENUM, explicit choices
// → CREATE TYPE posts_state AS ENUM ('DRAFT', 'PUBLISHED'); ... state posts_state NOT NULL
```

**Implementation outline:**

1. Proto:
   - Add `ENUM = 50` to `Type` in `proto/w17/field.proto`.
   - Add `SEM_ENUM = 50` to `SemType` in `proto/domains/compiler/types/ir.proto`.

2. IR (`srcgo/domains/compiler/ir/build.go`):
   - `protoKindToScalarCarrier`: handle `protoreflect.EnumKind` → `CARRIER_INT32`
     (wire type is int32 for proto enums).
   - `buildColumn`: when `fd.Kind() == EnumKind` and no explicit type,
     auto-set `semType = SEM_ENUM` + populate `col.Choices` = enum FQN
     from `fd.Enum().FullName()`.
   - `validateCarrierSemType`: `SEM_ENUM` valid on string / int32 / int64.
   - On string carrier + `SEM_ENUM`: validate `choices:` is set and
     resolves (reuse `resolveEnumValues` helper).
   - Track numeric values alongside names (we already have
     `values.Get(i).Number()`).
   - `attachChecks`: on int carrier + `SEM_ENUM`, synth `CHECK IN (1, 2, …)`
     instead of `CHECK IN ('A', 'B', …)` (which is today's choices behavior).

3. Emitter (`emit/postgres/`):
   - `columnType`: for string + `SEM_ENUM`, emit column type as
     `<table>_<col>` (the ENUM type name). Requires `tableName` in
     scope — currently `columnType(col)` doesn't have it. Thread
     `t.Name()` through or emit enum type name via dedicated helper.
   - `emitAddTable`: **prepend** `CREATE TYPE <table>_<col> AS ENUM (…)`
     statements for every ENUM column on string carrier (before
     `CREATE TABLE`).
   - Down: **append** `DROP TYPE IF EXISTS <table>_<col>;` after
     `DROP TABLE IF EXISTS <table>;`.
   - For int + `SEM_ENUM`: normal INTEGER/BIGINT + the CHECK IN
     emitted from `attachChecks` (already handles ChoicesCheck path).
   - Type name validation: `validateIdentifier` for the derived
     type name (same as table + constraint names, D14 pattern).

4. Capability catalog (`emit/postgres/capabilities.go`):
   - Add `emit.CapEnumType = "ENUM_TYPE"` to emit/capabilities.go.
   - Catalog entry: `{MinVersion: "8.3"}` for PG (CREATE TYPE AS ENUM
     landed in PG 8.3).

5. Fixtures:
   - `enum_string_backed` — proto enum with string carrier + PG ENUM storage.
   - `enum_int_backed` — proto enum field (Case A) and/or explicit int+ENUM.
   - Error fixtures:
     - `enum_requires_choices.proto` — `type: ENUM` without `choices:` on string.
     - `enum_on_bool_carrier.proto` — `type: ENUM` on non-valid carrier.

6. Plan (`plan/diff.go`):
   - No changes for iter-1 close-out (ENUM CREATE TYPE is inline in the
     AddTable op's SQL, not a separate plan Op). Alter-diff may split
     this later.

7. Docs:
   - New decision record **D17** in `iteration-1.md` — "ENUM type:
     carrier-dispatched storage".
   - Update Preset Bundles matrix with ENUM row.
   - Update Status block in `iteration-1-impl.md`.

**Open design question deferred per user:** default sem type for a
bare proto-enum field (no `(w17.field)` annotation). User said
"pozdeji rozhodneme, to je mala vec". For iter-1 close-out either:
- (a) **require explicit annotation** — `Status s = 1 [(w17.field) =
  { type: ENUM, choices: "..." }]` always needed, even though the
  proto field IS an enum. Conservative.
- (b) **auto-infer** — bare `Status s = 1;` defaults to INTEGER +
  CHECK IN numbers (matches proto wire). User opts into string+PG-ENUM
  via `string s = 2 [type: ENUM, choices: ...]`.

Recommend **(b)** — matches D14 zero-config philosophy + proto wire
semantics. But double-check with user before shipping.

---

### 2. Generated columns — queued

```proto
string full_name = 3 [
  (w17.field)     = { type: CHAR, max_len: 200 },
  (w17.db.column) = { generated_expr: "first_name || ' ' || last_name" }
];
```

PG emit: `full_name VARCHAR(200) GENERATED ALWAYS AS (first_name || ' ' || last_name) STORED`.

**Design points:**
- `generated_expr: string` on `(w17.db.column)` — opaque SQL (like
  raw_checks body). IR passes through verbatim.
- Validation: `generated_expr` set → `default_*` forbidden, `pk`
  forbidden (PG rejects generated PKs), `fk` forbidden (generated
  columns can't be FK sources in PG), `unique` allowed, nullable
  allowed.
- Emitter: after column type + NOT NULL, emit
  `GENERATED ALWAYS AS (expr) STORED` before any FK/DEFAULT.
- Per-dialect: PG 12+ supports `STORED` (we target PG 18, fine).
  MySQL 5.7+ has VIRTUAL + STORED. Iter-1 PG only.
- Capability: `emit.CapGeneratedColumn = "GENERATED_COLUMN"`,
  `MinVersion: "12.0"` in PG catalog.

**Scope:** ~80 LOC + 1 fixture + 2-3 error fixtures (no default+gen,
no pk+gen, no fk+gen).

---

### 3. Schema-qualified names — queued

```proto
option (w17.db.table) = { name: "events", schema: "reporting" };
```

PG: `CREATE TABLE reporting.events (...)`. Indexes + FKs in SQL also
reference `reporting.events`. Rollback `DROP TABLE reporting.events`.

**Design points:**
- `(w17.db.table).schema: string` — optional. When empty, emit bare
  `CREATE TABLE <name>` (no change to existing fixtures, no golden
  churn).
- IR: `Table.schema: string` field.
- Emitter: prefix `schema.` to every TABLE identifier when schema set.
- Name validation: schema name subject to NAMEDATALEN + reserved
  check independently (not combined with table name — they're
  separate identifiers).
- Cross-schema FKs in iter-1: allowed (PG doesn't care). Differ
  (iter-2) handles schema moves via `ALTER TABLE ... SET SCHEMA`.

**Scope:** ~60 LOC + 1 fixture + 1 error fixture (reserved schema
name). No existing-fixture regeneration because the default is
unchanged (empty schema = bare name).

---

### 4. Index sort order + NULLS FIRST/LAST — queued

Django: `Index(fields=['-created_at'])` (DESC), with nulls default
per-dialect.

**Design — BREAKING CHANGE:** `(w17.db.table).indexes[].fields` changes
from `repeated string` to `repeated IndexColumn`:

```proto
message IndexColumn {
  string name = 1;     // proto field name
  bool   desc = 2;     // ORDER BY DESC (default ASC)
  NullsOrder nulls = 3;  // UNSPECIFIED | FIRST | LAST
}
enum NullsOrder {
  NULLS_UNSPECIFIED = 0;
  NULLS_FIRST       = 1;
  NULLS_LAST        = 2;
}
```

PG emit: `CREATE INDEX x ON t (col1 DESC NULLS FIRST, col2);`.

**Design points:**
- IndexColumn used in `Index.fields` AND possibly `Index.include`
  (though INCLUDE doesn't really support ordering — keep `include`
  as `repeated string` for simplicity).
- Existing fixtures with custom indexes (product, multi_unique,
  fks_parent_child, pg_dialect, m2m_join — all with
  `indexes: [{ fields: ["a", "b"] }]`) migrate to the new shape.
  Since skeleton stage, break and re-regenerate goldens.
- `raw_indexes` unchanged — opaque body handles any ordering.
- Capability: `emit.CapIndexNullsOrder` already covered under
  existing btree functionality (PG has supported per-column ASC/DESC
  + NULLS FIRST/LAST since 8.3). No new catalog entry needed unless
  we want explicit tracking.

**Scope:** ~120 LOC + 1 new fixture (all-variants) + regenerate ~5
existing fixtures' input.proto.

---

## Parked to `iteration-2-backlog.md`

These stay in the backlog doc under the corresponding section:

- **EXCLUDE constraints** (PG-only, niche — booking non-overlap);
  raw_indexes covers most real cases.
- **RLS (Row-Level Security) policies** — needs auth-role context
  iter-1 doesn't have; pairs with platform + deploy client (iter-2+).
- **DOMAIN types** — `custom_type: "my_domain"` escape hatch
  already covers.
- Already-parked from prior decisions: COMMENT ON (pairs with
  admin-gen), db_collation, db_tablespace, deferrable constraints,
  range fields preset cluster, MAC_ADDRESS preset, immutable runtime
  enforcement via triggers, auto_now-on-UPDATE via triggers, JSON
  schema validation.

---

## Cross-cutting reminders for the next session

1. **Capability catalog discipline.** Every new SQL construct the
   emitter uses must have a cap ID constant in
   `emit/capabilities.go` + a catalog entry in
   `emit/postgres/capabilities.go`. Tests enforce coverage.

2. **D-record per feature.** Each of ENUM / generated / schema /
   index-order lands as a D-record (D17, D18, D19, D20 in order) in
   `iteration-1.md`. Rationale + spec behavior.

3. **Error-fixture diag discipline.** Each new validation path gets
   an error fixture in `ir/testdata/errors/` + a case in
   `TestBuildErrors` with `file:`, `why:`, `fix:` substring
   assertions.

4. **Apply-roundtrip.** `make test-apply` must stay green on every
   fixture after each feature lands. Re-run after each commit.

5. **Commit hygiene.** One feature = one commit. Clear message
   summarising spec + code + doc + tests.

6. **Skeleton stage = no backcompat.** Breaking changes to proto /
   IR are fine as long as fixtures migrate. User's explicit stance
   ("backcompat se bude resit za rok").

---

## Quick sanity commands

```bash
# Full test suite
cd srcgo && go test ./...

# Regenerate goldens after an intentional output change
cd srcgo && go test ./domains/compiler/ -run TestGoldens -update

# Apply roundtrip on PG 18 (requires Docker)
make test-apply

# Rebuild pb from proto changes
make schemagen
```

---

## Progress — resume here next session

- [x] Iter-1 closed through D16 (capability catalog + inspection interface)
- [x] `iteration-2-backlog.md` captures all parked items
- [x] `iteration-1-coverage.md` captures Django parity + test coverage
- [x] D17 (ENUM type, shipped 2026-04-22; open question resolved as
      option b — auto-infer on bare proto-enum fields)
- [x] D18 (Generated columns, shipped 2026-04-22; STORED-only per
      PG 18 surface, VIRTUAL parked for multi-dialect iter-2+)
- [x] D19 (namespace = schema XOR prefix via `(w17.db.module)`
      FileOptions extend; module-immutable, no per-message override;
      PREFIX baked into IR, SCHEMA qualifies at emit time; shipped
      2026-04-22)
- [x] D21 (default table name = `snake_case(message.local_name)`,
      no pluralisation, no package-derived prefix; reserved-keyword
      clashes surface at IR time with derivation-specific fix;
      composes with D19 namespace; shipped 2026-04-22)
- [x] D22 (ergonomic bundle — D22a COMMENT ON auto-from-doc-string
      + override annotation; D22b MAC_ADDRESS preset on MACADDR /
      VARCHAR override; D22c SMALL_INTEGER preset on int32 →
      SMALLINT; D22d path family POSIX_PATH / FILE_PATH /
      IMAGE_PATH with `extensions` list + `*` wildcard; shipped
      2026-04-22 as four commits under one D-record)
- [x] D23 (indexes structured: `repeated IndexField` with
      desc/nulls/opclass + `method` enum + free-form `storage`
      map; partial/expression indexes park to DQL iteration via
      `raw_indexes`; HASH/GIN/GIST/BRIN/SPGIST × options
      invariants enforced IR-time; shipped 2026-04-22)
- [x] Coverage sweep + E2E framework scaffold (shipped 2026-04-22,
      commit `f2e81c3`): unit tests pushed core from 76.9% → 88.5%
      cross-package; `docs/testing-e2e.md` documents the iter-2+
      E2E scope (target-version matrix, extensions, permissions,
      alter-diff data survival, multi-dialect) + harness sketch +
      deliberate deferral of full harness build until deploy
      client + alter-diff land.

---

## Resume point — 2026-04-22 end-of-session

**Iter-1 schema-gap close-out is DONE.** Every axis iter-1 will
ever carry is shipped; the IR is closed against schema-declaration
churn. Alter-diff (iter-2 M1) can start against the most complete
IR iter-1 will ever produce, with no mid-iteration reshapes
blocking the differ work.

Commit log for the close-out sweep:
```
f2e81c3 tests: coverage sweep — 76.9% → 88.5% core + E2E scaffold doc
232c7ea D23: indexes structured — method + per-field sort/nulls/opclass + storage
73d7a61 D22 docs: consolidated D-record + Status + handoff + coverage
c36eb7d D22d: path-family presets — POSIX_PATH / FILE_PATH / IMAGE_PATH
4b2bea2 D22c: SMALL_INTEGER preset — int32 → SMALLINT
c780db7 D22b: MAC_ADDRESS preset — MACADDR native + VARCHAR override
61784e7 D22a: COMMENT ON TABLE / COLUMN — auto from proto doc-strings + override
a21bc5b ir: pin camelToSnake acronym behaviour with unit test
63443fd D21: default table name = snake_case(message.local_name)
42e8aa3 D19: module namespace — schema XOR prefix via (w17.db.module)
6b3e404 D18: GENERATED ALWAYS AS (expr) STORED on (w17.db.column).generated_expr
04cf565 D17: ENUM type — carrier-dispatched storage
```

### Where to resume next session

User's choice at session end was "clear context and continue from
here." The next substantive block is **iter-2 M1 alter-diff**.

**First step when resuming:**
1. `git log --oneline -15` to refresh commit history.
2. Read `docs/iteration-1-impl.md` trailing Status block (iter-1
   is closed — nothing more to ship there).
3. Read `docs/iteration-2-backlog.md` — alter-diff is the top of
   Big Blocks; the local-schema-validator + DQL items are the
   other two mustshave items recorded from this session.
4. Open `docs/iteration-2.md` (doesn't exist yet — create it)
   and sketch the alter-diff M1 spec. Key anchors already in
   place:
     - **D10** (`project_differ_identity.md` memory): alter-diff
       uses proto field numbers, not names — rename detection
       is free, no Ent/Atlas heuristics.
     - **Table identity**: open question per D19's "identity for
       iter-2 alter-diff" section — `MessageFqn` vs `(mode, ns,
       name)` tuple. Resolve before coding.
     - IR already carries everything (namespace, Table.Comment,
       IndexMethod, …) — no more proto reshaping for alter-diff.

### Available background work (not blocking alter-diff)

- **Coverage to 95%+**: remaining gaps are mostly defensive
  branches + `main.go` CLI entry. Doable but diminishing returns.
- **More raw-SQL edge fixtures**: EXCLUDE constraints, deferrable
  FKs — these are parked per iter-2-backlog, can be pulled
  forward if a pilot surfaces need.
- **DQL iteration** (Doctrine-like ORM) — parked per user; huge
  block, separate milestone.

### Non-negotiable reminders for next session

- Keep the escape-hatch discipline: new condition / expression
  surfaces route through `raw_*`, not structured, until DQL
  lands. (See `project_dql_planned.md` memory.)
- Keep writing per-feature D-records in `docs/iteration-2.md`
  same format as iter-1 (Decision + Invariants + Escape Hatches
  + Rationale).
- Commit hygiene: one feature = one commit. Push after each.
- Run `make test-apply` before every ship to catch regressions
  on all 26 fixtures.

---

## 2026-04-23 — Conventions + Coverage Sweep (post iter-1 close-out)

Session ran **two full phases** before alter-diff, at user's explicit
request (`/clear` context; user statement "bez toho se nehnem dal" —
without this we don't move on):

### Phase A — conventions-global compliance
Audit found 4 functions egregiously over the 50-LOC cap in
`quality.md §Code Structure`. Refactored:

- `ir.buildTable`: 313 → 20 LOC + 9 per-stage helpers
- `ir.buildColumn`: 440 → 50 LOC + 14 per-stage helpers
- `pg.columnType`: 141 → 35 LOC + 8 per-carrier helpers
- `pg.emitAddTable`: 143 → 30 LOC + 6 sub-stage helpers

Remaining 18 functions over 50 LOC are pure dispatch switches or
cohesive matrix validators — registered in
[`docs/core-functions.md`](core-functions.md) with invariant +
rationale (the "special description" clause of quality.md).

Other cleanup: `srcgo/domains/compiler/examples/iteration-1/happy.proto`
moved to conventional `cmd/cli/testdata/happy.proto`; CLAUDE.md Known
Issues updated with real deviations (core functions, missing Makefile
targets, absent `srcgo/lib/` tier).

### Phase B — coverage sweep
Baseline coverage pre-sweep: 89.0 % cross-package (the single-binary
numbers in the older handoff are misleading — they reflect only what a
given test binary executes, not the union). Closed the gap to
**97.8 %** through:

- 1 new positive fixture (`empty_schema`) + 8 existing fixtures
  extended (index_methods with BTREE/GIST/SPGIST/NULLS_FIRST,
  numeric_spectrum with default_double / default_string-on-DECIMAL,
  multi_unique with UNIQUE+INCLUDE + explicit single-col-unique
  collision, fks_parent_child with self-FK BLOCK + explicit
  single-col-index collision, pg_dialect with required_extensions,
  storage_override with rare db_types, comments with column-rename
  + non-FK index, lists_and_maps with repeated Message +
  map<string,Timestamp/Duration>, enum_int_backed with nested enum).
- **34 new error fixtures** added (26 iter-1 baseline → 60 total),
  each firing one previously-uncovered validator branch.
- 3 new unit test files: `emit/postgres/column_dispatch_test.go`
  (exhaustive per-carrier dispatch), `ir/helpers_test.go` (pure-fn
  helpers + regexpQuote + validateIdentifier + checkSuffix +
  describeKind + resolveComment), plus extensions to
  `writer_test.go` (3 IO-error branches) and `loader_test.go`
  (nonexistent-file path).
- `srcgo/cover-all.sh` merge script for accurate cross-package
  coverage with `-coverpkg` (single `go test ./... -coverpkg=…`
  reports union but per-package binaries don't see each other).

Remaining ~2.2 % gap documented in
[`iteration-1-coverage.md §3.3-bis`](iteration-1-coverage.md) as
three structural exception categories:
1. `log.Fatalf` paths in `Config.NewConfigFromEnv` + `app.OutputDir`
   (need subprocess re-invocation; low-ROI).
2. Protoreflect defensive branches (`diag.At file==nil`,
   `loader.reparse` round-trip errors — unreachable from real
   protocompile-loaded descriptors).
3. Emit-layer `"ir invariant violated"` error returns — the IR
   validators catch the invalid combos upstream; several covered
   directly by synthetic-IR unit tests, rest remain as defence-
   in-depth.

None of these are user-visible bugs.

### Commit log for Phase A + B
```
88a0198 docs: iteration-1-coverage close-out — 97.8% + known exceptions
081091a tests: pgArrayOf error propagation from element columnType
4ae1d2c tests: NUMERIC needs precision + ENUM on list carrier
e285e31 tests: oneof + list sem mismatch + path extension edges
3244c7c tests: fk_target_column_missing + dup-synth skip branches
e2f85e5 tests: namespace keyword + enum disagreement + helper unit tests
b4be992 tests: NAMEDATALEN overflow for CHECK + ENUM type names + more
a1fa6e7 tests: regexpQuote + validateIdentifier edges + emit error paths
e0e3ae3 tests: 20 more error fixtures + loader/writer unit tests
f0e861e tests: 10 new error fixtures close remaining validator branches
d565007 tests: ir/helpers unit tests close pure-func branches
ee7d191 tests: unit suite for emit/postgres dispatch + sqlite stub + check branches
0da4ac2 fixtures: extend 7 goldens to close proto coverage gaps + empty-schema case
46ba6d6 cleanup: examples -> cmd/cli/testdata + CLAUDE.md Known Issues
4ead790 refactor: split 4 largest core fns + core-functions registry
```

### Resume point for the next session — **iter-2 M1 alter-diff**

Iter-1 close-out is now *really* done (schema-declaration IR closed,
conventions compliant, coverage at 97.8%). Next substantive block is
unchanged from the earlier handoff: **alter-diff**.

**First step when resuming:**
1. `git log --oneline -15` to refresh context (recent commits are the
   coverage sweep — iteration-1 is untouched since D23).
2. Skim `docs/iteration-1-impl.md` Status block once to refresh (no
   new iter-1 changes since the Phase-B sweep).
3. Read `docs/iteration-2-backlog.md` — alter-diff is top of Big
   Blocks; local-schema-validator + DQL are the other iter-2+
   must-haves.
4. Open (create) `docs/iteration-2.md` and sketch alter-diff M1 spec.
   Key anchors already in place:
   - **D10** (`project_differ_identity.md` memory): alter-diff uses
     proto field numbers, not names — rename detection is free, no
     Ent/Atlas heuristics.
   - **Table identity**: open question per D19's "identity for
     iter-2 alter-diff" section — `MessageFqn` vs `(mode, ns, name)`
     tuple. Resolve before coding.
   - IR now carries everything (namespace, Table.Comment,
     IndexMethod, per-field desc/nulls/opclass, storage_index,
     comments, choices, …) — no proto reshaping needed.

### Available background work (still not blocking alter-diff)

- Push 97.8 → 100 % via subprocess-based log.Fatalf tests and
  synthetic-descriptor tests for protoreflect defensive branches.
  Tracked as deliberate exceptions; pull forward only if a pilot
  shows a user-visible gap.
- More raw-SQL edge fixtures (EXCLUDE constraints, deferrable FKs)
  — parked per iter-2-backlog.
- DQL iteration — huge separate milestone, still parked.

### Non-negotiable reminders for next session (unchanged)

- Keep the escape-hatch discipline: new condition / expression
  surfaces route through `raw_*` until DQL lands
  (`project_dql_planned.md`).
- Per-feature D-records in `docs/iteration-2.md`, same format as
  iter-1 (Decision + Invariants + Escape Hatches + Rationale).
- Commit hygiene: one feature = one commit. Push after each.
- `make test-apply` before every ship — 16 fixtures on PG 18.

---

## 2026-04-24 — D28 classifier + D30 engine isolation shipped

**Status: Phase 2 + Phase 4 complete.** 10 commits in one session,
last `dfc2253`. The matrix-driven classifier + engine envelope +
adapter stack runs end-to-end; `wc generate` uses `engine.Plan` →
`FilesystemSink` with `--decide` flag support + auto-emit of
check.sql + CustomSQL splicing. See
[`iteration-2.md`](iteration-2.md) D28 / D29 / D30 sections for
the decisions + matrix.

### Commit trail (in order)

```
e9a4d74  docs: D30 engine isolation decision record
c314a79  proto: Plan envelope (Plan, Migration, ReviewFinding,
         Resolution, NamedSQL, Manifest, ColumnRef,
         FindingContext, Strategy/Severity enums)
fb54578  classifier: matrix index + 43 landmark tests
59a8a85  plan.Diff: DiffResult with classifier-driven Findings
         (cls-nil fallback preserves pre-D30 error path)
98debb1  engine: Sink + ResolutionSource interfaces +
         Memory impls + 9 tests
8ca16db  engine: FilesystemSink + CLI Decisions parser
         (18 tests covering layout + flag-syntax)
0a1c6b7  engine.Plan() top-level + cmd/cli refactor to call
         engine.Plan via FilesystemSink; COMPILER_CLASSIFICATION_DIR
         env var; 8 integration tests
cbb6a9d  classifier: exhaustive YAML coverage tests
         (253 sub-tests iterate every cell)
5edb68c  engine: --decide flag wiring + check.sql emit pipeline
         (per-FactChange classifier dispatch; NEEDS_CONFIRM cells
         emit NamedSQL in Migration.Checks[])
dfc2253  engine: CustomSQL splicing on CUSTOM_MIGRATION
         resolutions (attribution markers, rollback note)
```

### Where the engine lives

- `srcgo/domains/compiler/classifier/` — YAML loader + axis lookup
  + Fold strictness. Load once per process via `classifier.Load(dir)`.
- `srcgo/domains/compiler/plan/` — `plan.Diff(prev, curr, cls)`
  returns `*DiffResult{Plan, Findings}`.
- `srcgo/domains/compiler/engine/` — `Plan()` top-level, Sink +
  ResolutionSource interfaces, checks.go (FactChange → NamedSQL).
- `srcgo/domains/compiler/engine/memory/` — Memory impls (tests).
- `srcgo/domains/compiler/engine/filesystem/` — FilesystemSink.
- `srcgo/domains/compiler/decide/` — Decisions parser for
  `--decide` flag. Sibling to `engine/`, not underneath it —
  engine stays pure (D30); adapters live outside the engine tree.

### Classifier source of truth

`docs/classification/*.yaml` — four files:
- `strategies.yaml` — 5 strategies + ranks + DDL templates +
  governing-rule citation.
- `carrier.yaml` — 110 cells (D28.2).
- `dbtype.yaml` — 50 cells (D28.3).
- `constraint.yaml` — 56 cells (D28.1 column-level + table-level).

Reminder: **YAML wins on conflict** with the markdown tables in
`iteration-2.md`. Markdown is a rendering; YAML is authoritative
(per D28 banner). Edits go to YAML first.

### Governing rule pinned 2026-04-23

> "Types must be compatible; no silent coercion. If source data
> isn't already in the target's canonical form, author writes the
> conversion via DQL/CUSTOM_MIGRATION — compiler never guesses
> unit, encoding, or semantic intent."

Strict form check for `STRING→BOOL` ('0'/'1' only), `INT→BOOL`
(0/1 only), ISO-8601 for timestamp/duration. Unit-ambiguous casts
(TIMESTAMP↔INT, BYTES↔scalar) default to CUSTOM_MIGRATION.
`DROP_AND_CREATE` is user-opt-in only (2 exceptions in
`constraint.yaml` A7 generated_expr where PG semantics mandate it).

### Where to resume

**M4 is the natural next block.** Emitter-driven capability
tracking populates `Migration.Manifest.RequiredExtensions` +
`Capabilities`. Today manifest only carries
`AppliedResolutions` (audit trail). See `iteration-2.md`
Milestones for M4 scope; capability catalog from D16 is ready
to wire.

Other live threads:
- `wc_migrations` extensions (hash verify utility, multi-
  connection rollback).
- Makefile standard targets (pre-poc gate).
- Core-fn 100% coverage (Phase B iter-1 close-out).

**Parked (iter-3+):**
- DQL (Doctrine-inspired ORM).
- Local schema validator.

### Non-negotiable reminders

- YAML source of truth for D28 matrix. Markdown tables drift;
  YAML never.
- `engine.Plan` is pure per D30. No file I/O inside. Storage is
  Sink's concern.
- Findings are deterministic (SHA-256 of table FQN + axis + prev
  wire + curr wire). Resolutions survive re-runs idempotently.
- `make test-apply` before every ship — PG 18 apply-roundtrip
  now covers iter-1 + alter fixtures.
- CLI uses `COMPILER_CLASSIFICATION_DIR` env var to find YAMLs;
  defaults to `./docs/classification` (repo-root relative).
  Makefile test-apply sets the var; production users can
  override if they ship YAMLs elsewhere.

---

## 2026-04-25 — M4 Layer A+B + pre-Layer-C cleanup

**Status.** M4 Layer A (capability usage tracking) + Layer B
(manifest.json output) shipped. Adapter layering corrected
(`engine/cli/` lifted to `decide/` sibling per D30).
`test-apply` harness now runs the PG 14–18 matrix dynamically via
`scripts/test-apply.sh`. Pre-Layer-C coverage push from 91.6% →
~94% cross-package. Layer C (MySQL stub) explicitly paused by
user direction — infra is ready to plug in, cleanup must close
first.

### Commit trail (2026-04-25, pre-Layer-C)

```
0ccbc1d emit+engine: M4 Layer A — capability usage tracking + Manifest population
2881b12 filesystem: M4 Layer B — manifest.json alongside up/down.sql
7be9c9a decide: lift --decide flag parser out of engine/ into its own package
c13841c test-apply: matrix harness across PG 14-18 (last 5 majors)
9388203 engine+emit: lock dialect contract before MySQL stub (D32 + tests)
<pending> chore: pre-Layer-C cleanup (vet fix + coverage push + doc refresh)
```

### Where things landed

- `srcgo/domains/compiler/emit/usage.go` — collector passed through
  `DialectEmitter.EmitOp(op, *Usage)`; nil-safe; records
  TRANSACTIONAL_DDL once per non-empty run.
- `srcgo/domains/compiler/engine/plan.go buildManifest` —
  unions emitter-reported caps with catalog-derived Extensions
  (via optional `DialectCapabilities`) and IR-level
  `(w17.pg.field).required_extensions` on columns the plan
  touches.
- `srcgo/domains/compiler/engine/filesystem/sink.go` — writes
  `<Basename>.manifest.json` when Manifest has any populated
  slot; canonical `protojson.Marshal`; empty Manifest = no file.
- `srcgo/domains/compiler/decide/` — `--decide` flag parser,
  `DefaultSQLLoader`, `Decisions.ResolveAll`. Sibling to
  `engine/` per the D30 adapter rule.
- `scripts/test-apply.sh <dialect> <version>` — dialect-
  parametrised harness; per-fixture `.min-pg-version` gate
  (uuid_pk → 18+). Makefile drives
  `PG_VERSIONS = 14 15 16 17 18` by default.
- `testdata/pg_dialect/expected.manifest.json` — first manifest
  golden; opt-in per fixture; guards cap instrumentation drift.

### D32 pinned (2026-04-25)

Per-dialect authoring pass-through stays parallel:
`proto/w17/<dialect>/field.proto` per dialect + per-dialect IR
slot (`Column.Pg`, future `Column.Mysql`). No generalised
`variants[]` map. MySQL lands as its own extension file when
Layer C opens. See `iteration-2.md` D32 for rationale.

### Where to resume next session

**Layer C — MySQL emitter stub.** All hard blockers closed:
- Compile-time `DialectCapabilities` assertion on SQL dialects.
- Multi-dialect engine orchestration tests
  (`TestPlan_MultiDialect_*`).
- Proto extension pattern locked (D32).
- `test-apply` matrix ready to add `MYSQL_VERSIONS`.
- Manifest golden path opt-in per fixture.

Layer C adds:
1. `proto/w17/mysql/field.proto` + `(w17.mysql.field)`.
2. `Column.Mysql MysqlOptions` in `ir.proto` + IR builder read.
3. `srcgo/domains/compiler/emit/mysql/` — Emitter + catalog.
4. `pickEmitter` wire-up (today returns "not implemented").
5. `scripts/test-apply.sh` case `mysql)` arm + Makefile
   `MYSQL_VERSIONS = <last 5 majors, verified live>` +
   `test-apply-mysql` target.
6. Third bucket in `TestPlan_MultiDialect_HappyPath`.
7. A MySQL grand-tour fixture with its own
   `expected.manifest.json`.

### Still owed (soft, non-blocking)

- Makefile standard targets (pre-poc gate): `configure /
  install / up / audit / seed / nuke / neoc / migrate / e2e /
  loadtest`.

---

## 2026-04-25 (later session) — C-plan: D33 → E2E matrix → F1-3 → D34 enforcement

User explicitly scoped this push as "everything except Layer C
(new dialects). That's a big milestone; everything around it
must be stable first." Completed sequence:

### D33 — engine renders YAML strategy templates into Ops (commit e565fa6)

Closes the gap where `classifier.Cell.Using` was loaded from
YAML but never rendered into migration SQL. Cross-carrier
ReviewFindings resolved to LOSSLESS_USING / SAFE / NEEDS_CONFIRM
/ DROP_AND_CREATE now produce emittable Ops via
`engine.injectStrategyOps`. New proto variant
`FactChange_TypeChange` carries prev + curr Column snapshots +
rendered USING expressions. Emitter's `renderTypeChange`
delegates column-type rendering back to `columnType()` (keeps
cap instrumentation lighting up uniformly with AddColumn).

CUSTOM_MIGRATION stays on the string-splice path (opaque user
SQL). See D33 in iteration-2.md for the full strategy dispatch
table.

### E2E classifier-matrix test runner (commits 6583c08 →
8629949 → 4c04438)

New package `srcgo/domains/compiler/e2e/` (build-tagged `e2e`)
iterates every cell in `docs/classification/*.yaml` against
real PG containers on each version in `PG_VERSIONS`.
Invocation:

```bash
go test -tags=e2e ./domains/compiler/e2e/                        # PG 18 only
PG_VERSIONS="14 15 16 17 18" go test -tags=e2e -timeout 30m ./domains/compiler/e2e/
```

Coverage:
- carrier.yaml: 110 cells → 56 exercised + 54 skipped (LIST /
  MAP / MESSAGE carriers need element/fqn synthesizer
  enhancements)
- dbtype.yaml: 50 cells → 44 exercised + 6 skipped (JSON family)
- constraint.yaml: 57 cells → ~16 exercised (column in-axis
  nullable / max_len / numeric / default / comment) + table-
  level / index / FK / check axes marked Skip for follow-up
  waves

Results: **609 PASS / 0 FAIL / 515 SKIP** on the full PG 14-18
matrix (~10:40 wall on cold Docker cache).

Bugs the matrix runner caught (all fixed):
- YAML: BOOL → INT64 cast `col::bigint` — PG has no direct
  cast, fixed to `(col::int)::bigint`.
- Engine: template data bag missing Project context — widened
  to carry `{Col, Table, Project.Encoding}`; Encoding defaults
  to "escape" (PG's universally round-trippable encoding).
- Emit: `numericTypeSQL(0, nil)` → `NUMERIC(0)` (PG rejects);
  fixed to bare `NUMERIC`. Same for `varcharTypeSQL(0)` → plain
  `VARCHAR`.
- Harness: ForwardOnly cell mode — when reverse transition is
  CUSTOM_MIGRATION, skip diff.down + prev.down (production
  rollback always needs `--decide-reverse` anyway).
- Harness: waitReady now does real `psql SELECT 1` probe, not
  just `pg_isready` — caught a race on PG 16 cold-start.

### F1 coverage push 91.7% → 94.3% (commit e81850c)

Targeted unit tests for:
- classifier iterator contract (AllCarrierCells /
  AllDbTypeCells / AllConstraintCells) — was 0% outside e2e
  build tag
- classifyFactChange dispatch for every FactChange variant
  including new TypeChange (D33)
- renderUsingTemplate edge cases (empty / missing-key /
  Project.Encoding nested lookup)
- findColumnByRef nil + wrong-FQN + wrong-field
- varcharTypeSQL, numericTypeSQL, renderAlterColumnType branch
  pairs

Remaining ~5.7% gap documented in `iteration-1-coverage.md
§3.3-ter` as deliberate exceptions.

### F3 core-functions.md coverage audit (commit 1806c08)

15 of 22 >50-LOC registered core functions hit 100%; the 7
<100% functions are ≥90% with their specific defensive
branches documented individually. Policy update: quality.md
"100% coverage" reads as "100% of user-reachable paths" per
iter-1 §3.3-bis precedent. No outstanding convention
violations to discharge.

### D34 enforcement SHIPPED (commit faa59f8)

Runtime gate for the D34 invariant documented earlier.
`ir.BuildMany` rejects two dialects sharing a category with
diag.Error (why: D34 rationale, fix: split into two domains).
Static `ir.DialectCategory(d)` lookup is single source of
truth. Error fixture:
`ir/testdata/multi_connection_same_category/`.

Prepares Layer C: MySQL landing won't compound the problem
because BuildMany rejects "PG + MySQL in same domain" on the
spot.

### Ship status

All six items before Layer C closed:
1. ✓ D33 engine template rendering
2. ✓ E2E matrix runner (carrier + dbtype + constraint waves)
3. ✓ Coverage push 91.7% → 94.3%
4. ✓ Core-fn audit + deliberate-exception policy
5. ✓ D34 runtime enforcement
6. Makefile standard targets — still owed, soft, non-blocking

**Layer C is the next substantive item — NOT opened yet.**
User explicitly reminded (multiple times) that Layer C is a
big milestone that requires communication; don't sneak it in.

### Where to resume next session

Either:
- **Open Layer C**: MySQL emitter stub per D32 parallel
  pattern. Infra ready — matrix runner extends by adding
  MYSQL_VERSIONS; multi-dialect test adds third bucket; D34
  enforces PG-OR-MySQL per domain automatically.
- **Other iter-2+ backlog**: DQL, local schema validator,
  Makefile standard targets, MESSAGE/LIST/MAP synth in e2e
  harness. Each is a standalone track.

Pick the track at session start; communicate before opening
Layer C.
