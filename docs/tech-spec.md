# Technical Specification

## TL;DR

A developer tool where the entire project infrastructure is **declared, not coded**. The programmer defines schemas, DB models, queries, events, and mappings through a UI or DSL files — and receives compiled, ready-to-run binaries plus generated clients, gateways, and infra config. The only thing written by hand is business logic inside standard gRPC handlers. Everything else — DB layer, event system, API gateways, observability, Docker, k8s — is generated.

**Single source of truth:** Schema definitions (proto + extensions) drive all outputs: REST, gRPC, WebSocket, SSE, MCP, DB migrations, DB access layer, JS clients, OpenAPI, AsyncAPI, and more.

## Context and Motivation

This is not a greenfield paradigm. It is the **productization of conventions already practiced by hand** in this class of projects. See `docs/conventions-global/`:

- The three-layer gRPC split (Storage / Service / Facade) in `grpc.md` maps one-to-one onto the three generated layers below (DB gRPC / Standard gRPC / FE gRPC).
- The two-file eventbus skeleton in `eventbus.md` is what the event DSL generates.
- `protobridge` already produces REST/MCP zero-code from proto annotations — the gateway layer extends that pattern.
- `ent`-based migrations in `go.md` are what the schema generator replaces with plain SQL migrations.

The compiler's job is to remove every line of code a developer currently writes that can be mechanically derived from declared intent, while keeping the resulting output **indistinguishable from well-written hand-authored code following the same conventions**.

## Architecture Overview

```
┌─────────────────────────────────────────┐
│           Schema Definitions            │
│   (.proto + extensions, DSL files)      │
└────────────────────┬────────────────────┘
                     │
          ┌──────────▼──────────┐
          │   Platform / UI     │  ← visual editor or DSL files in git
          └──────────┬──────────┘
                     │ generators
     ┌───────────────┼───────────────────┐
     │               │                   │
┌────▼─────┐  ┌──────▼──────┐   ┌───────▼──────┐
│ DB gRPC  │  │Standard gRPC│   │  FE gRPC     │
│(generated)│ │(skeleton)   │   │(generated)   │
└────┬─────┘  └──────┬──────┘   └───────┬──────┘
     │               │                   │
     └───────────────▼───────────────────┘
                      │
        ┌─────────────▼─────────────┐
        │         Gateways          │
        │  REST · SSE · WS · MCP    │
        └─────────────┬─────────────┘
                      │
        ┌─────────────▼─────────────┐
        │        JS Clients         │
        │ OpenAPI · AsyncAPI · Proto │
        └───────────────────────────┘
```

## What the Developer Actually Writes

Only the implementation of standard gRPC handler methods. Everything else is generated or declared.

```go
// Generated skeleton — developer fills in only this:
func (s *UserService) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.User, error) {
    // DB gRPC methods available as dependencies — no ORM, no SQL
    user, err := s.db.CreateUser(ctx, &dbpb.CreateUserRequest{...})
    // business logic here
    return user, err
}
```

## DSL Strategy

Two classes of DSL, chosen by fit:

- **Schema / DB model / Events** → standard `.proto` files with custom options. Mature tooling, great LLM support, human-readable, no parser to maintain.
- **Queries and Mappings** → shared custom syntax, one parser, two standard libraries, two compilers. Proto is too coarse to express query joins/aggregations or cross-service mapping cleanly.

### Schema & Event DSL — Protobuf + Extensions

Covers types, fields, validations, UI metadata, DB model definitions (table, indexes, constraints), and event definitions (what gets emitted, on which mutation).

```protobuf
message Product {
  option (schema.meta) = { ui_label: "Product", soft_delete: true };
  option (db.table)    = { name: "products", timestamps: true };

  string id    = 1 [(field.meta) = { immutable: true }];
  string name  = 2 [(field.meta) = { ui_label: "Product name" },
                    (validate)   = { min_len: 1, max_len: 255 }];
  double price = 3 [(validate)   = { min: 0 }];
}

service ProductEvents {
  option (event.config) = {};
  rpc OnProductCreated(Product) returns (google.protobuf.Empty) {
    option (event.trigger) = { on: [CREATE] };
  }
}
```

### Query & Mapping DSL — Shared Custom Syntax

Assignment-style, `$` for references, `[*]` for array iteration, named blocks. Inspired by Doctrine's DQL: SQL-shaped but model-aware, so relation handling and result shapes are deterministic from the declared inputs/outputs.

#### Query DSL — declares DB operations, compiled to native SQL + gRPC wrapper

