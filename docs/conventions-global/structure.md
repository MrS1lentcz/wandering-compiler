# Structure

Directory layout of the project, how domains and modules are bounded, where persistent volumes live, and the top-level technology decisions that shape the layout.

## Project Structure

```
.
├── src<language_shortcut>/         # One directory per language (srcgo, srcrust, srcpy, …)
│   ├── Dockerfile
│   ├── <deps_file>                 # Cargo.toml, go.mod, requirements.txt, …
│   ├── domains/                    # Individual domains
│   │   └── <domain>/
│   ├── lib/                        # General-purpose libraries (not extensions)
│   ├── x/                          # Standard library extensions
│   │   ├── grpcx/
│   │   ├── djangox/
│   │   └── …
│   └── pb/                         # Generated output from proto/ for the given language
│
├── .env.example                    # Comments for all variables
├── .env.defaults                   # Default values – cp .env.defaults .env always works
├── .env                            # gitignore
├── compose.yaml
│
├── infra/
│   ├── <infra_services>/           # Sentinel, server configs, …
│   └── services/
│       └── <domain>/
│           └── <stage>/            # staging, production, …
│               ├── .env.defaults   # Non-secret, production-relevant values
│               ├── .env            # gitignore – filled manually (contains secrets)
│               └── compose.yaml    # Production compose
│
├── docs/
│   ├── conventions.md              # General conventions shared across apps
│   ├── acceptance-criteria.yaml    # General AC shared across apps
│   └── <domain>/                   # Optional – for smaller projects everything can live in docs/
│       ├── acceptance-criteria.yaml    # Optional – only if domain has specifics
│       ├── conventions.yaml            # Optional – only if domain has specifics
│       └── <other_doc_files>
│
├── proto/
│   ├── domains/
│   │   └── <domain>/               # Compiles as <project>/<domain>/…
│   │       ├── services/           # .proto files only, prefix "service_"
│   │       ├── types/              # .proto files only
│   │       └── ui/                 # REST API types – UI-specific, inherits from services/ and types/
│   ├── common/                     # Shared package across domains, names must be unique
│   │   ├── types/                  # .proto files only
│   │   └── <logical_block>/        # Arbitrary directories grouping a larger logical block of types
│   └── 3rdparty/                   # Third-party proto definitions
│
├── openapi/                        # Only if the project contains REST services
│   └── <app_name>.json
│
├── ui/                             # UI application (React Native + Expo, TypeScript, web-first)
│   ├── Dockerfile                  # Locks Node, npm versions; handles compilation — runs in compose
│   ├── app/                        # Expo Router – screens and navigation
│   ├── components/                 # Shared UI components
│   ├── package.json
│   └── …
│
├── tools/                          # Generators, test simulators and other tooling outside the codebase
│   └── <tool_name>/
│
├── PROJECT_STAGE                   # Current project stage (see process.md)
├── CLAUDE.md
├── Makefile
└── README.md
```

### Structure Notes

**`src<xx>/domains/`** — each domain has its own subdirectory. Each domain has its **own dedicated database** — domains never share a DB. Cross-domain communication happens exclusively via gRPC API (see Domain and Module Boundaries below).

**`src<xx>/lib/`** vs **`src<xx>/x/`** — `lib/` are general-purpose libraries with their own logic. `x/` are exclusively standard library or third-party framework extensions (they add behaviour, they do not introduce domain logic).

**`src<xx>/pb/`** — generated code from `proto/`, never edited manually. Regenerated via `make schemagen`.

**`proto/domains/<domain>/`** — each domain compiles as a single shared namespace `<project>/<domain>/…`. All services and types within a domain must have unique names. Proto files in `services/` carry the `service_` prefix (required for unique filenames in Go packages).

**`proto/common/`** — compiles as a single shared package for the entire project. Names must be unique across the entire `common/`.

**`ui/`** — UI application directory. Lives at the project root alongside `src<xx>/` directories, not inside them — the UI is not a backend service and does not share the `src<xx>/` conventions (no `ent`, no gRPC server, no DB migrations). It has its own `Dockerfile` and participates in `compose.yaml` like any other service.

---

## Domain and Module Boundaries

The architecture has two levels of boundaries with fundamentally different rules.

### Domain — Hard Boundary

A domain is a self-contained service with its own:
- **dedicated database** — no shared DB between domains, ever
- **own deployment** — separate binary, separate container
- **own `ent/schema/`** — data models are never shared at the DB level

Cross-domain communication is **exclusively via gRPC**. No other mechanism (shared DB, direct HTTP, message queue) is acceptable. This is intentional — gRPC gives you a typed, versioned contract that enforces the boundary at the API level.

