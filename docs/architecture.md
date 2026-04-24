# Architecture — layers, APIs, boundaries

This document is the black-and-white architectural contract for
`srcgo/domains/compiler/`. It describes:

1. The layers (loader → IR → classifier → plan.Diff → engine →
   emit → sink), what each owns, and where boundaries are drawn.
2. Each package's public surface — what it exports, what it
   consumes, who's allowed to import it.
3. The data-flow pipeline end-to-end from `.proto` input to
   `.sql` + `manifest.json` output.
4. The purity / adapter split per D30: engine is pure, adapters
   (filesystem, CLI, future platform) live outside.

Pair with:
- [`tech-spec.md`](tech-spec.md) — high-level product spec.
- [`conventions-global/`](conventions-global/) — shared
  convention docs every layer respects.
- [`iteration-2.md`](iteration-2.md) — per-decision records
  (D1–D34) for architectural choices.

---

## One-screen overview

```
  .proto files                             docs/classification/
        |                                         |
        v                                         v
    loader.Load                           classifier.Load
        |                                         |
   LoadedFile                                 Classifier
        |                                         |
        +------------+------------+---------------+
                     v
                 ir.Build(Many)  ──►  *irpb.Schema   (pure IR)
                     |
                     v
                 plan.Diff(prev, curr, cls)  ──►  DiffResult
                     |                                  {Plan, Findings}
                     v
         engine.Plan(prev, curr, cls, resolutions, emitterFor)
                     |
            ┌────────┼────────────────────────┐
            v        v                        v
    inject Ops for  emit.Emit(emitter, plan)  collectChecks
    resolved         |                        (NEEDS_CONFIRM
    findings         v                         check.sql per
    (D33)        dialect emitter               FactChange)
                 {up, down, usage}
                     |
                     v
                *planpb.Plan  ──►  Sink.Write(plan)
                                      |
                                      +── FilesystemSink (today)
                                      +── MemorySink (tests)
                                      +── PlatformSink (D29 future)
```

**Key invariants:**
- Everything to the left of `engine.Plan` produces pure data.
- `engine.Plan` is a pure function of (prev, curr, classifier,
  resolutions). No file I/O, no waiting.
- Adapters (`ResolutionSource`, `Sink`, `decide`) live OUTSIDE
  the engine package tree (per D30 / D34 placement rule).
- Every dialect belongs to exactly one category (RELATIONAL /
  KEY_VALUE / MESSAGE_BROKER). One domain = at most one
  connection per category (D34).

---

## Package catalogue

### `loader/` — proto → descriptors

**Purpose.** Parse `.proto` source into fully-resolved
descriptors with `w17`-extension annotations extracted into
Go-native structs.

**Public API.**
```go
func Load(ctx, path string, importPaths []string) (*LoadedFile, error)
func LoadMany(ctx, paths []string, importPaths []string) ([]*LoadedFile, error)

type LoadedFile struct {
    File     protoreflect.FileDescriptor
    Module   *dbpb.Module          // (w17.db.module)
    Messages []*LoadedMessage
}
type LoadedMessage struct {
    Descriptor protoreflect.MessageDescriptor
    Table      *dbpb.Table          // (w17.db.table)
    Fields     []*LoadedField
}
type LoadedField struct {
    Field    protoreflect.FieldDescriptor
    W17Field *w17pb.Field           // (w17.field)
    DbColumn *dbpb.Column           // (w17.db.column)
    PgField  *pgpb.PgField          // (w17.pg.field)
}
```

**Consumers.** `ir/` (builds IR from LoadedFile).

**Non-consumers.** Nobody else imports `loader`.

**Purity.** Reads files via `protocompile`; no IR mutation.

---

### `ir/` — Schema IR builder

**Purpose.** Validate + fold LoadedFiles into a canonical
`*irpb.Schema` — the pipeline's authoritative shape after all
proto-vocabulary resolution. Enforces every design decision
(D2 carrier/sem matrix, D14 db_type precedence, D17 ENUM types,
D19 namespace, D22 presets, D26 multi-connection, D34 dialect
categories).

**Public API.**
```go
func Build(lf *loader.LoadedFile) (*irpb.Schema, error)
func BuildMany(files []*loader.LoadedFile) (*irpb.Schema, error)

type Category int
const (
    CategoryUnspecified Category = iota
    CategoryRelational
    CategoryKeyValue
    CategoryMessageBroker
)
func DialectCategory(d irpb.Dialect) Category
```

**Consumers.** `plan/`, tests, `cmd/cli/`.

**Purity.** Pure function of `LoadedFile` → `Schema`. Errors
via `errors.Join` so one build surfaces every problem.

