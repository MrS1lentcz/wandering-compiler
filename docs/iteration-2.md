# Iteration 2 — Alter-Diff + Schema Evolution

Contract for iteration-2. Successor to `docs/iteration-1.md`: iter-1 closed
the initial-migration path end-to-end (proto → IR → SQL → applied). Iter-2
opens the **schema-evolution** path: real projects change over time, and
alter-diff is the missing piece between "one-shot generator" and "real
migration pipeline".

Companion documents:
- [`iteration-1.md`](iteration-1.md) — iter-1 contract (D1–D23), still
  authoritative for everything the differ consumes (IR shape, carrier
  dispatch, namespace rules, index structure, …).
- [`iteration-1-impl.md`](iteration-1-impl.md) — iter-1 implementation
  status. Closed 2026-04-22, reinforced with conventions + coverage
  sweep 2026-04-23.
- [`iteration-2-backlog.md`](iteration-2-backlog.md) — candidate list
  for iter-2+. This document formalises the pieces as we commit to
  them, milestone by milestone.

## Status

**In progress — 2026-04-25.** M1 / M2 / M3 shipped across
2026-04-23 → 2026-04-24 sessions (alter-diff end-to-end on PG 18,
multi-file schemas, multi-connection per domain, D28 classifier +
D30 engine isolation Phase 2+4). M4 is now the open milestone —
capability usage tracking + platform manifest first (Layers A+B),
MySQL stub follows in a later turn.

- design turn → D-record in this doc → implementation → per-feature
  commit → `make test-apply` green → push.

## Goal

Close the gap between "iter-1 generates an initial migration" and "wc
produces every migration a real project needs across its lifetime." Four
strands drive the iteration:

1. **Alter-diff.** `plan.Diff(prev, curr)` walks both schemas and emits
   the ordered sequence of Ops that takes the DB from prev to curr.
   Biggest block — 5+ milestones inside.
2. **Multi-file schemas** with cross-file FK. Iter-1 accepts one proto
   per `wc generate`; iter-2 accepts the set that makes up a domain
   (or multiple domains) and resolves FK across files.
3. **Multi-connection per domain.** A domain may run across more than
   one DB (PG main + SQLite configs + future KV); tables opt into
   secondary connections while keeping the typed-schema benefit.
   Pinned by D26.
4. **Dialect + platform readiness.** Feature-capability *usage tracking*
   (catalog was shipped iter-1; now the emitter records which caps it
   used), MySQL emitter stub, and the manifest format the deploy client
   will read.

DQL and the local schema validator stay parked in the backlog as
iter-2+ tracks — huge enough that they each deserve their own spec
when the M-blocks below land.

## Milestones

Ordered so each milestone compiles + tests against the previous one.
Only M1 has a locked design below; later Ms get their own section
when they open.

- **M1 — Complete alter-diff + applied-state tracking.** Every
  structural schema evolution wc can emit lands here: Add / Drop /
  Rename / Alter for tables, columns, indexes, FKs, CHECKs, raw_*
  entries, comments, namespace moves. `wc_migrations` bookkeeping
  table (D27) shipped alongside the first migration and `INSERT`ed
  into by every subsequent one. The intentional "big" milestone —
  partial alter-diff is useless in practice (real schema changes
  touch multiple axes at once), so we complete it before moving on.
- **M2 — Multi-file schemas + cross-file FK.** Loader already uses
  protocompile; CLI gate loosens to accept the file set. FK target
  resolution grows a schema-wide registry.
- **M3 — Multi-connection per domain.** Bucket IR by connection; run
  the differ + emit per bucket; output tree grows
  `out/<domain>/migrations/<dialect>-<version>/`. D26 locks the
  identity + invariants; M3 is the implementation milestone.
- **M4 — Capability usage tracking + MySQL stub + platform manifest.**
  Emitter records which caps it touched during a run; output gains a
  `manifest.json` with the cap list + extension prerequisites.
  MySQL emitter stub from iter-1 grows enough surface to run the
  grand-tour fixtures on a second dialect. Platform + deploy-client
  contract firms up around the manifest.
- **Iter-3+ — DQL, local schema validator.** Pulled from the backlog
  as the M1–M4 stack stabilises. Each is big enough to be its own
  iteration spec when it opens.

## M1 — Complete alter-diff + applied-state tracking

### Scope in

`plan.Diff(prev, curr)` emits every migration shape iter-1's IR can
describe. Every Op variant below is implemented, tested end-to-end
against PG 18, and carries a data-survival-roundtrip fixture.

- **Table-level ops.**
  - `AddTable` (iter-1 path reused verbatim).
  - `DropTable` (FQN only in prev).
  - `RenameTable` (`(w17.db.table).name` changed, FQN stable).
  - `SetTableNamespace` (`(w17.db.module).namespace` changed, FQN
    stable; SCHEMA→SCHEMA = `SET SCHEMA`, PREFIX→PREFIX = `RENAME TO`
    with new prefix, mode-change = data-preserving drop/recreate —
    see alter-strategies below).
  - `SetTableComment` (D22a — COMMENT ON TABLE add / change / drop).
- **Column-level ops.**
  - `AddColumn` (number only in curr).
  - `DropColumn` (number only in prev).
  - `RenameColumn` (number stable, `name` changed — free via D10).
  - `AlterColumn` — consolidated op carrying the changed-fact set:
    nullability, default, max_len / precision / scale, db_type,
    PgOptions, comment, generated_expr, checks (Length / Blank /
    Range / Regex / Choices), carrier transitions (the few safe ones;
    unsafe ones refuse with a fix hint).
- **Index ops.** `AddIndex`, `DropIndex`, `ReplaceIndex` (any fact
  change = drop + recreate; PG has no `ALTER INDEX` for fields /
  method / unique / include / storage). Identity = explicit name,
  falling back to derived name for unnamed indexes (D14 pattern).
- **Raw index / raw check ops.** `AddRawIndex`, `DropRawIndex`,
  `ReplaceRawIndex` / `AddRawCheck`, `DropRawCheck`,
  `ReplaceRawCheck`. Body-by-name identity (D11 pin). Opaque body
  compared as a string.
- **FK ops.** `AddForeignKey`, `DropForeignKey`, `ReplaceForeignKey`
  (column / target / deletion_rule change). Identity = derived FK
  constraint name.
- **Structured check ops.** `AddCheck`, `DropCheck`, `ReplaceCheck`.
  Identity = derived constraint name; body compared by variant +
  fields.
- **CLI.** `wc generate --prev path/to/prev.proto path/to/curr.proto`
  runs the differ; when `--prev` is absent, behaviour falls back to
  iter-1 (initial migration). Output naming unchanged per D5.