```
query GetUserInvoiceStats(user_id: string, from_date: timestamp) -> UserInvoiceStats {
  from: Invoice
  join: User via Invoice.user_id
  where: {
    Invoice.user_id: $user_id
    Invoice.created_at: > $from_date
  }
  aggregate: {
    total: sum(Invoice.amount)
    count: count()
    avg:   avg(Invoice.amount)
  }
}
```

#### Mapping DSL — declares FE gRPC transformation layer, compiled to generated Go

```
mapping UserToFE(u: UserInternal, orders: OrderService.GetOrders(user_id: u.id)) -> UserFE {
  u.name              → target.display_name
  u.email             → target.contact.email
  orders[*].id        → target.order_ids
  orders[0]           → target.latest_order via OrderSummaryMapper
  orders[*].total     → target.total_spent via sum()
}
```

#### Shared syntax rules

- Named blocks with typed inputs and return type
- `$param` or `source.field` for references
- `[*]` for array iteration, `[0]` for index access
- `via` for nested mapper or aggregation function
- No error handling in DSL — error strategy declared separately as annotation

## Three-Layer gRPC Architecture

Mirrors the Storage / Service / Facade layering in `docs/conventions-global/grpc.md`.

### 1. DB gRPC (fully generated) — corresponds to `StorageService`

Generated from DB model definitions + query DSL. The developer never writes SQL or ORM code.

- Each declared query becomes a gRPC method.
- Implementation is native SQL compiled from query DSL (Go binary; Rust possible later).
- All mutations accept an optional `transaction_id` — enables running multiple calls within a single transaction.
- Optional transaction-aware proxy routes transactional calls to the same DB node (relevant for connection-bound sessions in PostgreSQL).
- Queries are inspectable — enables future agent-based optimization (approximate counts, re-aggregation strategies at scale).

### 2. Standard gRPC (skeleton generated, logic manual) — corresponds to `Service`

- Generated skeleton with typed dependencies injected (DB gRPC client, event emitter, logger, tracer).
- Generated interceptor emits events automatically based on event DSL — developer does not write emit calls.
- Generated wrapper adds `Close() error` interface, prepares server, registers interceptors and options.
- Developer only implements handler method bodies.
- OpenTelemetry traces and health checks wired automatically.

### 3. FE gRPC (fully generated) — corresponds to `FacadeService`

- Declared via mapping DSL.
- Calls internal standard gRPC methods and maps responses to public FE types.
- Useful when FE types differ from internal BE types (different field names, flattened structures, combined responses).
- Supports array transformations, nested mappers, multi-source mappings.

## Generated Outputs

| Output | Source |
|---|---|
| DB migrations (SQL) | DB model definitions (proto + db extensions) |
| DB gRPC binaries | Query DSL → native SQL + gRPC server |
| Standard gRPC skeleton | Schema proto |
| FE gRPC | Mapping DSL |
| REST / SSE / WS / MCP gateway | Schema proto (protobridge) |
| OpenAPI schema | Schema proto |
| AsyncAPI schema | Event DSL |
| JS/TS clients (standard) | OpenAPI / AsyncAPI |
| JS/TS client (proto over HTTP) | Schema proto — binary transport |
| Proto files + compiled binaries | Schema proto → all target languages |
| gRPC app skeleton | Schema proto + config |
| Dockerfiles | Project config |
| k8s manifests | Project config |
| ENV files (defaults + examples) | Project config |
| CLI binaries (dev + server) | Project config |
| Admin UI (Django-style) | UI metadata on schema |

## JS/TS Client — Proto over HTTP

Custom generated client library using protobuf for marshalling over HTTP/WS instead of JSON:

- Binary payload — smaller, faster.
- Input validation generated from schema — request fails before sending if invalid.
- Full type safety.
- Debug mode flag switches to JSON transport for human-readable inspection. Since validations are generated from the same schema, debug mode is rarely needed — invalid requests are caught client-side before they reach the wire.

## CLI Binaries

### Developer CLI

- List open schema initiatives (pending changes).
- Pull latest generated code into local codebase.
- Show diff of what would change.
- Apply updates to managed files.

### Server CLI

- Run forward / backward DB migrations.
- Show migration status.
- No dependency on any framework migration system (replaces `ent`/Atlas, Django migrations, etc.).
- Migrations are plain SQL — portable, inspectable, committable to git.

## File & Directory Structure

Small files, organized by domain. Both UI and DSL files produce the same output — the UI is a visual editor over the DSL.

