# Iteration 1 — Implementation Plan

Companion to [`iteration-1.md`](iteration-1.md) (contract + decisions) and
[`experiments/iteration-1-models.md`](experiments/iteration-1-models.md)
(pilot example + IR shape). This document covers **how we build it**:
Go package layout, build order, testing strategy, and which acceptance
criterion each piece serves.

Nothing here is a new design decision. If implementation forces one, lift
it into `iteration-1.md` under Decisions first.

## Project layout

wandering-compiler follows `conventions-global/structure.md` and
`conventions-global/go.md` — it is a monorepo with `srcgo/` as the single Go
root and each component as a domain under `srcgo/domains/`. The compiler is
a domain even though it has no DB and no long-running service in
iteration-1: a domain is a "specific self-contained functional layer with a
shared external interface", and DB / gRPC daemon are possible attributes,
not required ones. Later components (platform, deploy client) join as
sibling domains without re-layout.

```
wandering-compiler/
├── CLAUDE.md
├── README.md
├── Makefile
├── PROJECT_STAGE                        # absent while in skeleton (per CLAUDE.md)
├── .gitignore                           # out/, .volumes/, srcgo/**/bin/, srcgo/pb/, srcgo/domains/**/gen/
├── .env.example
├── .env.defaults
├── compose.yaml                         # postgres for test-apply (M9)
│
├── docs/                                # (already exists)
│
├── proto/
│   ├── w17/                             # authoring vocabulary — published to users; consumed by loader
│   │   ├── db.proto                     # (w17.db.table), (w17.db.column)
│   │   ├── field.proto                  # (w17.field) — merged data semantics (M1 rev2)
│   │   └── pg/
│   │       └── field.proto              # (w17.pg.field) — Postgres dialect namespace (M1 rev3)
│   └── domains/
│       └── compiler/
│           ├── types/                   # compiler-internal proto (M2 rev2, 2026-04-21)
│           │   ├── ir.proto             # IR: Schema/Table/Column/Index/ForeignKey/Check/Default/SourceLocation — D4
│           │   └── plan.proto           # MigrationPlan + Op oneof (added in M3)
│           └── services/                # (iteration-1: empty; later: service_compile.proto)
│
├── srcgo/
│   ├── go.mod                           # single go.mod for the monorepo
│   ├── errors.go                        # package srcgo — shared errors
│   ├── lib/                             # (iteration-1: empty)
│   ├── x/                               # (iteration-1: empty)
│   ├── pb/                              # generated from proto/ — gitignored, regenerated via `make schemagen`
│   │   ├── w17/                         # compiled w17 options
│   │   └── domains/
│   │       └── compiler/
│   │           └── types/
│   │               ├── ir/              # package irpb (ir.pb.go) — M2 rev2
│   │               └── plan/            # package planpb (plan.pb.go) — M3
│   └── domains/
│       └── compiler/
│           ├── application.go           # package compiler — Application interface + module interfaces
│           ├── config.go                # package compiler — Config + NewConfigFromEnv()
│           ├── application/
│           │   ├── application.go       # app struct + facade
│           │   ├── options.go           # functional options, New()
│           │   └── module_output.go     # (minimal — writes resolved output dir; env-configured default)
│           ├── cmd/
│           │   └── cli/
│           │       ├── main.go          # kong root + kongplete
│           │       └── cmd_generate.go  # `wc generate` subcommand (built as binary `wc`)
│           ├── diag/                    # domain-local — shared *diag.Error (file:line:col + why/fix)
│           │   └── error.go
│           ├── loader/                  # domain-local — parse .proto via bufbuild/protocompile, decode w17 options
│           │   └── loader.go            # single file (options.go folded in; reparse helper is 15 lines)
│           ├── ir/                      # domain-local — validator + helpers over generated *irpb types (D4)
│           │   ├── build.go             # loader.LoadedFile → *irpb.Schema; all D2/D7/D8 invariants enforced here
│           │   └── display.go           # carrier/sem/auto name helpers — strip proto enum prefixes for diagnostics
│           ├── plan/                    # domain-local — differ (D4)
│           │   ├── diff.go              # Diff(prev, curr *irpb.Schema) (*planpb.MigrationPlan, error)
│           │   └── diff_test.go         # happy path + determinism + non-nil prev rejected
│           ├── emit/                    # domain-local — per-dialect SQL emitters (D4)
│           │   ├── dialect.go           # DialectEmitter interface + plan-level Emit orchestrator
│           │   ├── postgres/
│           │   │   ├── emit.go          # Emitter struct, EmitOp dispatch, emitAddTable (table body)
│           │   │   ├── column.go        # column line, carrier×type map, PG passthrough, DEFAULT + IDENTITY
│           │   │   ├── check.go         # Length / Blank / Range / Regex / Choices CHECK rendering
│           │   │   ├── index.go         # CREATE [UNIQUE] INDEX + INCLUDE + derived names
│           │   │   └── emit_test.go
│           │   └── sqlite/
│           │       └── emit.go          # stub, errors "not implemented in iteration-1" — AC #6 (M5)
│           ├── naming/                  # domain-local — D5 <NNNN>_<slug>.sql
│           │   └── name.go
│           ├── writer/                  # domain-local — write files into out/migrations/
│           │   └── writer.go
│           ├── testdata/                # golden-file cases — AC #5
│           │   ├── product/
│           │   │   ├── input.proto
│           │   │   ├── expected.up.sql
│           │   │   └── expected.down.sql
│           │   ├── no_indexes/
│           │   └── multi_unique/
│           ├── gen/                     # protobridge / proto stub output (iteration-1: empty) — gitignored
│           └── bin/                     # compiled binaries — gitignored
│
└── out/                                 # generator writes migrations here — gitignored, per D6
```

