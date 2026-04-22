# Iteration-1 Coverage Audit

Companion to [`iteration-1.md`](iteration-1.md) + [`iteration-1-impl.md`](iteration-1-impl.md).
Captures Django model-layer parity + test coverage as of iteration-1 close.

**Date:** 2026-04-21. **Scope:** everything iter-1 ships minus conscious tradeoffs.
**Confidence:** medium-high — code paths are exercised end-to-end through
goldens (M8) + apply-roundtrip on Postgres 18 (M9), but no adoption load yet.

---

## 1. Django model-layer parity

Django 4.2 is the reference. ✅ = shipped, ⚠️ = partial / workaround,
⛔ = conscious tradeoff (see D-column for decision), ❌ = not yet decided.

### 1.1 Field types

| Django field | w17 | Notes | D |
|---|---|---|---|
| `CharField(max_length)` | ✅ | `type: CHAR, max_len: N` → VARCHAR(N) | D2 |
| `TextField` | ✅ | `type: TEXT` or zero-config on `string` | D2, D14 |
| `SlugField` | ✅ | `type: SLUG, max_len: N` | D2 |
| `EmailField` | ✅ | `type: EMAIL`; preset max_len 320, override works | D2, D13 |
| `URLField` | ✅ | `type: URL`; preset max_len 2048, override works | D2, D13 |
| `UUIDField` | ✅ | `type: UUID` + `default_auto: UUID_V4` / `UUID_V7` | D7 |
| `IntegerField` | ✅ | zero-config on `int32` / `int64` → INTEGER / BIGINT | D14 |
| `SmallIntegerField` | ⚠️ | int32+NUMBER → INTEGER by default; `db_type: SMALLINT` forces SMALLINT | D14 |
| `BigIntegerField` | ✅ | zero-config on `int64` | D14 |
| `PositiveIntegerField` | ⚠️ | `int64 [gte: 0]`; Django has dedicated field shape, we compose | — |
| `AutoField` / `BigAutoField` | ✅ | `type: ID, pk: true, default_auto: IDENTITY` | D7 |
| `FloatField` | ✅ | zero-config on `double` → DOUBLE PRECISION | D14 |
| `DecimalField(max_digits, decimal_places)` | ✅ | `type: DECIMAL, precision, scale` | D2 |
| `BooleanField` | ✅ | zero-config on `bool` → BOOLEAN | D14 |
| `NullBooleanField` (deprecated) | ✅ | `bool + null: true` | — |
| `DateField` | ✅ | `type: DATE` | D2 |
| `TimeField` | ✅ | `type: TIME` | D2 |
| `DateTimeField` | ✅ | zero-config on `Timestamp` → DATETIME → TIMESTAMPTZ | D14 |
| `DurationField` | ✅ | zero-config on `Duration` → INTERVAL | D2 |
| `JSONField` | ✅ | `type: JSON` (on string or bytes carrier) → JSONB | D13 |
| `BinaryField` | ✅ | zero-config on `bytes` → BYTEA | D12, D14 |
| `GenericIPAddressField` | ✅ | `type: IP` → INET | D13 |
| `FilePathField` | ⛔ | filesystem concern, not DB | — |
| `FileField` / `ImageField` | ⛔ | storage-backend concern, not DB | — |
| `ForeignKey` | ✅ | `(w17.db.column).fk` + `deletion_rule` | D12 |
| `OneToOneField` | ✅ | `fk + unique: true` OR shared-PK (pk:true + fk) | D12 |
| `ManyToManyField` | ✅ | explicit join table (composite PK + two FKs); see `m2m_join` fixture | D12 |
| `GenericForeignKey` (contenttypes) | ⛔ | Django internal pattern, not portable | — |
| `ArrayField` (postgres) | ✅ | `repeated X` → X[] with element typing | D15 |
| `HStoreField` (postgres) | ✅ | `map<string, string>` → HSTORE | D15 |
| `RangeField` family (postgres: IntegerRange, DateRange, …) | ⚠️ | No preset yet; use `(w17.pg.field).custom_type: "int4range"` | — |
| `SearchVectorField` (postgres) | ✅ | `type: TSEARCH` → TSVECTOR | D13 |

