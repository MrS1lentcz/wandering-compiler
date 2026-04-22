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
│           │       ├── emit.go          # stub, errors "not implemented in iteration-1" — AC #6
│           │       └── emit_test.go     # compile-time DialectEmitter conformance + runtime error shape
│           ├── naming/                  # domain-local — D5 rev2: YYYYMMDDTHHMMSSZ
│           │   ├── name.go
│           │   └── name_test.go
│           ├── writer/                  # domain-local — write files into out/migrations/
│           │   ├── writer.go
│           │   └── writer_test.go
│           ├── testdata/                # golden-file cases — AC #5
│           │   ├── product/
│           │   │   ├── input.proto
│           │   │   ├── expected.up.sql
│           │   │   └── expected.down.sql
│           │   ├── no_indexes/
│           │   └── multi_unique/
│           ├── examples/                # user-facing runnable examples (M7)
│           │   └── iteration-1/
│           │       └── happy.proto      # copy of ir/testdata/happy.proto;
│           │                            # `out/` appears alongside when wc runs against it
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
| `.../compiler/naming` | `time.Time` | migration basename like `20260421T143015Z` | D5 rev2: compact UTC timestamp, no sequence state. CLI supplies `time.Now().UTC()`; tests inject a frozen clock. |
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

### M6 — naming + writer (timestamp-based per D5 rev2)

- `naming.Name(at time.Time) string` → `"20060102T150405Z"` (compact UTC
  ISO-8601). No op dispatch, no slug, no sequence state — see D5 rev2
  for the revised rationale.
- `writer.Write(dir, basename, up, down) (upPath, downPath string, err error)`
  — `os.MkdirAll(dir, 0o755)`, write `<basename>.up.sql` + `.down.sql`,
  return absolute paths. Guards: non-empty basename, no `/` or `..`
  (path-traversal), both SQL bodies non-empty.
- Before write the CLI (M7) composes `at := time.Now().UTC()` and passes
  it in; tests inject a frozen time.
- `.gitignore` at repo root covers the whole `out/` tree (per D6).
- **Serves AC #5 (unique, sortable file names), AC #4 (byte-identical
  SQL content — filename freshness on re-run is intentional per D5 rev2).**

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

