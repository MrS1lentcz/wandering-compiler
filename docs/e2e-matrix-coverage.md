# E2E matrix coverage — what's exercised, what's skipped

As of 2026-04-25 end-of-session. Pairs with
`srcgo/domains/compiler/e2e/` (build tag `e2e`).

Full matrix run on PG 14–18: **609 PASS / 0 FAIL / 515 SKIP**.

Every SKIP is deliberate — the harness tagged the cell with a
`SkipReason` explaining what's missing. No skip is a hidden
failure. This document maps every skip category to either an
**implementation task** (pure technical work, no decisions) or
a **decision pending** (waiting on a user-level ruling before
the engine can render the case cleanly).

---

## SKIP categories

### A. LIST / MAP / MESSAGE carrier synthesizer (270 skips — implementation)

**Cause.** The current `carrierSchema()` in
`srcgo/domains/compiler/e2e/synth.go` stands up scalar columns
only (BOOL / STRING / INT32 / INT64 / DOUBLE / TIMESTAMP /
DURATION / BYTES). Collection carriers need extra context:

- `CARRIER_LIST` — element carrier + element sem (for
  `TEXT[]`, `INTEGER[]`, …) or `element_is_message=true` for
  `repeated Foo` → JSONB.
- `CARRIER_MAP` — element carrier for the value side
  (K is always string per iter-1.6).
- `CARRIER_MESSAGE` — proto Message FQN (`Column.MessageFqn`)
  and the enclosing proto schema's imports.

**Work.** Extend `synth.go` with `listSchema` / `mapSchema` /
`messageSchema` helpers. Each needs a plausible default
(e.g. LIST → `repeated string`, MAP → `map<string,string>`,
MESSAGE → stub `Inner { int64 id = 1; }`). Update
`carrierSynthSkip()` to stop skipping once the synth covers
the case.

**Blast radius.** ~270 SKIP → PASS in one commit, per version.

**Decisions needed.** None.

---

### B. dbtype JSON family (30 skips — implementation)

**Cause.** `docs/classification/dbtype.yaml` declares a `JSON`
family for transitions between `JSON` / `JSONB` / `HSTORE`.
All three map to carriers in the collection family
(MAP / LIST), so they hit the same element-context gap as A.

**Work.** Falls out of A automatically — once `mapSchema` lands,
the JSON family dbtype cells stop skipping.

**Decisions needed.** None.

---

### C. Constraint non-column axes (215 skips)

Table-level + index / FK / check / raw-index / cross-axis
finding axes. Mixed — most are implementation, a handful need
decisions.

#### C.1 Pure implementation (185 of 215)

| Axis | Skips | Work |
|---|---|---|
| `allowed_extensions` (narrow / disjoint / to_wildcard / from_wildcard / widen) | 25 | Synth prev/curr with differing `(w17.field).allowed_extensions`. Engine path exists (in-axis FactChange). |
| `table_namespace` (schema_change / prefix_change / mode_switch) | 15 | Synth with `(w17.db.module).schema` vs `prefix` mode. Existing `emitSetTableNamespace` op handles emission. |
| `generated_expr` (add / drop / change) | 15 | Synth column with `(w17.db.column).generated_expr`. In-axis FactChange path already shipped. |
| `unique` (enable / disable) | 10 | Routes through Index bucket per constraint.yaml A5 — needs index-level synth (see `index_*` row). |
| `index_add` / `index_replace` / `index_drop` | 15 | Synth table with `(w17.db.table).indexes` differing. AddIndex/DropIndex/ReplaceIndex ops already emit. |
| `fk_add` / `fk_replace` / `fk_drop` | 15 | Synth table with `fk` field — needs a target table in the synth schema. Add/Drop/Replace FK ops already emit. |
| `check_add` / `check_replace` / `check_drop` | 15 | Synth column with CHECK-producing options (`max_len`, `min_len`, `pattern`, `choices`). |
| `raw_add` / `raw_replace` / `raw_drop` | 15 | Synth with `raw_indexes` or `raw_checks`. |
| `table_rename` | 5 | Synth with `(w17.db.table).name` differing while FQN stable. |
| `table_comment` | 5 | Synth with `(w17.db.table).comment`. |
| `numeric add_bound` | 5 | Needs unbounded-NUMERIC IR shape (sem=NUMBER + db_type=NUMERIC override) — IR currently rejects DECIMAL+precision=0. |
| `pg_required_extensions` | 5 | Manifest-only update; synth with differing `(w17.pg.field).required_extensions`. |

All ~185 are standalone synth helpers under
`srcgo/domains/compiler/e2e/constraint_cells.go`. Graduate
the Skip to a real run by adding the synth + removing the
SkipReason.

#### C.2 Decisions pending (30 of 215)

These axes produce ReviewFindings today (cross-axis) and the
engine has no automatic renderer for non-CUSTOM_MIGRATION
resolutions. D33 solved this for `carrier_change`; these
axes need the equivalent design decision before we can wire
them into the matrix.

##### D.1 `pk` enable / disable (10 skips)

**Current behaviour.** Plan.Diff emits a `pk_flip` Finding.
`engine.injectStrategyOps` ignores it (not carrier_change).
Resolution of any strategy is silently lost — no Op emitted.