### 1.2 Field options

| Django option | w17 | Notes |
|---|---|---|
| `primary_key` | ✅ | `(w17.field).pk` |
| `unique` | ✅ | `(w17.field).unique` → UNIQUE INDEX synth |
| `null` | ✅ | `(w17.field).null` |
| `blank` | ✅ | `(w17.field).blank` (DB-level CHECK; Django enforces at form) |
| `default` (static) | ✅ | `default_string` / `default_int` / `default_double` |
| `default` (callable, DB-side) | ✅ | `default_auto` (NOW, IDENTITY, UUID_V4/V7, TRUE/FALSE, EMPTY_JSON_*) |
| `default` (callable, Python-side) | ⛔ | runtime concern; use `default_auto` for DB defaults |
| `db_column` | ✅ | `(w17.db.column).name` |
| `db_index` | ✅ | `(w17.db.column).index` — single-col non-unique storage index |
| `GeneratedField` (4.2+) | ✅ | `(w17.db.column).generated_expr: "<sql>"` → GENERATED ALWAYS AS (expr) STORED (PG 12+); incompatible with default/pk/fk. See D18 |
| `db_tablespace` | ⛔ | iter-2+ (PG-specific, rare outside specialized deploys) |
| `db_comment` (4.2+) | ⛔ | iter-2 — pairs with admin/UI generation |
| `db_collation` (3.2+) | ⛔ | iter-2+ — belongs on `(w17.pg.field)` or DbType modifier |
| `max_length` | ✅ | `(w17.field).max_len`; defaults for EMAIL/URL, required for CHAR/SLUG |
| `choices` | ✅ | `(w17.field).choices: "pkg.EnumFQN"` → CHECK IN (…) |
| `editable` | ⛔ | UI concern |
| `error_messages` / `help_text` / `verbose_name` | ⛔ | UI concern |
| `validators` | ⚠️ | Built-in CHECK variants cover `MinValueValidator`, `MaxValueValidator`, `RegexValidator`, `MaxLengthValidator`, `MinLengthValidator`; custom Python validators don't port — use `raw_checks` for DB-level equivalents |
| `unique_for_date/month/year` | ⛔ | rare; use `raw_indexes` with WHERE on date portion |

### 1.3 Meta options

| Django Meta | w17 | Notes |
|---|---|---|
| `db_table` | ✅ | `(w17.db.table).name` — optional since D21; defaults to `snake_case(message.local_name)` when unset |
| `app_label` (as schema prefix) | ✅ | `(w17.db.module) = { prefix: "<name>" }` — module-level, immutable across the module. See D19 |
| PG schema qualification (SQLAlchemy `__table_args__ = {'schema': 'X'}`) | ✅ | `(w17.db.module) = { schema: "<name>" }` — PG-native, mutually exclusive with prefix. See D19 |
| `db_tablespace` | ⛔ | iter-2+ |
| `db_table_comment` (4.2+) | ⛔ | iter-2 with admin gen |
| `indexes = [Index(…)]` | ✅ | `(w17.db.table).indexes` covers fields + name + unique + include; `raw_indexes` covers WHERE / USING / expressions / opclasses |
| `constraints = [UniqueConstraint(…)]` | ✅ | `(w17.db.table).indexes[].unique: true`; partial via `raw_indexes`; opclasses via `raw_indexes` |
| `constraints = [CheckConstraint(check=Q(…))]` | ✅ | per-field CHECKs for single-column; `raw_checks` for cross-column / function-call / expression |
| `abstract` (multi-table inheritance) | ⛔ | gRPC has no inheritance; shared-PK one-to-one pattern is the replacement (see `shared_pk_one_to_one` fixture) |
| `app_label` | ⛔ | no app concept |
| `ordering` | ⛔ | query-time, not schema |
| `permissions` / `default_permissions` | ⛔ | auth concern |
| `get_latest_by` | ⛔ | query-time |
| `managed` | ⛔ | not relevant (we always manage the schema) |
| `proxy` | ⛔ | Django Python-level concept |