- **Determinism.** Same contract as iter-1 (AC #4): `Diff` is a pure
  function of (prev, curr) + their proto source positions.
- **Applied-state table.** Every generated migration, including
  the initial one, carries the `wc_migrations` bookkeeping SQL per
  D27. Initial migration `CREATE`s the table + `INSERT`s its own
  row. Subsequent migrations just `INSERT` (up) / `DELETE` (down).

### Single-connection assumption (M1 only; M3 opens multi-connection)

M1 runs against one connection per `wc generate` call. D26 locks the
multi-connection model so M3 doesn't need a proto reshape when it
opens — the IR fields `Schema.connection` + `Table.connection` land in
M1 and default to the project-level default connection so the single-
connection path is a degenerate case of the general shape. `plan.Diff`
signature stays `(prev, curr *irpb.Schema)` — each call is one
connection; M3 wraps it in a per-connection orchestrator.

### Scope out (explicit)

The following stay parked per iter-2 backlog + iter-3 horizon. None
of them is "partial alter-diff"; they're orthogonal concerns.

- **User-supplied data backfills.** Where alter-diff emits a
  `USING (…)` cast or refuses a lossy change, the author's fix is
  a separate hand-written migration (or an extra raw-SQL block in
  the schema). Compiler emits a `-- wc: lossy transform refused`
  diagnostic with a fix hint rather than silently reshaping data.
- **Cross-domain FK cycles**, `DEFERRABLE` constraints, EXCLUDE
  constraints — already parked in iter-2 backlog; alter-diff of
  them lands with their structured-surface graduation (DQL era).
- **Local schema validator** (replay migrations against local state)
  — iter-3 horizon.

### Design

**Differ shape.** `plan.Diff(prev, curr)` walks the two schemas in
four stages:

1. **Bucket tables by `MessageFqn`.**
   - `onlyPrev` → `DropTable`.
   - `onlyCurr` → `AddTable` (iter-1 path).
   - `both` → pass to stages 2–4 for that table pair.
2. **Per carried-over table, collect table-level fact changes.**
   - `name` change → `RenameTable`.
   - `namespace` / `namespace_mode` change → `SetTableNamespace`
     (strategy per alter-strategies table).
   - `comment` change → `SetTableComment`.
3. **Per carried-over table, bucket columns by proto field number.**
   - `onlyPrev` → `DropColumn`.
   - `onlyCurr` → `AddColumn`.
   - `both` → fact-compare. Differences collapse into **one**
     `AlterColumn` op carrying the changed-fact set (not N ops per
     changed field — one ALTER TABLE statement can combine most
     PG alter-column actions).
4. **Per carried-over table, set-diff indexes / FKs / checks / raw_*
   by their identity keys.**
   - Name present only on prev → `Drop…`.
   - Name present only on curr → `Add…`.
   - Both present with different facts → `Replace…` (= Drop + Add
     inside one Op for clarity in the plan).

**Ordered plan assembly.** Within one migration the Op order is:

1. **Drops first, leaves before roots.**
   - `DropForeignKey` / `DropCheck` / `DropIndex` / `DropRawIndex` /
     `DropRawCheck` (per-table, so constraints that block later drops
     get out of the way).
   - `DropColumn`.
   - `DropTable` — in reverse FK topological order (referencers
     first; iter-1's `topoSortByFK` reversed).
2. **Structural adds in dependency order.**
   - `AddTable` — iter-1 topological order.
   - `AddColumn`.
   - `AlterColumn` (type / nullability / default / comment / …).
   - `RenameColumn`, `RenameTable`, `SetTableNamespace`,
     `SetTableComment` — rename / move ops batched after structural
     adds so they don't conflict with a "new table of the same name".
3. **Index / constraint adds last.**
   - `AddIndex` / `AddRawIndex`, then `AddForeignKey`, then
     `AddCheck` / `AddRawCheck`. Order chosen so FKs land on
     indexed columns when possible (helps PG plan FK checks during
     table rewrites).
4. **`wc_migrations` row last.** `INSERT INTO wc_migrations VALUES
   (<ts>, now(), <content_sha256>)` appended to every `up.sql`;
   `DELETE FROM wc_migrations WHERE timestamp = <ts>` prepended (so
   a partial failure on down doesn't leave an applied-state entry
   without its matching forward SQL).

**Fact equality.** Every Column / Index / FK / Check field compared
structurally (proto message equality via `proto.Equal`). Differences
not recognised by the Op variants above fall through to an
`AlterColumn` with the changed-fact set, or to `ReplaceIndex` /
`ReplaceForeignKey` etc. No fact change silently no-ops.

**Rename detection.** Free via the identity keys: column rename =
same number, different name → one `RenameColumn` Op (no
Drop+Add false positive). Table rename inside the same FQN = same
FQN, different `Table.Name` → one `RenameTable`. Message FQN change =
explicit Drop + Add (D24 stance: FQN is the contract).

**Apply-roundtrip harness.** For each fixture pair
`(prev.proto, curr.proto)` the harness runs:

```
fresh DB
→ apply(prev up.sql)            # bootstrap via iter-1 path
→ apply(diff up.sql)             # the thing under test
→ schema introspection assertion # curr shape matches live DB
→ apply(diff down.sql)
→ schema introspection assertion # prev shape restored
→ apply(prev down.sql)
→ empty DB (sanity)
```

Goldens cover both up + down SQL. Introspection assertion = "set of
tables / columns / indexes / FKs / checks observed via
`information_schema` + `pg_catalog` matches the expected IR."

**Column alter strategies (per-fact table).** Each fact class has a
pinned strategy; emitter routes to it deterministically. Strategy
column values: **DIRECT** (plain `ALTER COLUMN …` — PG rejects the
apply if live data doesn't fit; that's the right outcome, deploy
client / platform handles pre-apply data validation per the platform
contract); **USING** (PG needs an explicit `USING <cast>` to do the
conversion at all — same fail-on-incompatible-data semantics);
**DROP+ADD** (PG has no ALTER for this fact — drop constraint /
index / generated expr, re-add); **REFUSE** (proto-wire breaking
change — compiler emits `diag.Error` at generate time because the
shape of the change doesn't have a non-destructive alter path).

The compiler does **not** emit warning comments or data-loss gating.
Data survival is the deploy client's job (runs pre-apply checks
against real data, surfaces issues to platform UI for human review);
PG itself is the last line of defence (refuses the ALTER at apply
time if live rows don't match). Compiler stays deterministic.

| Fact | Strategy |
|---|---|
| `name` | DIRECT — `ALTER TABLE t RENAME COLUMN a TO b` |
| `nullable` NOT NULL → NULL | DIRECT — `ALTER COLUMN DROP NOT NULL` |
| `nullable` NULL → NOT NULL | DIRECT — `ALTER COLUMN SET NOT NULL` (PG fails if any NULL row exists; deploy client pre-checks) |
| `default` add / change / drop | DIRECT — `SET DEFAULT …` / `DROP DEFAULT` |
| `comment` add / change / drop | DIRECT — `COMMENT ON COLUMN …` |
| `max_len` widen | DIRECT — `ALTER COLUMN TYPE VARCHAR(N_new)` |
| `max_len` narrow | DIRECT — `ALTER COLUMN TYPE VARCHAR(N_new)` (PG refuses apply if any row exceeds N_new) |
| `precision` / `scale` widen | DIRECT — `ALTER COLUMN TYPE NUMERIC(p_new, s_new)` |
| `precision` / `scale` narrow | DIRECT — PG refuses apply on overflow |
| `db_type` compatible (TEXT↔VARCHAR, JSON↔JSONB) | USING — `ALTER COLUMN TYPE … USING col::<new>` |
| `db_type` incompatible (INTEGER↔TEXT) | USING — emitter writes the cast; PG refuses apply if any row fails the cast |
| `pk` change | REFUSE — PK change is table-rebuild territory; explicit drop+recreate |
| `unique` add / drop | DROP+ADD — `ADD CONSTRAINT UNIQUE` / `DROP CONSTRAINT` |
| `storage_index` add / drop | DROP+ADD — implicit non-unique index |
| `carrier` change | REFUSE — proto wire type change = new field number; diag hints author |
| `element_carrier` / `element_is_message` | REFUSE — collection reshape |
| `enum_names` add value (proto enum appended) | DIRECT — `ALTER TYPE <enum> ADD VALUE 'new'` (PG 12+) |
| `enum_names` remove value | REFUSE — PG can't drop enum values; fix: drop+recreate type |
| `enum_numbers` change mapping | REFUSE — data-meaning change |
| `generated_expr` add | DROP+ADD — can't add GENERATED to an existing column in PG |
| `generated_expr` change | DROP+ADD |
| `generated_expr` remove | DIRECT — `ALTER COLUMN DROP EXPRESSION` (PG 13+) |
| `checks` any change | DROP+ADD the specific CHECK constraint |
| `allowed_extensions` change | DROP+ADD the derived regex CHECK |
| `pg.custom_type` change | REFUSE — custom_type is author-owned |
| `pg.required_extensions` change | IR-only — no column DDL emitted; extension manifest updates |

**Index / FK / constraint alter strategies.**

| Fact | Strategy |
|---|---|
| Index: any field change (fields, method, unique, include, storage, per-field desc/nulls/opclass) | DROP+ADD — PG has no `ALTER INDEX` for shape |
| Index: name change | Identity = name, so a name change *is* a drop+add by definition |
| Raw index / raw check: body change | DROP+ADD |
| FK: column / target / deletion_rule change | DROP+ADD — `ALTER CONSTRAINT` in PG only covers `DEFERRABLE` (not scope of iter-2) |
| Structured CHECK: any change | DROP+ADD |
| Table namespace SCHEMA↔SCHEMA | SAFE — `ALTER TABLE t SET SCHEMA new_schema` |
| Table namespace PREFIX↔PREFIX | SAFE — `ALTER TABLE old_prefix_t RENAME TO new_prefix_t` |
| Table namespace SCHEMA↔PREFIX | chain — `SET SCHEMA public` + rename + drop source schema (if empty) or reverse; emits explanatory comment |
| Table namespace NONE↔SCHEMA | `SET SCHEMA` |
| Table namespace NONE↔PREFIX | `RENAME TO` |

**Fixtures.** Minimum set (every row = one fixture pair in
`plan/testdata/alter/<name>/{prev.proto,curr.proto,up.sql,down.sql}`).
Grouped; grand-tour fixture at the bottom combines many axes.

- Table axis: `add_table`, `drop_table`, `rename_table`,
  `set_schema_move`, `prefix_to_schema`, `table_comment_change`.
- Column axis: `add_column`, `drop_column`, `rename_column`,
  `nullable_loosen`, `nullable_tighten`, `default_add`,
  `default_change`, `default_drop`, `max_len_widen`,
  `max_len_narrow`, `numeric_precision_widen`, `db_type_compat`
  (TEXT→CITEXT), `db_type_cast` (INT→TEXT via USING),
  `comment_change`, `enum_add_value`, `enum_remove_value_refuse`,
  `carrier_change_refuse`, `generated_expr_add`,
  `generated_expr_remove`.
- Index axis: `add_index`, `drop_index`, `replace_index_method`
  (BTREE → GIN), `replace_index_add_include`,
  `add_raw_index`, `replace_raw_index_body`.
- FK axis: `add_fk`, `drop_fk`, `replace_fk_deletion_rule`,
  `replace_fk_target`.
- CHECK axis: `add_length_check`, `drop_blank_check`,
  `replace_range_check`, `add_raw_check`, `replace_raw_check_body`.
- Applied-state: `wc_migrations_initial_create`,
  `wc_migrations_insert_only`,
  `wc_migrations_hash_detects_edit` (error fixture: edited-file
  content_sha256 mismatch).
- Orchestration: `alter_noop` (prev == curr, empty plan, no files
  written per Open Question #1), `alter_fk_chain_drop` (drop
  order = reverse topo), `alter_grand_tour` (one proto pair
  exercising ≥20 axes together — regression net).

Target count: **~40 fixture pairs.** Iter-1 shipped 16 positive
fixtures; M1 roughly doubles that.

### Plan op proto changes

`proto/domains/compiler/types/plan.proto` grows the `Op` oneof to
cover every variant. Every Op that targets a carried-over table
carries `table_message_fqn` + `table_name` + namespace fields so
emit stays Op-local (iter-1 plan.proto convention).

```proto
message Op {
  oneof variant {
    AddTable            add_table             = 1;   // iter-1
    DropTable           drop_table            = 2;
    RenameTable         rename_table          = 3;
    SetTableNamespace   set_table_namespace   = 4;
    SetTableComment     set_table_comment     = 5;

    AddColumn           add_column            = 10;
    DropColumn          drop_column           = 11;
    RenameColumn        rename_column         = 12;
    AlterColumn         alter_column          = 13;

    AddIndex            add_index             = 20;
    DropIndex           drop_index            = 21;
    ReplaceIndex        replace_index         = 22;
    AddRawIndex         add_raw_index         = 23;
    DropRawIndex        drop_raw_index        = 24;
    ReplaceRawIndex     replace_raw_index     = 25;

    AddForeignKey       add_foreign_key       = 30;
    DropForeignKey      drop_foreign_key      = 31;
    ReplaceForeignKey   replace_foreign_key   = 32;

    AddCheck            add_check             = 40;
    DropCheck           drop_check            = 41;
    ReplaceCheck        replace_check         = 42;
    AddRawCheck         add_raw_check         = 43;
    DropRawCheck        drop_raw_check        = 44;
    ReplaceRawCheck     replace_raw_check     = 45;

    WcMigrationsCreate  wc_migrations_create  = 90;  // D27 init
    WcMigrationsInsert  wc_migrations_insert  = 91;  // D27 per-migration
  }
}
```

Tag numbers are grouped (1-9 table, 10-19 column, 20-29 index,
30-39 FK, 40-49 check, 90-99 applied-state) so future additions
(DQL iteration, multi-file M2) have room to slot in without
renumbering.

Messages (abbreviated; all carry `w17.compiler.ir.*` fields + the
namespace-qualifier block the emitter needs):

```proto
message DropTable    { w17.compiler.ir.Table table = 1; }
message RenameTable  { string message_fqn = 1; string from_name = 2; string to_name = 3;
                       w17.compiler.ir.NamespaceMode namespace_mode = 4; string namespace = 5; }
message SetTableNamespace {
  string message_fqn = 1; string table_name = 2;
  w17.compiler.ir.NamespaceMode from_mode = 3; string from_namespace = 4;
  w17.compiler.ir.NamespaceMode to_mode   = 5; string to_namespace   = 6;
}
message SetTableComment {
  string message_fqn = 1; string table_name = 2;
  w17.compiler.ir.NamespaceMode namespace_mode = 3; string namespace = 4;
  string from = 5; string to = 6;  // "" = absent
}

message AddColumn    { string table_message_fqn = 1; w17.compiler.ir.Column column = 2;
                       string table_name = 3;
                       w17.compiler.ir.NamespaceMode namespace_mode = 4; string namespace = 5; }
message DropColumn   { /* same shape as AddColumn; column carries pre-state for DOWN */ }
message RenameColumn { string table_message_fqn = 1;
                       int32 field_number = 2;
                       string from_name = 3; string to_name = 4;
                       string table_name = 5;
                       w17.compiler.ir.NamespaceMode namespace_mode = 6; string namespace = 7; }

// AlterColumn carries the changed-fact set. Every FactChange has
// from + to. Emitter walks the set and chooses per-fact strategy
// per the table in "Column alter strategies".
message AlterColumn {
  string table_message_fqn = 1;
  int32  field_number = 2;
  string column_name = 3;
  string table_name = 4;
  w17.compiler.ir.NamespaceMode namespace_mode = 5; string namespace = 6;
  repeated FactChange changes = 10;
}

message FactChange {
  oneof variant {
    NullableChange  nullable  = 1;
    DefaultChange   default_  = 2;
    MaxLenChange    max_len   = 3;
    NumericChange   numeric   = 4;  // precision + scale
    DbTypeChange    db_type   = 5;
    PgOptionsChange pg        = 6;
    CommentChange   comment   = 7;
    GeneratedExprChange generated_expr = 8;
    ChecksChange    checks    = 9;   // carries diff of the repeated Check
    EnumValuesChange enum_values = 10;
    AllowedExtensionsChange allowed_extensions = 11;
    // Carrier / pk / storage_index changes go here with REFUSE
    // strategy — emitter writes `-- wc: lossy transform refused`
    // instead of DDL and the generator surfaces a diag.Error.
  }
}

message AddIndex / DropIndex / ReplaceIndex / AddForeignKey / …
// All follow the same "table context + target IR node" shape.

message WcMigrationsCreate {
  // Emitted in the very first migration for a (domain, connection).
  // Table lives in the connection's default schema — no qualifier.
}

message WcMigrationsInsert {
  // Emitted at tail of every up.sql; matching DELETE goes at head
  // of down.sql. Timestamp = the migration's D5 filename stem;
  // content_sha256 = sha256 of the up.sql body (computed AFTER the
  // body is otherwise finalised, and appended to the body as the
  // last statement — so the hash covers everything before it).
  string timestamp = 1;        // e.g. "20260423T143015Z"
  bytes  content_sha256 = 2;
}
```

Details (full FactChange sub-messages, the Replace* shapes) land
in the proto file itself during implementation; the shape above is
the skeleton the iter-2.md commit pins.

## Decisions

### D24 — Table identity = `MessageFqn` (pins D19 open question, added 2026-04-23)

**Decision.** The differ identifies tables across (prev, curr) by
`ir.Table.MessageFqn` — the proto message fully-qualified name. Rename
the proto message ⇒ FQN changes ⇒ emit `DropTable` + `AddTable`. A
change to `(w17.db.table).name` with the FQN held constant ⇒
`AlterTable RENAME` (M2+). A change to `(w17.db.module).namespace` or
mode ⇒ `AlterTable SET SCHEMA` (PG) or a prefix-rename (M2+); MessageFqn
is untouched.

**Rationale.**

1. **Consistent with D10.** D10 already chose proto field numbers
   (stable identity in source) over column names (ambiguous surface
   form) for per-column identity. For tables the analogue is
   message FQN: stable in source, guaranteed unique within a proto
   namespace, never legitimately "reused." The `(mode, ns, name)`
   tuple is the surface form — renaming the SQL table in a pilot
   would read as DropTable+AddTable under that key, which is
   user-hostile because no data-destroying change happened.
2. **Rename + namespace-move detection is free.** With FQN as
   identity, renaming the SQL table is an ALTER RENAME (data-
   preserving) and moving namespaces is SET SCHEMA (data-preserving).
   No heuristics, no similarity threshold.
3. **Proto field renames already absorbed.** A proto field rename
   (e.g. `name` → `display_name`) changes neither the field number
   nor the message FQN — column `AlterColumn` catches it at M2 and
   emits `ALTER COLUMN RENAME`. Table-level rename shape mirrors
   that exactly.
4. **Skeleton-stage reality.** `MessageFqn` is already on every IR
   table per D4 rev 2026-04-21. No proto reshape. No state migration.

**Invariants.**

- `ir.Table.MessageFqn` is required and validated non-empty in
  `ir.Build` (today's state).
- Two tables in the same schema can never share an FQN (proto
  itself forbids duplicate message names).
- MessageFqn is the only identity key the differ consults for
  tables. `Table.Name`, `Table.Namespace`, `Table.NamespaceMode`
  are compared as **facts**, not identity — their differences drive
  ALTER ops, not drop/create.

**Escape hatches.** None needed. If the user wants to force
drop+create (e.g. because the new table is semantically a
brand-new thing despite sharing the FQN with a legacy one), they
delete the message and add a fresh one with a different FQN. The
friction is proportional to the semantic weight of "I mean to
destroy my data."

### D25 — Prev IR source: `--prev` in iter-2, platform-supplied later (added 2026-04-23)

**Decision.** M1 CLI grows a `--prev <path.proto>` flag. When present,
the loader builds IR for both prev and curr protos and hands them to
`plan.Diff`. Absent → initial-migration path (prev = nil), identical
to iter-1. Multi-file prev (when M2 lands) uses `--prev-dir <path>`
or `--prev` repeated.

Long-term (platform milestone), the platform supplies the prev
proto directly — it stores every generated migration + its source
proto per iter-1 D6. The CLI flag stays as the local-dev path;
platform runs bypass it.

**Rationale.**

1. **Pure-function differ.** `plan.Diff(prev, curr)` stays a pure
   function of two IRs. Where prev comes from is a CLI / platform
   concern orthogonal to the diff logic.
2. **No stateful `out/` layer.** The iter-1 D6 stance ("migrations
   are not source-committed") rules out storing prev IR in the
   user's repo. Storing it in `out/` is fragile across machines,
   same reasoning D5 used to reject sequence numbers.
3. **Local-dev flow works without the platform.** Pilots run `wc
   generate --prev old.proto new.proto`, review the diff, iterate.
   No platform dependency to unblock M1.
4. **Local schema validator (backlog) builds on this.** The
   validator's "replay migrations against a local DB state"
   approach is an alternate prev source that sits behind the same
   interface — feed the replayed schema in as prev. Future work,
   but the design stays compatible.

**Invariants.**

- `--prev` takes a proto path, not an IR / SQL snapshot. IR is
  internal and the compiler owns it end-to-end; the user talks
  in proto.
- Prev proto is validated through the same `ir.Build` pipeline as
  curr. Any prev-side validation error is a user-visible error
  (can't diff against a broken schema).
- Platform takes over prev-supply transparently: the CLI stub
  that talks to the platform feeds `--prev` behind the scenes.

### D26 — Multi-connection per domain; identity = `(dialect, version)` (added 2026-04-23)

**Decision.** A domain may declare multiple DB connections (main PG +
side SQLite for configs, future KV store, …). The schema compiler
carries connection-scoping into the IR so migrations, emitters, and
DQL can reason per connection without losing the typed-schema benefit
that protobuf provides.

- `(w17.db.module)` (FileOptions) gains a `connection` block:
  `{ name: string, dialect: enum { POSTGRES, MYSQL, SQLITE, … },
     version: string }`. Absent → module runs on the project-level
  default connection.
- `(w17.db.table)` gains an optional `connection: string` that must
  resolve to a connection declared in a module option in the same
  domain. Absent → table inherits its module's connection.
- **Identity within a domain = `(dialect, version)` pair.** Two
  connection declarations in one domain must have distinct
  `(dialect, version)` — same PG 17 twice is forbidden. The domain
  boundary is the DB-isolation boundary; "two of the same" means
  two domains.
- **Connection directory key** = `<dialect>-<version>` (lower-kebab,
  version normalised to `<major>[.<minor>]` where DDL can differ —
  MySQL 8.0 vs 8.4 keeps both; PG 17 vs 18 keeps both).
- **Output tree.** `out/<domain>/migrations/<dialect>-<version>/
  YYYYMMDDTHHMMSSZ.{up,down}.sql`. D6 carries: `out/` stays
  gitignored; migrations remain platform artifacts.
- **Differ runs per connection.** `plan.Diff` signature unchanged; the
  orchestrator buckets tables by connection key and calls Diff N×.
- **Capability catalog** dispatches per `(dialect, version)`. PG 17
  and PG 18 share most caps but the catalog can differ where new
  syntax landed (e.g. `uuidv7()` on PG 18+).

**Rationale.**

1. **Typed-schema over non-relational surfaces.** Authors often
   need a small SQLite for configs or a KV store for session blobs
   alongside the main DB. Today they hand-write the wrapper;
   wc can give them the same proto-first, gRPC-typed experience
   over every backend.
2. **Domain = isolation boundary.** Forbidding two `(PG, 17)` in
   one domain prevents the "sub-domain-within-domain" anti-pattern.
   If a project wants to split the users DB from the sessions DB
   and both are PG 17, that's two domains. The convention makes
   the split visible in the source tree.
3. **Migration layout is self-documenting.** Operator lands in
   `out/users/migrations/postgres-17/` and instantly knows what
   they're looking at. No manifest needed to decode filenames.
4. **DQL intelligence angle.** Cross-connection reads split into
   per-connection fetches + app-layer compose. Cross-connection
   mutations carry a `non_atomic` flag surfaced in the generated
   handler wrapper + admin UI — not a block, a warning badge
   (author chose the split; wc just makes it visible).

**Invariants.**

- `(dialect, version)` uniqueness enforced in `ir.Build` at the
  domain level. Violation surfaces as
  `diag.Error("connection already declared", why:"two modules in
  domain X both declared (postgres, 17)", fix:"split into two
  domains or merge the modules")`.
- `(w17.db.table).connection` must reference a name declared in a
  module option in the same domain. Cross-domain references are
  forbidden at this axis (FK across domains is M2's concern; a
  cross-domain `connection` pointer would fracture the isolation
  boundary).
- `Schema.connection` and `Table.connection` are IR-level facts
  carried on every Schema/Table. M1 populates them with the
  default; M3 activates the non-default path.
- Capability validation runs per-connection. A table using
  `uuidv7()` on a `(postgres, 17)` connection fails at IR build
  time with a capability-floor diagnostic.

**Escape hatches.**

- Need two `(PG, 17)` connections in one domain → author splits
  into two domains. The friction is proportional to the structural
  signal ("these are two separate responsibilities, your directory
  layout should reflect that").
- Need a dialect/version the emitter doesn't support yet → author
  stays on the supported floor or lands the emitter first. No
  silent fallback to a "closest-supported" version (that would
  defeat the capability-floor discipline).
- Want to bypass the per-connection split for a quick script →
  standard raw-SQL escape hatch via `raw_checks` / `raw_indexes`
  stays per-table and runs inside that table's own connection.

**Relation to prior decisions.**

- **D4** (own IR + differ + per-dialect emitters) — D26 extends
  the per-dialect axis to per-connection, no shape collision.
- **D6** (migrations are platform artifacts, gitignored) — layout
  tweak only; the gitignored + platform-owned principle carries.
- **D10 / D24** (identity keys) — unchanged; both still scoped
  per-connection now.
- **D16** (capability catalog) — gains a per-connection dispatch
  layer; same constants, per-connection selection.
- **D19** (namespace: schema XOR prefix) — composes with
  connection: each module in a domain picks namespace + connection
  independently.

**Timing.** D26 locks the model now so M1 builds the IR shape and
default-connection handling correctly. Actual multi-connection
orchestration is M3.

### D27 — Applied-state tracking via `wc_migrations` table (added 2026-04-23)

**Decision.** Every target DB carries exactly one bookkeeping table
named `wc_migrations` that records which migrations have been applied.
One domain = one DB (primary connection); secondary connections in a
domain (e.g. side SQLite for configs) are separate DB instances with
their own `wc_migrations`. Either way, on any given DB instance the
table is unique — no domain scoping needed in the name or schema
because the DB itself is domain-scoped by D26's
"(dialect, version) unique per domain" rule.

The schema compiler emits the `CREATE TABLE` in the very first
migration for a (domain, connection) and an `INSERT` row at the tail
of every subsequent migration's `up.sql`. Down-migrations prepend a
matching `DELETE FROM wc_migrations` so rollback is symmetric.

```sql
CREATE TABLE wc_migrations (
  timestamp      TIMESTAMPTZ PRIMARY KEY,  -- D5 filename stem, parsed to TIMESTAMPTZ
  applied_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  content_sha256 BYTEA NOT NULL             -- sha256 of the up.sql body
);
```

Placement: connection's default schema (PG `public`). One table per
DB instance. Multi-module domains share the one table at the domain
level — module namespaces (D19, SCHEMA / PREFIX) are user-schema
scoping for the author's tables, not for wc bookkeeping.

**Rationale.**

1. **Minimal state needed for deploy client.** The deploy client
   connects, reads `SELECT max(timestamp) FROM wc_migrations`,
   compares to the platform-known next migration, applies missing
   ones in order. Timestamp-as-PK is the whole story because the
   platform already serialises migration creation — no DAG /
   merge-branch problem to solve (that's the Django pain).
2. **Django difference in one line.** Django stores
   `(app, name, applied)`; a DAG is required because per-app linear
   chains merge via `dependencies = [(…)]`. wc's platform refuses
   concurrent generations for a connection, so the sequence is
   always linear and the PK is just the timestamp.
3. **Integrity check via `content_sha256`.** If a migration file
   is hand-edited after platform approval, the hash diverges when
   the deploy client re-computes it. Deploy client refuses to
   apply and surfaces the mismatch. Costs 32 bytes per row;
   prevents a whole class of "migration drifted between approval
   and apply" bugs.
4. **No application-level migration manager.** The `wc_migrations`
   table is *the* source of truth on the DB. No separate `.migrations`
   directory scan, no in-memory state, no sidecar files. Operator
   can inspect applied state with `psql` alone.

**Invariants.**

- `timestamp` in the table = D5 filename stem parsed to
  `TIMESTAMPTZ`. Format `YYYYMMDDTHHMMSSZ` round-trips via PG
  `to_timestamp('…', 'YYYYMMDD"T"HH24MISS"Z"')`.
- `content_sha256` is sha256 over the full `up.sql` body *up to*
  (excluding) the final `INSERT INTO wc_migrations …` statement.
  This makes the hash self-consistent — the hash-carrying INSERT
  statement can't include itself in the hash it stores.
- Emit order: initial migration for a (domain, connection) carries
  both `WcMigrationsCreate` and `WcMigrationsInsert` Ops. Every
  subsequent migration carries only `WcMigrationsInsert`.
- Down-migration shape: `DELETE FROM … WHERE timestamp = '<ts>';` at
  the very *head* of `down.sql`. Initial-migration down carries
  `DROP TABLE wc_migrations` at the end (after all other drops).
- Hash comparison is deploy-client's responsibility; compiler just
  emits the value it computes at generate time. Tests cover the
  "edited file detected" roundtrip.

**Escape hatches.**

- Author wants to skip `wc_migrations` for a one-off scratch DB
  → `--no-applied-state` CLI flag. Not intended for production;
  tested locally then disabled.
- Author already has their own migration tracking (legacy import)
  → first wc migration carries `IF NOT EXISTS` on the `CREATE
  TABLE`, and inserts via `ON CONFLICT (timestamp) DO NOTHING` so
  re-running against an existing state doesn't fight the legacy
  table. Makes opt-in gentle.

**Timing.** Lands in M1 alongside every Op above. Without
`wc_migrations` the deploy client has no way to distinguish
"migration N applied" from "migration N not applied," so partial
M1 shipping blocks future platform work. Small cost, large
unblock.

### D28 — Migration-safety classification matrix (added 2026-04-23)

**Status: draft — YAML extraction in progress (2026-04-23).**
Captures the complete fact-pair × strategy table that the differ uses
to classify every column / table / constraint change. Today's binary
SAFE / REFUSE split is too coarse for production use — D28 expands
it into **five** strategies and pins each fact transition to one.

> **Authoritative source of truth:** `docs/classification/*.yaml`.
> The markdown tables in D28.1–4 below are a human-readable rendering
> — the YAML wins on conflict. Phase 2 tests load the YAML directly
> and drive the classifier through every cell.
>
> Files:
> - `strategies.yaml` — the 5 strategies, rank-ordered, with DDL templates.
> - `carrier.yaml` — D28.2 (carrier × carrier, ~125 cells).
> - `dbtype.yaml` — D28.3 (dbType × dbType within carrier, flat per-family cells).
> - `constraint.yaml` — D28.1 (axis-indexed cells for column + table-level axes).
> - `sem.yaml` — **does not exist by design.** D28.4 sem transitions
>   are pure compositions of carrier.yaml + dbtype.yaml + constraint.yaml
>   cells; classifier synthesises them via the fold algorithm (see
>   D28.4 summary). Nothing independent to store.

**Strategies** (full definitions in `classification/strategies.yaml`):

- **SAFE** — type + data both fit; clean ALTER, no user input
  needed. (e.g. NOT NULL → NULL, max_len widen, default add.)
- **LOSSLESS_USING** — PG cast handles the conversion in-place,
  deterministically + value-preservingly. No check.sql, no decision.
  (e.g. TEXT ↔ CITEXT, JSON ↔ JSONB, BOOL → STRING.)
- **NEEDS_CONFIRM** — types are theoretically convertible but
  data may not fit (string→int, max_len narrow). Differ auto-emits
  a companion `check.sql`; deploy client runs it pre-apply. Zero-count
  → proceed; non-zero → block with a decision menu
  (DROP_AND_CREATE / CUSTOM_MIGRATION).
- **DROP_AND_CREATE** — author-acknowledged lossy change; existing
  data lost. Universal escape for any transition the compiler can't
  safely automate; requires explicit `--decide <col>=drop_and_create`.
  Never emitted silently.
- **CUSTOM_MIGRATION** — author writes SQL that transforms existing
  data before (or instead of) the structural change. Differ wraps
  the block in a managed transaction. Requires explicit
  `--decide <col>=custom:<path>`.

**REFUSE removal (2026-04-23).** Prior draft carried a sixth
strategy REFUSE for transitions with no "automatic" path. That
framing was structurally redundant — DROP_AND_CREATE is a universal
escape (drop the column, add it fresh; data is lost but schema
moves). Every prior REFUSE cell collapses to either DROP_AND_CREATE
(when the natural intent is "accept data loss") or CUSTOM_MIGRATION
(when the natural intent is "preserve data via custom SQL"). The
markdown tables below and the `carrier.yaml` file already carry
the reclassification; other YAML files migrate next turn.

**Governing rule (2026-04-23, user):**
> "Types must be compatible; no silent coercion. If source data
> isn't already in the target's canonical form, author writes the
> conversion via DQL/CUSTOM_MIGRATION — compiler never guesses
> unit, encoding, or semantic intent."

Practical consequences pinned in all three YAMLs:
- **Strict compatibility checks.** STRING→BOOL accepts only `'0'` /
  `'1'`; INT→BOOL accepts only `0` / `1`. No `'true'`/`'yes'`/`t`
  parsing; no "nonzero = true" remap.
- **Unit-ambiguous casts → CUSTOM_MIGRATION.** TIMESTAMP↔INT,
  DURATION↔INT, BYTES↔non-string-scalar all default to the author
  writing SQL. Compiler refuses to pick s/ms/μs or BE/LE.
- **Encoding is project-level.** BYTES↔STRING uses
  `{{.Project.Encoding}}` (from `w17.yaml`), never per-column
  `--decide`.
- **DROP_AND_CREATE is user-opt-in only.** The classifier never
  defaults to D except where PG semantics mandate it (A7
  generated_expr add/change: generated columns are value-rewriting
  by definition). User opts into D via `--decide
  col=drop_and_create` when accepting data loss is fine.

Post-rule counts: `carrier.yaml` 1 S / 13 U / 16 N / 0 D / 80 C;
`constraint.yaml` 27 S / 0 U / 20 N / 2 D / 8 C; `dbtype.yaml` 6 S /
20 U / 24 N / 0 D / 0 C. (CUSTOM_MIGRATION dominates `carrier.yaml`
because most cross-carrier transitions are unit/semantic-ambiguous
by the rule.)

**Decision plumbing (D29-aware):**

- Standalone mode: CLI flags `--decide users.email=using` or
  decisions YAML via `--decisions <file>`.
- Tool-integrated mode (D29): decisions live in the tool's
  migration plan, surfaced in tool UI with data-impact analysis,
  immutable after migration approval. CLI is a transparent
  client.

**Check.sql generation:**

Per fact-change that lands in NEEDS_CONFIRM, the emitter produces a
parallel `check.<ts>.sql` artifact with validation queries:

- `string → int`: `SELECT count(*) FROM t WHERE col !~ '^-?[0-9]+$' LIMIT 1`
- `max_len 200 → 50`: `SELECT count(*) FROM t WHERE char_length(col) > 50 LIMIT 1`
- `NULL → NOT NULL`: `SELECT count(*) FROM t WHERE col IS NULL LIMIT 1`

Operator (CI / deploy client / tool) runs check.sql before the
real migration; non-zero count → abort with structured fail
report.

**Test discipline:**

D28 is the foundation of an exhaustive table-driven test matrix:

- Carrier × carrier (8 × 8 = 64 cells)
- Sem × sem within carrier (~150 cases)
- DbType × DbType (~50 reachable cells)
- Constraint changes (max_len, precision, scale, nullable,
  unique, default, comment, etc.)

**Estimated ~500-800 generated test cases.** Each verifies:

1. Correct classification (SAFE / USING / NEEDS_CONFIRM / etc.).
2. Generated up.sql matches expected pattern.
3. Generated check.sql (when applicable) matches.
4. Apply-roundtrip on PG with seeded data — both happy path
   (data fits) and unhappy path (check.sql blocks the apply).

**Phasing:**

1. Document the full classification matrix in YAML (paper exhaustive,
   every fact transition pinned). **Status: complete as of 2026-04-23.
   All three active YAMLs (`carrier.yaml`, `dbtype.yaml`,
   `constraint.yaml`) shipped; `sem.yaml` is absent by design
   (reductions only).**
2. Refactor `plan/diff.go` strategy classifier from binary →
   five-strategy enum; emit structured "needs decision X" objects
   driven off the YAML (load once at build, switch on strategy).
3. CLI `--decide` flag plumbing.
4. `check.sql` emit pipeline (template rendering from YAML).
5. Test matrix exhaustive — table-driven test generator reads the
   YAML files, synthesises (prev, curr) Column pairs, asserts
   classifier pins each cell's expected strategy.

**Open questions:**

- ~~Should NEEDS_CONFIRM auto-generate check.sql even without user
  decision, so reviewer can preview risk?~~ — **resolved 2026-04-23
  via D30.** Engine always produces check.sql strings as part of
  `Migration.Checks[]`; no gating on user decision. Where/whether
  to persist them is `Sink`'s policy, not the engine's. The
  "storage location" concern that earlier deferred this question
  is now outside the engine layer entirely.
- **DROP_AND_CREATE proto-side semantics** — two workflows co-exist
  for a type change on an existing column:
    1. **Keep the proto field number, change the type.** D10 sees
       the number stable, matrix activates, classifier proposes
       DROP_AND_CREATE. Author confirms via `--decide`. Compiler
       emits `DROP COLUMN + ADD COLUMN`. **Side effect:** proto
       wire compatibility breaks (old readers see incompatible
       type for the kept field number).
    2. **Renumber the proto field + reserve the old number.**
       D10 sees old number missing + new number present ⇒ plain
       `DropColumn` + `AddColumn` without matrix involvement.
       No `--decide` needed. Proto wire stays clean (old readers
       see unknown field).
  Workflow 1 is faster; workflow 2 is safer. Compiler stance?
  Options: (a) refuse workflow 1 entirely; (b) allow with loud
  `diag.Warning`; (c) allow silently behind `--decide`. **Lean:
  (b) — warn but don't block; author's proto-wire discipline is
  their call.**
- ~~CUSTOM_MIGRATION location — inline in proto or external in
  decisions YAML?~~ — **resolved 2026-04-23** (user): custom SQL
  lives only in CLI-passed file or platform UI; never in proto,
  never in git-tracked decisions file. Proto is point-in-time-
  decision-free (matches D29 "decisions live in the tool, not
  in git").

### D28.1 — Classification matrix: column-constraint axes (added 2026-04-23)

**Status: draft, awaiting cell-by-cell sign-off.** Phase 1a of D28 —
the subset of axes that can change independently of the column's
carrier + dbType. Carrier-axis (D28.2) and dbType-axis (D28.3) land
in follow-up turns; they compose with this axis, not redundantly.

Scope: every `Column` fact the IR carries today (see `plan/diff.go`
`buildFactChanges`) plus table-level axes that diff without column
context. Each axis is an independent dimension — a single alter may
touch multiple, and the emitted strategy per alter is the **strictest**
across axes (SAFE < LOSSLESS_USING < NEEDS_CONFIRM < DROP_AND_CREATE
< CUSTOM_MIGRATION). "Add" / "drop" of a whole column never appears
here — that's `AddColumn` / `DropColumn`, pre-D28.

Terminology:
- **widen**: new accepts a superset of old values (VARCHAR(50)→VARCHAR(200), INTEGER→BIGINT).
- **narrow**: new accepts a subset (VARCHAR(200)→VARCHAR(50)).
- **Current**: today's binary classifier (for context; asterisk = silent-wrong risk the matrix closes).
- **check.sql**: pre-apply validation query the differ auto-generates for NEEDS_CONFIRM; D28 open question #1 leans "always emit, regardless of user decision."

#### A1. `nullable`

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| NULL → NULL | — | — | — | No change. |
| NOT NULL → NOT NULL | — | — | — | No change. |
| NOT NULL → NULL | **SAFE** | SAFE | — | Relaxing a constraint is always safe; DB accepts everything it accepted before plus NULLs. |
| NULL → NOT NULL | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE <col> IS NULL LIMIT 1` | Live rows may be NULL; PG refuses `SET NOT NULL` with a rewrite error. If author adds a `default` in the same migration, the differ's emit order handles backfill; without one, user must decide (backfill / skip / abort). |

#### A2. `default`

Applies to all carriers. `Default` proto message variants: literal scalar, `AutoKind` (AUTO_NOW / AUTO_UUID_V4 / etc.), empty JSON array/object, generator. Change classification is value-independent.

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| none → literal/auto | **SAFE** | SAFE | — | Affects only future inserts; existing rows retain prior values. |
| literal/auto → none | **SAFE** | SAFE | — | Drops the clause; existing rows unaffected. |
| literal A → literal B | **SAFE** | SAFE | — | Same as above; PG `ALTER COLUMN SET DEFAULT` is instantaneous. |
| auto (AUTO_NOW) ↔ literal timestamp | **SAFE** | SAFE | — | Default-clause swap; no row rewrite. |
| AUTO_IDENTITY on → off | **NEEDS_CONFIRM** | SAFE* | — | PG `DROP IDENTITY` is DDL-only, but the author loses the sequence. User confirms intent; down-migration recreates the sequence (value continuity not guaranteed). |
| off → AUTO_IDENTITY | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE <col> IS NOT NULL LIMIT 1` on an integer column | Turning an existing int column into IDENTITY requires a fresh sequence seeded above `max(col)`; differ needs to emit `ADD GENERATED ... ALWAYS AS IDENTITY` with `RESTART WITH (SELECT COALESCE(MAX(<col>),0)+1 FROM <t>)`. Check.sql verifies no NULL rows (IDENTITY columns are NOT NULL). |

#### A3. `max_len` (string carriers on VARCHAR / CHAR)

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| 0 → N (add bound) | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE char_length(<col>) > N LIMIT 1` | Unbounded→bounded; PG errors on ALTER if any row exceeds N. |
| N → 0 (remove bound) | **SAFE** | SAFE | — | Unbounding; VARCHAR(N) → TEXT / VARCHAR is a widen. |
| N → M, M > N (widen) | **SAFE** | SAFE | — | Strict widen; no data at risk. |
| N → M, M < N (narrow) | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE char_length(<col>) > M LIMIT 1` | PG refuses narrow ALTER if any row > M (actually truncates in very old PG — PG 9.2+ errors). Check lets reviewer see impact before apply. |

#### A4. Numeric `precision` / `scale` (DBT_NUMERIC)

NUMERIC-only; precision / scale on other numeric dbTypes is N/A (raised at IR build).

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| (P,S) → (P',S') with P' ≥ P, S' ≥ S (both widen) | **SAFE** | SAFE | — | Strict widen; no overflow possible. |
| (P,S) → (P',S') with P' ≥ P, S' < S (scale narrow) | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE scale(<col>) > S' LIMIT 1` (PG `scale()` available PG 13+) | Scale narrow truncates decimals; author decides whether truncation is acceptable. |
| (P,S) → (P',S') with P' < P (precision narrow) | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE <col> >= 10^(P'-S') OR <col> <= -10^(P'-S') LIMIT 1` | Overflow risk; PG errors on apply. |
| unbounded → (P,S) (add constraint) | **NEEDS_CONFIRM** | SAFE* | same as precision narrow | Going from unbounded NUMERIC to typed NUMERIC(P,S). |
| (P,S) → unbounded (drop constraint) | **SAFE** | SAFE | — | Widen to NUMERIC-unbounded. |

#### A5. `unique` flag

Iter-1 IR synthesises `unique:true` into a `UNIQUE INDEX` inside `Table.Indexes`. Alter-diff routes this axis through the Index bucket (see `diff.go` line 299–302), **not** the FactChange stream. Left here for completeness.

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| false → true | **NEEDS_CONFIRM** | (via Index add) | `SELECT <col>, count(*) FROM <t> GROUP BY <col> HAVING count(*) > 1 LIMIT 1` | `CREATE UNIQUE INDEX` errors if duplicates exist; user decides to dedupe or abort. |
| true → false | **SAFE** | (via Index drop) | — | `DROP INDEX` is always safe. |

#### A6. `pk`

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| false → true | **CUSTOM_MIGRATION** | REFUSE | — | PK change is table-rebuild territory (PG allows `ADD PRIMARY KEY` only if the column is NOT NULL + UNIQUE; composite PK even trickier). Default = CUSTOM_MIGRATION (author wants to keep existing rows as PKs). `--decide pk=drop_and_create` fallback if author wants fresh seed. |
| true → false | **CUSTOM_MIGRATION** | REFUSE | — | Dropping PK breaks referential integrity for every FK pointing here; author must write the FK-rewrite plan. |

#### A7. `generated_expr` (GENERATED ALWAYS AS ... STORED)

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| "" → expr (plain → generated) | **DROP_AND_CREATE** | SAFE* | — | PG can't convert a plain column to generated in-place; natural path is drop + add-as-generated (values recompute from new expr). |
| expr → "" (generated → plain) | **NEEDS_CONFIRM** | SAFE* | — | PG 18 supports `ALTER COLUMN DROP EXPRESSION` which materialises current values. User confirms: keep materialised values vs. drop-and-backfill. |
| expr A → expr B | **DROP_AND_CREATE** | SAFE* | — | PG has no direct "rewrite generated expression"; drop + add-as-generated recomputes every value from new expr. Author opts into CUSTOM_MIGRATION if they need a dual-write / staged cutover. |

#### A8. `comment`

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| any → any | **SAFE** | SAFE | — | `COMMENT ON COLUMN` is metadata-only; instant and reversible. Always SAFE. |

#### A9. `allowed_extensions` (path family)

Applies to SEM_FILE_PATH / SEM_IMAGE_PATH. Emitted as a CHECK regex in iter-1.

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| list A → superset B | **SAFE** | SAFE | — | Widen of allowed extensions; existing rows already pass the old check, pass the new. |
| list A → subset B | **NEEDS_CONFIRM** | SAFE* | `SELECT count(*) FROM <t> WHERE <col> !~ '<regex-from-B>' LIMIT 1` | Narrow; rows with a now-forbidden extension exist. Check surfaces count; user decides migration / rejection. |
| list A → disjoint B | **NEEDS_CONFIRM** | SAFE* | same regex check | Treated as narrow-to-new-set; same mechanics. |
| any → `[*]` (allow-all wildcard) | **SAFE** | SAFE | — | Maximum widen — drops the CHECK. |
| `[*]` → list | **NEEDS_CONFIRM** | SAFE* | regex check | Adds a constraint where none existed. |

#### A10. `enum_values` (SEM_ENUM)

EnumFqn swap was REFUSE in today's `diff.go` `enumValuesFactChange`; D28 promotes it to DROP_AND_CREATE.

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| names A → A ∪ {new} (add only) | **SAFE** | SAFE | — | Additive; string-backed PG ENUM uses `ALTER TYPE ADD VALUE`, int-backed updates the CHECK IN (…) list. |
| names A → A \ {removed} (remove only) | **CUSTOM_MIGRATION** | REFUSE | — | PG can't drop enum values; int-backed CHECK narrow would reject existing rows. Default = author rewrites affected rows to a surviving value, then re-emits with new enum. DROP_AND_CREATE fallback if author wants to discard affected rows. |
| names A → B with rename in place (same slot) | **CUSTOM_MIGRATION** | REFUSE | — | Rename = remove + add = removal applies. (String-backed PG ENUM has `ALTER TYPE RENAME VALUE` — future SAFE row; out-of-scope M1.) |
| enum_fqn "pkg.A" → "pkg.B" | **DROP_AND_CREATE** | REFUSE | — | Different enum entirely. Default = fresh start with new enum; author opts into CUSTOM_MIGRATION if they want to remap old values. |

#### A11. `pg.required_extensions`

Manifest-only impact; no DDL emitted per column (extensions are installed at schema setup per D2.6 / M4 manifest).

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| list A → list B (any change) | **SAFE** | SAFE | — | Manifest consumer (M4) decides whether `CREATE EXTENSION` is needed; column DDL unaffected. |

#### A12. `pg.custom_type`

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| any → any (string change) | **CUSTOM_MIGRATION** | REFUSE | — | custom_type is author-owned opaque DDL; the compiler doesn't know its cast semantics. Default = author supplies migration SQL; DROP_AND_CREATE fallback loses the custom-typed data. |

#### A13. `element_carrier` / `element_is_message` (repeated / map element)

| From → To | Strategy | Current | check.sql | Rationale |
|---|---|---|---|---|
| any change | **CUSTOM_MIGRATION** | REFUSE | — | Element reshape under a list/map is carrier-axis (see D28.2 carrier matrix); collection evolution needs author-written jsonb_array transform. DROP_AND_CREATE fallback discards the collection contents. |

#### Table-level axes (diff via non-column ops)

| Axis | From → To | Strategy | Current | Rationale |
|---|---|---|---|---|
| `Table.Name` (with namespace unchanged) | any → any | **SAFE** | SAFE (RenameTable) | `ALTER TABLE RENAME TO` is instant; FK + index names PG auto-updates. |
| `Table.NamespaceMode` + `Table.Namespace` | SCHEMA(a) → SCHEMA(b) | **SAFE** | SAFE | `ALTER TABLE SET SCHEMA` is instant; cross-schema FKs continue working. |
| | PREFIX(a) → PREFIX(b) | **SAFE** | SAFE | Under the hood = RENAME TO new-prefixed name. |
| | NONE → SCHEMA | **SAFE** | SAFE | SET SCHEMA on default. |
| | SCHEMA → NONE | **SAFE** | SAFE | SET SCHEMA to public (default). |
| | SCHEMA ↔ PREFIX (mode switch) | **SAFE** | SAFE | Chain: SET SCHEMA + RENAME TO. |
| `Table.Comment` | any → any | **SAFE** | SAFE | `COMMENT ON TABLE`; metadata. |
| `Table.Indexes` (structured) | add | **NEEDS_CONFIRM** (conditional) | SAFE | Adding a `UNIQUE` index: same as A5 false→true (check for dupes). Adding a non-unique index: SAFE. BRIN/GIN/GIST add: SAFE (no uniqueness). Emit order: check.sql only when `unique:true`. |
| | drop | **SAFE** | SAFE | `DROP INDEX`. |
| | replace (internal change: method, partial, opclass, …) | **SAFE** | SAFE | DROP + CREATE; no data change. Uniqueness flip within replace: treat replace as drop + add, apply A5 rules on the add half. |
| `Table.ForeignKeys` | add | **NEEDS_CONFIRM** | SAFE | `ADD FOREIGN KEY` validates existing data — fails apply if any child row has no parent. Check.sql: `SELECT count(*) FROM <child> WHERE <col> IS NOT NULL AND <col> NOT IN (SELECT <pkcol> FROM <parent>) LIMIT 1`. |
| | drop | **SAFE** | SAFE | Dropping a constraint is safe. |
| | replace (target / on_delete) | **NEEDS_CONFIRM** (target change) / **SAFE** (on_delete only) | SAFE | Target change is drop + add; fold to add-half NEEDS_CONFIRM. Pure on_delete change: `ALTER CONSTRAINT` is safe. |
| `Table.Checks` (structured: len / blank / range / regex / choices) | add | **NEEDS_CONFIRM** | SAFE | `ADD CONSTRAINT CHECK` validates existing rows. Check.sql = the CHECK predicate negated: `SELECT count(*) FROM <t> WHERE NOT (<predicate>) LIMIT 1`. |
| | drop | **SAFE** | SAFE | Dropping a constraint is always safe. |
| | replace | **NEEDS_CONFIRM** | SAFE | Fold as drop + add; add-half dominates. |
| `Table.RawChecks` / `Table.RawIndexes` | add / drop / replace | **NEEDS_CONFIRM** on add/replace, **SAFE** on drop | SAFE | Body is opaque (D11); compiler can't derive check.sql, so add/replace always emit NEEDS_CONFIRM with a stub check (`-- raw_* body is opaque; reviewer must hand-validate`). Drop stays SAFE. |

#### Strictness fold (multi-axis alters)

When one alter touches multiple axes (e.g. `max_len 200 → 50` + `nullable NULL → NOT NULL`), the emitted strategy is the **strictest**:

```
SAFE < LOSSLESS_USING < NEEDS_CONFIRM < DROP_AND_CREATE < CUSTOM_MIGRATION
```

Check.sql for a NEEDS_CONFIRM multi-axis alter is the AND of all per-axis checks:

```sql
SELECT count(*) FROM <t>
WHERE char_length(col) > 50
   OR col IS NULL
LIMIT 1;
```

#### What this axis does NOT cover

Still deferred to follow-up subsections:

- **D28.2 — Carrier × Carrier** (8 × 8 grid). E.g. STRING → INT32 (NEEDS_CONFIRM + USING cast), STRING → MESSAGE (DROP_AND_CREATE), INT32 → INT64 (SAFE widen).
- **D28.3 — DbType × DbType** (~50 reachable). E.g. VARCHAR → TEXT (SAFE), INTEGER → BIGINT (SAFE widen), TEXT ↔ CITEXT (LOSSLESS_USING), JSON ↔ JSONB (LOSSLESS_USING). Includes cross-family DROP_AND_CREATE cases (TIMESTAMP → JSON).
- **D28.4 — Sem × Sem within carrier** (~150). E.g. SEM_EMAIL → SEM_URL on string (SAFE — same carrier, regex differs, fold via A9-style CHECK replace), SEM_CHAR → SEM_TEXT (SAFE — maps to dbType change TEXT).

Most Sem changes degenerate into constraint-axis (regex CHECK) + dbType changes, so D28.4 will be short.

### D28.2 — Classification matrix: carrier × carrier transitions (added 2026-04-23)

**Status: draft, awaiting cell-by-cell sign-off.** Phase 1b of D28.
Today's classifier REFUSEs every carrier change (`plan/diff.go:250`);
D28.2 opens the grid to SAFE / LOSSLESS_USING / NEEDS_CONFIRM /
DROP_AND_CREATE / CUSTOM_MIGRATION where PG casts make that viable.

**Identity reminder:** this matrix covers the case where the author
kept the proto **field number** stable but changed the proto type.
A carrier change with a renumbered field degenerates to `DropColumn` +
`AddColumn` (D10 rename-detection sees a new number) and bypasses
this matrix. The matrix fires only when number is stable and carrier
differs.

**Meta-rule — escape hatch universality:** every cell's "recommended
strategy" is the default the differ emits, but author can always
override to a *more permissive* strategy via `--decide`:

```
SAFE ← LOSSLESS_USING ← NEEDS_CONFIRM ← DROP_AND_CREATE ← CUSTOM_MIGRATION
```

DROP_AND_CREATE is the universal fallback: every cell can override to
it (at the cost of data loss). CUSTOM_MIGRATION further up the chain
preserves data via author-written SQL. Matrix shows the *strictest*
(safest) strategy that a clean default can emit; override-relaxations
are plumbed at `--decide` parsing time.

**Legend:**
- `—` no change
- `S` SAFE (plain `ALTER COLUMN TYPE`, no USING)
- `U` LOSSLESS_USING (USING cast, deterministic + value-preserving)
- `N` NEEDS_CONFIRM (USING cast exists but may fail or reshape data; check.sql auto-emitted)
- `D` DROP_AND_CREATE (explicit `--decide` required; data lost)
- `C` CUSTOM_MIGRATION (explicit `--decide` required; author writes SQL preserving data)

#### Grid A — scalar × scalar (8 × 8)

Excludes MAP / LIST / MESSAGE (collection carriers covered in Grid B).

| from\to | BOOL | STRING | INT32 | INT64 | DOUBLE | TIMESTAMP | DURATION | BYTES |
|---|---|---|---|---|---|---|---|---|
| **BOOL** | — | U | U | U | U | C | C | N |
| **STRING** | N | — | N | N | N | N | N | N |
| **INT32** | N | U | — | S | U | C | C | C |
| **INT64** | N | U | N | — | N | C | C | C |
| **DOUBLE** | N | U | N | N | — | C | C | C |
| **TIMESTAMP** | C | U | C | C | C | — | C | C |
| **DURATION** | C | U | C | C | C | C | — | C |
| **BYTES** | C | N | C | C | C | C | C | — |

#### Grid A cell details

Only non-trivial cells are annotated. Obvious symmetries collapsed
(INT64 → INT32 mirrors DOUBLE → INT32 shape — "narrow integer with
overflow check").

| From | To | Cell | USING expression | check.sql | Rationale |
|---|---|---|---|---|---|
| BOOL | STRING | `U` | `col::text` | — | PG emits `'t'`/`'f'`; deterministic, reversible. |
| BOOL | INT32/INT64 | `U` | `col::int` / `col::bigint` | — | PG maps false→0, true→1; reversible via `<>0`. |
| BOOL | DOUBLE | `U` | `col::int::double precision` | — | Chains bool→int→double; still lossless. |
| BOOL | BYTES | `N` | `decode(col::text, 'escape')` | — | Encoding choice ambiguous (hex vs escape); author picks. |
| BOOL | TIMESTAMP/DURATION | `D` | — | — | Semantically meaningless; default is schema correction (accept data loss). |
| STRING | BOOL | `N` | `col::boolean` | `SELECT count(*) FROM t WHERE col NOT IN ('t','f','true','false','yes','no','y','n','1','0','TRUE','FALSE','Yes','No','Y','N','True','False') LIMIT 1` | PG accepts a specific set; anything else errors at apply. Check.sql validates pre-apply. |
| STRING | INT32 | `N` | `col::int` | `SELECT count(*) FROM t WHERE col !~ '^-?[0-9]+$' LIMIT 1` | Parse risk; also overflow risk if values exceed INT32. |
| STRING | INT64 | `N` | `col::bigint` | `SELECT count(*) FROM t WHERE col !~ '^-?[0-9]+$' LIMIT 1` | Parse risk only (INT64 range dwarfs typical string-numeric). |
| STRING | DOUBLE | `N` | `col::double precision` | `SELECT count(*) FROM t WHERE col !~ '^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$' LIMIT 1` | Parse risk. |
| STRING | TIMESTAMP | `N` | `col::timestamptz` | `SELECT count(*) FROM t WHERE col IS NOT NULL AND NOT col ~ '^\d{4}-\d{2}-\d{2}' LIMIT 1` (ISO-8601-ish coarse guard) | PG's timestamp parser accepts many formats; coarse regex catches clearly-broken rows. Pre-apply. |
| STRING | DURATION | `N` | `col::interval` | coarse guard as above | Interval parser permissive; same pattern. |
| STRING | BYTES | `N` | `decode(col, 'hex')` OR `col::bytea` (escape form) | encoding-specific | Author must decide encoding; check.sql keyed on chosen encoding. |
| INT32 | STRING | `U` | `col::text` | — | Canonical decimal; reversible. |
| INT32 | INT64 | `S` | (implicit; PG accepts plain `ALTER TYPE bigint`) | — | Strict widen. No USING. |
| INT32 | DOUBLE | `U` | `col::double precision` | — | Fits exactly; INT32 max (~2.1e9) << 2^53. |
| INT32 | BOOL | `N` | `col::boolean` | — | Works in PG (0=false, nonzero=true); author confirms intent (convention varies — some codebases use -1 for "unknown"). |
| INT32 | TIMESTAMP | `N` | `to_timestamp(col)` | — | Epoch-seconds assumed; author may have meant milliseconds. |
| INT32 | DURATION | `N` | `make_interval(secs => col)` | — | Seconds assumed; user confirms unit. |
| INT32 | BYTES | `N` | `set_bytea_output + int4send(col)` | — | Endianness + byte-order choice; author confirms. |
| INT64 | INT32 | `N` | `col::int` | `SELECT count(*) FROM t WHERE col > 2147483647 OR col < -2147483648 LIMIT 1` | Overflow risk; check.sql catches. |
| INT64 | DOUBLE | `N` | `col::double precision` | `SELECT count(*) FROM t WHERE abs(col) > 9007199254740992 LIMIT 1` (2^53) | Above 2^53, doubles lose integer precision. |
| DOUBLE | INT32/INT64 | `N` | `col::int` / `col::bigint` | `SELECT count(*) FROM t WHERE col <> floor(col) LIMIT 1` + overflow check | Truncation + (for INT32) overflow. |
| DOUBLE | BOOL | `N` | `col::int::boolean` | `SELECT count(*) FROM t WHERE col <> 0 AND col <> 1 LIMIT 1` | Typical intent is "nonzero → true", but many rows (0.5, 2.3) trigger ambiguity. |
| TIMESTAMP | STRING | `U` | `col::text` | — | ISO-8601 canonical; reversible. |
| TIMESTAMP | INT32 | `N` | `extract(epoch from col)::int` | `SELECT count(*) FROM t WHERE extract(epoch from col) > 2147483647 LIMIT 1` | Y2038 risk if times > 2038-01-19 (INT32 epoch overflow). |
| TIMESTAMP | INT64 | `N` | `extract(epoch from col)::bigint` | — | No overflow but unit ambiguity (s vs. ms vs. μs). |
| TIMESTAMP | DOUBLE | `N` | `extract(epoch from col)` | — | Precision loss below microseconds; author confirms. |
| TIMESTAMP | DURATION | `C` | — | — | Timestamp → interval is non-unique (interval relative to what?). Author writes migration. |
| TIMESTAMP | BYTES | `N` | `convert_to(col::text, 'UTF8')` | — | Text→bytes fallback; reversible if encoding fixed. |
| TIMESTAMP | BOOL | `D` | — | — | No sensible cast. |
| DURATION | STRING | `U` | `col::text` | — | `'1 day 02:03:04'` canonical; reversible. |
| DURATION | INT32/INT64 | `N` | `extract(epoch from col)::int` | overflow check | Unit ambiguity + (INT32) overflow risk. |
| DURATION | TIMESTAMP | `C` | — | — | Same as TIMESTAMP → DURATION: non-unique. |
| DURATION | BOOL | `D` | — | — | No sensible cast. |
| BYTES | STRING | `N` | `encode(col, 'hex')` OR `convert_from(col, 'UTF8')` | encoding-specific | Author picks encoding. UTF-8 decode may fail on non-text bytea. |
| BYTES | INT* / DOUBLE / TIMESTAMP / DURATION / BOOL | `N` | custom decoder (`get_byte`, bit-level ops) | custom validator | Byte-level interpretation; user confirms. |

#### Grid B — collection carriers (MAP / LIST / MESSAGE)

Collections ↔ scalar and collections ↔ collections. Most transitions
are CUSTOM_MIGRATION because data reshape is not automatically
derivable from schema alone.

| from\to | BOOL | STRING | INT32 | INT64 | DOUBLE | TIMESTAMP | DURATION | BYTES | MAP | LIST | MESSAGE |
|---|---|---|---|---|---|---|---|---|---|---|---|
| **MAP** | C | U | C | C | C | C | C | C | — | C | C |
| **LIST** | C | U | C | C | C | C | C | C | C | — | C |
| **MESSAGE** | C | U | C | C | C | C | C | C | C | C | — |

Scalar → collection rows (all `C` by default — wrapping is author's
choice; override to `D` via `--decide` if fresh start is fine):

| from\to | MAP | LIST | MESSAGE |
|---|---|---|---|
| **BOOL / STRING / INT32 / INT64 / DOUBLE / TIMESTAMP / DURATION / BYTES** | C | C | C |

| From | To | Cell | Expression | Rationale |
|---|---|---|---|---|
| MAP | STRING | `U` | `col::text` (jsonb serialises canonically) | JSONB cast to text is deterministic; reversible via `col::jsonb` on the reverse migration. |
| MAP | scalar except STRING | `D` | — | No sensible single-value extraction; default intent is schema correction. `--decide col=custom:<path>` to preserve via CUSTOM_MIGRATION. |
| MAP | LIST | `C` | `jsonb_agg(value ORDER BY key)` or custom | Drops keys; author picks value-extraction order. |
| MAP | MESSAGE | `C` | field-by-field extract | Shape change; author maps keys to message fields. |
| LIST | STRING | `U` | `col::text` | Same as MAP. JSONB array serialises. |
| LIST | scalar except STRING | `D` | — | Same reasoning as MAP. |
| LIST | MAP | `C` | `jsonb_object_agg(idx, value)` or custom | Keys must be synthesised; author owns the key scheme. |
| LIST | MESSAGE | `C` | index-based field extract | Shape change. |
| MESSAGE | STRING | `U` | `col::text` | Same. (Bytes-backed MESSAGE: falls through to `D` — opaque.) |
| MESSAGE | scalar except STRING | `D` | — | No canonical single-value projection. |
| MESSAGE | MAP | `C` | field→key,value fold | Author picks fold. |
| MESSAGE | LIST | `C` | field values in order | Author picks projection. |
| scalar | MAP/LIST/MESSAGE | `D` | — | Wrapping a scalar into a collection is ambiguous; default = schema correction. Author opts into `C` with custom SQL if the scalar values are e.g. JSON-encoded strings worth preserving. |

**Note on MESSAGE carrier:** iter-1 supports a subset — a proto
`Message` can land in a column either as JSON/JSONB (default, same
as MAP / LIST) or as a proto-bytes blob via `CARRIER_MESSAGE` +
custom_type escape. For JSONB-backed MESSAGEs the grid above applies
directly. For bytes-backed MESSAGEs every cell collapses to `D` (the
compiler has no opaque-payload introspection; preservation requires
a custom decoder → `C` with author-supplied SQL).

#### Common failure modes the matrix steers around

1. **"I'll just flip STRING to INT32, PG will cast."** Today's emit
   stops at REFUSE, but if it didn't, rows like `"n/a"` would fail
   at `ALTER TABLE ... ALTER COLUMN col TYPE int USING col::int` —
   and the migration rolls back mid-way, leaving the table locked.
   Matrix routes through NEEDS_CONFIRM + check.sql, so the failure
   surfaces before the lock.
2. **"Widen INT32 to INT64, that's just SAFE."** True for the type,
   but downstream app code that reads the column via generated
   Go / JS may have compile-time assumptions (Go `int32` ≠ `int64`,
   JS `number` ≠ `bigint`). The differ stays SAFE at the DB layer;
   the D29 tool surface logs "app code re-gen required" as a
   companion concern.
3. **"MAP → LIST is obviously `jsonb_values()`."** PG has
   `jsonb_each`, `jsonb_object_keys`, `jsonb_array_elements`, but no
   canonical "values in key-insertion order" — JSONB stores keys
   sorted lexicographically. CUSTOM_MIGRATION makes the author pick
   the ordering explicitly.

### D28.3 — Classification matrix: dbType × dbType within carrier (added 2026-04-23)

**Status: draft, awaiting cell-by-cell sign-off.** Phase 1c of D28.
The `db_type` override lets authors pin a column's physical PG type
independently of the carrier's default (e.g. `string` carrier but
`DBT_UUID`, `int64` carrier but `DBT_INTEGER`). Changing the
override *within* the same carrier is the D28.3 territory; it's
mostly deterministic PG casts, a handful of NEEDS_CONFIRM cells for
narrow casts.

Cross-carrier dbType changes degenerate to the D28.2 carrier matrix
— carrier-axis dominates, dbType-axis folds in as a secondary PG
`USING` adjustment.

Grids are per-carrier family. Cells not listed are R (no valid
same-carrier cast between unrelated dbTypes, e.g. UUID ↔ INET).

**Legend:** same as D28.2 (`S` / `U` / `N` / `C` / `R`).

#### Grid C1 — STRING carrier

| from\to | TEXT | VARCHAR | CITEXT | UUID | INET | CIDR | MACADDR | TSVECTOR |
|---|---|---|---|---|---|---|---|---|
| **TEXT** | — | S‡ | U | N | N | N | N | U |
| **VARCHAR** | S | — | U | N | N | N | N | U |
| **CITEXT** | U | U | — | N | N | N | N | U |
| **UUID** | U | U | U | — | D | D | D | D |
| **INET** | U | U | U | D | — | N | D | D |
| **CIDR** | U | U | U | D | U | — | D | D |
| **MACADDR** | U | U | U | D | D | D | — | D |
| **TSVECTOR** | U | U | U | D | D | D | D | — |

‡ TEXT → VARCHAR with an explicit `max_len` narrow triggers the A3 check.

| From | To | Cell | USING / check.sql | Rationale |
|---|---|---|---|---|
| TEXT / VARCHAR | CITEXT | `U` | `col::citext` | Data-preserving; changes comparison semantics (case-insensitive) but keeps bytes. Reversible. |
| TEXT / VARCHAR / CITEXT | UUID | `N` | `col::uuid` + `SELECT count(*) FROM t WHERE col !~ '^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$' LIMIT 1` | Cast errors on non-UUID rows; check validates pre-apply. |
| TEXT / VARCHAR / CITEXT | INET | `N` | `col::inet` + regex check (IPv4 / IPv6 permissive) | Format check needed. |
| TEXT / VARCHAR / CITEXT | CIDR | `N` | `col::cidr` + regex check (network/mask) | CIDR stricter than INET (host bits must be zero beyond the mask); check.sql enforces. |
| TEXT / VARCHAR / CITEXT | MACADDR | `N` | `col::macaddr` + regex check | Format check. |
| TEXT / VARCHAR / CITEXT | TSVECTOR | `U` | `to_tsvector('simple', col)` | Language-config free; reversible via back-to-text (lossless on `simple` config; author should confirm if they need a specific language config — park as follow-up). |
| UUID / INET / CIDR / MACADDR / TSVECTOR | TEXT / VARCHAR / CITEXT | `U` | `col::text` | PG renders canonical string form; reversible. |
| INET | CIDR | `N` | `col::cidr` + `SELECT count(*) FROM t WHERE host(col) <> host(network(col)) LIMIT 1` | PG's INET → CIDR cast strips host bits; rows with non-zero host bits lose data silently. Check.sql surfaces them so author can decide (keep host bits = stay INET, or normalize first). |
| CIDR | INET | `U` | `col::inet` | Relax; always lossless. |
| UUID / MACADDR / TSVECTOR ↔ network family | `D` | — | No sensible cast between unrelated type families; default intent is schema correction (accept data loss). Author opts into `C` with custom SQL if they have a specific transformation. |

#### Grid C2 — INT32 / INT64 carriers (integer family)

For `int32` carrier: valid dbTypes = SMALLINT, INTEGER.  
For `int64` carrier: valid dbTypes = INTEGER, BIGINT.

Cross-carrier (INT32 ↔ INT64) is the carrier matrix (D28.2 — SAFE widen / NEEDS_CONFIRM narrow).

| from\to | SMALLINT | INTEGER | BIGINT | NUMERIC |
|---|---|---|---|---|
| **SMALLINT** | — | S | S | U |
| **INTEGER** | N | — | S | U |
| **BIGINT** | N | N | — | U |
| **NUMERIC** | N | N | N | — |

| From | To | Cell | check.sql | Rationale |
|---|---|---|---|---|
| SMALLINT → INTEGER → BIGINT | `S` | — | Strict widen within integer family; no USING needed. |
| INTEGER → SMALLINT | `N` | `SELECT count(*) FROM t WHERE col > 32767 OR col < -32768 LIMIT 1` | Overflow check. |
| BIGINT → INTEGER | `N` | `SELECT count(*) FROM t WHERE col > 2147483647 OR col < -2147483648 LIMIT 1` | Overflow. |
| BIGINT → SMALLINT | `N` | (range check narrower) | Overflow. |
| integer → NUMERIC | `U` | — | Always fits; `col::numeric`. |
| NUMERIC → integer | `N` | `SELECT count(*) FROM t WHERE col <> floor(col) LIMIT 1` + overflow | Truncation + overflow. |

#### Grid C3 — DOUBLE carrier (floating-point family)

Valid dbTypes: REAL, DOUBLE_PRECISION, NUMERIC.

| from\to | REAL | DOUBLE_PRECISION | NUMERIC |
|---|---|---|---|
| **REAL** | — | S | N |
| **DOUBLE_PRECISION** | N | — | N |
| **NUMERIC** | N | N | — |

| From | To | Cell | check.sql | Rationale |
|---|---|---|---|---|
| REAL → DOUBLE_PRECISION | `S` | — | Strict widen (6 digits → 15 digits precision). |
| DOUBLE_PRECISION → REAL | `N` | `SELECT count(*) FROM t WHERE col::real::double precision <> col LIMIT 1` | Precision loss; check.sql finds rows that can't round-trip. |
| REAL / DOUBLE → NUMERIC | `U` | — | Data-preserving in both directions of magnitude; `col::numeric`. |
| NUMERIC → REAL / DOUBLE | `N` | round-trip check | Precision of NUMERIC > 17 digits can't fit in DOUBLE. |

#### Grid C4 — TIMESTAMP carrier (date/time family)

Valid dbTypes: DATE, TIME, TIMESTAMP, TIMESTAMPTZ.

| from\to | DATE | TIME | TIMESTAMP | TIMESTAMPTZ |
|---|---|---|---|---|
| **DATE** | — | C | U | U |
| **TIME** | C | — | C | C |
| **TIMESTAMP** | N | N | — | N |
| **TIMESTAMPTZ** | N | N | N | — |

| From | To | Cell | USING / check.sql | Rationale |
|---|---|---|---|---|
| DATE → TIMESTAMP / TIMESTAMPTZ | `U` | `col::timestamp` / `col::timestamptz` | Midnight in session timezone; reversible via cast back (may lose tz info on backward). |
| TIMESTAMP → DATE | `N` | `SELECT count(*) FROM t WHERE col::time <> '00:00:00' LIMIT 1` | Truncates time-of-day; check surfaces rows with non-midnight times. |
| TIMESTAMP → TIME | `N` | — | Drops date component; check irrelevant (data reshape is the point; user confirms intent). |
| TIMESTAMP → TIMESTAMPTZ | `N` | — | PG applies session timezone; author must confirm target timezone matches data's assumed tz. |
| TIMESTAMPTZ → TIMESTAMP | `N` | — | Drops timezone, leaving local-time; ambiguous if data spans multiple tz. |
| TIME ↔ DATE / TIMESTAMP / TIMESTAMPTZ | `C` | — | TIME has no date; combining requires custom date. Default = CUSTOM_MIGRATION (author supplies the date). DROP_AND_CREATE fallback if fresh column is acceptable. |

#### Grid C5 — BYTES carrier

Valid dbTypes: BYTEA (PG), BLOB (MySQL stub).

Single-cell matrix; BYTEA ↔ BLOB is effectively a dialect-axis rename
(same wire shape). Classification `S` within one dialect isn't
reachable (you don't change dbType within PG from BYTEA to anything
else in the same carrier). Cross-dialect moves are M4 territory.

#### Grid C6 — MAP / LIST carrier (JSON family)

Valid dbTypes: JSON, JSONB, HSTORE (HSTORE map-only).

| from\to | JSON | JSONB | HSTORE |
|---|---|---|---|
| **JSON** | — | U | N |
| **JSONB** | U | — | N |
| **HSTORE** | U | U | — |

| From | To | Cell | USING / check.sql | Rationale |
|---|---|---|---|---|
| JSON → JSONB | `U` | `col::jsonb` | Normalises whitespace + key order; semantically equivalent. |
| JSONB → JSON | `U` | `col::json` | Deterministic text form. |
| JSON / JSONB → HSTORE | `N` | `SELECT count(*) FROM t WHERE jsonb_typeof(col) <> 'object' OR EXISTS (SELECT 1 FROM jsonb_each(col) WHERE jsonb_typeof(value) <> 'string') LIMIT 1` | HSTORE is string→string only; JSON may nest. Check rejects incompatible shapes. |
| HSTORE → JSON / JSONB | `U` | `col::jsonb` (via `hstore_to_jsonb(col)` or direct cast) | All HSTORE values are strings by construction → always fits as JSON strings. Lossless. |

#### Grid C7 — BOOL / DURATION carriers (trivial)

Single valid dbType each (BOOLEAN, INTERVAL respectively). No
intra-carrier dbType transition exists; all moves are carrier-axis
(D28.2).

#### DbType changes compose with constraint axes

A change like `VARCHAR(200) → TEXT` reduces to (no dbType change in
family) + (A3 max_len removed). Classification = max-axis strictness,
which is SAFE per A3 "remove bound". Emitter materialises this as
one `ALTER COLUMN TYPE text` (PG accepts without USING when both
are string-family).

### D28.4 — Classification matrix: sem × sem within carrier (added 2026-04-23)

**Status: draft.** Phase 1d of D28 — the final axis. SemType is a
design-intent label the author puts on a column (`SEM_EMAIL`,
`SEM_UUID`, `SEM_MONEY`, …); it drives iter-1 dbType selection +
CHECK synthesis. A sem change within the same carrier decomposes
into **at most two** lower-level axis changes:

1. **dbType axis** (D28.3) — EMAIL and URL both land on TEXT; CHAR
   lands on VARCHAR; UUID lands on UUID; IP lands on INET. Changing
   sem between cells in the same dbType column introduces no
   dbType-axis change.
2. **CHECK axis** (D28.1 A13) — EMAIL has a regex CHECK; URL has a
   different regex; SLUG has another. Changing sem between two with
   synthesised CHECKs is a `ReplaceCheck` op; add/drop of the CHECK
   is the Add/Drop variant.

D28.4 introduces **no new strategy codes**. The classifier reduces a
sem transition to (dbType change if any, CHECK delta) and folds per
the strictness rule.

#### Reduction table — string carrier (the interesting one)

| From sem | To sem | dbType delta | CHECK delta | Composite strategy |
|---|---|---|---|---|
| TEXT | CHAR | TEXT → VARCHAR(n) | none (both unconstrained CHECK-wise) | A3 "0 → N max_len" = **N** |
| CHAR | TEXT | VARCHAR(n) → TEXT | none | **S** (A3 remove bound) |
| TEXT / CHAR | EMAIL | none (still TEXT/VARCHAR) | add regex CHECK | A13 add = **N** (NEEDS_CONFIRM with `count(*) WHERE col !~ '<email>' LIMIT 1`) |
| EMAIL | TEXT | none | drop CHECK | A13 drop = **S** |
| EMAIL | URL | none | replace CHECK (different regex) | A13 replace = **N** |
| EMAIL | SLUG | none | replace CHECK | A13 replace = **N** |
| TEXT | UUID | TEXT → UUID | none | D28.3 C1 = **N** (regex + cast validation) |
| UUID | TEXT | UUID → TEXT | none | D28.3 C1 = **U** |
| TEXT | JSON | TEXT → JSON | none | D28.3 C1 cross-family = **N** (`col::json`; parse errors possible) |
| JSON | TEXT | JSON → TEXT | none | **U** (serialise back) |
| TEXT | IP | TEXT → INET | none | D28.3 C1 = **N** |
| TEXT | MAC | TEXT → MACADDR | none | D28.3 C1 = **N** |
| TEXT | TSEARCH | TEXT → TSVECTOR | none | D28.3 C1 = **U** |
| SLUG | URL | none | replace CHECK | **N** |
| POSIX_PATH | FILE_PATH | none | add extension-regex CHECK | A9 `[*]` → list = **N** |
| FILE_PATH | POSIX_PATH | none | drop CHECK | A9 list → `[*]` = **S** |
| FILE_PATH | IMAGE_PATH | none | replace CHECK (different extension list) | A9 narrow / reshape = **N** |
| ENUM (string-backed) | TEXT / VARCHAR / CITEXT | drop PG ENUM type | none | **U** — `ALTER COLUMN TYPE text USING col::text`; canonical text form of the enum value is preserved. Down direction requires DROP_AND_CREATE (can't re-narrow text → ENUM without validation; that's NEEDS_CONFIRM per A13 CHECK add). |
| ENUM (string-backed) | non-text dbType | drop PG ENUM type + dbType change | possibly CHECK | **D** — schema correction; data lost. Author opts into C to transform existing enum strings. |
| ENUM (int-backed) | non-ENUM | drop CHECK-IN | none | **S** — CHECK IN (…) drop is SAFE. |
| anything non-ENUM | ENUM | add PG ENUM type | CHECK for int-backed variant | **N** (validate existing rows against enum set) + requires emitter to emit `CREATE TYPE` before `ALTER TYPE` |

#### Reduction table — int carriers

| From sem | To sem | dbType delta | CHECK delta | Composite |
|---|---|---|---|---|
| NUMBER / ID / COUNTER ↔ each other | none | none | none | — (no-op; metadata-only label change) |
| NUMBER | SMALL_INTEGER | INTEGER → SMALLINT | none | D28.3 C2 narrow = **N** |
| SMALL_INTEGER | NUMBER | SMALLINT → INTEGER | none | **S** |
| NUMBER | PERCENTAGE | none (INTEGER) | add `CHECK col BETWEEN 0 AND 100` | A13 add = **N** (auto-check on existing rows) |
| PERCENTAGE | NUMBER | none | drop CHECK | **S** |
| NUMBER | MONEY | INTEGER → BIGINT (if needed) | none | **S** widen |
| NUMBER | DECIMAL | INTEGER → NUMERIC | none | D28.3 C2 = **U** |
| DECIMAL | NUMBER | NUMERIC → INTEGER | none | **N** truncation |
| RATIO | any | NUMERIC(P,S) → other | CHECK `col BETWEEN 0 AND 1` drop | fold per axis |

#### Reduction table — date/time carriers

Straightforward 1:1 mapping to dbType; DATE / TIME / DATETIME /
INTERVAL each pin a single dbType in iter-1. Sem changes here
collapse to D28.3 C4 cells. No new strategy territory.

| From sem | To sem | Reduces to |
|---|---|---|
| DATE ↔ DATETIME | D28.3 C4 DATE ↔ TIMESTAMP |
| DATETIME ↔ TIME | D28.3 C4 TIMESTAMP ↔ TIME = **C** |
| TIME ↔ DATE | D28.3 C4 = **C** |

#### SEM_AUTO

`SEM_AUTO` + `AutoKind` is a default-axis decoration (D28.1 A2);
dropping AUTO is a default remove, adding AUTO is a default add.
Changing AutoKind (AUTO_NOW ↔ AUTO_UUID_V4) is A2 "default A →
default B" = **S**.

#### Summary of D28 phase 1

Four matrices together define every column-level transition the
differ sees. Classifier implementation plan:

```
classifyFactChange(prev, curr):
  if carrier(prev) != carrier(curr):
    return D28.2 cell
  if dbType(prev) != dbType(curr):
    return D28.3 cell
  for each constraint-axis delta in D28.1:
    collect per-axis strategy
  return strictest(collected)
```

No third-axis branching needed; sem is a label, not a separate
classification target.

### D29 — Schema source-of-truth: tool + git lock-file model (added 2026-04-23)

**Status: north-star architecture.** Pins the long-term shape of
how schemas, generated code, and migrations relate to git
repositories and the hosted platform tool. No implementation
work today — CLI standalone mode (current reality) keeps
working unchanged. D29 governs every architectural decision
made between now and tool integration so we don't paint
ourselves into a corner.

**Mental model: schema is a package, tool is a registry, git is a consumer.**

Borrows from proven patterns:

- **Buf Schema Registry / npm registry** — schema lives
  centrally; consumer repo holds a lock file referencing
  versions.
- **Cargo.lock / go.sum / package-lock.json** — reproducibility
  via hash, not via vendored copy.
- **Atlas migration approval** — migrations are immutable after
  approval; deploy is read-only consumer.

**Artifact placement (where things live and why):**

| Artifact | Location | Rationale |
|---|---|---|
| Schema source (`*.proto` with `w17.*`) | **Tool as source of truth, locally cached (gitignored)** | Cross-service registry; DBA approval workflow; full audit trail. Locally cached so dev / Claude can edit. |
| Schema reference (`w17.yaml`) | **Git** — consumer repo | The single bridge between git PR and tool version. Mergeable like any source. Conflicts on the version field only, easy to resolve. |
| Generated code (proto stubs, gRPC handlers) | **Gitignored, regenerated from `w17.yaml`** | No noise in PR diffs. CI / local `wc sync` regenerates from cached or fetched schema. New contributor: one `wc sync` and they're up. |
| Migration SQL | **Tool, immutable after approval** | Single source of truth for deploys. Git never carries SQL → no drift between "what's in git" and "what runs on DB". |
| Decisions (D28 strategy choices) | **Tool, attached to migration plan** | Reviewed in tool UI with data-impact analysis ("this NEEDS_CONFIRM affects 2.3M rows"). Immutable post-approval. |
| Application code (handlers, DQL queries) | **Git** | Standard application code; imports gen code. |

**Versioning convention (the elegant part):**

Code semver `XX.YY.zz` where:

- **major (`XX`)** — schema **breaking** change (drop column,
  type change with data loss, REFUSE → DROP_AND_CREATE
  acknowledged).
- **minor (`YY`)** — schema **additive** change (add column,
  widen type, add index — forward-compatible: old code runs
  against new schema).
- **patch (`zz`)** — code-only change, no schema impact.

The convention enables **automatic deploy-time compatibility
checking**: app `14.25.99` declares min schema `14.25` → DB
schema is `14.26` (additive bump) → deploy OK. App `14.25.99`
against schema `15.0` → REFUSE without explicit major-bump
acknowledgement.

**Tool determines major vs minor automatically** from the
D28 classification matrix:

- All-SAFE / all-LOSSLESS_USING migration → **minor** bump.
- Any NEEDS_CONFIRM / DROP_AND_CREATE / REFUSE-overridden →
  **major** bump (and requires elevated approval).

**`w17.yaml` shape:**

```yaml
# w17.yaml — committed to git
schema:
  version: "14.26"             # production-ready reference (tagged)
  draft_id: "01J9X4Z2K..."     # UUID while schema is unmerged in tool
  hash: "sha256:abc...def"     # integrity, CI verifies
deploy:
  min_schema: "14.25"          # min compatible schema (forward-compat range)
  max_schema: "14.26"          # max known-good (before next major)
```

`draft_id` is auto-cleared by a tool bot-PR after schema is
merged in tool (becomes a real `version`). `hash` protects
against tool-side mutation: CI re-fetches by version/UUID,
recomputes hash, asserts match.

**Workflow:**

```
1. wc sync                     # cache schema per w17.yaml (skip if cache hit)
2. <edit proto locally>        # gitignored area
3. wc generate                 # POST diff to tool → returns draft_id, regen code locally
4. <write app code, tests>
5. git diff                    # sees: app code + w17.yaml (draft_id changed)
6. git push, open PR
7. CI: wc verify              # re-syncs, regens, hash matches → green
8. Tool review (PR-link in commit)
9. Tool approves → schema becomes version "14.26"; bot-PR updates w17.yaml
10. CI re-runs, mergeable
11. Merge to main
12. Deploy reads w17.yaml, fetches matching schema/migration version, applies
```

**Multi-team conflict resolution:**

- **Schema:** two teams editing same message → tool serializes
  drafts (linearized access). Second team's `wc sync` says
  "rebase your draft on top of merged version."
- **Lock file:** classic git merge conflict on `w17.yaml`
  version field → resolved by `wc sync && wc regen` after
  pull.
- **Generated code:** never in git, no conflicts ever.
- **Cross-service awareness:** tool tracks which git repos
  consume which schema versions; surfaces "service B uses
  v14.25, you're bumping to 14.26 (additive, OK)" in approval
  UI.

**Atomicity (git ↔ tool):**

Tool approval is a **CI gate**:

1. Tool migration plan: `draft → review → approved → applied`.
2. Git PR has required CI check `wc check` calling tool API:
   "is migration X approved?"
3. CI fails until tool returns `approved`.
4. Merge possible only when both git review + tool approval pass.
5. Post-deploy: tool flips to `applied`, immutable forever.

**Reverse:** revert PR in git → wc CLI generates down migration
on the down branch → tool approves → deploy applies down →
app reverts.

**Cache + offline story:**

- `~/.wc-cache/<version_or_uuid>/` holds fetched schema +
  pre-generated code.
- `wc sync` checks cache first; falls back to tool API on miss.
- CI cache keyed on hash, shared across builds.
- New contributor without tool credentials: anonymous read-only
  schema fetch (open-source / docs reading); no schema edit
  permission.
- Fully offline (after first sync): everything works from cache.

**Environment-aware deploy client:**

- `local` / `ci` mode: accepts UUID-versioned schemas
  (work-in-progress).
- `staging` / `production` mode: refuses UUIDs; only
  tagged-version schemas allowed (`14.26`, not
  `01J9X4Z2K...`). Forces all production deploys to reference
  approved-and-published schema versions.
- Mode flag is deploy-client config, not in `w17.yaml` (env-
  specific).

**Cross-service compatibility tracker:**

Tool tracks which git repos / services pin which schema versions:

- Bumping schema `14.26 → 14.27`: tool surfaces in approval UI:
  - "Service B (last seen on `14.25`) is 2 versions behind, forward-compatible? ✅"
  - "Service C (last seen on `14.26-draft`) actively testing the bump, ✅"
  - "Service D (last seen on `13.99`) is a major version behind. REFUSE deploy without explicit upgrade."

This makes schema a **discoverable service contract** across
microservices.

**Open architectural questions (parked to iter-3+):**

- **Rollback across minor versions** — `14.26.05 → 14.25.99`
  requires schema downgrade. Tool orchestrates down migration
  with same approval flow as forward.
- **Hotfix branches off old versions** — pinning `14.26` while
  main is on `15.0`: tool supports "supported old version"
  status; hotfixes against deprecated schemas have their own
  approval path.
- **Schema deprecation lifecycle** — `draft → approved → active
  → deprecated → archived`. Deploy refuses `archived`.
- **Multi-tenant schema isolation** — single self-hosted tool
  per org vs SaaS multi-org. SaaS adds cross-org schema sharing
  semantics.

**Implications for `wc` CLI today:**

CLI must have **two modes with one interface**:

1. **Standalone (today's reality):** proto in git, `wc generate`
   produces SQL to `out/`, decisions via `--decide` flags, no
   tool. Works for single-repo simple projects.
2. **Tool-integrated (D29 target):** proto fetched from tool,
   `wc generate` POSTs migration plan to tool, gets `draft_id`,
   updates `w17.yaml`. Decisions surfaced in tool UI.

CLI **command surface is identical** in both modes. A
configuration file `wc.toml` decides where the migration plan
goes (local `out/` vs tool API). This separation means:

- Standalone mode = today, works as-is.
- Tool mode = layered on top of standalone without breaking
  changes.
- No decision about the tool today preempts anything in CLI / IR.

**Timing:**

- D29 is fixed as the architectural north star.
- **No code changes today** — CLI standalone mode is the
  current reality and keeps working unchanged.
- Tool integration is iter-3+ work, after D28 (migration
  safety) lands and after the hosted platform itself is
  built (separate big iter-3 milestone).
- Every architectural decision between now and then is
  measured against D29: does it make tool integration easier
  or harder?

### D30 — Engine isolation: pure `Plan()` + Sink / ResolutionSource adapters (added 2026-04-23)

**Status: locked.** Draws the layering boundary between the
compiler engine (pure, stateless function) and everything outside
it (storage, resolution delivery, approval workflows). Makes D29
tool integration an adapter choice instead of an engine concern,
and dissolves D28 Open Question #1 (migration file storage) as
not-this-layer's-problem.

**Principle.** The engine takes `(prev IR, curr IR, resolutions)`
and returns `(migrations, findings)`. It writes no files, reads
no registry, waits on no user input. Every side effect — file I/O,
HTTP calls, UI prompts, re-run coordination — lives in an adapter.

**Engine public API.**

```go
// Single entry point. Pure, idempotent, stateless. Safe to call
// concurrently with different inputs.
func Plan(prev, curr *ir.Schema, resolutions []Resolution) (*Plan, error)
```

**Plan shape** (proto message in `plan.proto`; Go types shown for
brevity):

```go
type Plan struct {
    Migrations []Migration     // one per target Connection
    Findings   []ReviewFinding // blocked decisions, if any
}

type Migration struct {
    Connection Connection
    UpSQL      string
    DownSQL    string
    Checks     []NamedSQL      // NEEDS_CONFIRM pre-apply queries
    Manifest   Manifest        // caps, extensions, D28 decisions applied
}

type ReviewFinding struct {
    ID        string           // deterministic hash of the decision point
    Column    ColumnRef        // table.column + proto field number
    Axis      string           // "carrier_change" / "generated_expr" / …
    Proposed  Strategy         // classifier default (e.g. CUSTOM_MIGRATION)
    Options   []Strategy       // set --decide can pick from
    Rationale string           // human-readable why
    Context   FindingContext   // prev + curr fact snapshots
}

type Resolution struct {
    FindingID string
    Strategy  Strategy  // SAFE / USING / NEEDS_CONFIRM / DROP_AND_CREATE / CUSTOM_MIGRATION
    CustomSQL string    // only when Strategy == CUSTOM_MIGRATION
    DecidedAt time.Time
    Actor     string    // free-form ("jdubansky@email" / "platform-bot" / "cli")
}
```

**Adapter interfaces** (not part of engine; live in separate Go
packages so engine tests never touch them):

```go
// ResolutionSource — supplies resolutions to the engine. Impls:
//   - MemorySource    (tests)
//   - decide.Decisions (parses --decide flags + --decisions YAML;
//                       lives in domains/compiler/decide/, NEVER
//                       under engine/ — adapters sit outside the
//                       pure engine tree)
//   - PlatformSource  (D29 future; calls hosted tool API)
type ResolutionSource interface {
    Lookup(findingID string) (Resolution, bool)
    All() []Resolution
}

// Sink — serialises Plan artifacts. Impls:
//   - MemorySink      (tests; captures Plan in-memory for assertions)
//   - FilesystemSink  (today's wc generate --out <dir> behavior)
//   - PlatformSink    (D29 future; pushes to registry)
type Sink interface {
    Write(plan *Plan) error
}
```

**Finding lifecycle (external policy, engine agnostic).**

1. Caller assembles known resolutions from whatever source.
2. `Plan(prev, curr, known)` → (migrations, findings).
3. For each finding with a matching Resolution (by ID): migration
   is complete; sink writes it.
4. Findings without matches → returned as-is. Caller decides what
   to do:
   - CLI: print as `diag.Error`, exit non-zero.
   - Platform: surface as approval task in UI.
   - CI: block the PR.
5. Caller gathers the missing resolutions and re-calls Plan
   idempotently. Finding IDs are deterministic hashes of
   (column fqn, axis, prev fact snapshot, curr fact snapshot),
   so resolutions survive re-runs as long as the inputs don't
   change.

**Idempotence rule.** Same `(prev, curr, resolutions)` → byte-
identical `Plan`. This extends iter-1 AC #4 across resolutions.
Tests enforce.

**Statelessness rule.** No caches, no globals, no I/O inside the
engine. Goroutine-safe with different inputs. Each run is a pure
function evaluation.

**Why this resolves Q1 (migration file storage).**

Q1 asked: "where do migration SQL, check.sql, manifest files live —
`out/<domain>/...`? platform registry? git-tracked?" Under D30,
the engine doesn't care. `Plan.Migrations[i].UpSQL` is a string.
`Plan.Migrations[i].Checks[j].SQL` is a string. Where those strings
get serialised is Sink's call. Standalone CLI today uses
`FilesystemSink`; platform mode tomorrow uses `PlatformSink`; both
consume the same `Plan` struct.

**Why this resolves Q2-like proto-wire concerns gracefully.**

Findings are structured enough to carry a Warning-severity field
(future: `Severity` enum — Info / Warning / Block). Proto-wire-
breaking carrier change gets `Severity: Warning` + proposed
`Strategy: CUSTOM_MIGRATION`; blocking policies (CI, approval UI)
layer on top.

**Impact on D28 phasing.**

- **Phase 2** (classifier + `diff.go` refactor): implements `Plan()`
  with `ResolutionSource` injected. Same LOC budget as before
  (~400-600).
- **Phase 3** (`--decide` flag): becomes the `decide.Decisions`
  implementation in `domains/compiler/decide/` (sibling to
  `engine/`, not underneath it). ~50 LOC flag parser; trivial.
- **Phase 4** (check.sql emit): **unblocked.** Engine emits
  check SQL as `Migration.Checks[]` strings. Storage layout is
  Sink's concern.
- **Phase 5** (exhaustive tests): use `MemorySink` + in-memory
  `ResolutionSource`; assert `Plan` shape directly. No filesystem
  round-trip in the unit tests; apply-roundtrip fixtures continue
  to use `FilesystemSink` + `make test-apply`.

**Impact on today's `cmd/cli/`.**

Today's pipeline inlines `os.WriteFile` calls throughout
`cli+ir+emit`. Refactor under D30:

1. `cli` becomes: parse flags → load prev/curr IR → build
   `decide.Decisions` → `Plan(prev, curr, resolutions)` → pass
   result into `FilesystemSink.Write(plan)`.
2. `emit/postgres` stops writing files; returns strings +
   manifest structs upstream.
3. `FilesystemSink` lands in a new package
   (`srcgo/domains/compiler/sink/filesystem/`) with the file-layout
   logic extracted from `emit` + `cli`.

~150 LOC code motion; no behavior change for the default
`wc generate --out <dir>` CLI invocation.

**Impact on D29 (future platform integration).**

Adapter story, not engine work. `PlatformResolutionSource` +
`PlatformSink` slot in as alternate impls. The hosted tool runs
the same engine binary (or same library linked into the tool);
only the adapters differ. D29's lock-file / registry / approval
UI all sit cleanly outside the engine.

**Rationale (user, 2026-04-23):**

> "engine by mel generovat veci, ale neresit kam. Kdyz diff ma
> problem, melo by to byt review finding s resolution objektem,
> na ktery se ceka. Odkud se vezmou resolutions neni vec engine.
> Ted pro testovani si vystup muzeme davat kam potrebujeme,
> tzn implementujeme nejaky dummy connector na to, ale realne
> by to vlastne melo mit jen interni rozhrani."

Clean separation: compiler = deterministic function; storage +
approval = policy layers outside.

**Open sub-question (non-blocking).**

- **Where does the `Plan` proto live?** Likely `plan.proto` gets
  new top-level messages (Plan, Migration, ReviewFinding,
  Resolution) alongside the existing Ops. Alternative:
  `engine.proto` as a fresh file to keep Plan-the-envelope
  separate from Op-the-atom. Lean: extend `plan.proto` — they're
  the same conceptual domain, and a fresh file just to separate
  "Plan-wrapper" from "Plan-Ops" is ceremony without payoff.

## Acceptance criteria for M1

1. `wc generate --prev prev.proto curr.proto` emits a migration pair
   describing the diff across every Op variant listed in Scope in.
   For `prev == curr` the plan is empty and **no files are written**
   (Open Question #1 resolved as "skip"; `alter_noop/` fixture asserts
   the no-file behaviour).
2. The M1 fixture set (~40 pairs listed in Design) covers every Op
   variant plus the grand-tour composite (~20 axes in one pair).
   Every fixture apply-rollback-rounds cleanly on PG 18 against a
   fresh DB.
3. Apply-roundtrip shape on PG 18 for every fixture:
   `fresh DB → apply(prev up) → apply(diff up) → introspect(curr) →
   apply(diff down) → introspect(prev) → apply(prev down) →
   introspect(empty)` — every step green.
4. Same (prev, curr) input produces byte-identical diff SQL on
   re-run (AC #4 of iter-1 extended).
5. Decision-required cases (column carrier change, PK change, enum
   value removal, `pg.custom_type` change) surface a `diag.Error`
   with `file:line:col` + `why:` + `fix:` listing the available
   strategies (DROP_AND_CREATE / CUSTOM_MIGRATION). Fixture
   `…_needs_decide` per case. No silent wrong SQL; no auto-emitted
   destructive migration without `--decide`.
6. Narrow / incompatible-cast changes emit plain SQL; PG is the
   apply-time gate (refuses ALTER if live data doesn't fit).
   Compiler adds no warning comments; data-survival validation is
   the deploy client's job (M4+).
7. `wc_migrations` table is created in the initial migration and
   an INSERT row lands at the tail of every `up.sql`. Fixture
   `wc_migrations_hash_detects_edit` asserts that hand-edited SQL
   files produce a hash mismatch the deploy-client harness can
   detect (harness itself is M4 deliverable; M1 emits the hash and
   provides a utility to verify it).
8. Coverage floor ≥ 97.8 % cross-package (iter-1 close-out baseline;
   M1 shouldn't regress it; realistically moves up since every Op
   gets unit coverage on top of fixtures).

## Open questions

All prior iter-2 M1 open questions resolved as of 2026-04-23:

1. **Empty-plan emit behaviour — resolved: SKIP.** `prev == curr`
   emits nothing to `out/` (no files, no placeholder). Fixture
   `alter_noop/` asserts zero files written.
2. **`wc_migrations` placement — resolved: one per DB, domain-
   scoped by design.** D26 pins "one domain = one DB"; therefore
   on any DB instance there's exactly one `wc_migrations` in the
   connection's default schema. No module-namespace scoping, no
   domain column, no name suffix.
3. **Narrow / incompatible-cast UX — resolved: compiler emits plain
   SQL, PG gates at apply time, deploy client + platform UI handle
   data-survival review.** No warning comments, no `--allow-*`
   flags. Compiler stays deterministic; data validation is the
   platform's responsibility (confirmed with user's earlier
   architectural decision — pre-apply checks on real data live
   in the deploy client / UI, not in the compiler).

M1 coding is unblocked.

## M4 — Capability usage tracking + platform manifest

**Locked design 2026-04-25 for Layers A+B.** Layer C (MySQL stub) is
deferred to a later turn; the contract Layer B pins is explicit so
Layer C can land without reshaping anything.

### Scope in — Layer A+B

- **Usage collector** in `emit/`: emitters call `usage.Use(capID)`
  at every use-site that references a D16 capability. `DialectEmitter`
  grows a `*Usage` parameter on `EmitOp`; `Emit()` constructs the
  collector, plumbs it through per-op dispatch, and returns it.
- **Postgres emitter instrumentation**: every dispatch path that
  produces a typed column / index / constraint / comment calls
  `Use()` with the right cap ID. Instrumented surfaces:
  - column types: JSONB, JSON, UUID, BYTEA, BOOLEAN, INET, CIDR,
    MACADDR, TSVECTOR, Array (`pgArrayOf`), HSTORE (map-string),
    CITEXT (db_type), ENUM_TYPE (string+SEM_ENUM), NUMERIC,
    DOUBLE_PRECISION, DATE/TIME/TIMESTAMP/TIMESTAMPTZ, INTERVAL,
    SCHEMA_QUALIFIED (every site that qualifies with a namespace);
  - defaults: fn `gen_random_uuid()`, fn `uuidv7()`;
  - column modifiers: IDENTITY_COLUMN, GENERATED_COLUMN, COMMENT_ON;
  - indexes: per-method (GIN/GIST/BRIN/SPGIST/HASH), INCLUDE_INDEX,
    storage WITH-map exercise. Partial/expression parked on
    raw_indexes (DQL).
  - FK ops: ON_DELETE_RESTRICT, ON_DELETE_SET_DEFAULT;
  - transactional wrapper: TRANSACTIONAL_DDL (once per non-empty
    migration, not per op — BEGIN/COMMIT is a per-migration
    concern).
- **Manifest population** in `engine.buildManifest`:
  - `Capabilities` = sorted + deduped union of `usage.Sorted()`.
  - `RequiredExtensions` = sorted + deduped union of (a) the
    catalog's `Requirement(cap).Extensions` for every cap in
    `Capabilities`, resolved via the `DialectCapabilities` the
    emitter implements (emitters without it contribute nothing —
    redis stub today), and (b) IR-level
    `(w17.pg.field).required_extensions` propagated on every
    column the emitted SQL references.
  - Manifest emitted with zero capabilities + zero extensions +
    zero applied resolutions collapses to `nil` (today's
    behaviour for empty applied-only). A non-empty Capabilities
    or RequiredExtensions list always triggers Manifest
    emission.
- **`FilesystemSink` output**: when Manifest non-empty, Sink writes
  `<ts>-<name>.manifest.json` next to `up.sql` / `down.sql`. Format
  is canonical `protojson.Marshal` output (sorted keys,
  deterministic). Empty Manifest → no file written (stays AC #1
  clean: a no-op migration writes nothing).

### Scope out (Layer C follow-up)

- **MySQL stub**: parallel emitter + catalog; EmitOp returns
  not-implemented like sqlite's stub until a pilot needs it.
  Instrumentation harness (usage collector + Requirement lookup)
  applies uniformly once MySQL emission bodies land.
- **Platform consumption**: deploy client / hosted platform reads
  the manifest.json in a later iteration. M4 Layer A+B only ships
  the on-disk artifact; the UI / API contract lands with D29.
- **Usage-driven apply-time gating**: emitter validating usage
  against a configured target PG version / extension set. Deferred
  per D16 phase 2.

### Acceptance criteria for M4 Layer A+B

1. `emit.Emit(e, plan)` returns `(up, down string, usage *Usage,
   err error)`. `usage.Sorted()` is deterministic (sorted,
   deduped) across re-runs on identical plans.
2. Every capability in `postgres/pgCatalog` used anywhere by the
   emitter is recorded via `Use()` at the relevant dispatch site.
   Test iterates each cap ID and asserts at least one Op in the
   fixture corpus drives `Use` for it (invariant test: "no dead
   cap IDs in the catalog").
3. `engine.buildManifest` populates `Capabilities` +
   `RequiredExtensions` in sorted, deduped form. A grand-tour
   fixture (re-use iter-1 `pg_dialect` or the M1 `alter_grand_tour`)
   asserts the expected lists byte-for-byte in a golden.
4. `FilesystemSink` writes `<ts>-<name>.manifest.json` alongside
   `up.sql` / `down.sql` iff Manifest non-empty. Empty manifest
   migrations write no JSON file (AC #1 preservation). Contents
   are stable `protojson.Marshal` output (re-run → byte-identical).
5. `make test-apply` stays green on PG 18 — manifest artifacts
   don't interfere with the apply-roundtrip harness (harness
   ignores non-`.sql` files). Coverage floor 96.3 % cross-package
   (M3 baseline; manifest code adds unit tests that should nudge
   it up, not down).

### D31 — Capability usage tracking via `*Usage` collector + post-emit union (added 2026-04-25)

**Status: locked.** Captures the plumbing decision for Layer A of
M4.

**Decision.** Track capability usage at emission time via an
explicit collector passed through `DialectEmitter.EmitOp`. Engine
consumes the collector post-emit, unions with catalog-derived
extension requirements + IR-level `required_extensions`, and
persists the result on `Migration.Manifest`.

**Shape.**

```go
// emit/usage.go
type Usage struct {
    set map[string]struct{} // internal — never serialised
}

func (u *Usage) Use(cap string) {
    if u == nil || cap == "" { return }
    if u.set == nil { u.set = map[string]struct{}{} }
    u.set[cap] = struct{}{}
}

func (u *Usage) Sorted() []string { /* sorted dedup */ }

// emit/dialect.go
type DialectEmitter interface {
    Name() string
    EmitOp(op *planpb.Op, usage *Usage) (up, down string, err error)
}

func Emit(e DialectEmitter, plan *planpb.MigrationPlan) (up, down string, usage *Usage, err error)
```

**Why a parameter, not a receiver field.** Emitters are shared
zero-value singletons today (`postgres.Emitter{}`), reused across
every `wc generate` invocation in a process. Storing state on the
receiver would force per-run instances or lock-contended state.
An explicit parameter keeps the emitter stateless + the engine
controls the collector lifecycle per-migration.

**Why widen `EmitOp` instead of an optional interface.** The
alternative was "emitters that want to opt into tracking implement
`EmitOpWithUsage`; callers type-assert." One real impl + two stubs
isn't enough surface area to justify the duplicated dispatch.
Widening `EmitOp` is a one-line migration for each stub and makes
the tracking contract mandatory — which matches D16's iter-2+
vision of every cap referenced by emission showing up on the
manifest.

**Why `Use()` on a `*Usage` and not a closure.** A pointer-method
receiver is no-op on nil (`if u == nil { return }`), so zero-value
construction stays possible for tests that don't care about usage.
A closure (`func(cap string)`) would need nil-handling at every
call site. Small thing, repeated 40+ times across the emitter.

**Why union with IR `required_extensions` at engine layer.** Two
paths today carry extension data: the catalog (per-cap
`Requirement.Extensions`) and the author's explicit
`(w17.pg.field).required_extensions` on a column using a
`custom_type` that the compiler can't classify. Both converge on
the manifest. Union at engine keeps emitter code focused on the
capability IDs it knows about; IR-level extensions flow through
the IR the engine already walks.

**Escape hatch.** Emitters without a `DialectCapabilities` impl
(redis today) contribute no extension inferences. Their
`Capabilities` list is still populated from their `Use()` calls —
those IDs just don't resolve to `Requirement.Extensions`, so the
manifest's `RequiredExtensions` stays limited to IR-level entries
for that connection.

**Rationale.** Makes manifest.json a self-contained artifact:
platform / deploy client reads one file and knows which caps the
migration relied on + which extensions the target needs loaded —
no cross-reference back to the source IR. Same data the D16
catalog already curates, just pivoted from "which caps does PG
support" to "which caps does this migration use".

**Open sub-question (non-blocking).** `TRANSACTIONAL_DDL` is
always-on for transactional PG emission — every non-empty
migration wraps in BEGIN/COMMIT. Recording it every time inflates
the cap list noise-wise. Lean: record it, let downstream filter
for "interesting caps" if the UI wants. Consistent with every
other cap — emission uses it, cap list names it.

### D32 — Per-dialect authoring pass-through: parallel proto extensions, not generalised (added 2026-04-25)

**Status: locked.** Settles the proto-shape question for the
`(w17.pg.field) { custom_type, required_extensions }` escape
hatch as more dialects (MySQL, future) need their own.

**Decision.** Each dialect ships its own proto extension file +
IR slot. MySQL gets `proto/w17/mysql/field.proto` with
`(w17.mysql.field)`, an `IR Column.Mysql MysqlOptions = <next-fnum>`,
and the MySQL emitter reads only `Column.Mysql`. PG keeps
`(w17.pg.field)` and `Column.Pg`. Cross-dialect generalisation
(`(w17.dialect.field).variants[mysql|pg|…]`) is rejected: it
gains nothing concrete + complicates per-dialect proto extensions
that *don't* exist on every dialect (PG `required_extensions` ≠
MySQL `engine`/`collation` ≠ Redis `key_pattern`).

**Why parallel.**

1. Pattern already in place. `proto/w17/pg/field.proto` says
   *explicitly*: "Mandatory rule: no dialect is allowed to infer
   column type from `custom_type`. Every dialect namespace ships
   its own escape hatch." MySQL just continues the pattern.
2. Each dialect's options ARE different. PG has
   `required_extensions`; MySQL has `engine` (InnoDB / MyISAM),
   `collation` (utf8mb4_general_ci / …), `partition` clause.
   Forcing them into a shared proto message means optional fields
   that only one dialect uses, which the other dialect's authoring
   has to ignore.
3. Compile-time safe. PG's `Column.Pg` field has number 31; MySQL
   gets the next free number (33 today, since list/map carrier
   reservations took the slot pattern past 31). Adding doesn't
   break PG wire-compat.
4. Reader simplicity. The PG emitter does
   `col.GetPg().GetCustomType()`. MySQL emitter does
   `col.GetMysql().GetCustomType()`. No type assertion, no
   variant dispatch, no risk of cross-dialect bleed.

**Where the IR-level union lives.** `engine.buildManifest`
already merges per-column `(w17.pg.field).required_extensions`
into `Manifest.RequiredExtensions`. When MySQL lands, the same
merge runs `(w17.mysql.field).required_plugins` (or whatever
the parallel field is) into the same Manifest slot — the slot
name `RequiredExtensions` stays as a dialect-neutral "things the
DBA must install before apply" label rather than reshaping the
manifest message. Documented at the IR-builder layer, not at the
emitter layer.

**Mandatory rule preserved.** No emitter is allowed to infer
column type from another dialect's `custom_type`. PG ignores
`(w17.mysql.field).custom_type` and vice versa. Cross-dialect
schemas (one proto Message compiled for both PG and MySQL via
multi-domain D26) set both extensions explicitly when needed.

**Open sub-question (non-blocking).** Should `Manifest.
RequiredExtensions` rename to a dialect-neutral label like
`RequiredArtifacts` or `RequiredPrerequisites`? Lean: no — the
PG term is already what most ops engineers think of, and renaming
churns the manifest contract for the deploy client (D29) before
the deploy client even exists. Revisit if MySQL's plugin model
makes the field name actively misleading.

## Where this document stops

Everything past M4 Layer A+B is backlog territory
(`iteration-2-backlog.md`) or Layer C. When Layer A+B ships, open
the Layer C section (MySQL stub) here the same way D-records
landed in iter-1: one milestone at a time.
