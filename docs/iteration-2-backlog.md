# Iteration-2 Backlog

Captured 2026-04-21 as iteration-1 closed. Not a committed scope — this
file is where ideas and parked work land so they don't drift. Each entry
has a **why**, a **sketch** of the implementation direction, and a
**preconditions** note (what must ship before it's tractable).

`iteration-2.md` (the actual spec) cherry-picks from here and
formalises entries as they graduate.

## Status as of 2026-04-25

**Shipped** (formalised in `iteration-2.md` M-records):
- M1 Alter-diff — full op surface + applied-state (D27).
- M2 Multi-file schemas + cross-file FK.
- M3 Multi-connection per domain (D26).
- M4 Layer A — capability usage tracking → Manifest (D31).
- M4 Layer B — FilesystemSink writes manifest.json.
- D28 Migration-safety classification matrix + classifier pkg.
- D30 Engine isolation (pure `Plan` + `Sink`/`ResolutionSource`).
- D32 Per-dialect authoring pass-through (parallel proto ext).
- Per-dialect test-apply matrix (PG 14–18 dynamic, scripts/).

**Next open in iter-2:**
- M4 Layer C — MySQL emitter stub (paused by user direction;
  infrastructure ready to plug in).
- Pre-Layer-C cleanup — coverage push, Makefile standard targets,
  core-fn 100% coverage backlog.

**Still parked (iter-3+):**
- DQL (Doctrine-inspired ORM).
- Local schema validator.
- Hosted platform + deploy client (D29 north-star).

Individual entries below are left as written for historical
context — don't re-derive from the entry if `iteration-2.md` has
a formalised D-record.

---

## Big blocks (full milestones)

### DQL — ORM-like query language

**Why.** User decision 2026-04-22: a future iteration introduces
DQL (Doctrine-inspired, ORM-like) as the authoring surface for
conditions / expressions / queries. Replaces opaque SQL strings
in structured messages.

**Scope surfaces that land on DQL when it ships:**
  - Partial index predicates (`Index.where`) — currently via
    `raw_indexes`.
  - Expression indexes (`IndexField.expr`) — currently via
    `raw_indexes`.
  - CHECK constraint bodies (cross-column, function-call,
    expression) — currently via `raw_checks`.
  - Query generation (iter-2+ M-block).
  - gRPC handler query bodies.

**Naming consistency:** DQL uses `fields` (not `columns`) — same
identifier as the schema's `(w17.field)` and proto field names.
Author sees the same word whether declaring a schema, writing a
query, or building a gRPC method.

**Escape-hatch discipline today:** until DQL lands, the `raw_*`
escape hatches (raw_indexes, raw_checks) own every opaque-SQL
authoring path. Structured messages (`Index`, CHECK synths) do
NOT accept opaque strings — authors who need them go through
raw, migrate to DQL when it lands. This keeps the structured
surface DQL-ready from day one.

### Local schema validator (post-commit check)

**Why.** User decision 2026-04-22: later phases keep a local
representation of the current DB schema state and replay
migrations against it as a "post-commit check" validation phase.
Catches drift / impossibilities / missing capabilities before the
migration reaches a real target DB.

**Benefits:** universality (same check in CI / local dev /
pre-merge, no running target DB needed), 100% stability
(deterministic replay), catches problems at the authoring
boundary rather than apply time.

**Preconditions.** Hosted platform + deploy client. The pieces
exist in iter-1 (IR + capability catalog); the persistent state
store + CI integration are the new work.

---

## Big blocks (original list)

### Alter-diff

**Why.** `plan.Diff(nil, curr)` handles only the initial-migration case.
Real schemas evolve: columns get added, renamed, type-changed; indexes
added / dropped; tables dropped. Without alter-diff, every schema
change means "wipe and re-create" — unusable on any live system.

**Sketch.** `plan.Diff(prev, curr)` walks both schemas. Identity key =
proto field number (D10, already decided). Per-table pass bucketises
columns by number across prev/curr → {both-present with equal facts →
no-op, both with differing → AlterColumn, only prev → DropColumn, only
curr → AddColumn}. Per-message-FQN table-level dispatch {both → no-op
or alter, only prev → DropTable, only curr → AddTable}. Index / FK /
CHECK diffs are set-diffs keyed by derived-or-explicit name.

New plan Op variants: DropTable, AddColumn, DropColumn, AlterColumn,
RenameColumn, AddIndex, DropIndex, AddForeignKey, DropForeignKey,
AddCheckConstraint, DropCheckConstraint.

**Preconditions.** None — all the IR shape is already there (proto
field numbers persisted on SourceLocation, derived names stable).

**Expected scope.** ~2000 LOC + 20+ fixtures covering each diff shape.
Biggest iter-2 block.

---

### Multi-connection per domain (DB-per-domain + per-table override)

> **Pinned 2026-04-23 as D26 in `iteration-2.md`. Actual
> implementation lands as M3.** Design decided, implementation
> follows once M1–M2 (alter-diff + multi-file) stabilise the
> differ + loader infrastructure it extends.

**Why.** A single project often needs a main relational DB plus a
small side store — SQLite for static configs, a KV engine for session
blobs, a dedicated reporting DB. Today authors hand-roll the wrapper
over each backend. A schema compiler that already owns proto-first
types + typed gRPC surfaces can extend that typing benefit to *every*
backend, relational or not. The compiler keeps the structured-schema
value proposition even when the DB underneath doesn't offer it.

**Sketch.**

  - `(w17.db.module).connection = { name, dialect, version }`
    (FileOptions). Absent → project default.
  - `(w17.db.table).connection = "side_configs"` optional override
    (must resolve inside the same domain).
  - `ir.Schema.connection` + `ir.Table.connection` populated at
    build time; differ runs per connection; emitter dispatches per
    `(dialect, version)`.
  - **Output:** `out/<domain>/migrations/<dialect>-<version>/
    YYYYMMDDTHHMMSSZ.{up,down}.sql` (D6 still applies — gitignored).
  - **Invariant.** Within a domain, `(dialect, version)` pair must be
    unique. Two of the same = domain split. Enforces clean boundaries
    at the IR level.

**DQL angle (parks with the DQL block).**

  - **Cross-connection reads.** Planner splits per-connection,
    composes in the app layer. Simple nested-loop by default; can
    grow smarter over time.
  - **Cross-connection mutations.** Carry an `@non_atomic` flag
    into generated handler wrappers + admin UI. Not a block — a
    warning badge. Authors chose the split; compiler makes the
    consequence visible so nobody assumes atomicity they don't have.
  - **2PC out of scope.** Saga / outbox is an application pattern,
    not a compiler feature.

**Preconditions.** M1–M2 (differ infrastructure + multi-file loader).
MySQL / SQLite dialect emitters when those connections are declared
(iter-2 backlog "Additional dialects").

---

### Multi-file schemas + cross-file FK

**Why.** Real projects split schemas across files (one per domain /
bounded context). Iter-1 accepts exactly one proto per `wc generate`
and same-file FK references only. A pilot with ≥2 domain files can't
express cross-file references.

**Sketch.** `wc generate` accepts multiple proto paths (already does
— but rejects beyond one). Loader builds the full set of
LoadedFile. IR builds every schema; cross-file FK targets resolve
through a schema-wide table-name registry. Table name collisions
across files fail loud with both source locations.

**Preconditions.** None — loader already uses protocompile which
handles imports, just the CLI gate needs loosening.

---

### Hosted platform + deploy client

**Why.** Per D6, iter-1 outputs SQL to `out/` locally and users apply
via `psql -f`. No migration storage, no approval workflow, no
applied-state tracking, no audit trail. Organisations beyond one
developer need this.

**Sketch.** Three sibling components in this repo under
`srcgo/domains/`:

  - `platform/` — gRPC service + UI (approval, audit, version history).
    Stores every generated migration + the proto schema it was
    generated from. Operators review + approve before apply.
  - `deploy/` — lightweight client that pulls approved migrations +
    applies them + records applied-state. Runs in each target
    environment.
  - `compiler/` (current) — pushes generated migrations into the
    platform via gRPC rather than writing to `out/`.

**Preconditions.** Platform needs alter-diff first (to have something
non-trivial to review). Deploy client needs a stable migration
manifest format (see "Feature capability tracking" below).

---

### Additional dialects

**Why.** Real multi-dialect support (MySQL 8+, SQLite-as-production,
MS SQL Server). The DialectEmitter interface exists from day one;
adding an emitter should be additive.

**Sketch.** Per dialect, implement `DialectEmitter` + `DialectCapabilities`.
DbType enum values already include cross-dialect types (SMALLINT,
JSON, BLOB, etc.) — PG emitter maps per-dialect too (BLOB → BYTEA),
others map to their native names. AUTO dispatch (D15) already
anticipates native-fallback-to-JSON ladder per dialect.

**Preconditions.** Dialect-aware test-apply target (currently PG-only).
Per-dialect golden suite (same fixture inputs, different expected SQL
per dialect).

**Priority order.** MySQL 8+ first (widest adoption), SQLite-as-
production second (embedded / edge deploys), MS SQL Server third (if
a pilot asks).

---

### `wc lint` / `diff` / `viz` / `changelog`

**Why.** The IR carries more information than `wc generate` emits. A
rich CLI multiplies the value of the proto source-of-truth:

  - **`wc lint`** — static checks against conventions (PK on every
    table, immutable on audit fields, FK must have an index, …).
  - **`wc diff`** — show planned alter-diff without emitting SQL.
  - **`wc viz`** — schema graph (Graphviz / Mermaid / d2).
  - **`wc changelog`** — human-readable summary of what changed
    between two proto revisions, derived from alter-diff.

**Preconditions.** `wc lint` stands alone. `wc diff` + `wc changelog`
need alter-diff. `wc viz` stands alone (consumes IR).

---

## Vocabulary additions (small)

Each of these is a focused DSL extension — few proto changes, few
emitter changes, documented trade-offs already explored in the
chat history.

### Structured index shapes (graduate from raw_indexes)

> **Promoted 2026-04-22: this is D23, scheduled right after D22 and
> before alter-diff (iter-2 M1).** User flagged that the original
> piecemeal D20 "index sort order + NULLS FIRST/LAST" slot should
> be replaced by a broader indexes + constraints dynamic redesign
> matching Django's shape. NULLS FIRST/LAST lands inside this
> redesign (as a field on the new `IndexColumn` inside the
> structured messages), not as a standalone feature. Must happen
> BEFORE alter-diff so the differ works against the final
> index/CHECK IR shape, not an intermediate one that would be
> rewritten.

**Why.** D11 raw_indexes is the escape hatch; it takes opaque SQL
bodies. When patterns stabilise (GIN, GIST, partial, expression),
they graduate to typed message shapes (`GinIndex`, `PartialIndex`,
`ExpressionIndex`) with compiler validation + alter-diff-friendly
structure. Pattern mirrors D9's custom_type → curated flags
graduation.

**Sketch.** New messages in `(w17.db.table)`:

```proto
message GinIndex {
  repeated string fields = 1;
  string operator_class = 2;  // "gin_trgm_ops", "jsonb_path_ops", …
  repeated string required_extensions = 3;
  string where = 4;  // optional partial
}

message PartialIndex {
  repeated string fields = 1;
  bool unique = 2;
  string where = 3;  // the predicate (validated as SQL text)
}

message ExpressionIndex {
  repeated string expressions = 1;  // each is a SQL expression
  bool unique = 2;
}
```

**Preconditions.** D11 raw_indexes adoption. When raw_index bodies
repeat the same pattern across pilots, graduate to a typed shape.

### COMMENT ON from proto doc-strings

**Why.** Django 4.2 added `db_comment=` and `db_table_comment`;
matches PG / MySQL `COMMENT ON`. We already capture proto leading
comments via protocompile's `SourceLocations` — can propagate into
SQL COMMENT statements.

**Sketch.** At emit time, if the column/table has a non-empty proto
doc-comment, append `COMMENT ON COLUMN t.c IS '…'` after the CREATE
TABLE. Escape single quotes.

**Preconditions.** Admin / UI generator (iter-2+) consumes the same
doc-strings — better to do them together.

### db_collation

**Why.** Django 3.2's `db_collation=` on CharField / TextField. Real
use: case-insensitive equality without CITEXT extension (PG: `"en-x-icu"`
collation), or locale-specific sort orders.

**Sketch.** Add `(w17.db.column).collation: string`. PG emits
`COLLATE "<name>"`. Validated against carrier (string only).

**Preconditions.** None.

### db_tablespace

**Why.** PG-specific; rare but legitimate for multi-tier storage
(hot/warm SSD/HDD splits). Django has it on Meta + Index.

**Sketch.** `(w17.pg.field).tablespace` + `(w17.pg.table).tablespace`.
Emitter appends `TABLESPACE <name>`. Authoring-surface PG-only.

**Preconditions.** None. Rare enough to keep on `(w17.pg.*)` rather
than lift.

### Deferrable FK / CHECK constraints

**Why.** SQL:2003 deferrable constraints delay enforcement to COMMIT
time. Django exposes via `deferrable=Deferrable.DEFERRED`. Useful for
circular FK inserts (parent/child write in same transaction).

**Sketch.** `(w17.db.column).deferrable: enum { INITIALLY_IMMEDIATE,
INITIALLY_DEFERRED }`. Emitter appends `DEFERRABLE INITIALLY
{IMMEDIATE|DEFERRED}`.

**Preconditions.** None.

### Range fields (PG-specific preset cluster)

**Why.** Django postgres `IntegerRangeField`, `DateRangeField`,
`NumericRangeField`, `TsRangeField`, `TstzRangeField`. PG native
types (`int4range`, `daterange`, …) with operators for overlap,
containment, adjacency. Today expressible via `custom_type: "int4range"`
— could graduate to a preset cluster.

**Sketch.** Add Type values `INT_RANGE`, `NUM_RANGE`, `DATE_RANGE`,
`TS_RANGE`, `TSTZ_RANGE`. PG emits `int4range`, `numrange`,
`daterange`, `tsrange`, `tstzrange`. Carrier is string (wire is `[a,b)`
literal form) or bytes.

**Preconditions.** None. Graduation-candidate only; user asks → add.

### Element typing for map values

**Why.** D15 restricts maps to `map<K, V>` with V dispatching on
carrier. No per-value sem refinement (e.g., `map<string, URL>` to
indicate values are URLs). Iter-2+ can add `element_type` on
`(w17.db.column)` for map value refinement.

**Sketch.** `(w17.db.column).element_type: Type` — applies to the
value type of a map field. Storage picks up the sem's preset
(VARCHAR(2048) values packaged into JSONB / HSTORE accordingly).

**Preconditions.** None, but low priority until a pilot asks.

### MAC_ADDRESS preset

**Why.** Django has no `MacAddressField`, but we already considered.
Iter-1 expressible via `custom_type: "MACADDR"`. Graduation candidate
when enough PG-native networking types accrete.

**Sketch.** `Type.MAC` → PG MACADDR (native), MySQL VARCHAR(17),
SQLite TEXT.

**Preconditions.** None.

### `immutable` runtime enforcement

**Why.** Iter-1 records `(w17.field).immutable` on the IR but doesn't
enforce it in SQL. Full enforcement needs a trigger that rejects
UPDATEs changing the column.

**Sketch.** Emitter generates a trigger per immutable column:

```sql
CREATE FUNCTION <table>_<col>_immutable_check() RETURNS TRIGGER AS $$
BEGIN
  IF NEW.<col> IS DISTINCT FROM OLD.<col> THEN
    RAISE EXCEPTION 'column <col> is immutable';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER <table>_<col>_immutable BEFORE UPDATE ON <table>
  FOR EACH ROW EXECUTE FUNCTION <table>_<col>_immutable_check();
```

**Preconditions.** Trigger vocabulary (generic — also for
`auto_now`-on-update). First trigger feature sets the pattern.

### `auto_now` on UPDATE (trigger-based)

**Why.** Django's `auto_now=True` updates the column to current time
on every save. Needs a trigger. Iter-1 has `default_auto: NOW` which
covers INSERT only (`auto_now_add`).

**Sketch.** `(w17.field).default_auto: NOW_ON_UPDATE` (new variant)
→ trigger + `BEFORE UPDATE` hook.

**Preconditions.** Trigger vocabulary (see immutable).

---

## Reliability + tooling

### Feature capability tracking (partial — interface shipped iter-1)

**Status.** `srcgo/domains/compiler/emit/capabilities.go` (iter-1 close-out)
defines `DialectCapabilities` + the cap-ID constants. PG + SQLite
implement it. What's there: declarative catalog of every feature
the PG emitter may reference + per-feature Requirement (MinVersion,
Extensions).

**What's NOT wired yet (iter-2):**

  - **Usage tracking.** Emitter doesn't collect which caps it actually
    used during `Emit()`. Needs a per-call context that each emit
    function feeds ("I emitted JSONB in column X", "I emitted
    gin_trgm_ops in index Y").
  - **Manifest output.** After tracking: emit a JSON manifest
    alongside the up/down SQL listing every cap + where it's
    used. Format spec ties into deploy client.
  - **Target-DB config.** `Emitter{ TargetVersion: "14.0",
    AvailableExtensions: ["hstore", "citext"] }`. Validates usage
    against the declared target; fails the generate with a clear
    diagnostic when a cap exceeds the target's floor.
  - **CLI exposure.** `wc generate --target-pg-version=14
    --extensions=hstore,citext` to declare target config.

**Why.** Iter-1 assumes PG 18. Real deploys run on 14-17 or even older.
Without capability awareness the generator happily emits `uuidv7()`
(PG 18+) or `INCLUDE` (PG 11+) against a target that rejects them at
apply. Pre-flight validation turns runtime mystery errors into
design-time diagnostics.

**Preconditions.** None for tracking + manifest; CLI target config
pairs with multi-version CI testing (iter-2 dialect suite).

### JSON Schema validation from proto messages

**Why.** Django doesn't do this natively — only app-level via
pydantic or third-party libraries. A schema compiler that knows proto
shape AND stores data as JSON(B) can attach `CHECK
(jsonb_matches_schema('{...}', col))` constraints guaranteeing shape
conformance at the DB layer. Bypasses all app-level code paths
(direct SQL, ops incidents, async workers) — same invariant
guarantee.

**Dialect capabilities.**

  - **PostgreSQL** — `pg_jsonschema` extension (Supabase, open
    source). Adds `jsonb_matches_schema(schema::jsonb, doc::jsonb) →
    bool`. Draft 2020-12 support. Requires extension install.
  - **MySQL 8.0.17+** — native `JSON_SCHEMA_VALID(schema, doc)`.
    Usable in CHECK constraint. Zero-extension.
  - **Oracle 21c+** — native `IS JSON VALIDATE USING '<schema-name>'`
    syntax. Schema registered separately via `DBMS_JSON_SCHEMA.REGISTER`.
  - **SQL Server** — no native schema validation; only `ISJSON()`.
  - **SQLite** — no native; json1 has `json_valid()` (syntax only).

**Design sketch.**

Opt-in per column via `(w17.db.column).validate_json_shape: bool`:

```proto
repeated Event events = 1 [
  (w17.db.column) = { validate_json_shape: true }
];
// → JSONB[] (per D15 dispatch) + CHECK (every element matches
//   the JSON schema derived from the Event message)
```

For scalar `type: JSON` columns, the author provides a schema-
describing proto message FQN:

```proto
string config = 2 [
  (w17.field)     = { type: JSON },
  (w17.db.column) = { validate_json_shape: "pkg.ConfigSchema" }
];
```

Compiler pipeline:

  1. Proto descriptor → JSON Schema (well-established; libraries
     exist — `protoc-gen-jsonschema`, Buf Connect's codegen).
  2. Schema includes `additionalProperties: true` to preserve
     proto's forward-compat (new fields added to proto mustn't
     reject existing rows).
  3. Emitter wraps the schema literal in a CHECK using the
     dialect's validator function.
  4. `(w17.pg.field).required_extensions` auto-populates with
     `pg_jsonschema` when PG emitter uses this path.

**Challenges.**

  - **Schema evolution** — changing the proto message changes the
    schema; existing rows may not conform. Alter-diff emits DROP
    CONSTRAINT + ADD CONSTRAINT NOT VALID + VALIDATE CONSTRAINT
    (PG pattern) and the operator reviews in the platform.
  - **Performance** — pg_jsonschema benchmarks ~5-15% write overhead.
    Opt-in-only keeps perf budget out of default path.
  - **Extension ubiquity** — pg_jsonschema isn't in every managed PG
    (AWS RDS, GCP CloudSQL, …). Platform documents the
    prerequisite; deploy client verifies pre-apply.

**Preconditions.** Alter-diff (for schema-change mechanics). Platform
+ deploy client (for extension verification flow). Pairs naturally
with admin-gen where the generated admin UI can display
schema-validation errors.

**Competitive framing.** If shipped, this is a feature Django / ent /
Prisma / SQLAlchemy / gorm don't have at the DB layer. The
schema-compiler angle is uniquely positioned to do it — proto is the
single source of truth, and the same schema generates gRPC stubs,
REST validators, AND DB constraints.

### CHECK-verbosity flag (open question #2 from iter-1)

**Why.** Iter-1 emits full CHECK constraints by default. Some deploys
want opt-out (write-throughput vs DB-enforced correctness trade-off).
`--check-constraints=full|length-only|off` CLI + generator flag.

**Preconditions.** None. Minor dimension tuning — add when a pilot
asks.

---

## Cross-cutting

### Per-dialect test-apply matrix

**Why.** M9 tests against PG 18 only. MySQL emitter (iter-2) + dialect
regression testing needs test-apply per dialect.

**Sketch.** `make test-apply` grows a `DIALECT=` variable defaulting
to `postgres`. Per-dialect Dockerfile (already have PG:18-alpine).

**Preconditions.** MySQL emitter (or any second real emitter).

### Pilot adoption

**Why.** AC #7 (revised 2026-04-21) replaced external-pilot with
synthetic grand-tour matrix — but once the platform + deploy client
exist, a real pilot against a production codebase is still the
ultimate validation.

**Sketch.** Pick a real project (invoice system / CMS / whatever),
port its ent schema / Django models / raw SQL to w17 proto, run
through the full platform pipeline end-to-end with approval +
deploy. Report friction.

**Preconditions.** Platform + deploy client + alter-diff +
multi-file. Last iter-2 milestone.

### Projection generation (proto → view)

**Why.** Parked from iter-1 (`w17.schema.projection`). Real use case:
generate read-optimised views from the authoring schema, with
materialised-view variants for derived aggregates.

**Sketch.** `(w17.projection) = { name: "active_customers", from:
"customers", filter: "deleted_at IS NULL", fields: ["id", "name"] }`.
Emitter produces `CREATE VIEW` or `CREATE MATERIALIZED VIEW`.

**Preconditions.** None. Separate feature from main alter-diff
track.