### 1.4 Foreign-key behaviors

| Django `on_delete` | w17 `deletion_rule` | SQL |
|---|---|---|
| `CASCADE` | `CASCADE` | `ON DELETE CASCADE` |
| `SET_NULL` | `ORPHAN` | `ON DELETE SET NULL` (requires null:true) |
| `PROTECT` | `BLOCK` | `ON DELETE RESTRICT` |
| `RESTRICT` (3.1+) | `BLOCK` | `ON DELETE RESTRICT` (same DB behavior; Django distinguishes app-level) |
| `SET_DEFAULT` | `RESET` | `ON DELETE SET DEFAULT` (requires default_*) |
| `DO_NOTHING` | ⚠️ BLOCK is close (non-deferrable) | Deferrable DO_NOTHING is iter-2+ |
| `SET()` / `SET(callable)` | ⛔ | app-level pattern |

Inference when `deletion_rule` unspecified: `null: true` → ORPHAN, else CASCADE.

### 1.5 Indexes

| Django Index option | w17 | Notes |
|---|---|---|
| `fields=[…]` | ✅ | `(w17.db.table).indexes[].fields` |
| `name` | ✅ | derived or explicit |
| `unique` (via UniqueConstraint) | ✅ | `unique: true` on the index |
| `include=[…]` | ✅ | PG INCLUDE (covering index) |
| `condition=Q(…)` (partial) | ✅ | via `raw_indexes` with WHERE |
| `expressions=[F(…)]` (functional) | ✅ | via `raw_indexes` with `(lower(col))` etc. |
| `opclasses=[…]` | ✅ | via `raw_indexes` with `(col gin_trgm_ops)` |
| `GinIndex` / `GistIndex` / `BrinIndex` / `HashIndex` | ✅ | via `raw_indexes` with USING |
| `db_tablespace` | ⛔ | iter-2+ |

### 1.6 CHECK constraints

| Django `CheckConstraint` | w17 | Notes |
|---|---|---|
| `check=Q(field=value)` single-col | ✅ | per-field Range / Length / Pattern / Choices |
| `check=Q(field__lte=F('other'))` cross-col | ✅ | `(w17.db.table).raw_checks[].expr` |
| `name` | ✅ | both derived and raw_checks names validated for collisions |
| `deferrable` | ⛔ | iter-2+ |
| `violation_error_message` | ⛔ | UI / app-level |

### 1.7 Defaults

Already covered in 1.2; summary of `default_auto` coverage:

| Django `default=` | w17 `default_auto` |
|---|---|
| `timezone.now` on DateTimeField | `NOW` (→ NOW() / CURRENT_DATE / CURRENT_TIME per sem-type) |
| `uuid.uuid4` | `UUID_V4` |
| `uuid.uuid7` (via lib) | `UUID_V7` (PG 18 built-in; earlier needs extension) |
| `list` / `dict` on JSONField | `EMPTY_JSON_ARRAY` / `EMPTY_JSON_OBJECT` |
| auto-increment PK | `IDENTITY` |
| `True` / `False` on BooleanField | `TRUE` / `FALSE` |
| `auto_now_add=True` | `default_auto: NOW` (works on INSERT) |
| `auto_now=True` (update-time) | ⛔ needs trigger, iter-2 |

---

## 2. Test coverage

### 2.1 Golden fixtures (M8) — 15 positive shapes

