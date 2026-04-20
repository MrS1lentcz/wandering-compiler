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
│   │   ├── db.proto                     # (w17.db.table)
│   │   ├── field.proto                  # (w17.field)
│   │   └── validate.proto               # (w17.validate)
│   └── domains/
│       └── compiler/
│           ├── types/                   # (iteration-1: empty; grows when compiler exposes gRPC types)
│           └── services/                # (iteration-1: empty; later: service_compile.proto)
│
├── srcgo/
│   ├── go.mod                           # single go.mod for the monorepo
│   ├── errors.go                        # package srcgo — shared errors
│   ├── lib/                             # (iteration-1: empty)
│   ├── x/                               # (iteration-1: empty)
│   ├── pb/                              # generated from proto/ — gitignored, regenerated via `make schemagen`
│   │   └── w17/                         # compiled w17 options
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
│           ├── loader/                  # domain-local — parse .proto via bufbuild/protocompile, decode w17 options
│           │   ├── loader.go
│           │   └── options.go
│           ├── ir/                      # domain-local — dialect-agnostic IR (D4)
│           │   ├── schema.go            # Schema, Table, Column, Index, ForeignKey
│           │   ├── checks.go            # Check tagged union: Length/Blank/Range/Regex
│           │   ├── types.go             # ProtoCarrier, SemanticType enums
│           │   └── build.go             # loader output → IR
│           ├── plan/                    # domain-local — differ (D4)
│           │   ├── plan.go              # MigrationPlan, Op interface
│           │   ├── ops.go               # AddTable (iteration-1 only)
│           │   └── diff.go              # Diff(prev, curr *ir.Schema) *MigrationPlan
│           ├── emit/                    # domain-local — per-dialect SQL emitters (D4)
│           │   ├── dialect.go           # DialectEmitter interface
│           │   ├── postgres/
│           │   │   └── emit.go
│           │   └── sqlite/
│           │       └── emit.go          # stub, errors "not implemented in iteration-1" — AC #6
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
| `srcgo/domains/compiler/loader` | `*.proto` paths | parsed descriptors + decoded w17 option values keyed by message / field | Uses [`github.com/bufbuild/protocompile`](https://github.com/bufbuild/protocompile) — no shelling out to `protoc`. |
| `.../compiler/ir` | loader output | `*ir.Schema` | Validates invariants (every field has `type`, `CHAR`/`SLUG` have `max_len`, FKs target exists, etc.). Invariant violations become loader errors with file:line. |
| `.../compiler/plan` | two `*ir.Schema` (prev, curr) | `*plan.MigrationPlan` | Iteration-1: prev is always nil; output is one `AddTable` per table. |
| `.../compiler/emit` | `*plan.MigrationPlan` + `DialectEmitter` | up SQL + down SQL strings | `DialectEmitter` is the interface; `postgres.Emitter` is the only real impl, `sqlite.Emitter` is the stub from AC #6. |
| `.../compiler/naming` | `[]plan.Op` + sequence | migration basename like `0001_create_products` | Sequence source for iteration-1 is the count of existing files in `out/migrations/`; the platform (later) will own sequencing server-side. |
| `.../compiler/writer` | basename + up/down SQL | two files in `out/migrations/` | Only responsibility: write bytes. |
| `.../compiler/application` | Config + options | `compiler.Application` (facade) | Constructed at startup by `cmd/cli/main.go`. Iteration-1 has essentially one module (output writer factory); more modules appear when gRPC / platform integration lands. |
| `.../compiler/cmd/cli` | CLI flags + input path | exit code | Wires loader → builder → diff → emit → name → writer via `Application`. No business logic. |

## Build order (milestones)

Each milestone is independently testable. Ship them in order; do not skip.

### M1 — w17 option schemas compile

- Write `proto/w17/{db,field,validate}.proto` against the vocabulary in
  `iteration-1.md` "In scope".
- `make schemagen` produces `srcgo/pb/w17/*_pb.go`.
- Hand-written test: a tiny `.proto` file that imports our options and sets
  one of each, loaded via `protocompile` in a Go test that pulls the option
  values out. Proves the proto vocabulary is well-formed.
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

**Status (2026-04-20).** Skeleton + M1 complete.
- Skeleton: `srcgo/go.mod` (Go 1.26), `Makefile` placeholders, `.gitignore`.
- M1: `proto/w17/{db,field,validate}.proto` authored; `make schemagen` emits
  `srcgo/pb/w17/{field,validate}.pb.go` and `srcgo/pb/w17/db/db.pb.go`
  (gitignored). `TestW17VocabularyCompiles` loads a fixture via
  `bufbuild/protocompile` and reads every option value through the generated
  extensions — green.
- Extension layout split: `proto/w17/db.proto` is proto package `w17.db` →
  Go package `dbpb` (subdir); `field.proto` and `validate.proto` share proto
  package `w17` → Go package `w17pb`. Flat `proto/w17/` authoring layout is
  preserved; split only affects Go output.

**Next:** M2 — loader + IR builder.