- Makefile target `test-apply`: spins up ephemeral `postgres:18-alpine` per
  `go.md` §Schema Migrations ("migration DB is purely temporary — never
  contains production or local data"), runs `psql -f` on the generated up,
  then down, then up again, confirming clean apply and clean rollback.
- **Serves AC #2, AC #3**.

### M10 — grand-tour fixture matrix (revised 2026-04-21)

- **Replaces the original "pilot project adoption" framing.** AC #7 was
  rewritten the same day; see `iteration-1.md` AC #7 rev 2026-04-21 for
  the "why" writeup. Short version: single-repo adoption only proves
  what that one repo happens to exercise, and iter-1 has no
  applied-state tracking to make a real pilot rigorous anyway. A
  combinatorial synthetic matrix is stronger for vocabulary
  adequacy — explicit, repo-local, survives refactors, checks into
  goldens *and* apply-roundtrip.
- Fixture set under `srcgo/domains/compiler/testdata/` — each new
  subdirectory is one `input.proto` + `expected.up.sql` +
  `expected.down.sql`, auto-picked up by the M8 runner
  (`goldens_test.go`) and the M9 harness (`make test-apply`) without
  any new wiring.
- Coverage axes (full list in `iteration-1.md` AC #7): every
  `(carrier, type)` cell of D2, every `AutoDefault`, every CHECK
  variant, three PK shapes, four index shapes, four FK shapes, seven
  table archetypes including PG dialect passthrough + custom_type
  escape hatch.
- Scope control: **no global cartesian product.** Each axis is covered
  at least once, plus a handful of targeted pairings the memory file
  flagged as interaction-risky (UUID PK + non-null string columns,
  DECIMAL + numeric range checks, PG custom_type + required_extensions,
  composite PK + FK-referencing-it, self-ref FK + orphanable). Estimate
  6–8 fixture directories.
- Expected outcomes per `iteration-1.md` AC #7 writeup: either the
  matrix ships green (iter-1 closes) or a fixture surfaces a gap and
  that gap either (a) gets a narrow IR / emitter fix in the same batch
  (per-fixture, small) or (b) becomes an iter-2 backlog entry with the
  fixture parked. Gaps are the **output** of this milestone, not a
  failure mode.
- Pre-M10 prep already shipped (commit `b955464`): `attachChecks`
  blank-synth gated on `semTypeStoresAsString`, `defaultRegexFor`
  dropped redundant UUID pattern — so fixtures are free to combine UUID
  PKs, DECIMAL columns, and non-nullable strings without tripping synth
  paths.
- **Serves AC #7 (revised), closes iteration-1.**

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
| 2 | Applies cleanly to PG 14+ | M4, M9 |
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

**Status (2026-04-22).** Skeleton + M1 + M1 rev2 + M1 rev3 + M2 + M2 rev2 + M3 + M4 + M5 + M6 + M7 + M8 + M9 + M10 + reliability polish + D11 raw-escape-hatches + D12 FK relocation / deletion_rule / bytes carrier + D13 preset lift (JSON / IP / TSEARCH; EMAIL / URL max_len defaults + override) + D14 zero-config per-carrier defaults + orthogonal DbType override axis + D15 collection carriers (map / repeated) + AUTO dispatch + element typing + D16 dialect-capability catalog + inspection interface + D17 ENUM type (carrier-dispatched storage: string → CREATE TYPE AS ENUM; int / proto-enum field → CHECK IN numbers) + D18 generated columns (GENERATED ALWAYS AS (expr) STORED on `(w17.db.column).generated_expr`; incompatible with default / pk / fk) complete; **iter-1 schema-gap close-out in progress (D17, D18 shipped; D19, D20 queued per `docs/SESSION-HANDOFF.md`).** See `iteration-2-backlog.md` for the next-iteration candidate list.
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

- **M5 (shipped, 2026-04-21) — sqlite stub emitter.**
  `srcgo/domains/compiler/emit/sqlite/emit.go` implements
  `emit.DialectEmitter`: `Name() == "sqlite"`, `EmitOp` returns
  `errors.New("sqlite emitter: not implemented in iteration-1")` for
  every op variant. The value proposition is compile-time: a second
  implementation forces the interface to stay dialect-agnostic (AC #6).
  Test carries a `var _ emit.DialectEmitter = sqlite.Emitter{}` ensuring
  the interface check survives refactors, plus runtime assertions that
  the stub returns an error marked with "not implemented in iteration-1"
  and produces no partial SQL, and that `emit.Emit` wraps the stub error
  with the dialect name (no silent swallowing).

- **M6 (shipped, 2026-04-21) — naming + writer.**
  `srcgo/domains/compiler/naming/name.go` is a two-function package:
  `Name(at time.Time) string` → compact UTC ISO-8601
  (`20260421T143015Z`) via `time.Format` on a fixed layout. Per D5
  rev2 there is no op dispatch, no slug, no sequence state — review
  happens in the platform UI (D6) and timestamps sidestep the
  cross-machine sequence drift that gitignored `out/` would otherwise
  cause. Tests pin the exact format, exercise a non-UTC input to prove
  the UTC normalisation, and regex-guard the fixed-width shape.
  `srcgo/domains/compiler/writer/writer.go` exposes
  `Write(dir, basename, up, down) (upPath, downPath, err)`: `os.MkdirAll`
  the dir (creates missing parents), write the two `.sql` files at
  0644, return absolute paths for CLI diagnostics. Guards reject empty
  basenames, path-traversal attempts (`/`, `..`), and empty SQL bodies.
  Tests cover happy path + TempDir layout, auto-create of missing
  parent chain, overwrite idempotency, traversal rejections, empty-body
  rejection, and AC #4 determinism across two writes.

- **M7 (shipped, 2026-04-21) — CLI + Application.**
  `srcgo/domains/compiler/application.go` declares the minimal
  `compiler.Application` interface (`OutputModule` + `Config()`);
  `config.go` adds `compiler.Config` with a single knob (`OutputDir`,
  `env:"COMPILER_OUTPUT_DIR"` default `./out`) parsed via
  `github.com/caarlos0/env/v11` per convention. `application/` ships
  the facade (`application.go`), functional-options wiring
  (`options.go`, `WithOutputModule` + `New` returning
  `(Application, io.Closer, error)`), and `module_output.go` —
  factory wrapper that lifts `cfg.OutputDir` into a resolved
  `OutputModule` (no third-party SDK, no port receivers, < 25 lines,
  per go.md §module_n.go). The binary is `wc`, built from
  `srcgo/domains/compiler/cmd/cli/` with `main.go` (kong root +
  `kongplete.Complete`) and `cmd_generate.go`. `GenerateCmd` wires
  `loader → ir.Build → plan.Diff → emit.Emit(postgres.Emitter{}) →
  naming.Name(time.Now().UTC()) → writer.Write` end-to-end; surface is
  `wc generate --iteration-1 [-o DIR] [-I DIR]… <proto>`. `--iteration-1`
  is kong-`required:""` so the output surface stays locked to the
  iteration-1 shape; `--out` overrides `cfg.OutputDir`; `-I / --import`
  is repeatable and is how users point at the `w17/*.proto` vocabulary
  (the input proto's directory is always added automatically). One
  polish touch on top of the spec: the CLI `os.Stat`s the proto path
  upfront so a typo reports "stat: no such file" instead of
  protocompile's misleading last-import-lookup cascade. `*diag.Error`
  surfaces round-trip through kong's `FatalIfErrorf` so
  `file:line:col` + `why:` + `fix:` lines land verbatim in the user's
  terminal. `make build` now actually compiles the binary
  (`cd srcgo && go build -o domains/compiler/bin/wc
  ./domains/compiler/cmd/cli`); `make test` likewise runs
  `cd srcgo && go test ./...`. Smoke-tested against
  `ir/testdata/happy.proto` — generated SQL matches the postgres
  emitter's own pipeline test (M4) byte-for-byte, filenames are
  `YYYYMMDDTHHMMSSZ.{up,down}.sql` per D5 rev2. Pilot-facing copy
  lives at `srcgo/domains/compiler/examples/iteration-1/happy.proto`
  — domain-local, not a repo-root `examples/`, because the compiler
  is a domain and keeping examples next to the code they exercise is
  the only layout that stays correct when later components add Go
  example functions or runnable demos. Duplicated rather than
  symlinked — `testdata/` is the test fixture, `examples/` is the
  user's entry point; the two rot at different speeds. The
  generator's `out/` directory lands next to whatever proto the user
  runs from, covered by the repo-root `.gitignore out/` pattern which
  matches at any depth.

- **M8 (shipped, 2026-04-21) — golden-file test suite.**
  `srcgo/domains/compiler/testdata/{product,no_indexes,multi_unique}/`
  — three single-table fixtures, each carrying `input.proto` plus
  expected `up.sql` / `down.sql` bytes. `product` exercises SLUG +
  URL regex, DATE with `CURRENT_DATE`, PERCENTAGE with author-supplied
  bounds, COUNTER, and an IDENTITY pk. `no_indexes` is the minimal
  case (PK + two plain columns, no unique / no storage-index / no
  table index / no FK) so the "empty CREATE INDEX block, empty DROP
  INDEX block" emitter path has a pin. `multi_unique` stacks two
  single-col `(w17.field).unique` synths (email, username) plus one
  named multi-col UNIQUE table-level index (tenant_id, handle) and
  proves reverse DROP INDEX order + derived-vs-explicit name mix. The
  runner lives at `srcgo/domains/compiler/goldens_test.go` as
  `package compiler_test` (external; depends only on the public
  surface of loader / ir / plan / emit / emit/postgres), auto-
  discovers subdirectories of `testdata/`, and runs one `t.Run`
  per case. Each subtest compiles the fixture all the way through
  the M7 pipeline in-memory, diffs against the expected files, and
  re-runs the pipeline once more to reassert AC #4 byte-determinism.
  `go test ./domains/compiler/ -update` rewrites the expected files
  from the current pipeline output (never touches `input.proto`,
  never creates new case directories). Verified: (a) three cases
  pass green; (b) mutating one golden surfaces a clear `--- got ---
  / --- want ---` diff; (c) `-update` restores the run cleanly.
  Serves AC #5.

  (A known gap surfaced while building `product` and documented here
  in the first M8 writeup — blank-check / UUID-regex synth on
  non-string SQL storage — was fixed as the first commit of the
  M10 prep batch. `attachChecks` now guards the blank synth on
  `semTypeStoresAsString`, and `defaultRegexFor` no longer emits the
  redundant UUID pattern. M10 grand-tour fixtures can freely combine
  UUID PKs, DECIMAL columns, and non-nullable strings without
  tripping the synth paths.)

- **M9 (shipped, 2026-04-21) — apply round-trip against real Postgres.**
  `make test-apply` boots one ephemeral `postgres:18-alpine` via
  `docker run --rm -d` (no host port publish — all traffic goes
  through `docker exec`, no port juggling, no collision with a local
  PG), polls `pg_isready -U postgres -q` on a 60s budget per
  `go.md` §Schema Migrations, then iterates
  `srcgo/domains/compiler/testdata/*/` and for each fixture:
  `CREATE DATABASE test_<name>`, then `psql -v ON_ERROR_STOP=1 -f`
  in up → down → up order, each piped over `docker exec -i`. A
  `trap EXIT` `docker kill`s the container on any exit path (success,
  error, SIGINT) — `--rm` handles removal. One DB per fixture, not
  one-DB-shared, so leftover state from a broken fixture can't mask
  the next one; each fixture starts from an empty schema. The
  up → down → up chain catches three distinct bugs: (a) up SQL that
  fails to apply (AC #2), (b) down SQL that leaves residue (re-up
  would error with `relation already exists`, AC #3), (c) down SQL
  that fails outright. Verified against `product`, `no_indexes`, and
  `multi_unique` — all three apply, roll back, and re-apply on PG 18
  without warnings or errors. AC #2's "PG 14+" floor is unchanged —
  PG 18 is a strict superset for the DDL we emit (no syntax added in
  our output path crosses a 14/15/16/17/18 version gate). Test-apply
  is **not** wired into `make test` because it requires Docker; CI
  composition (back-version matrix, parallel fixture runs, etc.) is
  an iteration-2 concern.
  Fixtures tested are the committed golden SQL, not fresh generator
  output — M8's golden test already guarantees the two are
  byte-identical, so the composition "M8 green + M9 green"
  transitively proves fresh generator output applies on PG. Serves
  AC #2 and AC #3.

- **M10 (shipped, 2026-04-21) — grand-tour fixture matrix closes iter-1.**
  Seven new fixture dirs under `srcgo/domains/compiler/testdata/`,
  auto-picked up by M8 (golden byte-match) and M9 (apply roundtrip)
  without any new wiring:
  - `uuid_pk` — UUID PK with UUID_V7, UUID_V4 on a second column, EMAIL
    type-implied regex, CHAR with max_len, TEXT with explicit
    min_len + max_len (both-bounds length CHECK).
  - `numeric_spectrum` — every numeric (carrier, type) cell of D2
    except COUNTER-int32 (spec-rejected), range CHECKs via gt/lt and
    gte/lte (the symmetric pair collapses to BETWEEN in SQL), DECIMAL
    with precision + scale.
  - `temporal_full` — DATE+CURRENT_DATE, TIME+CURRENT_TIME,
    DATETIME+NOW(), INTERVAL bare (no default_auto support).
  - `flags_enums_json` — bool+TRUE, bool+FALSE, TEXT+EMPTY_JSON_ARRAY,
    TEXT+EMPTY_JSON_OBJECT, Choices via proto enum FQN, pattern
    override (proves author regex wins over any type-implied one).
  - `pg_dialect` — all four curated flags (jsonb, inet, tsvector,
    hstore) + custom_type escape hatch (MACADDR — built-in, no
    extension required, validates the override path without pulling
    pgvector into test-apply).
  - `fks_parent_child` — FK with CASCADE-inferred (orphanable unset on
    NOT NULL), FK with SET NULL (orphanable:true + null:true), self-ref
    FK, INCLUDE covering index, storage-index synth co-existing with
    FK.
  - `m2m_join` — composite PK on a join table (two columns with
    pk:true → table-level `PRIMARY KEY (…)`), two inline FKs,
    exercises plan.Diff topological sort (lexical `product_tags` <
    `products` contradicts FK order; topo must win).

  Gaps discovered while building the matrix and fixed in this batch
  (narrow, in-situ):
  - `ir.build.attachChecks`: string-only synths (blank, length, regex,
    choices) now gate on `columnStoresAsString`, which combines the
    sem-type axis (UUID / DECIMAL → non-string storage) with the
    PG-passthrough axis (jsonb / inet / tsvector / hstore / custom_type
    redirect storage regardless of sem type). New helpers
    `columnStoresAsString` + `pgOverridesStorage`; pg.field block in
    `buildColumn` now runs BEFORE attachChecks so the synth layer can
    see the override.
  - `plan.Diff`: now topological by FK dependency, not lexical.
    Referenced tables come before referencers; self-FKs don't create
    ordering constraints (PG accepts inline self-REFERENCES); ties
    break lexically for AC #4 determinism. Multi-table FK cycles are
    rejected with a clear error (out of scope per iter-1.md "Not in
    scope"). New helper `topoSortByFK` + three new regression tests in
    `diff_test.go`.
  - `Makefile test-apply` now `CREATE EXTENSION IF NOT EXISTS hstore`
    in each per-fixture DB. hstore is a PG contrib module, built into
    the `postgres:18-alpine` image but needs activation — one
    per-database line, scoped to test-apply only (production users
    activate extensions themselves or via the parked platform).

  (Gaps initially parked here — DECIMAL + range, silent-drop of
  explicit string options under pg.field override, FK to composite-PK
  column, `1e+06` scientific notation — were **all closed** in the
  same-day reliability polish batch below.)

  **Serves AC #7 (revised), closes iteration-1.**

- **Reliability polish (shipped, 2026-04-21) — close silent-failure
  surfaces before opening iter-2.** Audit pass after M10 surfaced a
  handful of scenarios where `wc generate` would produce SQL that
  either (a) silently lost author intent, (b) PG would reject at
  apply with a cryptic error, or (c) was legitimately a spec/code
  mismatch. All six fixed in one batch; seven new error-class
  fixtures guard the regressions:
  - `(w17.pg.field)` storage override now rejects non-TEXT sem types
    and explicit string-only CHECK options (`min_len` / `max_len` /
    `pattern` / `choices` / `blank`). Forces authors to pair overrides
    with `type: TEXT` so there is exactly one source of truth for the
    SQL column shape.
  - FK target column must have single-col uniqueness (PK or UNIQUE
    index). Catches the composite-PK-member case at IR time instead
    of at PG apply with `no unique constraint matching given keys`.
  - Reserved Postgres keywords (category R, ~95 words) rejected as
    table or column names — previously emitted unquoted, which fails
    at apply with a syntax error that points at the SQL line instead
    of the proto source.
  - Identifiers > 63 bytes rejected (table names, column names,
    derived index names, derived CHECK constraint names). Closes the
    silent-truncation / pg_class-collision window.
  - `DECIMAL + gt/gte/lt/lte` now accepted. `iteration-1.md` D2
    always permitted it ("bounds carried via double, precision-limited
    by double's range"), but `numericOnly` guard rejected via the
    string carrier. Widened to `numericForRange` that accepts
    (`CARRIER_STRING` + `SEM_DECIMAL`).
  - Index name resolution moved from the emitter into `ir.Build` so
    collision detection (explicit name vs. synth'd `<table>_<cols>_uidx`)
    is possible at IR time. Emitter now just reads `idx.Name`.
  - `emit.Emit` wraps up / down in `BEGIN; … COMMIT;` — all-or-nothing
    migrations (PG transactional DDL). See `iteration-1.md`
    "Apply requirements".
  - Cosmetic: `fmtDouble` uses `'f'` + precision 0 for
    integer-valued doubles (`1000000` instead of `1e+06`); applies to
    both range CHECKs and literal double defaults.

  Side-effects on existing fixtures: `ir/testdata/happy.proto`'s
  `metadata` column dropped `blank: true, max_len: 4000` — those
  explicit string-only options were silently dropped under the
  `jsonb: true` override and now error. `numeric_spectrum`'s
  `exact_amount` picked up `gte: 0, lte: 1000000000` as the positive
  test for the widened range validation.

  All ten grand-tour fixtures stay green on M8 goldens (regenerated
  for the `BEGIN; / COMMIT;` wrap + `1e+06 → 1000000`) and M9
  `make test-apply` against `postgres:18-alpine`. TestBuildErrors
  grew by seven cases — the full expected `file:`, `why:`, `fix:`
  substring set now runs on every new rejection path.

  **Why do this now, rather than in iter-2.** The iter-2 backlog
  dwarfs these fixes (alter-diff alone is many commits), and every
  bug here is in the IR / emit layer that alter-diff will extend.
  Leaving silent failures in the generator while stacking alter-diff
  on top would confound any iter-2 bug hunt. Close the reliability
  window first, then build forward.

- **D11 raw-escape-hatches (shipped, 2026-04-21) — close Django-parity
  gaps the curated vocabulary can't reach.** Parity audit surfaced
  three material gaps that blocked realistic schemas: cross-column
  CHECK constraints (Django's `CheckConstraint` with multi-col `Q()`),
  partial / expression / non-btree indexes (Django's `GinIndex`,
  `Index(..., condition=Q(...))`, `Index(..., expressions=[F(...)])`),
  and operator-class indexes (e.g. `gin_trgm_ops`). The same fixture
  set already shipped a `tsvector` column that was effectively
  useless — no way to build a GIN index on it.

  `(w17.db.table)` grew two opaque-SQL escape hatches matching the
  `(w17.pg.field).custom_type` shape:
  - `raw_checks: [{ name, expr }]` — `CONSTRAINT <name> CHECK (<expr>)`
  - `raw_indexes: [{ name, unique, body }]` — `CREATE [UNIQUE] INDEX
    <name> ON <table> <body>`

  Name validation goes through the full identifier pipeline
  (NAMEDATALEN, reserved PG keywords, collision across derived /
  explicit / raw names). Body / expr are opaque — author owns SQL
  syntax and apply-time correctness, same contract as `custom_type`.
  Design rationale + future "graduate to structured messages" path
  recorded as D11 in `iteration-1.md`.

  IR additions: `irpb.Table.RawChecks` + `irpb.Table.RawIndexes` +
  two new messages in `ir.proto`. Emitter additions: raw CHECKs
  render inline with derived CHECKs in declaration order; raw
  indexes render as separate `CREATE [UNIQUE] INDEX` statements after
  structured indexes, participate in the down-block reverse-drop.

  Fixture updates:
  - `pg_dialect` grows two GIN indexes (on `tsvector` + `jsonb`) —
    previously the columns had no way to be queryable.
  - New `raw_checks_and_indexes` fixture exercises cross-column CHECK
    (`start_at <= end_at`), function-call CHECK
    (`(price * 100) = floor(price * 100)`), partial UNIQUE index
    (`(email) WHERE deleted_at IS NULL`), and expression index
    (`(lower(customer_name))`).

  Four new error-class fixtures guard the validation surface:
  `raw_check_empty_name`, `raw_check_collides_with_derived`,
  `raw_index_empty_body`, `raw_index_collides_with_synth`.

  All 11 grand-tour fixtures (3 original + 7 M10 + 1 new) stay green
  on M8 goldens and M9 `make test-apply` against `postgres:18-alpine`.

- **D12 FK relocation + deletion_rule + bytes (shipped, 2026-04-21)
  — final Django-parity close-out for iter-1.** Three coordinated
  vocabulary changes:

  - `fk` moves from `(w17.field)` to `(w17.db.column)`. FKs are
    DB-engine rules (same family as `index`, `raw_indexes`,
    `raw_checks`), not data-shape semantics that a form builder or
    API validator would interpret. `(w17.field)` is now the
    authoring-surface layer ANY consumer can read (types, nullability,
    validators, defaults); `(w17.db.column)` is the migration-generator
    layer.
  - `orphanable: optional bool` is replaced by `deletion_rule: enum`
    on `(w17.db.column)`, extended to the full palette:
    `CASCADE / ORPHAN / BLOCK / RESET`. `ORPHAN` preserves the
    property-shape idiom as an enum variant; `BLOCK` (SQL RESTRICT)
    and `RESET` (SQL SET DEFAULT) close the real Django-parity gaps
    the audit surfaced. Naming stays non-hook (no `on_*` prefix).
    IR inference keeps the old default: unspecified rule →
    `null:true` maps to ORPHAN, else CASCADE.
  - `bytes` carrier lands. Maps to `BYTEA` on Postgres; like `bool`,
    carries no `type:` refinement (single-channel storage). `(w17.field)`
    is optional on bytes columns the same way it is on bools.

  IR changes: `irpb.Carrier` gains `CARRIER_BYTES`; `irpb.FKAction`
  gains `FK_ACTION_RESTRICT` + `FK_ACTION_SET_DEFAULT`. PG emitter
  gains `BYTEA` mapping + `RESTRICT` / `SET DEFAULT` on-delete clauses.
  `resolveFKAction` helper converts `(w17.db.column).deletion_rule`
  + (w17.field).null inference into the concrete FKAction, rejecting
  `ORPHAN` without null and `RESET` without default_*.

  Two new positive fixtures, one error fixture renamed + one added:

  - `testdata/bytes_column/` — bare `bytes` + `bytes [(w17.field) = { null: true }]`
    → `BYTEA NOT NULL` / `BYTEA NULL`.
  - `testdata/shared_pk_one_to_one/` — UserProfile + AdminExtra where
    the child's `profile_id` is both PK and FK. The only Django
    multi-table-inheritance shape that survives the "no schema
    inheritance" constraint; works with existing vocabulary, this
    fixture pins the pattern as a regression guard.
  - `fks_parent_child` extended with `auditor_id` (deletion_rule:
    BLOCK) and `fulfilled_by_id` (deletion_rule: RESET + default_int)
    so every variant of the enum ends up in at least one apply
    roundtrip.
  - `orphanable_requires_null.proto` → renamed to
    `orphan_requires_null.proto`; message + fix updated for
    `deletion_rule: ORPHAN`.
  - `reset_requires_default.proto` — new error fixture for
    `deletion_rule: RESET` without `default_*`.

  Every fixture that used `fk` on `(w17.field)` migrated:
  `happy.proto`, `vocab_fixture.proto`, `fks_parent_child`, `m2m_join`,
  `examples/iteration-1/happy.proto`, two existing FK-error fixtures.

  All 13 grand-tour fixtures (3 original + 7 M10 + 1 D11 + 2 D12
  new) green on M8 goldens + M9 `make test-apply` against
  `postgres:18-alpine`.

- **D17 ENUM type (shipped, 2026-04-22) — schema-gap close-out,
  first of four before alter-diff.** `(w17.field).Type = ENUM` is
  a carrier-dispatched preset with three author paths — bare
  proto-enum field (auto-inferred), `string + ENUM + choices:`
  (PG ENUM storage), and `int + ENUM + choices:` (CHECK IN
  numbers). Full rationale + invariants + escape hatches live in
  `iteration-1.md` D17.

  Proto changes: `w17.Type` gains `ENUM = 50`; `ir.SemType` gains
  `SEM_ENUM = 50`; `ir.Column` gains `enum_fqn` / `enum_names` /
  `enum_numbers`; `ir.ChoicesCheck` gains `repeated int64 numbers`
  (exclusive with the existing `values` list, emitter dispatches
  on whichever is populated).

  IR changes: `protoKindToScalarCarrier` routes `EnumKind` →
  `CARRIER_INT32` so proto-enum fields get a real carrier.
  `buildColumn` auto-infers `SEM_ENUM` on scalar proto-enum
  fields and resolves enum side-data via the new
  `resolveEnumColumn` helper (mirrors the `choices:` resolver but
  returns both names and numbers). SEM_ENUM joins the non-
  stringStorage list so blank / length / regex / choices-string
  synths skip on string+ENUM columns (the PG ENUM type enforces
  membership). Int + SEM_ENUM synths a `ChoicesCheck{numbers:…}`.
  `<table>_<col>` identifier validation for the derived PG ENUM
  type name runs alongside the existing CHECK-constraint name
  length checks.

  PG emitter: `columnType` takes a table name (threaded from
  `renderColumn`) and emits the derived type identifier for
  string+SEM_ENUM. `emitAddTable` prepends `CREATE TYPE
  <table>_<col> AS ENUM (names…)` statements before `CREATE
  TABLE` and appends `DROP TYPE IF EXISTS <table>_<col>;` after
  `DROP TABLE` on the down path. `renderChoices` dispatches on
  `ChoicesCheck.Numbers` vs `.Values`.

  Capability catalog: `CapEnumType = "ENUM_TYPE"` in
  `emit/capabilities.go`, `{MinVersion: "8.3"}` in the PG
  catalog (CREATE TYPE AS ENUM landed in PG 8.3).

  Two positive fixtures land in `testdata/`:
  `enum_string_backed` (string + ENUM + PG CREATE TYPE) and
  `enum_int_backed` (bare proto-enum field + explicit
  `int64 + ENUM + choices:` in the same table). Two new error
  fixtures — `enum_requires_choices.proto` and
  `enum_on_bool_carrier.proto` — join `TestBuildErrors`. All 15
  grand-tour fixtures (13 previous + 2 D17 new) green on M8
  goldens + M9 `make test-apply` against `postgres:18-alpine`.

  **Open-question resolution.** Auto-infer (option b from the
  handoff) ships as the chosen default for bare proto-enum
  fields: matches D14 zero-config philosophy + proto wire
  semantics. Authors opt out via explicit `type: NUMBER` or
  move to string+ENUM for PG-native storage.

- **D18 generated columns (shipped, 2026-04-22) — schema-gap
  close-out, second of four before alter-diff.** `(w17.db.column)`
  gains a `generated_expr: string` opaque-SQL field. When set, the
  column emits as `GENERATED ALWAYS AS (<expr>) STORED`. Full
  rationale + invariants + escape hatches live in `iteration-1.md`
  D18.

  Proto changes: `w17.db.Column` gains `generated_expr = 6`;
  `ir.Column` gains `generated_expr = 38` (pass-through IR field,
  treated as opaque like `RawCheck.expr` / `RawIndex.body`).

  IR changes: `buildColumn` reads
  `lf.Column.GetGeneratedExpr()` alongside the existing
  `.name` / `.index` plumbing. Validation rejects three
  combinations with `diag.Error` (`file:` / `why:` / `fix:`): (1)
  any `(w17.field).default_*` variant on a generated column — PG
  rejects DEFAULT on `GENERATED ALWAYS AS` columns; (2) `pk: true`
  — PG rejects STORED generated columns as primary keys; (3) any
  `(w17.db.column).fk` on the same column — the FK contract can't
  act on a column the author doesn't own. `unique`, `null`, and
  CHECK synths (blank / length / regex / range / choices / raw)
  remain allowed; CHECKs apply to the computed value, a useful
  feature for enforcing invariants on derived data.

  PG emitter: `renderColumn` adds a third mutually-exclusive
  value-source branch after IDENTITY / DEFAULT —
  `GENERATED ALWAYS AS (<expr>) STORED` appended after nullability.
  IR enforces the combination is exclusive so the else-if chain is
  sound. No changes to the down path: generated columns drop with
  the table (same as any other column).

  Capability catalog: `CapGeneratedColumn = "GENERATED_COLUMN"`
  in `emit/capabilities.go`, `{MinVersion: "12.0"}` in the PG
  catalog (SQL:2016 feature landed in PostgreSQL 12;
  STORED-only on PG — VIRTUAL parks for multi-dialect in iter-2).

  One positive fixture lands in `testdata/`: `generated_stored`
  exercises the full `full_name = first_name || ' ' || last_name`
  shape on a User with identity PK + CHAR columns + storage-index
  sugar on the generated column. Three new error fixtures —
  `generated_with_default.proto`, `generated_with_pk.proto`,
  `generated_with_fk.proto` — join `TestBuildErrors`, each
  asserting the corresponding `file:` / `why:` / `fix:`
  substrings. All 16 grand-tour fixtures (15 previous + 1 D18 new)
  green on M8 goldens + M9 `make test-apply` against
  `postgres:18-alpine`.

  **Design note.** VIRTUAL generated columns deliberately parked.
  PG 18 supports STORED only; MySQL + SQLite offer VIRTUAL.
  Shipping STORED today keeps the compiler honest about PG's
  actual surface. The multi-dialect expansion (iter-2+) can add a
  `virtual:` sibling flag on `(w17.db.column)` for emitters that
  support it; until then, `raw_indexes` covers the
  expression-index shape that's the most common VIRTUAL substitute.

**Next:** iteration-2 planning. The backlog (alter-diff, multi-file
schemas, platform, deploy client, MySQL / SQLite-as-production
emitters, `wc lint` / `diff` / `viz` / `changelog`, projections,
`immutable` runtime enforcement, CHECK-verbosity flag, structured
message shapes for the common raw-index patterns — GinIndex /
PartialIndex / ExpressionIndex — D11 writeup explains the
graduation path, COMMENT ON from proto doc strings alongside
admin/UI generation) sits cleanly on top of a reliability-sealed
iter-1 with Django-parity vocabulary closed. No known silent-failure
scenarios remain in the core pipeline; `(w17.field)` and
`(w17.db.column)` form a coherent two-layer surface that matches
the "data semantic vs DB-engine rule" split.
