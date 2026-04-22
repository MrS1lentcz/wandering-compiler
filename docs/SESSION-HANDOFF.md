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
- [ ] **← YOU ARE HERE.** Iter-1 schema-gap close-out done. Next:
      open `iteration-2.md` with alter-diff as M1, against an IR
      that's closed on every schema-declaration axis iter-1 will
      ever carry.
- [ ] D22 (optional bundle — COMMENT ON auto-from-docstring +
      override annotation; path-family presets IMAGE_PATH /
      POSIX_PATH / FILE_PATH with `extensions` list + `*` wildcard;
      MAC_ADDRESS with CHECK when storage doesn't enforce;
      SMALL_INTEGER preset).
- [ ] **D23 (right after D22, before alter-diff) — indexes +
      constraints dynamic redesign.** Originally scoped as D20 "index
      sort order + NULLS FIRST/LAST" but user flagged 2026-04-22 that
      the right move is a broader overhaul matching Django's shape
      (structured `GinIndex` / `PartialIndex` / `ExpressionIndex`
      messages graduating from `raw_indexes`, column-level sort +
      nulls inside indexes, structured CHECK variants beyond the
      current raw_checks escape hatch). NULLS FIRST/LAST lands inside
      this redesign, not as a standalone feature. Must happen BEFORE
      alter-diff so the differ works against the final index/CHECK IR
      shape, not an intermediate one that will be rewritten.
- [ ] Open `iteration-2.md` with alter-diff as M1 (after D23).

After D17–D20 land, schema declaration is exhaustively closed and
iter-2 alter-diff can begin against the most complete IR iter-1 will
ever have.
