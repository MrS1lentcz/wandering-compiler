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

**Draft — 2026-04-23.** Scope sketched; M1 design locked. No code shipped
yet. Milestones land one at a time, same ritual as iter-1:

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

- **M1 — Alter-diff core: tables + structural columns.**
  Handles `AddTable`/`DropTable`/`NoOpTable`, plus within-table
  `AddColumn`/`DropColumn`. Column *content* changes land in M2. First
  shippable slice of the differ infrastructure.
- **M2 — Column-level fact alter.** `AlterColumn` (type / nullability /
  default / CHECK changes) + `RenameColumn` (free from D10 number
  identity). The pain point of alter-diff; splits into sub-slices per
  fact class.
- **M3 — Index + raw_index diffs.** `AddIndex`/`DropIndex`, identity =
  explicit name (fall back to derived name for unnamed). raw_indexes
  use per-name identity (D11 already pins).
- **M4 — FK + CHECK diffs.** Both on name-identity; mostly mechanical.
  Includes deletion_rule changes (ALTER CONSTRAINT on PG).
- **M5 — Multi-file schemas + cross-file FK.** Loader already uses
  protocompile; CLI gate loosens to accept the file set. FK target
  resolution grows a schema-wide registry.
- **M6 — Multi-connection per domain.** Bucket IR by connection; run
  the differ + emit per bucket; output tree grows
  `out/<domain>/migrations/<dialect>-<version>/`. D26 locks the
  identity + invariants; M6 is the implementation milestone.
- **M7+ — Capability usage tracking, MySQL stub, platform contract,
  DQL opening, local validator.** Pulled from the backlog as the M1–M6
  stack stabilises.

## M1 — Alter-diff core

### Scope in

- `plan.Diff(prev, curr)` handles `prev != nil`. prev IR is built via
  the same `ir.Build` path as curr, from a proto file supplied to the
  CLI (see D25 below).
- **Table-level ops.**
  - New Op variants: `DropTable`, `NoOpTable` (no-op placeholder so the
    plan has one entry per output table, keeping `AddTable` and its
    siblings uniform for platform consumers).
  - Identity = `MessageFqn` (D24 pins the D19 open question).
- **Column-level structural ops within a carried-over table.**
  - New Op variants: `AddColumn`, `DropColumn`.
  - Identity = proto field number (D10). Number present only in prev →
    `DropColumn`; only in curr → `AddColumn`; both → pass through to M2.