Direct DB access across domain boundaries (shared tables, cross-DB JOINs, FK references to another domain's tables) is **strictly forbidden**. Violating this collapses the boundary and makes independent deployment and scaling impossible.

### Module — Soft Boundary

A module is a logical grouping of closely related models within a domain. It represents a coherent piece of functionality — the boundary is conceptual, not technical.

Rules for modules:
- A module is typically a few models that naturally belong together
- Models within a module can freely reference each other (FK, edges)
- Models can reference models from **other modules within the same domain** — this is fine and expected
- The boundary is enforced by naming convention (file names, table prefixes), not by Go packages or DB isolation

**What makes a module:** a group of models that together represent one coherent concern. Examples: `user` (identity, credentials), `billing` (invoices, payments, line items), `session` (sessions, stages, logs). The boundary is intuitive — if models feel like they belong to the same feature, they are in the same module.

### Summary

| | Domain | Module |
|---|---|---|
| Boundary type | Hard — technical | Soft — conceptual |
| Own database | ✅ Yes | ❌ No — shares domain DB |
| Cross-boundary references | gRPC API only | FK/edges allowed |
| Isolation enforced by | DB separation + API contract | Naming convention |

### Example

A project with two domains, each with its own DB:

```
domains/
├── billing/        ← own DB: billing_invoice, billing_payment, …
└── platform/       ← own DB: user_user, session_session, project_project, …
```

`platform` communicates with `billing` only via gRPC — never via a shared table or direct query.

---

## Implementation Architecture

Domain and module boundaries define *where services and data live*. Implementation architecture defines *where business logic and DB access live inside a single domain*. This is the single strongest internal rule of the project — it is what keeps domains composable and prevents each one from drifting into a tangled monolith.

### The Three Layers

Inside a domain, code is strictly partitioned into three layers:

1. **`grpcapi/storage/`** — the **only** place the database is touched. Every SQL query, every `ent` call, every transaction lives here. No other code — no other handler, no `lib`, no `x` — reads or writes the DB directly.
2. **`srcgo/lib/` and `srcgo/x/`** — the **default home for business logic**. Anything that can be expressed without knowing which domain it belongs to gets generalised and lifted out of the domain: algorithms, pure calculations, format conversions, policy engines, validation helpers, extensions of third-party libraries, …
3. **`grpcapi/business/`** — a **thin bridge** between the two worlds above. A `business/` handler receives a gRPC call, delegates data access to `storage`, delegates computation to `lib`/`x`, and composes the result. It is glue, not the place where real work happens.

### Why

- **Reuse across domains** — logic in `lib`/`x` can be pulled into any other domain without cross-domain coupling. Logic wedged inside a `business/` handler is stuck there forever.
- **Testability** — `lib`/`x` packages are pure Go; they are tested without a DB, without gRPC, without fixtures. `storage/` is tested against a real DB. `business/` is trivial to test because it is a bridge.
- **Separation of concerns** — DB code, business logic and gRPC plumbing are three fundamentally different kinds of code with different review, testing and change characteristics. Mixing them is the fastest way to produce code nobody can safely evolve.
- **Staff-level discipline** — this split is what separates a codebase that scales from one that calcifies. It is non-negotiable, even when a feature "would be faster" implemented directly in `business/`.

### Rule of Thumb

> If a piece of logic could be described without mentioning the domain it lives in — it belongs in `lib/` or `x/`, not in `grpcapi/business/`.

A `business/` handler that grows past "call `storage`, call `lib`, map the result" is a signal that its core logic is misplaced. Extract it — into `lib/` if it carries its own logic, into `x/` if it merely extends a standard library or framework — and reduce the handler back to a bridge.

Similarly: if a `business/` handler reads or writes the DB directly — even once, even for a "small" query — it is a bug. That query belongs in a Storage RPC; `business/` calls the Storage service instead.

### Exception: Domain-Local Packages

There is one legitimate exception to the `storage` / `lib`+`x` / `business` split: code that is **abstract** (not a handler, not a one-shot command) yet **cannot be lifted into `lib`/`x`** because it works with types specific to a single domain — domain `ent` models, domain gRPC types, domain events, etc. Generalising such code would require erasing the very types it is built on.

**When it arises.** Typical cases are reconcilers, background workers, schedulers, domain-specific scoring / planning / matching engines, **port adapters** (see below), **domain event orchestrators** (types that parse a domain event, call Storage/Service RPCs and emit further events — see [eventbus.md](eventbus.md)), or any other piece of logic that is reused across handlers (or lives outside handlers entirely) and is meaningful only inside this one domain.

**Port adapters.** When a domain defines a **port** — a Go interface that abstracts an infrastructure capability (e.g. `sessionspawn.ContainerManager`, a hypothetical `storage.ObjectStore`, `eventbus.Bus`) — each **adapter implementation** of that port is its own subpackage, **not** an inline factory in `application/`. The adapter owns its dependencies (third-party SDKs), its own types, and its own tests. `application/module_*.go` only constructs it and hands it to the option. Typical layout:

```
srcgo/domains/<domain>/
├── sessionspawn/
│   ├── port.go         ← port interface + shared types
│   ├── docker/         ← adapter (imports docker SDK; has its own tests)
│   └── k8s/            ← adapter (imports k8s SDK; has its own tests)
└── application/
    └── module_sessionspawn.go   ← factory: picks docker vs. k8s from Config, constructs, returns
```

The same pattern applies to any other port introduced by the domain (S3 adapters, message-bus adapters, metrics-sink adapters, …). If the domain has exactly one adapter today, the port + single subpackage split is still the default — it keeps the third-party SDK out of `application/` and leaves the door open for a second adapter without a rewrite.

**Where it lives.** As a new package directly inside the domain directory, alongside `grpcapi/`, `application/`, `cmd/`, `ent/`, etc. The package is named after what it does — e.g. `worker/`, `reconciler/`, `scheduler/`, `scoring/`, `matching/`, or a port name like `sessionspawn/` with adapter subpackages. There is no fixed list; domains create these as needed.

```
srcgo/domains/<domain>/
├── application/
├── application.go
├── config.go
├── ent/
├── grpcapi/
├── cmd/
├── worker/          ← domain-local package
├── scoring/         ← domain-local package
└── …
```

**Two directions of dependency.** A domain-local package may go either way relative to `grpcapi/`:

- **Used by `grpcapi/`** — handlers (typically in `business/`) import the package to share non-trivial domain logic that is wrong to duplicate across handlers and wrong to push into `lib`/`x` because it is domain-typed.
- **Uses `grpcapi/`** — the package is a consumer of the domain's own gRPC services (e.g. a worker that drives Storage and Service RPCs in a loop) and is itself run from `cmd/` or wired into `application/`.

**The rule of thumb still applies.** Before adding a domain-local package, try to lift the logic into `lib`/`x` — if it can be expressed without the domain's types, it belongs there. Domain-local packages are an **escape hatch**, not the default; without the discipline, they become the drawer where everything unplaced ends up.

**No layer rules are bypassed.** A domain-local package **does not** touch the DB directly — that is still exclusively the job of `grpcapi/storage/`. If a domain-local package needs data, it calls the Storage service like any other client. The escape hatch is about *where domain-typed logic lives*, not about which code gets to talk to the database.

### Interaction with gRPC Layers

See [grpc.md](grpc.md) for the layered service model (`StorageService`, `Service`, `FacadeService`). The implementation rule above is what gives those layers their meaning:

- `StorageService` handlers live in `grpcapi/storage/` — DB only, no cross-service calls, no business logic above the SQL layer
- `Service` handlers live in `grpcapi/business/` — bridge only; real logic delegates to `lib`/`x`
- `FacadeService` handlers (when present) live in `grpcapi/facade/` — same bridge principle, composing over multiple services

---

## Technology Decisions

### Network Layer — TLS/SSL

- **No service handles TLS/SSL** — applies to REST, gRPC and any other protocol
- All public APIs are behind a load balancer that handles SSL termination
- Services listen on plain HTTP ports (80, etc.)

### Backoffice Services — Django (`srcpy`)

- Backoffice services are **always implemented in Django** (`srcpy`)
- Django is a consumer of the DB schema, not its source of truth — see [go.md](go.md) for the `ent` / `inspectdb` workflow

---

## Volumes — `.volumes/`

All persistent Docker volumes are stored under a single **`.volumes/`** directory at the project root. This applies consistently across local development and production environments.

```
.volumes/
├── <domain_name>/     # Volume for a project domain (e.g. .volumes/forge/)
├── postgres/          # Volume for a 3rd-party service
├── minio/
├── redis/
└── nginx/
```

### Rules

- `.volumes/` always lives at the **project root** — never inside `src<xx>/`, `infra/`, or anywhere else.
- Every service (domain or 3rd-party tool) gets its **own subdirectory** — scoped by name. No service writes directly into `.volumes/` root.
- Domain volumes use the **domain name** as the subdirectory: `.volumes/<domain_name>/`.
- 3rd-party services use the **tool name** as the subdirectory: `.volumes/postgres/`, `.volumes/minio/`, `.volumes/nginx/`, etc.
- `.volumes/` is in `.gitignore` — never committed.
- The `nuke` Makefile target removes `.volumes/` entirely.

### Rationale

Using named subdirectories means volumes from different services never collide, the layout is identical on every machine (local and prod), and adding a new service requires no coordination with existing volume paths.

### Compose Example

```yaml
services:
  postgres:
    volumes:
      - ./.volumes/postgres:/var/lib/postgresql/data

  forge:
    volumes:
      - ./.volumes/forge:/data
```