**Invariants enforced.**
- D2 / D14: every (Carrier, SemType) pair is either valid or
  produces a diag.Error with why/fix.
- D10: column identity = proto field number (rename is free).
- D24: table identity = `MessageFqn` (proto-level, survives
  `(w17.db.table).name` changes).
- D26: `(dialect, version)` unique per domain; duplicate
  rejected with connection-name conflict diag.
- D34: at most one connection per DialectCategory per domain;
  two RELATIONAL dialects rejected at build time.

---

### `classifier/` — D28 matrix index

**Purpose.** Load-once, query-many index over
`docs/classification/*.yaml`. Dispatches
(axis, from, to) → `Cell{Strategy, Using, CheckSQL,
Rationale}`. Source of truth for migration-safety decisions.

**Public API.**
```go
func Load(dir string) (*Classifier, error)

type Classifier struct { /* private */ }
func (c *Classifier) Carrier(from, to irpb.Carrier) Cell
func (c *Classifier) DbType(carrier irpb.Carrier, from, to irpb.DbType) Cell
func (c *Classifier) Constraint(axis, caseID string) Cell
func (c *Classifier) Fold(cells ...Cell) Cell
func (c *Classifier) Rank(s planpb.Strategy) int32

// Iterators (used by e2e matrix runner):
func (c *Classifier) AllCarrierCells() []CarrierEntry
func (c *Classifier) AllDbTypeCells() []DbTypeEntry
func (c *Classifier) AllConstraintCells() []ConstraintEntry

type Cell struct {
    Strategy  planpb.Strategy
    Using     string
    CheckSQL  string
    Rationale string
}
```

**Consumers.** `plan/` (for Finding emission), `engine/`
(for USING template rendering + check.sql).

**Purity.** Immutable after Load. Safe for concurrent use.

**Authoritative source.** YAML always wins over any markdown
table in `iteration-2.md`. Generated cells (missing YAML entry)
synthesise CUSTOM_MIGRATION per D28 governing rule.

---

### `plan/` — Differ

**Purpose.** Compute the structural `*planpb.MigrationPlan`
and `[]*planpb.ReviewFinding` from (prev, curr) IR snapshots.
Core of the alter-diff.

**Public API.**
```go
func Diff(prev, curr *irpb.Schema, cls *classifier.Classifier) (*DiffResult, error)

type DiffResult struct {
    Plan     *planpb.MigrationPlan    // Ops the differ could produce directly
    Findings []*planpb.ReviewFinding  // decision-required axes (carrier, pk, ...)
}
```

**Consumers.** `engine/` (via `engine.Plan`).

**Purity.** Pure function. When `cls` is non-nil, decision-
required axes produce Findings; when nil, they produce errors
(pre-D30 behaviour preserved for low-level tests).

**What it does NOT do.**
- Never emits SQL strings — that's emitter responsibility.
- Never reads files.
- Never renders strategy templates — that's engine's job (D33).

---

### `emit/` — Per-dialect SQL emission

**Purpose.** Render `*planpb.MigrationPlan` to up + down SQL,
dialect-specific. Records capability usage (D16) via
`*Usage` collector threaded through every dispatch site.

**Public API.**
```go
type DialectEmitter interface {
    Name() string
    EmitOp(op *planpb.Op, usage *Usage) (up, down string, err error)
}
type DialectCapabilities interface {
    Name() string
    Requirement(cap string) (Requirement, bool)
}
type Transactional interface {
    Transactional() bool  // optional; non-SQL dialects opt-out
}

type Usage struct { /* private */ }
func (u *Usage) Use(cap string)
func (u *Usage) Sorted() []string

func Emit(e DialectEmitter, plan *planpb.MigrationPlan) (up, down string, usage *Usage, err error)
```

**Subpackages.**
- `emit/postgres/` — production emitter. Implements
  DialectEmitter + DialectCapabilities (compile-time assertion
  in capabilities.go). Covers every Op variant plus the D33
  TypeChange for cross-carrier injection.
- `emit/sqlite/` — stub. EmitOp returns "not implemented";
  DialectCapabilities has empty catalog. Exists to keep the
  interface dialect-agnostic.
- `emit/redis/` — KV stub. EmitOp emits `# wc:` comments + a
  SCAN/UNLINK Lua for DropTable. Opts out of transactional
  wrapper; does NOT implement DialectCapabilities (KV has no
  catalog surface).

**Contract.**
- SQL dialects MUST implement DialectCapabilities (PG, SQLite,
  future MySQL). Compile-time `var _ DialectCapabilities =
  Emitter{}` enforces.
