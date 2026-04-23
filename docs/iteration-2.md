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

**Draft — 2026-04-23.** M1 rescoped (2026-04-23 turn-2) to
**complete alter-diff** — every Op variant lands together, no
deferred "comes later in M2" subset. No code shipped yet.
Milestones land one at a time, same ritual as iter-1:

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

**Status: design pending.** Captures the complete fact-pair × strategy
table that the differ uses to classify every column / table /
constraint change. Today's binary SAFE / REFUSE split is too coarse
for production use — D28 expands it into six strategies and pins
each fact transition to one.

**Strategies:**

- **SAFE** — type + data both fit; clean ALTER, no user input
  needed. (e.g. NOT NULL → NULL, max_len widen, default add.)
- **LOSSLESS_USING** — PG cast handles the conversion in-place;
  data may overflow but PG fails apply rather than silent
  corruption. (e.g. TEXT ↔ CITEXT, JSON ↔ JSONB.)
- **NEEDS_CONFIRM** — types are theoretically convertible but
  data may not fit (string→int, max_len narrow). User must
  explicitly opt in via `--decide` flag; differ generates a
  paired `check.sql` that pre-validates current data. If the
  check fails on real data, user falls back to DROP_AND_CREATE
  or CUSTOM_MIGRATION.
- **DROP_AND_CREATE** — author-acknowledged lossy change; emit
  drops the column + adds a fresh one. Effectively a destructive
  migration with explicit user opt-in.
- **CUSTOM_MIGRATION** — escape hatch where author writes their
  own SQL/DQL block that knows how to transform the column's data
  before the structural change applies.
- **REFUSE** — default for changes that can't be safely automated
  (carrier change, message ↔ scalar, custom_type swap). Differ
  surfaces a strategy menu; user picks one of the above
  alternatives explicitly.

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

1. Document the full classification matrix here in D28
   (paper exhaustive, every fact transition pinned).
2. Refactor `plan/diff.go` strategy classifier from binary →
   six-strategy enum; emit structured "needs decision X"
   objects.
3. CLI `--decide` flag plumbing.
4. `check.sql` emit pipeline.
5. Test matrix exhaustive.

**Open questions:**

- Should NEEDS_CONFIRM auto-generate check.sql even without user
  decision, so reviewer can preview risk? (Lean: yes.)
- DROP_AND_CREATE proto-side semantics — does it require a new
  proto field number + reserved old, or is it acceptable as-is
  with a force flag in the decision? (Lean: force flag with
  reserved-number warning, since reserved-number requirement is
  proto convention not enforceable in compiler.)
- CUSTOM_MIGRATION location — inline in proto as
  `(w17.db.column).migration_custom_sql` or external in
  decisions YAML? (Lean: decisions YAML; proto is point-in-time-
  decision-free.)

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
5. REFUSE cases (column carrier change, PK change, enum value
   removal, `pg.custom_type` change) surface a `diag.Error` with
   `file:line:col` + `why:` + `fix:`. Fixture `…_refuse` per
   refused case. No silent wrong SQL.
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

## Where this document stops

Everything past M1's acceptance criteria is backlog territory
(`iteration-2-backlog.md`). When M1 lands, open the M2 section here
the same way D-records landed in iter-1: one milestone at a time.