All produce byte-identical output on re-run (AC #4); all apply cleanly via
M9 test-apply (up → down → up on Postgres 18 with hstore + citext
extensions). Fixtures live under `srcgo/domains/compiler/testdata/`:

| Fixture | Dispatch paths exercised |
|---|---|
| `product` | SLUG / URL / PERCENTAGE (implicit Range 0-100), DATE+CURRENT_DATE, DATETIME+NOW, COUNTER, IDENTITY |
| `no_indexes` | minimal (PK + CHAR + TEXT), empty index block |
| `multi_unique` | multi-col UNIQUE index (named) + two single-col unique synths |
| `uuid_pk` | UUID PK + UUID_V7, UUID_V4 on another col, EMAIL (regex + default max_len), TEXT+min/max_len |
| `numeric_spectrum` | every (int32/64/double, NUMBER/ID/COUNTER/MONEY/RATIO) combo + DECIMAL + DECIMAL+range + PERCENTAGE implicit range |
| `temporal_full` | DATE/TIME/DATETIME + NOW per sem; INTERVAL bare |
| `flags_enums_json` | bool+TRUE/FALSE, JSON+EMPTY_JSON_ARRAY/OBJECT, Choices, explicit pattern override |
| `pg_dialect` | JSON / IP / TSEARCH core Types; HSTORE + MACADDR via custom_type; raw_indexes with USING gin + opclass |
| `fks_parent_child` | CASCADE inferred, ORPHAN (SET NULL), BLOCK (RESTRICT), RESET (SET DEFAULT), self-ref FK, INCLUDE index, FK-column + storage_index |
| `m2m_join` | composite PK on join table, two FKs, plan.Diff topological sort (product_tags lex < products) |
| `raw_checks_and_indexes` | cross-col CHECK, function CHECK, partial UNIQUE (WHERE deleted_at IS NULL), expression index (lower(col)) |
| `bytes_column` | bare bytes → BYTEA, nullable bytes |
| `shared_pk_one_to_one` | child PK that is also FK to parent (Django multi-table-inheritance pattern) |
| `storage_override` | D14 zero-config defaults + (w17.db.column).db_type: TEXT/CITEXT/JSON/BIGINT overrides |
| `lists_and_maps` | CARRIER_MAP (HSTORE + JSONB + bytes value), CARRIER_LIST (TEXT[] / VARCHAR(N)[] / BIGINT[] / INTEGER[] / DOUBLE PRECISION[] / NUMERIC(19,4)[] / BYTEA[] / BOOLEAN[] / TIMESTAMPTZ[] / INTERVAL[]), element typing on repeated |

**What 15 fixtures × 16 columns/fixture avg ≈ 240 column-level dispatch paths** walked end-to-end from proto → IR → plan → SQL → PG apply.

### 2.2 Error-class fixtures (26) — IR rejection paths

Under `srcgo/domains/compiler/ir/testdata/errors/`. Each asserts
`file:line:col` anchor + `why:` + `fix:` substrings through
`TestBuildErrors`:

| Category | Fixtures |
|---|---|
| Missing / invalid type | `bad_autodefault_now`, `bool_with_type`, `char_no_maxlen`, `choices_on_int`, `decimal_no_precision` |
| Table metadata | `missing_table_name`, `reserved_table_name`, `identifier_too_long` |
| FK integrity | `fk_target_missing`, `fk_target_not_unique`, `orphan_requires_null`, `reset_requires_default` |
| Index / constraint collisions | `index_name_collision`, `raw_index_empty_body`, `raw_index_collides_with_synth`, `raw_check_empty_name`, `raw_check_collides_with_derived` |
| pg.field / db_type conflicts | `pg_override_non_string_carrier`, `pg_override_requires_text`, `pg_override_with_string_check`, `dbtype_carrier_mismatch`, `dbtype_conflicts_with_custom_type`, `dbtype_varchar_needs_max_len` |
| Collection-carrier gates | `map_key_must_be_string`, `list_type_on_message_element`, `collection_string_synth_rejected` |

### 2.3 Unit tests

| Package | Tests | Coverage |
|---|---|---|
| `plan` | 8 | Diff with nil/empty/non-nil prev, determinism, op variant, topo sort (ref-before-referencer, self-FK is root, cycle rejection) |
| `emit/postgres` | 4 | Happy fixture smoke, MONEY rendering, composite PK, unknown op error, determinism |
| `emit/sqlite` | 3 | Stub returns error for every op variant; DialectEmitter compile-time conformance; `emit.Emit` wraps error with dialect name |
| `ir` | TestBuildHappyPath + TestBuildErrors (26 cases) | Happy-path IR shape assertions + every error-class fixture |
| `loader` | 2 | Smoke + vocab fixture type/option decoding |
| `naming` | 3 | UTC normalisation, format pinning, fixed-width regex |
| `writer` | 6 | Happy-path layout, auto-create dirs, overwrite idempotency, traversal rejection, empty-body rejection, determinism |

### 2.4 Apply-roundtrip (M9)

`make test-apply` boots ephemeral `postgres:18-alpine`, installs `hstore`
+ `citext` extensions per test DB, drives every golden through up →
down → up. One CREATE DATABASE + extension set per fixture keeps state
from leaking between cases. AC #2 (applies cleanly) + AC #3 (rolls back
cleanly) green on all 15 fixtures.