- **Deterministic ordering.** Same determinism contract as iter-1
  (AC #4): `Diff` is a pure function of (prev, curr) plus their proto
  source positions, and topo-sort breaks ties lexically (already in
  place per iter-1 `topoSortByFK`).
- **CLI.** `wc generate --prev path/to/prev.proto path/to/curr.proto`
  runs the differ; when `--prev` is absent, behaviour falls back to
  iter-1 (initial migration). Output naming unchanged per D5.

### Single-connection assumption (M1 only; M6 opens multi-connection)

M1 runs against one connection per `wc generate` call. D26 below locks
the multi-connection model so M6 doesn't need a proto reshape when it
opens — the IR fields `Schema.connection` + `Table.connection` land in
M1 and default to the project-level default connection so the single-
connection path is a degenerate case of the general shape. `plan.Diff`
signature stays `(prev, curr *irpb.Schema)` — each call is one
connection; M6 wraps it in a per-connection orchestrator.

### Scope out (explicit, parked to M2+)

- Any ALTER of an existing column — type change, nullability flip,
  default change, CHECK add/remove. Both-present columns flow through
  M1 as no-ops at the column axis; the table is still carried over,
  and the diff just registers "this column still exists."
- Index, FK, CHECK diffs. M1 assumes they are unchanged across
  (prev, curr) and short-circuits to no-op when the within-table
  column set is unchanged. If any table changes *anywhere* in index /
  FK / CHECK territory, M1 surfaces a clear diagnostic pointing at M2
  (see "Escape hatch for partial diffs" below) rather than emitting a
  silently-wrong migration.
- Column rename detection. Free with D10 identity once M2 lands, but
  for M1 a number-renamed-in-curr column reads as an `AlterColumn`
  candidate which M1 doesn't handle yet → surfaces the same M2
  diagnostic.

### Design

**Differ shape.** `plan.Diff(prev, curr)` splits into three stages:

1. **Bucket tables by `MessageFqn`** across prev / curr. Produces three
   lists:
   - `onlyPrev` → emit `DropTable`.
   - `onlyCurr` → emit `AddTable` (iter-1 path reused verbatim).
   - `both` → pass to stage 2.
2. **For each `both` table, bucket columns by proto field number.**
   Produces:
   - `onlyPrev` → emit `DropColumn`.
   - `onlyCurr` → emit `AddColumn`.
   - `both` → facts-compare; if facts differ → **error diagnostic**
     pointing at M2 (scope-out; parked). If facts equal → column
     no-op.
3. **Assemble ordered plan.** Op order within a migration:
   1. `DropTable` ops first (in reverse FK topological order so a
      table isn't dropped while something still references it).
   2. `AddTable` ops (iter-1 topological order — referenced-first).
   3. Within carried-over tables: `DropColumn` before `AddColumn`
      (so renumbered replacements work cleanly; rename detection in
      M2 will collapse matching pairs).
   The `both` tables with zero column-level ops emit **no Op at all** —
   the plan simply omits them. `NoOpTable` is reserved as a plan
   variant but unused in M1 output; it lands in M2 when the platform
   needs "this table exists but nothing changed" as an explicit
   plan-level fact.

**Fact equality for M1 columns.** Intentionally narrow: equality
means **every Column field matches**, including all nested messages
(Default, Checks, PgOptions, etc.). Any mismatch is the M2 signal, not
an M1 silent no-op. Over-matching is safer than under-matching for the
first slice — false positives of the "please use M2" diagnostic are
better than shipping wrong SQL.

**Rename detection (table-level).** D10 says message rename ⇒
DropTable + AddTable. M1 honours this directly: rename → FQN changes →
`onlyPrev` + `onlyCurr` buckets both populated. Down-migration safety
is the author's responsibility in M1 (data loss warning needs to
surface in diagnostics, but the op itself is clean).

**Apply-roundtrip.** M1 grows the test harness: for each new fixture
pair `(prev.proto, curr.proto)`, the harness runs prev's `up.sql`,
then curr's `up.sql` (= the diff from prev → curr), then curr's
`down.sql`, then prev's `down.sql`, ensuring every migration in the
chain applies + rolls back cleanly. Goldens cover the diff SQL.

**Fixtures for M1.** Minimum set:

- `alter_add_table/` — prev has table A; curr has A + B → emits
  `AddTable B`.
- `alter_drop_table/` — prev has A + B; curr has only A → emits
  `DropTable B`.
- `alter_add_column/` — prev has A(f1); curr has A(f1, f2) → emits
  `AddColumn f2 on A`.
- `alter_drop_column/` — prev has A(f1, f2); curr has A(f1) → emits
  `DropColumn f2 on A`.
- `alter_noop/` — prev == curr → empty plan.
- `alter_fk_chain/` — prev/curr both have A + B where B→A; drop both
  → `DropTable` order reverses topo (B before A).
- Error fixtures: one per M2-deferred case (AlterColumn attempt,
  index change, FK change, CHECK change) each firing the M2-scope
  diagnostic.

### Plan op proto changes

`proto/domains/compiler/types/plan.proto` grows the `Op` oneof:

```proto
message Op {
  oneof variant {
    AddTable      add_table      = 1;  // iter-1
    DropTable     drop_table     = 2;  // M1
    AddColumn     add_column     = 3;  // M1
    DropColumn    drop_column    = 4;  // M1
    NoOpTable     no_op_table    = 5;  // reserved for M2
    AlterColumn   alter_column   = 6;  // M2
    RenameColumn  rename_column  = 7;  // M2
    AddIndex      add_index      = 8;  // M3
    DropIndex     drop_index     = 9;  // M3
    AddForeignKey add_foreign_key = 10; // M4
    DropForeignKey drop_foreign_key = 11; // M4
    AddCheck      add_check      = 12; // M4
    DropCheck     drop_check     = 13; // M4
  }
}

message DropTable {
  // Full prev-side Table. Carries the namespace / name the emitter
  // needs to produce DROP; down-migration reproduces the table so
  // it needs every column fact too.
  w17.compiler.ir.Table table = 1;
}

message AddColumn {
  string table_message_fqn = 1;   // identifies the carrier table
  w17.compiler.ir.Column column = 2;
  // Table context the emitter needs to qualify the ALTER TABLE —
  // namespace fields copied from the carrier Table at diff time so
  // emit stays Op-local (per iter-1 plan.proto convention).
  string table_name = 3;
  w17.compiler.ir.NamespaceMode namespace_mode = 4;
  string namespace = 5;
}

message DropColumn {
  string table_message_fqn = 1;
  // Only the column proto_name + name are strictly needed for the
  // UP DDL; the full Column is carried so DOWN can re-add it.
  w17.compiler.ir.Column column = 2;
  string table_name = 3;
  w17.compiler.ir.NamespaceMode namespace_mode = 4;
  string namespace = 5;
}
```

Numbers 5–13 are reserved now so M2/M3/M4 don't churn the oneof tags.

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
to iter-1. Multi-file prev (when M5 lands) uses `--prev-dir <path>`
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
  forbidden at this axis (FK across domains is M5's concern; a
  cross-domain `connection` pointer would fracture the isolation
  boundary).
- `Schema.connection` and `Table.connection` are IR-level facts
  carried on every Schema/Table. M1 populates them with the
  default; M6 activates the non-default path.
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
orchestration is M6.

## Acceptance criteria for M1

1. `wc generate --prev prev.proto curr.proto` emits a migration pair
   describing the diff. For `prev == curr` the plan is empty and
   `up.sql` / `down.sql` are empty migrations (or skipped — lands
   alongside fixture `alter_noop/`).
2. The M1 fixture set listed in Design covers every Op introduced
   (`DropTable`, `AddColumn`, `DropColumn`) plus topological-order
   correctness under FK chains.
3. Each emitted migration applies + rolls back cleanly on PG 18:
   `apply(prev.up) → apply(curr_vs_prev.up) → apply(curr_vs_prev.down)
   → apply(prev.down)` — every step green against a fresh DB.
4. Same (prev, curr) input produces byte-identical diff SQL on
   re-run (AC #4 of iter-1 extended).
5. M2-deferred cases (AlterColumn, Index / FK / CHECK changes)
   surface a `diag.Error` with `file:line:col` + `why:` + `fix:`
   pointing at "parked until M2" — no silent wrong SQL.
6. Coverage floor ≥ 97.8 % cross-package (iter-1 close-out baseline;
   M1 shouldn't regress it).

## Open questions (resolve before M1 coding)

- **Empty-plan emit behaviour.** `alter_noop/`: does wc write empty
  `up.sql` / `down.sql` files, skip them entirely, or emit a "no
  changes" placeholder comment? Leaning toward **skip** — matches
  D6's "migration as unit of work" framing; an empty unit isn't a
  unit. Platform integration will want a sentinel later; M1 decision
  is reversible.
- **Error fixture UX for M2-deferred diffs.** One generic "parked
  until M2" diagnostic vs. five specific ones ("AlterColumn type
  change parked", "index change parked", …). Lean toward specific
  — matches the iter-1 `why:` / `fix:` discipline. Worth one
  user confirmation before wiring.
- **NoOpTable usefulness in M1 output.** Keeping the Op wire
  variant reserved (numbered 5) without emitting it yet is fine;
  platform may want it to track "I reviewed this table's hash is
  unchanged." Decision deferred to M2 when the platform contract
  firms up.

## Where this document stops

Everything past M1's acceptance criteria is backlog territory
(`iteration-2-backlog.md`). When M1 lands, open the M2 section here
the same way D-records landed in iter-1: one milestone at a time.
