# Go Conventions (`srcgo`)

Everything specific to the Go side of the project: the `srcgo` layout, the `gox` foundation library, the domain `application/` DI pattern, `cmd/` classification with the CLI convention, and the `ent`-driven data model / migration workflow.

## `srcgo` Structure

`srcgo/` is the **monorepo root package** (`package srcgo`) with a single `go.mod` for the entire project. It contains shared abstractions and standalone shared packages consumed by domains. Domain logic does not belong here — each domain is fully self-contained in its own directory.

```
srcgo/
├── go.mod                           # Single go.mod for the entire monorepo
├── errors.go                        # package srcgo — general shared errors
├── lib/                             # General-purpose libraries (own logic, no domain content)
│   ├── persistence/
│   │   ├── interfaces.go
│   │   ├── postgres.go
│   │   └── …
│   └── …
├── x/                               # Standard library / framework extensions (behaviour only)
│   ├── grpcx/
│   └── …
└── domains/
    └── <domain_name>/
        ├── application.go           # package <domain> — interfaces + Application composition
        ├── config.go                # package <domain> — Config struct + NewConfigFromEnv()
        ├── application/
        │   ├── application.go       # app struct, facade implementing the Application interface
        │   ├── options.go           # functional options + factory types
        │   ├── module_scoring.go    # one file per domain module
        │   ├── module_prompt.go
        │   ├── module_geo.go
        │   └── …
        ├── gen/                     # Generated code only — protobridge outputs, proto stubs, etc. Never hand-edited.
        ├── grpcapi/                 # gRPC handlers implementing the proto-generated service interfaces
        │   ├── storage/             # Storage-level handlers — thin CRUD / persistence-facing RPCs
        │   ├── business/            # Business-level handlers — domain logic, orchestration across modules
        │   └── facade/              # (optional) Facade handlers — composition over storage + business for external consumers
        ├── bin/                     # gitignore — compiled binaries (scoped per domain, never at project root)
        ├── cmd/
        │   ├── <service_name>/      # Entry point for each service (main.go only)
        │   └── cli/                 # CLI application (see CLI section)
        └── ent/
            └── schema/
```

## Standard Go Foundation Library — `gox`