- Non-SQL dialects (Redis, future KV) may opt out.
  `engine.buildManifest` handles the missing impl gracefully.
- `Transactional` is opt-out only (Redis returns false).

**Purity.** Stateless. Emitter is typically a zero-value
struct; the `Usage` collector is the only per-run state.

---

### `engine/` — Pure planning orchestrator (D30)

**Purpose.** Compose classifier + plan.Diff + emit into a
pure function of `(prev, curr, classifier, resolutions)` →
`*planpb.Plan`. The compiler's entrypoint for anyone who
wants a finished plan without touching files.

**Public API.**
```go
type EmitterFor func(*irpb.Connection) (emit.DialectEmitter, error)

func Plan(
    prev, curr *irpb.Schema,
    cls *classifier.Classifier,
    resolutions []*planpb.Resolution,
    emitterFor EmitterFor,
) (*planpb.Plan, error)

// Adapter interfaces (consumed, not implemented, here):
type ResolutionSource interface {
    Lookup(findingID string) (*planpb.Resolution, bool)
    All() []*planpb.Resolution
}
type Sink interface {
    Write(plan *planpb.Plan) error
}
```

**Subpackages.**
- `engine/memory/` — `MemorySource` + `MemorySink` for tests.
- `engine/filesystem/` — `FilesystemSink` writes up/down.sql +
  check.sql + manifest.json under a directory tree.

**Non-subpackage adapters** (per D30 placement rule — adapters
live as siblings of engine, not underneath):
- `decide/` — `--decide` flag parser → `[]*planpb.Resolution`.
  Separate package because it reads files (`DefaultSQLLoader`)
  and engine mustn't depend on file I/O.

**Contract.**
- Pure function. Same inputs → byte-identical output (idempotent
  across runs).
- No file I/O, no global state, no waiting on user.
- Every strategy resolution for cross-carrier findings either:
  (a) injects Ops via D33 template rendering (SAFE /
  LOSSLESS_USING / NEEDS_CONFIRM / DROP_AND_CREATE), or
  (b) splices CUSTOM_MIGRATION SQL as a string.

---

### `writer/` — Filesystem writer helper

**Purpose.** Thin wrapper around `os.WriteFile` that writes
`<basename>.up.sql` + `<basename>.down.sql` with a single
call + integrity hash. Used by `FilesystemSink`.

**Public API.**
```go
func Write(dir, basename, up, down string) (upHash, downHash string, err error)
```

**Consumers.** `engine/filesystem/` only.

**Purity.** File I/O, by definition. Kept small so the bulk of
engine/filesystem stays focused on Sink orchestration.

---

### `naming/` — Timestamp → migration filename

**Purpose.** `Name(time)` → canonical `YYYYMMDDTHHMMSSZ`
migration basename. Used by CLI to stamp generated migrations.

**Public API.** `func Name(t time.Time) string`.

---

### `diag/` — User-facing error shape

**Purpose.** `diag.Error` carries `file:line:col` + `why:` +
`fix:` fields. Every user-facing error flows through here so
the message shape is consistent.

**Public API.**
```go
func Atf(file protoreflect.FileDescriptor, format string, args ...any) *Error
func (e *Error) WithWhy(why string) *Error
func (e *Error) WithFix(fix string) *Error
// Error.Error() renders the multi-line format.
```

**Consumers.** `ir/` (build-time errors), `engine/`, `plan/`.

---

### `decide/` — --decide flag parser

**Purpose.** Parse CLI `--decide <key>=<strategy>` /
`--decide <key>=custom:<path>` into `[]*planpb.Resolution`
keyed against Findings emitted by `engine.Plan`.

**Public API.**
```go
func Parse(flags []string, loader func(path string) (string, error)) (*Decisions, error)
func DefaultSQLLoader(path string) (string, error)

type Decisions struct { /* private */ }
func (d *Decisions) ResolveAll(findings []*planpb.ReviewFinding) []*planpb.Resolution
func (d *Decisions) Unresolved(findings []*planpb.ReviewFinding) []*planpb.ReviewFinding
```

**Consumers.** `cmd/cli/cmd_generate.go` only.

**Placement note.** Sibling of `engine/`, NOT under it —
`decide` reads files (DefaultSQLLoader), and D30 says engine
is pure. Moving this under `engine/cli/` was an earlier
mistake that got corrected (commit `7be9c9a`).

---

### `cmd/cli/` — Kong-based CLI entrypoint

**Purpose.** Wire flags → loader → ir.BuildMany → engine.Plan
→ FilesystemSink.Write. Per
`conventions-global/go.md` §CLI, uses kong + per-command
`cmd_<n>.go` files.

