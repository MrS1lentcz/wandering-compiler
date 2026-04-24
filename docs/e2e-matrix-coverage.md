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

##### D.1 `pk` enable / disable — **RESOLVED via D39** (2026-04-25)

Option C shipped: single-column PK flip is NEEDS_CONFIRM with an
engine-rendered template; multi-column PK swap (two pk_flip
findings on one table) hard-errors pointing at CUSTOM_MIGRATION.

- `pk/enable` CUSTOM_MIGRATION → NEEDS_CONFIRM. Template =
  `ALTER TABLE t ADD PRIMARY KEY (col);`. Risk = apply fails on
  NULLs or duplicates.
- `pk/disable` CUSTOM_MIGRATION → NEEDS_CONFIRM. Template =
  `ALTER TABLE t DROP CONSTRAINT <table>_pkey;`. Risk = FK
  referential graph surprises.
- Swap (id disable + uuid enable on one table) still routes
  through CUSTOM_MIGRATION. Engine hard-errors with the count of
  concurrent pk_flip findings + a `--decide … =custom:<sql>`
  pointer.

Green on PG 14-18. See D39 in iteration-2.md.

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

##### D.5 `element_reshape` — **CONFIRMED as CUSTOM_MIGRATION** (2026-04-25)

Per the D37 principle (CUSTOM_MIGRATION only for genuinely non-
deterministic transitions), MAP value-carrier / LIST element-
carrier reshape is the canonical non-deterministic case: a
`string→int64` reshape of every element has no automatic
template (which encoding? parse-with-default or parse-or-skip?
what about unparseable values?). Stays CUSTOM_MIGRATION with
the Finding path wired as today. B1 hard-error list keeps
`element_carrier_reshape` as CUSTOM-only.

No D-record — this is a no-change confirmation of existing
behaviour.

---

## Summary (2026-04-25 update)

- **Implementation-pending**: ~485 SKIPs across non-column
  synthesizer waves (LIST/MAP/MESSAGE carriers, JSON dbtype family,
  table-level / index / fk / check / raw constraint axes,
  numeric add_bound). Each is its own wave; not blocked.
- **Decision-pending: ALL CLOSED.** D36 → D40 graduated every
  previously-decision-pending axis to its final classifier
  strategy:
  - D.1 pk_flip → D39 (single-column NEEDS_CONFIRM, swap stays CUSTOM)
  - D.2 pg_custom_type → D36 (typed registry, registered vs unregistered)
  - D.3 enum_values/remove → D37 (NEEDS_CONFIRM rebuild) +
    D40 enum_fqn_change (same template, three branches)
  - D.4 default identity + auto_kind_change → D38
  - D.5 element_carrier_reshape → confirmed CUSTOM_MIGRATION
    (genuinely non-deterministic; codified in
    feedback_strategy_semantics memory)

The principle codified along the way:
**CUSTOM_MIGRATION is reserved for non-deterministic transitions.
Deterministic-but-destructive = NEEDS_CONFIRM with engine-rendered
template. Engine writes the SQL; user confirms the destructive
outcome.** See iteration-2.md D37-D40.

B1 hard-error list residue after D40: only
`element_carrier_reshape`. Implementation-pending waves can now
proceed without re-review.