Notes:

- **Single `go.mod`** at `srcgo/go.mod` per `go.md` §srcgo Structure. No
  sub-module boundaries inside the repo.
- **Binary name vs. package name.** The `wc` binary is built from
  `srcgo/domains/compiler/cmd/cli` — the `cli` package name is the
  convention, `wc` is the output name (via `go build -o wc`).
- **`application.go` + `config.go` exist from day one** even though the
  compiler needs almost no startup-wired infrastructure. Per `go.md`
  §Domain Structure, they are mandatory for every domain.
- **`proto/w17/`** is at the repo's `proto/` root (not under
  `proto/domains/compiler/`) because the w17 vocabulary is the compiler's
  *published* surface consumed by end users of the project, not a
  compiler-internal type. It sits alongside `proto/domains/` the same way
  `proto/common/` would.
- **`testdata/`** lives inside the domain that uses it, per Go convention.
  No root-level `testdata/`.

## Package responsibilities

| Package | Input | Output | Notes |
|---|---|---|---|
| `proto/w17/` | — | option descriptors | Source of truth for the authoring vocabulary. Compiles into `srcgo/pb/w17/`. |
| `proto/domains/compiler/types/` | — | `irpb`, `planpb` generated types | Compiler-internal schema / plan messages (D4 rev 2026-04-21). Private — not part of the user-facing vocabulary. |
| `srcgo/domains/compiler/diag` | descriptor + msg | `*diag.Error` | Shared user-facing diagnostic type (file:line:col + `why:` + `fix:`). See feedback memory "user-friendly errors". |
| `srcgo/domains/compiler/loader` | `*.proto` paths | `*LoadedFile` (Go struct wrapping `protoreflect.FileDescriptor` + decoded w17 options) | Uses [`github.com/bufbuild/protocompile`](https://github.com/bufbuild/protocompile) — no shelling out to `protoc`. Stays Go (not proto) because it carries non-serializable descriptor handles — the proto boundary starts at `ir.Build`. |
| `.../compiler/ir` | loader output | `*irpb.Schema` | Validates invariants (every field has `type`, `CHAR`/`SLUG` have `max_len`, FKs target exists, etc.). Invariant violations become `*diag.Error` aggregated via `errors.Join`. Helpers are free Go functions over generated `irpb` types. |
| `.../compiler/plan` | two `*irpb.Schema` (prev, curr) | `*planpb.MigrationPlan` | Iteration-1: prev is always nil; output is one `AddTable` op per table. |
| `.../compiler/emit` | `*planpb.MigrationPlan` + `DialectEmitter` | up SQL + down SQL strings | `DialectEmitter` is the Go interface; `postgres.Emitter` is the only real impl, `sqlite.Emitter` is the stub from AC #6. |
| `.../compiler/naming` | `[]plan.Op` + sequence | migration basename like `0001_create_products` | Sequence source for iteration-1 is the count of existing files in `out/migrations/`; the platform (later) will own sequencing server-side. |
| `.../compiler/writer` | basename + up/down SQL | two files in `out/migrations/` | Only responsibility: write bytes. |
| `.../compiler/application` | Config + options | `compiler.Application` (facade) | Constructed at startup by `cmd/cli/main.go`. Iteration-1 has essentially one module (output writer factory); more modules appear when gRPC / platform integration lands. |
| `.../compiler/cmd/cli` | CLI flags + input path | exit code | Wires loader → builder → diff → emit → name → writer via `Application`. No business logic. |

## Build order (milestones)

Each milestone is independently testable. Ship them in order; do not skip.

### M1 — w17 option schemas compile (revised 2026-04-20 — M1 rev2 + rev3)

- Write `proto/w17/{db,field}.proto` and `proto/w17/pg/field.proto`
  against the vocabulary in `iteration-1.md` "In scope". `(w17.validate)`
  was merged into `(w17.field)` in M1 rev2 — there is no `validate.proto`.
  `(w17.db.column)` is the field-level storage-override extension.
  `(w17.pg.field)` is the first dialect-specific extension namespace
  (added in rev3).
- `make schemagen` produces `srcgo/pb/w17/*.pb.go`,
  `srcgo/pb/w17/db/db.pb.go`, and `srcgo/pb/w17/pg/field.pb.go`.
- Hand-written test: a tiny `.proto` file that imports our options and sets
  one of each (including `unique`, `(w17.db.column)`, `orphanable`,
  `choices`, DECIMAL + precision/scale, `default_auto: NOW / UUID_V4 /
  TRUE / IDENTITY`, `(w17.pg.field)` with curated flags + the
  `custom_type` escape hatch, temporal types, `Index.name` +
  `Index.include`), loaded via `protocompile` in a Go test that pulls the
  option values out. Proves the proto vocabulary is well-formed.
- **Serves AC #1** (option schema surface).

### M2 — loader + IR builder

- `srcgo/domains/compiler/loader` parses a user `.proto`, returns descriptors +
  decoded option values.
- `srcgo/domains/compiler/ir` builds `*Schema` from loader output. All
  validation (missing `type`, unknown FK target in the same file, `max_len`
  missing on `CHAR`/`SLUG`) happens here. Errors carry file:line from the
  descriptor.
- Unit tests: one per error class + one happy path.
- **Serves AC #1**.

### M3 — plan (trivial)

- `plan.Diff(nil, schema) → {AddTable, AddTable, …}`.
- Unit test: two tables in, two `AddTable` ops out, order stable by
  table-name sort (determinism — AC #4).
- **Serves AC #1, AC #4**.

### M4 — postgres emitter

- `emit/postgres.Emitter` renders each `Op` to up + down SQL.
- Check rendering is dispatched on `Check` variant:
  `LengthCheck` → `char_length(col) <= N`, `RegexCheck` → `col ~ 'pat'`, etc.
- Deterministic column, constraint, and index ordering — all explicit, no
  map iteration in the output path.
- **Serves AC #1, AC #2, AC #4**.

### M5 — sqlite stub emitter (dialect contract proof)

- `emit/sqlite.Emitter` implements `DialectEmitter` but every method returns
  `errors.New("sqlite emitter: not implemented in iteration-1")`.
- It exists to catch PG-shaped leaks in the interface *while iteration-1 is
  small*. If the PG emitter has the only valid implementation and the
  interface accidentally names a PG-only concept, the stub's compile will
  catch it.
- **Serves AC #6**.

### M6 — naming + writer

- `naming.Name(ops, seq)` → `0001_create_products`.
- `writer.Write(dir, basename, up, down)` → two files.
- Before write: ensure `out/migrations/` exists; `.gitignore` at repo root
  covers the whole `out/` tree.
- **Serves AC #5 (deterministic file names), AC #4 (byte-identical).**

### M7 — CLI + Application

- `srcgo/domains/compiler/application.go` — minimal `Application` interface
  (output dir getter for now).
- `srcgo/domains/compiler/application/` — `app{}` facade + `New()` + one
  `module_output.go`.
- `srcgo/domains/compiler/cmd/cli/main.go` — kong root, kongplete setup.
- `cmd_generate.go` — `wc generate --iteration-1 <proto-file>... [--out ./out]`.
  Wiring only. Errors bubble up with file:line.
- Binary name is `wc` (built with `go build -o wc ./srcgo/domains/compiler/cmd/cli`).
- **Serves AC #1**.

### M8 — golden tests

- `srcgo/domains/compiler/testdata/{product,no_indexes,multi_unique}/` —
  three cases.
- One `go test` file loads each `input.proto`, runs the full pipeline to
  in-memory SQL strings, diffs against `expected.{up,down}.sql`.
- `go test -update` flag regenerates goldens (for intentional changes).
- **Serves AC #5**.

### M9 — apply + round-trip against real Postgres

- Makefile target `test-apply`: spins up ephemeral `postgres:16-alpine` per
  `go.md` §Schema Migrations ("migration DB is purely temporary — never
  contains production or local data"), runs `psql -f` on the generated up,
  then down, then up again, confirming clean apply and clean rollback.
- **Serves AC #2, AC #3**.

### M10 — pilot adoption

- One table from the pilot project (picked from
  `docs/conventions-global/`) replaces its hand-written migration with
  `wc`'s output. Pilot applies the SQL manually via `psql -f` since the
  platform + deploy client don't exist yet (D6). Side-by-side compare for
  behavioral equivalence.
- **Serves AC #7**.

## Testing strategy

- **Unit tests** next to the code —
  `srcgo/domains/compiler/ir/ir_test.go`,
  `srcgo/domains/compiler/plan/diff_test.go`,
  `srcgo/domains/compiler/emit/postgres/emit_test.go`.
- **Golden tests** in `srcgo/domains/compiler/testdata/` — per M8. Updates
  via `go test -update`.
- **Determinism** is a first-class test: every unit test that produces
  user-visible output runs twice and asserts byte-identity (AC #4).
- **Integration test** against real Postgres runs in Makefile-orchestrated
  ephemeral container (M9). No Docker calls inside Go tests.
- **No mocks for the loader.** The loader is tested against small real
  `.proto` fixtures in `testdata/` — parsing behavior is exactly what we
  need to exercise, and protocompile is fast enough to run per-test.

## Mapping to acceptance criteria

| AC # | From `iteration-1.md` | Milestone(s) |
|---:|---|---|
| 1 | `wc generate` emits proto + migrations | M1–M7 |
| 2 | Applies cleanly to PG 14 | M4, M9 |
| 3 | Rolls back cleanly | M4, M9 |
| 4 | Byte-identical on re-run | M3, M4, M6, M8 |
| 5 | Golden-file test suite | M8 |
| 6 | Stub second dialect emitter | M5 |
| 7 | Pilot replaces hand-written migration | M10 |

## Open implementation questions

These are **implementation-shape** questions, not design questions — answer
in code, not in docs:

1. **protocompile vs protoreflect.** Default choice:
   `bufbuild/protocompile` (handles parsing + validation in one pass,
   first-class custom option decoding). Fallback if it surprises us: drop
   down to raw descriptor parsing via
   `google.golang.org/protobuf/reflect/protoreflect`.
2. **CLI flag shape.** Draft: `wc generate [--iteration-1] <proto-file>...
   [--out ./out]`. The `--iteration-1` flag exists because later iterations
   will add more output kinds; it lets us lock behavior for this iteration.
3. **Error reporting format.** Default: `file.proto:LINE:COL: message`
   format compatible with editor jump-to-error. Not worth deferring; wire
   it from M2 onward.
4. **`out/` directory location.** Default: relative to cwd. Override via
   `--out`. No auto-discovery — the user is always in charge of where
   output goes.
5. **`Application` surface in iteration-1.** Likely one or two getters
   (output directory, CHECK verbosity flag). Keep minimal; add modules only
   when iteration-2+ brings real dependencies (gRPC clients to the platform,
   dialect plug-ins loaded dynamically, etc.).

## Out of scope even for impl

- Hot-reload / watch mode.
- `wc lint`, `wc diff`, `wc viz`, `wc changelog` — future iterations.
- Proto imports other than `google/protobuf/timestamp.proto` and the
  `w17/*.proto` option files — iteration-1 rejects any other import with a
  clear error.
- Multi-file schemas with cross-file FKs — a single input `.proto` per run.
  Multi-file orchestration comes with iteration-2.
- Pretty-printed SQL. We emit tight, deterministic SQL; formatting is a
  later concern once golden-tests stabilize.
- Compiler-as-gRPC-daemon. Arrives when the hosted platform calls the
  compiler as a service; iteration-1 is CLI-only.

## What "ready to start coding" looks like

- [x] This doc is committed.
- [x] `PROJECT_STAGE` stays absent (skeleton — per CLAUDE.md).
- [x] `srcgo/go.mod` is initialized with a module name
      (`github.com/MrS1lentcz/wandering-compiler/srcgo`, Go 1.26).
- [x] `Makefile` has placeholder targets for `build`, `schemagen`, `test`,
      `test-apply`.
- [x] `.gitignore` covers `out/`, `srcgo/pb/`, `srcgo/**/gen/`,
      `srcgo/**/bin/`, `.volumes/`, `.env`.

**Status (2026-04-21).** Skeleton + M1 + M1 rev2 + M1 rev3 + M2 + M2 rev2 + M3 + M4 complete; **M5 next.**
- Skeleton: `srcgo/go.mod` (Go 1.26), `Makefile` placeholders, `.gitignore`.
- M1 rev3 lands four Django-parity fills + a dialect-extension namespace:
  - `(w17.field).orphanable` (optional bool, FK-only) — property-shape
    answer to `ON DELETE CASCADE / SET NULL`; inferred from `null` when
    unset. Richer Django `on_delete` (`PROTECT`, `RESTRICT`, …) stays as a
    UI/analysis concern. See D8.
  - `(w17.field).choices` (FQN of a proto enum, cross-file permitted) —
    emits `CHECK col IN ('VAL1', 'VAL2', …)`. Reuses proto enums rather
    than a parallel inline list. See D8.
  - `type: DECIMAL` with `(w17.field).precision` + `(w17.field).scale`
    (string carrier for lossless wire). MONEY/PERCENTAGE/RATIO remain as
    fixed-shape double-carried presets. See D2.
  - `AutoDefault.IDENTITY` — auto-increment integer PK. Renders as
    `GENERATED BY DEFAULT AS IDENTITY` (PG/Oracle/DB2/MSSQL),
    `AUTO_INCREMENT` (MySQL), `AUTOINCREMENT` (SQLite). See D7.
  - New `proto/w17/pg/field.proto` → `(w17.pg.field)` — first
    dialect-specific extension namespace. Carries `jsonb` / `inet` /
    `tsvector` / `hstore` curated flags plus a `custom_type` +
    `required_extensions` escape hatch for pgvector / PostGIS / custom
    DOMAINs. See D9.
- M1 rev2 (previously shipped) remains the base: merged `(w17.validate)`
  into `(w17.field)`; `(w17.db.column)` for storage-only options;
  temporal types; `default` oneof + `AutoDefault` enum.
- `make schemagen` emits `srcgo/pb/w17/field.pb.go`,
  `srcgo/pb/w17/db/db.pb.go`, and `srcgo/pb/w17/pg/field.pb.go`
  (gitignored). `TestW17VocabularyCompiles` now also exercises
  `orphanable`, `choices`, DECIMAL + precision/scale, `default_auto:
  IDENTITY`, and `(w17.pg.field)` with both `jsonb` and the
  `custom_type` / `required_extensions` escape hatch — green.
- Extension layout: `proto/w17/db.proto` → `w17.db` / `dbpb`;
  `proto/w17/field.proto` → `w17` / `w17pb`; `proto/w17/pg/field.proto` →
  `w17.pg` / `pgpb` (subdir). Each new dialect namespace is a new subdir.

- M2 lands `srcgo/domains/compiler/loader` (single-file `loader.go` —
  `options.go` folded in; the typed-options helper is 15 lines),
  `srcgo/domains/compiler/ir` (`types.go`, `schema.go`, `checks.go`,
  `build.go`), plus a new `srcgo/domains/compiler/diag` package carrying
  the shared user-facing `*diag.Error` type (file:line:col + `why:` + `fix:`
  — see feedback memory). `ir.Build` enforces every D2 / D7 / D8 / D9
  invariant and aggregates errors via `errors.Join` so one run surfaces
  every problem. Tests: `loader/loader_test.go` (happy-path shape),
  `ir/build_test.go` (happy path + 8 error-class fixtures under
  `ir/testdata/errors/`, each asserting `file:`, `why:`, `fix:` substrings).

- **M2 rev2 (shipped, 2026-04-21) — IR as proto, not Go structs.** D4
  revised (iteration-1.md) + tech-spec Strategic Decision #8 added.
  `proto/domains/compiler/types/ir.proto` now defines `Schema` / `Table` /
  `Column` / `Index` / `ForeignKey` / `PgOptions` / `SourceLocation` plus
  `Check` and `Default` oneof messages; `Carrier`, `SemType`, `FKAction`,
  `AutoKind`, `RegexSource` are proto enums. `make schemagen` emits
  `srcgo/pb/domains/compiler/types/ir.pb.go` (package `irpb`). The Go-
  struct files (`schema.go`, `checks.go`, `types.go`) are gone; the IR
  package is now `build.go` + a thin `display.go` with carrier/sem/auto
  name helpers (proto's enum `String()` returns `SEM_CHAR` /
  `CARRIER_STRING` / `AUTO_NOW` — trimmed to the authoring-surface form
  for diagnostics). `ir.Build` returns `*irpb.Schema`, populates
  `SourceLocation` via `file.SourceLocations().ByDescriptor(d)`, and
  stores FK references by `proto_name` (not Go pointer) so the IR is
  wire-safe. `loader.LoadedFile` stays a Go struct (parse container
  holds non-serializable descriptor handles; proto boundary starts at
  `ir.Build`). `build_test.go` type-switches on generated `Check` /
  `Default` oneof wrappers (`ck.GetChoices()`, `def.GetAuto()`, …) — all
  eight error-class fixtures + happy path still green.

- **M3 (shipped, 2026-04-21) — trivial differ.** `proto/domains/compiler/types/plan.proto`
  introduces `MigrationPlan` / `Op` oneof / `AddTable{ ir.Table table }` —
  iter-1 ships only `AddTable`; `DropTable` / `AddColumn` / `AlterColumn` /
  `RenameColumn` / `AddIndex` / `DropIndex` land as pilot schemas surface
  real alter-diff needs. Differ at `srcgo/domains/compiler/plan/diff.go`:
  `Diff(prev, curr *irpb.Schema) (*planpb.MigrationPlan, error)` — rejects
  non-nil `prev` with a "not supported in iteration-1" error (alter-diff
  arrives iteration-by-iteration); for `prev == nil` walks `curr.Tables`
  in lexical name order and emits one `AddTable` per table. Tests
  (`diff_test.go`) cover happy path (reverse-sorted input → sorted ops),
  empty inputs, non-nil-prev rejection, oneof-variant regression guard,
  and AC #4 determinism (two runs → byte-identical deterministic
  `proto.Marshal`).
- **M3 layout fork resolved.** Two proto files in one Go package directory
  is illegal, so `ir.proto` and `plan.proto` now live in sibling subdirs
  under `srcgo/pb/domains/compiler/types/`: `.../types/ir` →
  package `irpb`, `.../types/plan` → package `planpb`. `ir.proto`
  go_package bumped to `…/types/ir;irpb`; `plan.proto` authored with
  `…/types/plan;planpb`. The three `irpb` imports in
  `srcgo/domains/compiler/ir/` updated; existing tests green. Proto
  import path stays `domains/compiler/types/ir.proto` — only the Go
  output moved.

- **M4 (shipped, 2026-04-21) — postgres emitter.**
  `srcgo/domains/compiler/emit/` with the narrow `DialectEmitter` contract
  (`Name() string` + `EmitOp(*planpb.Op) (up, down, err)`) and a free
  `Emit(e, plan)` orchestrator that concatenates up blocks forward and
  down blocks in reverse (rollback undoes in inverse application order).
  `emit/postgres/` splits into `emit.go` (dispatch + table body),
  `column.go` (carrier×type mapping per `iteration-1-models.md`,
  `(w17.pg.field)` passthrough incl. `custom_type` escape hatch, DEFAULT
  literal / `NOW()` / `CURRENT_DATE` / `CURRENT_TIME` / `gen_random_uuid()`
  / `uuidv7()` / `'[]'` / `'{}'` / `TRUE` / `FALSE`, `IDENTITY` as
  `GENERATED BY DEFAULT AS IDENTITY` column modifier), `check.go`
  (Length / Blank / Range / Regex / Choices → `CONSTRAINT
  <table>_<col>_<suffix> CHECK (…)` with fixed suffix-per-variant naming),
  and `index.go` (named `CREATE [UNIQUE] INDEX` with `INCLUDE`, derived
  names `<table>_<cols>_{uidx,idx}` when the IR leaves the name empty).
  Composite PK renders as a table-level `PRIMARY KEY (…)`; single-col PK
  is inlined on the column line. Down SQL: `DROP INDEX IF EXISTS` in
  reverse, then `DROP TABLE IF EXISTS`. Tests: happy-fixture pipeline
  smoke (loader → ir.Build → plan.Diff → emit, structural assertions
  on the SQL), MONEY → NUMERIC(19,4) regression guard, composite-PK
  rendering, unknown-op error path, and AC #4 determinism (two pipeline
  runs → byte-identical up/down).
- **Drive-by fix in `ir.Build` (M4).** The unique-index synthesis loop
  now skips PK columns. Without this, every PK column picked up a
  duplicate `CREATE UNIQUE INDEX <table>_<col>_uidx` on top of the
  `PRIMARY KEY` declaration that already implies one — redundant in
  pg_indexes and noisy in the migration. Matches the reference SQL in
  `iteration-1-models.md`.

**Next:** M5 — sqlite stub emitter (implements `DialectEmitter`; every
`EmitOp` returns `errors.New("sqlite emitter: not implemented in
iteration-1")`). Exists to catch PG-shaped leaks in the interface while
the interface is still small — AC #6.