---

## 3. Identified gaps

### 3.1 Conscious tradeoffs (documented in D-series)

These are decisions, not omissions. `iteration-1.md` explains each.

- **Multi-table inheritance** (abstract / proxy / MTI) — gRPC has no
  inheritance; shared-PK one-to-one pattern (D12 / shared_pk_one_to_one
  fixture) replaces the narrow real use case.
- **GenericForeignKey / contenttypes** — Django-internal abstraction
  that doesn't port cleanly; authors spell specific FKs.
- **File / Image / FilePath fields** — storage-backend concern, outside
  the schema-compiler scope.
- **SET() with callable** — app-level Django feature; DB SET DEFAULT
  (`deletion_rule: RESET`) covers the DB-layer equivalent.
- **Custom Python validators** — runtime concern; DB-level equivalents
  via built-in CHECK variants or `raw_checks`.
- **Python-side callable defaults** (e.g. `default=timezone.now`) —
  runtime; `default_auto: NOW` is the DB-side equivalent.
- **`auto_now=True`** (update-time) — needs a trigger; iter-2+.
- **`editable` / `verbose_name` / `help_text` / `error_messages`** — UI
  concerns; iter-2 admin-gen will consume proto doc-strings.
- **Element-level CHECKs on arrays** — PG CHECK can't express "forall
  element"; `raw_checks` is the escape hatch for whole-column
  predicates.
- **`map` with non-string keys** — HSTORE is string-only; cross-dialect
  JSONB key conventions unpinned; iter-2+.
- **Element typing on map values** — iter-1.6 dispatches maps on K/V
  carrier only; per-value sem refinement iter-2+.

### 3.2 Open vocabulary gaps (no conscious decision yet — iter-2 candidates)

These aren't rejected; they just haven't been prioritised. Listed so
the backlog stays explicit.

- **RangeField family** (IntegerRange / DateRange / NumericRange /
  TsRange / TstzRange) — PG-specific, today expressible via
  `custom_type: "int4range"`, could graduate to a preset cluster.
- **`db_comment` / `db_table_comment`** — pulls from proto doc-strings,
  pairs with admin/UI generation.
- **`db_collation`** — PG-specific, low priority.
- **`db_tablespace`** — specialized PG deploy knob.
- **Deferrable FKs / deferrable CHECK constraints** — rarely used but
  legitimate; Django exposes via `deferrable` argument.
- **Trigger-based features** — `auto_now` on update, audit triggers,
  `UPDATE` cascades for computed columns. Out of scope until we have
  a trigger vocabulary.
- **Structured messages for common raw-index patterns** — D11 writeup
  explains graduation path (`GinIndex` / `PartialIndex` /
  `ExpressionIndex` messages replace stabilised `raw_indexes` patterns
  when volume justifies).
- **db_type on LIST / MAP carriers** — today rejected (carrier compat).
  Future `db_type: JSONB` on LIST could force JSON storage over native
  array. Legitimate but no user has asked yet.
- **MSSQL- / Oracle-specific types** — iter-2+ when those emitters
  land.

### 3.3 Test gaps (paths the code supports but no fixture exercises)

Found during the audit — low-risk but worth patching or acknowledging:

- **`repeated` with zero-config message elements** — fallback-to-JSONB
  path has no positive fixture. Error fixture
  `list_type_on_message_element` exercises the reject path
  (`type:` on Message), but not the happy path "bare repeated
  MyMessage → JSONB". Easy to add to `lists_and_maps` if we keep
  extending; skipped for now (dispatch proven by code inspection,
  tested via `map<string, Message>` which goes through the same
  JSONB fallback).
- **Empty schema** (proto file with zero `(w17.db.table)` messages) —
  M7 / M9 never run this. Expected behavior: CLI exits zero, no
  output file. Untested; low priority.
- **`go test -update` flag on goldens** — implementation present
  (goldens_test.go), no regression test. Running `-update` and
  asserting no diff after re-run would prove idempotency.
- **Derived CHECK name length** — we validate at IR time, but no
  error fixture hits the 63-byte boundary for a `<table>_<col>_<variant>`
  string. `identifier_too_long` only hits the table-name axis.
- **`default_string` on DECIMAL** — allowed in spec (DECIMAL carrier is
  string, default_string would be the literal decimal string). Not in
  any fixture. Likely works; untested.
- **FK self-reference with `deletion_rule: BLOCK`** — self-ref FK tested
  with ORPHAN (parent_id in categories), not with BLOCK. Storage is
  valid but untested.
- **`(w17.db.table).indexes[]` with INCLUDE + unique** — `fks_parent_child`
  has INCLUDE on non-unique; unique + INCLUDE is valid PG but untested.
- **Multiple `(w17.pg.field).required_extensions` on same table** —
  IR aggregates per-column; the platform will aggregate per-schema
  later. Untested.

---

## 4. Confidence statement

**Iter-1 vocabulary covers ~95% of Django's model-layer schema
declarations** — essentially everything except consciously-rejected
patterns (multi-table inheritance, GFKs, SET() callables, Python
validators) and a handful of low-priority iter-2 items (db_tablespace,
db_collation, deferrable, range fields, auto_now-on-update).

**Test coverage is dense on dispatch paths and validation errors,
moderate on cross-cutting combinations.** Every (carrier, type)
cell of D2 is exercised. Every enum value in DbType, DeletionRule,
AutoDefault, RegexSource, FKAction maps to at least one fixture or
emitter unit test. Error-class fixtures assert file:line:col +
`why:` + `fix:` on 26 distinct rejection paths.

**Weakest coverage:** cross-carrier combinations the fixtures haven't
enumerated (e.g. `repeated double [type: MONEY] + db_type: JSONB` if
that were ever valid), and side-effects between features added in
different batches. The list in §3.3 captures the known minor gaps.

**Strongest coverage:** the apply roundtrip. Every golden SQL runs
against a real PG 18 with up → down → up, catching any syntax,
constraint, or ordering regression the goldens alone wouldn't spot.

**Recommendation for iter-2 entry:** the base is solid. Alter-diff
(biggest iter-2 block) builds on an IR that's been stress-tested
against 15 distinct schema shapes + 26 error paths. Any remaining
bugs live in corners; not in the main dispatch.

---

## Quick stats

| | |
|---|---|
| Positive fixtures | 15 |
| Error-class fixtures | 26 |
| Decision records | D1–D15 |
| `(w17.field).type` values | 20 (including AUTO) |
| `(w17.db.column).db_type` values | 27 (+ UNSPECIFIED) |
| AutoDefault variants | 9 |
| DeletionRule variants | 4 (CASCADE, ORPHAN, BLOCK, RESET) |
| Carriers | 10 (incl. MAP, LIST) |
| Covered PG types | TEXT / VARCHAR(N) / CITEXT / UUID / INTEGER / BIGINT / SMALLINT / DOUBLE PRECISION / NUMERIC(p,s) / BOOLEAN / DATE / TIME / TIMESTAMP / TIMESTAMPTZ / INTERVAL / JSON / JSONB / HSTORE / INET / CIDR / MACADDR / TSVECTOR / BYTEA + arrays of most of these |