**Decision wanted.**
1. **Option A**: `pk_flip` stays CUSTOM_MIGRATION-only (user
   always writes the SQL via `--decide`). PG PK changes are
   non-trivial (constraint drop + add + potentially column
   rewrite); author knows the transaction boundaries better
   than a template.
2. **Option B**: Engine auto-renders `ALTER TABLE ... DROP
   CONSTRAINT <table>_pkey; ALTER TABLE ... ADD PRIMARY KEY
   (<cols>);` for DROP_AND_CREATE resolution. Works for most
   small tables; ugly for large ones (full table rebuild).
3. **Option C**: Hybrid — engine auto-renders for single-column
   PK flips; refuses composite PK changes as CUSTOM_MIGRATION-
   only.

**My suggestion.** Option A. PK flips are rare; CUSTOM_MIGRATION
matches the "no silent coercion" rule.

##### D.2 `pg_custom_type` (5 skips)

**Current behaviour.** Plan.Diff emits a `custom_type_change`
Finding. Same flow as pk_flip — ignored by
injectStrategyOps, silent-empty migration on resolution.

**Decision wanted.**
1. **Option A**: CUSTOM_MIGRATION-only. custom_type is already
   the escape-hatch for opaque PG types (vector, PostGIS,
   PostGIS, domains) — changing the opaque-type string
   genuinely requires author SQL.
2. **Option B**: Engine auto-renders DROP COLUMN + ADD COLUMN
   for DROP_AND_CREATE (same shape as Layer C Drop+Add cell).

**My suggestion.** Option A. custom_type is opaque; compiler has
no template to render.

##### D.3 `enum_values` remove — **RESOLVED via D37** (2026-04-25)

Plan.Diff emits a `enum_values_remove` ReviewFinding with
Proposed=NEEDS_CONFIRM; engine's `injectEnumRemoveValue` renders
the 4-statement rebuild (`CREATE TYPE new / ALTER COLUMN USING /
DROP TYPE old / RENAME`). Author opts in via `--decide <col>:
enum_values_remove=needs_confirm` — active decision, engine-
owned SQL. Principle codified: CUSTOM_MIGRATION is only for
genuinely non-deterministic transitions (e.g. json→boolean);
deterministic-but-destructive = NEEDS_CONFIRM. Green on PG 14-18
in e2e matrix. See D37 in iteration-2.md for full rationale.

##### D.4 `default` identity_add / identity_drop / auto_kind_change — **RESOLVED via D38** (2026-04-25)

All three sub-axes now exercised end-to-end on PG 14-18:

- `identity_add` — classifier CUSTOM_MIGRATION → NEEDS_CONFIRM.
  Plan.Diff emits `default_identity_add` Finding; engine's
  `injectDefaultIdentity` builds AlterColumn with DefaultChange;
  emit renders `ADD GENERATED BY DEFAULT AS IDENTITY` + `setval(
  pg_get_serial_sequence, MAX+1)` under the ALTER's ACCESS
  EXCLUSIVE lock.
- `identity_drop` — classifier already NEEDS_CONFIRM; engine
  `injectDefaultIdentity` + emit `DROP IDENTITY` template.
  Down direction recreates via `ADD GENERATED + setval`.
- `auto_kind_change` — classifier SAFE; in-axis FactChange.
  Root-cause of prior "silent-empty" was `renderDefaultChange`
  passing a synthetic empty column to `defaultExpr`, which
  failed sem-type-sensitive autos (`AUTO_NOW` on DATETIME →
  NOW(), on DATE → CURRENT_DATE). Fixed by plumbing the real
  post-change column through the dispatcher.

See D38 in iteration-2.md.

##### D.5 `element_reshape` (5 skips)

**Current behaviour.** Plan.Diff emits an
`element_carrier_reshape` Finding. Same ignore-on-resolution
issue as pk_flip.

**Decision wanted.** MAP value-carrier / LIST element-carrier
change — auto-renderable?

1. **Option A**: CUSTOM_MIGRATION-only. Element-type changes
   are data-migration territory (every value re-encoded).
2. **Option B**: Engine auto-emits DropColumn + AddColumn
   (structural reset; data loss acknowledged).

**My suggestion.** Option A — same "no silent coercion"
rationale as carrier_change default.

---

## Summary

- **Implementation-pending**: ~485 SKIPs. Roughly 5 focused
  synthesizer commits land them all. Each commit is a standalone
  wave — LIST carriers, MAP carriers, MESSAGE carrier,
  constraint table-level, constraint index/fk/check/raw.
- **Decision-pending**: ~30 SKIPs across 5 axes (pk,
  pg_custom_type, enum_values remove, default identity lifecycle,
  element_reshape). My recommendation for each is **keep
  CUSTOM_MIGRATION-only** unless there's a strong reason to
  template (matches the "no silent coercion" rule; author owns
  decisions with data impact).

**When user signs off on the decisions**, the engine gets a
small D33-style follow-up record (D35 tentatively) that maps
each decision-pending axis to its renderer. Implementation
waves can then proceed without blocking on re-review.