```
project/
├── schema/
│   ├── user.proto           # types + field metadata + validations
│   ├── product.proto
│   └── order.proto
├── db/
│   ├── models/
│   │   ├── user.proto       # db extensions on schema types
│   │   └── product.proto
│   └── queries/
│       ├── user_queries.qd  # query DSL files
│       └── invoice_stats.qd
├── events/
│   ├── user_events.proto    # event DSL (proto extensions)
│   └── order_events.proto
├── mappings/
│   └── user_fe.md           # mapping DSL files
├── config/
│   ├── project.proto        # infra config (docker, k8s, env)
│   └── services.proto
└── gen/                     # generated — do not edit
    ├── go/
    ├── ts/
    └── sql/
```

## Event System

- Events declared in event DSL (proto extensions on service methods).
- Standard gRPC interceptor generated automatically — emits declared events without developer involvement.
- Subscriber routing calls target gRPC methods.
- No need to write emit calls, tests for emission, or wiring code.
- Event transport configurable (NATS by default). Contract matches the two-file eventbus skeleton defined in `docs/conventions-global/eventbus.md`.

## Strategic Decisions (locked for v1)

These resolve the biggest ambiguities in the spec and should be treated as fixed unless explicitly revisited. Every downstream design decision flows from them.

| # | Decision | Choice |
|---|---|---|
| 1 | Proto-only vs. custom DSL | Proto for schema and events (mature tooling); custom DSL for query and mapping only (proto cannot express them cleanly). |
| 2 | Escape hatches | **Mandatory, not optional.** Every generator and DSL has a documented fall-back to hand-written code (raw SQL block in query DSL, Go function in mapping DSL, `// wc:keep` preserved regions in skeletons). |
| 3 | DB targets v1 | **Postgres only.** SQLite at most for tests. MySQL deferred to v2. |
| 4 | Implementation language | Go first for all generators and the DB gRPC runtime. Rust reserved as a future swap for the DB gRPC binary if a measured perf case appears. |
| 5 | Build-time vs. runtime | The product is a **compiler** (build-time). Runtime is thin: generated gRPC services + `protobridge`. No heavyweight framework to import. |
| 6 | Gen model | `gen/` is untouchable. Skeletons land in `src/` via `wc pull` on first scaffold; subsequent changes go through `wc diff` / `wc apply`. |
| 7 | Schema evolution | Compiler detects breaking proto changes semantically and warns before deploy. |

## Implementation Plan — Phases

Every phase ends with a pilot project migrating a real piece of hand-written code to the generated equivalent. No phase is "done" until a pilot dogfoods it.

### Phase 0 — Foundations (3–4 weeks)

- Proto extensions: `schema.meta`, `field.meta`, `validate`, `db.table`, `event.config`, `event.trigger`.
- Project config proto (`config/project.proto`).
- Generator framework: plugin-based, deterministic, idempotent, incremental.
- Golden-file test framework for generators.
- Monorepo scaffolding.
- **Deliverable:** "hello world" generator producing a `.go` file from a `.proto`, covered by golden tests.

### Phase 1 — Schema → SQL migrations (4–6 weeks)

- `db.table` extension (name, indexes, constraints, timestamps, soft-delete).
- Migration generator: proto diff → plain SQL up/down.
- Server CLI: `migrate up/down/status`.
- **Deliverable:** One pilot project drops `ent` migrations and runs on generated SQL migrations.
- **Success metric:** No regression in production migrations; dev loop "edit proto → migration ready" under 3s.

### Phase 2 — DB gRPC generator + Query DSL (2–3 months) — main pillar

- CRUD generator from model (Create/Get/Update/Delete/List with filters).
- Query DSL: grammar, parser, typechecker against models.
- Query DSL → Postgres SQL compiler.
- `transaction_id` plumbing.
- StorageService generator (Go).
- Integration tests against real Postgres.
- **Deliverable:** Pilot project has Storage layer fully generated — no hand-written SQL.
- **Success metric:** 80%+ of existing Storage handlers expressible in query DSL; the rest have a documented escape-hatch path.

### Phase 3 — Service skeleton + auto-event interceptor (4–6 weeks)

- Skeleton generator: handler stubs, DI (DB client, event emitter, logger, tracer).
- Event DSL proto extensions.
- Auto-emission interceptor from event DSL.
- OTel + health wiring.
- Developer CLI: `list` / `pull` / `diff` / `apply`.
- **Deliverable:** Pilot project has no hand-written `emit()` calls or DI wiring; only handler bodies.
- **Success metric:** LOC in `application/` wiring drops by ≥50%.

### Phase 4 — FE gRPC + Mapping DSL (1.5–2 months)

- Mapping DSL (same syntactic principles as query DSL).
- Compiler → Go.
- Multi-source mappings, aggregations, nested mappers.
- FacadeService generator.
- **Deliverable:** Pilot project has its FacadeServices generated.
- **Success metric:** FE-driven shape change → zero backend PR for recompile, DSL edit only.

### Phase 5 — Gateways (4–6 weeks)