**Public API.** None — kong dispatches `main` → subcommand
`Run()` methods.

**Consumers.** End users via `wc` binary.

---

### `application/` — DI facade

**Purpose.** Process-lifecycle-scoped state (config,
output directory). Empty today; kept per
`conventions-global/go.md` §application so future platform
client / build cache wire in without reshape.

**Public API.** `application.New(...)` + `Application` interface
for config + close lifecycle.

---

### `e2e/` — Classifier-matrix test harness

**Purpose.** Iterate every cell in
`docs/classification/*.yaml` against real PG containers. Build
tag `e2e`; doesn't run on default `go test`.

**Public API.** None — tests only. Invoke:
```bash
go test -tags=e2e ./domains/compiler/e2e/
PG_VERSIONS="14 15 16 17 18" go test -tags=e2e -timeout 30m ./...
```

**See** `docs/e2e-matrix-coverage.md` for the skip-triage map.

---

## Boundary rules

### BR-1 — Engine is pure (D30)

Engine package (and everything it imports from `plan`,
`classifier`, `emit`, `ir`, `diag`, `naming`, `writer`) MUST
NOT perform file I/O, HTTP, or block on user input. If a
feature needs one of those, it lives in an adapter.

### BR-2 — Adapters sit as siblings of `engine/` (D30 placement)

`decide/`, `engine/filesystem/`, `engine/memory/` — names
differ based on whether the adapter is a Sink/Source subtype
(lives under `engine/` as its impl) or an independent
boundary crosser (`decide/` parses CLI flags, not an engine
type — lives as sibling).

Rule: if the adapter **implements** an interface defined in
`engine/`, it lives under `engine/`. If it produces **inputs**
to engine.Plan (Resolutions), it lives as a sibling.

### BR-3 — YAML wins over markdown

`docs/classification/*.yaml` is authoritative. The markdown
tables in `iteration-2.md` are rendering aids that WILL
drift; never copy from markdown to code.

### BR-4 — One dialect per category per domain (D34)

Two RELATIONAL dialects in one domain is rejected at
`ir.BuildMany` time. Cross-category is fine (PG + Redis).
Same-category is multi-domain architecture.

### BR-5 — Proto is the wire format

Every inter-package data shape — IR, MigrationPlan, Findings,
Resolutions, Manifest — is a proto message. Go-native types
only for:
- Internal state (classifier's private keys).
- Purely process-local things (diag.Error's fluent builder).

Rationale: downstream tooling (back-compat lint, visual diff,
platform API) consumes the wire format without speaking Go.

### BR-6 — Field numbers are identity (D10)

For alter-diff: column = proto field number; table = proto
MessageFqn. Rename is free (update `name`, keep number / fqn).

### BR-7 — Per-dialect authoring pass-through (D32)

Each dialect gets its own `proto/w17/<dialect>/field.proto` +
IR slot + per-dialect emit reader. No generalised
`variants[]` map. MySQL's future `(w17.mysql.field)` slots
parallel to PG's `(w17.pg.field)`.

---

## Data-flow contracts

### Input

- `.proto` files under a project's protos directory, obeying
  `w17/` vocabulary.
- `docs/classification/*.yaml` (repo-bundled).
- Optional `--decide` flags for ReviewFinding resolutions.

### Intermediate

- `*loader.LoadedFile` — descriptors + extracted w17
  annotations.
- `*irpb.Schema` — validated, folded IR.
- `*classifier.Classifier` — immutable D28 matrix index.
- `*planpb.MigrationPlan` — structural Op stream.
- `[]*planpb.ReviewFinding` — decision-required axes.
- `[]*planpb.Resolution` — user decisions (from decide / future
  platform source).

### Output

- `*planpb.Plan` — per-connection Migrations + unresolved
  Findings.
- `FilesystemSink.Write`: `<out>/migrations/<conn-dir>/
  <basename>.{up,down}.sql` + `<basename>.check.<n>.sql` +
  `<basename>.manifest.json`.

### Determinism

Every transformation above is a pure function. Same inputs →
byte-identical outputs across runs. Finding IDs are
deterministic SHA-256 of (column FQN, axis, prev fact, curr
fact) so resolutions survive re-runs without regenerating IDs.

---

## Where this document stops

Implementation details — per-function signatures inside
ir/build.go helpers, classifier load.go YAML parsing,
emit/postgres renderer internals — live in the code itself
with doc comments at each function. Anything in this document
that contradicts the code means the code is the truth and this
doc needs an update.

When adding a new package under `srcgo/domains/compiler/`,
add a section above with purpose + public API + consumers +
purity. When removing a package, remove its section here too.