Every Go project uses **[github.com/MrS1lentcz/gox](https://github.com/MrS1lentcz/gox)** as a shared foundation library. It covers the low-level plumbing that every service needs but should not re-implement:

- **gRPC server bootstrap** — starting a gRPC server with health check endpoint out of the box
- **gRPC metadata helpers** — reading and writing gRPC metadata
- **Sentry interceptor** — gRPC interceptor that automatically reports errors to Sentry
- **Sentry initialisation helper** — standardised Sentry setup at service startup

`gox` is a direct dependency in `go.mod`. It is not vendored into `srcgo/` — it is an external library maintained separately.

## Proto-Driven Tooling — `protobridge`

Every Go project also uses **[github.com/MrS1lentcz/protobridge](https://github.com/MrS1lentcz/protobridge)** — a companion library that turns proto annotations into runtime surfaces without hand-written glue:

- **Zero-code REST server** — generated purely from `.proto` annotations, no hand-written HTTP handlers or routing
- **Zero-code MCP server** — the same annotations expose an MCP endpoint alongside the gRPC and REST surfaces
- **Plus everything else needed to wire these up** — the REST and MCP sides are fully runtime-generated from the proto contract

This makes the `.proto` file the single source of truth for gRPC, REST and MCP at once: adding an RPC and annotating it automatically exposes it over all three surfaces without writing server code.

In addition, `protobridge` ships a **partial event sourcing framework specific to Go**. This is the one area where it is *not* zero-code like the REST/MCP surfaces — the ES framework supplies concrete Go code and patterns you integrate with, not a generated runtime. It is Go-only by design.

`protobridge` is a direct dependency in `go.mod`, alongside `gox`, and is never vendored into `srcgo/`.

## `srcgo` Root Package — Shared Abstractions

The root package contains only things shared across **all** domains — general errors, utility types and the two shared package trees `lib/` and `x/`. No domain logic belongs here.

**`srcgo/lib/`** — general-purpose libraries with their own logic (e.g. `persistence/` provides DB abstractions). Each package is independent — domains import only what they need.

**`srcgo/x/`** — standard library and third-party framework extensions that add behaviour only (e.g. `grpcx/` extends gRPC). They never introduce domain logic.

The split mirrors the top-level `src<xx>/lib/` vs `src<xx>/x/` distinction — if a package has its own logic, it is a `lib`; if it only extends something existing, it is an `x`.

## Domain Structure — `application.go` and `config.go`

Each domain has its own `application.go` and `config.go` at the root of its directory (same package as the domain).

**`<domain>/application.go`** defines **interfaces** — module interfaces and the composite `Application` interface:

```go
// Each module is a standalone interface
type PersistenceModule interface {
    Database() *sql.DB
}

type GeoModule interface {
    AreaQueryServiceClient() geoplatform_api.AreaQueryServiceClient
}

// Application is a composition of all domain modules
type Application interface {
    PromptModule
    ScoringModule
    PersistenceModule
    GeoModule
    Config() Config
}
```

**`<domain>/config.go`** defines the domain's **`Config` struct** loaded from ENV via `github.com/caarlos0/env`:

```go
func NewConfigFromEnv() Config {
    cfg := Config{}
    if err := env.Parse(&cfg); err != nil {
        log.Fatalf("Failed to load configuration from environment: %v", err)
    }
    return cfg
}
```

Config contains only values relevant to the given domain — ENV variables are prefixed with the domain name (see [tooling.md](tooling.md) — ENV Variables).

## `application/` — Facade Implementation

`application/` is **dependency injection of process-wide, pre-configured resources** — nothing else. It holds things that are either:

- **configured via ENV at startup** — DB connections, gRPC clients to other services, object storage clients, Sentry, … (everything whose configuration comes from the domain's `Config` struct)
- **shared across multiple gRPC handlers** — so they are built once, globally, and reused

Anything that is configured **at runtime** — per-request state, per-call parameters, values derived from an incoming message, request-scoped context — **does not belong here**. Such things must be constructed as part of the gRPC handler initialisation (or similar per-call scope) and passed in explicitly. The `application/` layer is intentionally static: once `New()` finishes, nothing inside it should change for the lifetime of the process.

Rule of thumb: if you cannot decide the value of something from `.env` alone, it does not go into `application/`.

`application/application.go` is the **facade** — it implements the `Application` interface via an `app` struct that holds the individual modules. Each getter contains a fail-fast guard:

```go
func (a *app) Database() *sql.DB {
    if a.persistence == nil {
        log.Fatal("Persistence module is not initialized")
    }
    return a.persistence.Database()
}
```

`log.Fatal` in getters is **intentional fail-fast** — if you start a service without a configured module it must die immediately. The goal is to prevent a process running half-way and producing inconsistent data.

`application/options.go` defines the **functional options pattern** — each module has its own `With<Module>` option accepting a factory function:

```go
type ScoringModuleFactory func(Config) (ScoringModule, io.Closer, error)

func WithScoringModule(f ScoringModuleFactory) Option {
    return func(o *appOpts) { o.scoring = f }
}
```

Factory functions always return `(Module, io.Closer, error)` — the error allows factories to surface initialization failures (connection refused, bad config, …) cleanly. The module lifecycle (connection, pool, …) is explicitly managed via `app.Close()`. Factories are registered via options; the `New()` function in `application/options.go` calls them in dependency order and returns an error if any factory fails.

Each domain module has its **own file** `module_<n>.go` alongside `application.go` and `options.go`. Modules from shared packages (e.g. `persistence/`) are wired in `application/` via options only and do not get their own `module_*.go` file — their logic lives in the shared package.

### `module_<n>.go` — Factory Wrapper Only

A `module_<n>.go` file is a **factory wrapper**, not the home of the module. Its job is to read config, call a constructor from the package where the implementation lives, and return the wired-up module to the option. That is it.

**May contain:**

- reading values from the domain's `Config`
- calling the constructor from the package where the implementation lives (e.g. `sessionspawn/docker.New(...)`, `eventbus.NewNATSBus(...)`, `persistence.New(...)`)
- a short adapter if the constructed value's shape does not match the module interface

**Must not contain:**

- direct calls to third-party SDKs (`docker/*`, `aws-sdk-go/*`, `stripe/*`, `nats.go`, …) — the SDK belongs behind a port, consumed inside the implementation package
- methods with receivers implementing a port interface (`func (s *X) Foo(...)`) — that is the implementation, not the factory
- its own unit tests — the tested logic lives in the implementation package

**Signals you are breaking this rule.** `module_X.go` has grown past ~50 lines; contains `func (s *X) Foo(...)` receivers implementing the module's port interface; imports a third-party SDK directly. At that point the implementation belongs in its own package — typically a domain-local package (see [structure.md](structure.md) — Exception: Domain-Local Packages) — and `module_X.go` collapses back into pure construction.

### Domain Orchestrators Are Not DI Modules

If a type parses a domain event, calls Storage/Service RPCs, and emits further events, it is a **domain orchestrator**, not a DI module. It lives in its own domain-local package (see [structure.md](structure.md) — Exception: Domain-Local Packages).

`application/` only **instantiates** it and passes it along — typically into `eventbus.RegisterSubscribers` or a similar wiring point — and does not own its logic. If you catch an event handler growing inside `module_*.go` (state, business branches, RPC calls, event emission), that is the smell: the orchestrator has been inlined into the factory. Extract it into its own package, let `module_*.go` construct it, and hand it to whatever consumes subscribers.

## Application DI Facade — Startup Infra Goes Through It

Anything wired at `application.New` via a `With<X>()` option **must** be reachable via an `app.X()` getter on the `Application` interface. Handlers, subscribers, workers, and any other wiring code that already receives `app` must **not** also take that same dependency as a separate constructor parameter.

**Rule by pattern.** If you find yourself writing `NewSomething(app forge.Application, X ...)` where `X` is initialised once at process startup from the domain's `Config` / a `With*` option (NATS bus, S3 client, DB pool, container manager, build pipeline, gRPC client to another domain, …), `X` belongs on `Application`. Fix the interface, not the call site.

### Why

1. **Validated on startup, fail-fast.** The `app` getters `log.Fatal` when their module was never configured (see `application/application.go` — same guard as `Database()`, `Container()`, …). A parallel parameter silently accepts `nil` and surfaces the problem mid-request — or, worse, during a rare code path that only executes in production.
2. **One source of truth.** Mixed access (half the code reaches the bus through `app.Bus()`, half through a separate `bus` parameter) forces every reader to verify which branch a given handler uses. Uniform access through the facade removes that whole question.
3. **Refactor safety.** When infrastructure gets split or replaced (in-process → TCP, NATS → other broker, …), there is exactly one wiring point to change. Parallel parameters mean N call sites to hunt down.

### Checklist when introducing a new startup-configured dependency

- Add the `With<X>()` option in `application/options.go`.
- Store the value on the `app` struct.
- Add the `X() T` getter on `<domain>.Application` (or on `application.Application` if the type pulls in process-only packages — e.g. `*ent.Client`, `sessionspawn.ContainerManager`).
- **Do not** also add it as a handler / subscriber / worker constructor parameter. Reach it through `app.X()`.

### Signals you are about to break this rule

- A `With<X>()` option already exists for the thing you are about to pass in — if it's an option, it's DI, not a parameter.
- Every other dependency already on `Application` (Clients, Build, Database, Config, Container, …) is env-configured; the one you are adding next to `app` is too. That outlier **is** the smell.
- Silence when `Deps.X` is nil. `app.X()` would `log.Fatal` if unconfigured — a parallel parameter just lets `nil` propagate.

### Exception

Process-local state that is **not** env-configured is fine to pass as a constructor parameter — a per-request logger, a `context.Context`, an in-memory ring-buffer manager scoped to one worker, a callback closure, etc. The rule is specifically about things that come out of the domain's `Config` / `With*` module factories and are shared process-wide.

## Domain Structure — `bin/` and `cmd/`

**`bin/`** — directory for compiled binaries. Lives at `srcgo/domains/<domain_name>/bin/` — never at the project root. The entire directory is in `.gitignore`. Binaries are built via `make build` — never committed.

Scoping `bin/` per domain prevents output name collisions: domains are developed independently and have no awareness of what binaries other domains produce. A root-level `bin/` would silently overwrite binaries from different domains that happen to share a name.

**`cmd/<service_name>/`** — each service has its own entry point. The directory contains **only `main.go`** with a single `main` function (see [quality.md](quality.md) — no additional function definitions in `main.go`).

### `cmd/` sub-package classification

Not every `cmd/` sub-package is equal. The rule is:

| Type | `cmd/` sub-package | Examples |
|---|---|---|
| **Daemon / long-running process** | own `cmd/<name>/main.go` | `cmd/server/`, `cmd/gateway/`, `cmd/mcp-server/` |
| **Production operational command** | subcommand in `cmd/cli/` | `migrate apply`, `migrate diff` |
| **Development-only command** | subcommand in `cmd/dev/` | `seed`, `nuke`, `neoc` |

**Daemons** run indefinitely and are managed by a process supervisor (Docker, systemd, etc.). They warrant a separate binary.

**Production operational commands** are one-shot invocations safe to run against a production system. They **must** live in `cmd/cli/` as kong subcommands — never as separate `cmd/<name>/main.go` binaries.

**Development-only commands** are commands that are dangerous or meaningless in production (seeding fake data, wiping the database, resetting state). They **must** live in `cmd/dev/` as a separate kong CLI — completely isolated from `cmd/cli/`. This prevents accidental execution of destructive dev tooling on a production server where the binary should simply not be present.

> Rule of thumb: if running the command on a live production system would be dangerous or wrong, it belongs in `cmd/dev/`, not `cmd/cli/`.

> `cmd/seed/` is a legacy exception that predates this rule. New dev commands must go into `cmd/dev/`.

### Protobridge daemons — `cmd/gateway/` and `cmd/mcp-server/`

REST and MCP surfaces generated by `protobridge` (see Proto-Driven Tooling) are exposed by dedicated daemons. They live under `cmd/` like any other daemon, with standardised names so every project looks the same:

- **`cmd/gateway/`** — the REST gateway
- **`cmd/mcp-server/`** — the MCP server

When a domain exposes more than one REST or MCP surface, each gets the same prefix with a descriptive suffix: `cmd/gateway-<name>/`, `cmd/mcp-server-<name>/`. The prefix stays as the primary token so `ls cmd/` groups them together and the role is immediately visible.

These daemons belong in `cmd/`, **not** in `gen/`. `gen/` is reserved for zero-code library output — generated stubs, proto types, runtime glue. `protobridge` also generates the `main.go`, `Dockerfile` and k8s manifests for the gateway / MCP server, but those are deployment artefacts of a first-class service and belong alongside every other `cmd/<daemon>/`.

## CLI (`cmd/cli/`)

The domain CLI lives in `cmd/cli/`. It uses **[kong](https://github.com/alecthomas/kong)** for command definition and **[kongplete](https://github.com/willabides/kongplete)** for shell autocomplete.

### `cmd/cli/` Structure

```
cmd/cli/
├── main.go       # Only: root CLI struct definition + kong.Parse() + kongplete setup
├── cmd_<n>.go    # One file per command
└── …
```

**`main.go`** contains exclusively:
- root CLI struct aggregating all subcommands
- `kongplete` initialisation for shell autocomplete
- call to `kong.Parse()` and `Run()`

**`cmd_<n>.go`** — one file per command. The filename matches the command name with a `cmd_` prefix. Each file contains the command struct with kong annotations and its `Run` method.

### Example

**`main.go`:**
```go
package main

import (
    "github.com/alecthomas/kong"
    "github.com/willabides/kongplete"
)

var cli struct {
    Deploy   DeployCmd   `cmd:"" help:"Deploy an application"`
    Rollback RollbackCmd `cmd:"" help:"Rollback to previous version"`

    InstallCompletions kongplete.InstallCompletions `cmd:"" help:"Install shell completions"`
}

func main() {
    ctx := kong.Parse(&cli,
        kong.Name("myapp"),
        kong.Description("MyApp CLI"),
        kong.UsageOnError(),
    )

    kongplete.Complete(ctx)

    if err := ctx.Run(); err != nil {
        kong.DefaultResolver(nil)
        ctx.FatalIfErrorf(err)
    }
}
```

**`cmd_deploy.go`:**
```go
package main

type DeployCmd struct {
    AppID  string `arg:"" help:"Application ID"`
    Env    string `short:"e" default:"staging" enum:"staging,production" help:"Target environment"`
    DryRun bool   `short:"n" help:"Print actions without executing"`
}

func (c *DeployCmd) Run() error {
    // implementation
    return nil
}
```

### Rules

- **Types and validation belong in struct tags** — not in the `Run()` body. Kong handles validation before calling `Run()`.
- **`Run()` must not contain flag parsing** — business logic only.
- **Each command in its own file** — `cmd_<n>.go`. No commands defined in `main.go`.
- **Shell autocomplete always** — `kongplete.InstallCompletions` is part of every CLI as a standard subcommand.

---

## Data Models — Source of Truth

| Layer | Technology | Role |
|---|---|---|
| Relational DB schema | **Go / `ent`** | ✅ Source of truth for DB models |
| Migrations | **Go CLI (`ent` schema diff)** | ✅ Source of truth for DB migrations |
| Django models | `inspectdb` generator | 🔄 Generated from DB schema, never written by hand |

**Workflow:**

1. Schema changes are made in Go (`ent`)
2. Migrations are generated via Go CLI `ent` schema diff (`make makemigrations`)
3. Migrations are applied to the DB (`make migrate`)
4. Django models are regenerated via `inspectdb` — **never edited manually**

> ⚠️ Django is not the source of truth — any manual edit to Django models will be overwritten on the next regeneration.

## ent — Module Scoping and Table Prefixes

Every `ent` model must have a **table prefix derived from its module**. Purpose:

- allows reuse of model names across modules (e.g. `Status` can exist in both `invoice` and `user`)
- tables are naturally grouped in the DB when sorted alphabetically
- prevents collisions with Django framework tables

**Naming conventions:**

| Module | Model | Table |
|---|---|---|
| `invoice` | `Status` | `invoice_status` |
| `user` | `Status` | `user_status` |
| `invoice` | `Document` | `invoice_document` |

The table prefix is set in `ent` via the `Annotations` method on each schema:

```go
func (Status) Annotations() []schema.Annotation {
    return []schema.Annotation{
        entsql.Annotation{Table: "invoice_status"},
    }
}
```

**Forbidden prefixes — Django framework collisions:**

The Django framework uses the following reserved prefixes. No module or table in the project may use these prefixes in their original form:

| Forbidden prefix | Django origin | Replacement |
|---|---|---|
| `auth_` | `django.contrib.auth` | `authx_` |
| `admin_` | `django.contrib.admin` | `adminx_` |
| `django_` | Django internal tables | `djangox_` |
| `contenttypes_` | `django.contrib.contenttypes` | `contenttypesx_` |

Rule: any module or table whose name would result in a Django reserved prefix gets an `x` suffix. Applies consistently to everyone — no exceptions.

**File organisation:**

`ent` generates the entire package from a single `schema/` directory. Files are flat but named with a module prefix:

```
srcgo/domains/
└── <domain>/
    └── ent/
        └── schema/
            ├── invoice_status.go
            ├── invoice_document.go
            ├── user_status.go
            └── user_profile.go
```

One `ent/schema/` per domain — all models of all services within a domain share one flat structure. Per-module subdirectories are not used — the `ent` generator works with a single input path and splitting it would require custom merge logic.

**Primary model rule** — all models follow the `<module>_<model>` pattern, including the primary model where the module name matches the model name. There are no exceptions for new work. Some existing primary models use the legacy `<module>.go` filename (e.g. `user.go`, `project.go`, `session.go`) — these will be migrated to `<module>_<model>.go` over time. Do not introduce new `<module>.go` files:

| Module | Model | File | Table |
|---|---|---|---|
| `user` | `User` | `user_user.go` | `user_user` |
| `session` | `Session` | `session_session.go` | `session_session` |
| `session` | `Status` | `session_status.go` | `session_status` |
| `billing` | `Invoice` | `billing_invoice.go` | `billing_invoice` |

**Adding a new model:**

1. Create `<module>_<model>.go` in `ent/schema/`
2. Define the struct with `Fields()`, `Edges()`, and `Annotations()` — `Annotations()` is mandatory on every model
3. Run `make schemagen` — ent regenerates the entire package
4. Run `make makemigrations` to generate the SQL migration, then `make migrate` to apply it

```go
// ent/schema/session_status.go
package schema

import (
    "entgo.io/ent"
    "entgo.io/ent/dialect/entsql"
    "entgo.io/ent/schema"
    "entgo.io/ent/schema/field"
)

type SessionStatus struct {
    ent.Schema
}

func (SessionStatus) Fields() []ent.Field {
    return []ent.Field{
        field.String("label").NotEmpty(),
    }
}

func (SessionStatus) Annotations() []schema.Annotation {
    return []schema.Annotation{
        entsql.Annotation{Table: "session_status"},
    }
}
```

## Schema Migrations

`make makemigrations` always spins up its own **ephemeral Postgres container** — regardless of whether Postgres from `make up` is running locally. Reason: migration generation must be deterministic and must not depend on local state.

**`make makemigrations` flow:**

1. Starts an ephemeral `postgres:16-alpine` container with a random host port (auto-assigned via `-P`)
2. Waits for Postgres to become ready (bounded 60s timeout)
3. Runs `go run ./domains/forge/cmd/cli migrate diff` — computes the SQL diff between the current DB state and the desired `ent` schema
4. Removes the container

**`make migrate`** applies the generated migrations to the target DB (as configured in `.env`).

> The migration DB is purely temporary — it never contains production or local data. It is a diff environment only.