- Audit existing `protobridge` for SSE / WS / MCP coverage gaps.
- OpenAPI generator from proto.
- AsyncAPI generator from event DSL.
- **Deliverable:** Pilot project exposes REST + SSE + WS + MCP with no hand-written gateway code.

### Phase 6 — JS/TS clients (1.5–2 months)

- Standard client from OpenAPI / AsyncAPI.
- Proto-over-HTTP binary client.
- Generated client-side validation (schema → zod-like or custom).
- Debug mode JSON switch.
- **Deliverable:** One UI on proto-over-HTTP, one on classic REST — both generated.

### Phase 7 — Admin UI generator (6–8 weeks)

- UI metadata → generated admin (list / detail / create / edit / filters).
- Filtering reuses existing query DSL constructs.
- Permissions vs. business rules: scope to review, possibly v2.
- **Deliverable:** Pilot admin 100% generated, zero hand-written admin pages.
- **Note:** A Django-admin-style UX is an achievable target; django-admin is a thin shell over models + metadata, which is exactly the input shape already available.

### Phase 8 — Infra generation (3–4 weeks)

- Dockerfiles from project config.
- k8s manifests.
- ENV scaffolding (defaults + examples) — connects to `docs/conventions-global/tooling.md`.
- **Deliverable:** `wc init` → runnable binary + docker-compose in under 60s.

### Phase 9 — Platform UI / Visual editor (2–3 months)

- Visual editor over DSL files.
- Git sync (reads/writes the same files the CLI does).
- Schema initiatives (pending changes view).
- Diff view.
- **Deliverable:** A non-developer (PM, analyst) can author a schema change without touching an IDE.

### Phase 10 — LSP + dev-loop polish (6–8 weeks)

- LSP server for Query / Mapping DSL: autocomplete, diagnostics, go-to-def.
- Incremental generation.
- Error messages with "did you mean" hints and source pointers.
- **Deliverable:** DSL IDE UX comparable to mainstream languages.

### Cross-cutting (runs throughout)

- Golden-file tests for every generator.
- Dogfooding on 1–2 pilot projects (not just one — avoids overfit).
- Public documentation of DSL syntax.
- Migration guides from existing hand-written code to generated equivalents.

## Out of Scope (v1)

- Custom business logic generation.
- Privacy policies.
- Complex conditional error strategies in DSL.
- Agent-based query optimization (declared as a future capability, not delivered).
- Rust implementation of DB gRPC (Go first; Rust on demand).
- MySQL and other DB dialects.

## Open Questions

Questions that remain deliberately unresolved and will be answered when the corresponding phase starts:

- **Transactions across services.** `transaction_id` covers Storage→Storage. Service→Service composition under a single transaction is undefined — saga vs. 2PC vs. scoped linearization needs a dedicated design pass.
- **Rich DB types.** `decimal`, `interval`, `jsonb`, discriminated unions and recursive types in proto need explicit extension design.
- **Schema versioning and backward compatibility.** How generated clients handle wire-level drift between deployed service versions.
- **Permissions model.** Where it lives (schema? separate DSL? out of scope entirely and delegated to handler code?).
- **Multi-tenant isolation.** Whether it is a platform concern or something the generated code is expected to opt into explicitly.

## Future / Agent Capabilities

The declarative nature of queries enables production-time agent optimization:

- Read-only agent with access to table stats.
- Approximate counts (`COUNT(*)` → estimated) for large tables.
- Automatic query re-planning at scale (e.g. switch from a single complex query to 1+N simpler queries above a threshold).
- Deadlock-potential analysis at schema definition time.
- Index suggestion based on declared query patterns.

## Relation to `docs/conventions-global/`

The conventions are **authoritative** over this spec. When the two disagree, the spec is wrong and gets updated. The compiler's job is to produce output that a human following the conventions would have written by hand — not to invent a parallel convention set.

Direct mappings:

| Convention | Spec element |
|---|---|
| `grpc.md` Storage / Service / Facade layers | DB gRPC / Standard gRPC / FE gRPC |
| `grpc.md` in-binary-by-default deployment | Generated wiring binds clients in-process by default |
| `eventbus.md` two-file skeleton + proto-as-source-of-truth | Event DSL output shape |
| `go.md` `ent` + schema migrations | Replaced by generated plain-SQL migrations |
| `go.md` `protobridge` for REST/MCP | Gateway layer extends `protobridge` |
| `structure.md` directory layout | `project/` layout above aligns with it |
| `tooling.md` ENV scoping | Phase 8 infra generation targets it |
| `process.md` `PROJECT_STAGE` | Generators respect the stage (e.g. no precommit hooks in skeleton) |
