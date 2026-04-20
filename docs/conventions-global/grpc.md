# gRPC Service Layers

How a domain's gRPC surface is structured (up to three named layers) and how those services are deployed — in one binary by default, split over TCP only when scaling actually demands it.

> On the Go side, REST and MCP exposures of a gRPC service are generated zero-code from proto annotations via `protobridge` (see [go.md](go.md) — Proto-Driven Tooling). The layer structure below describes the gRPC contract; `protobridge` projects it onto REST/MCP automatically.

## Service Layers

A domain's gRPC surface is not required to be flat. It is structured as up to three layers, each with a **mandatory suffix** in the service name. The suffix is what prevents name collisions — the same noun (e.g. `Task`) can appear in all three layers simultaneously as `TaskService`, `TaskStorageService` and `TaskFacadeService`.

### Layer 1 — Storage service (`<Name>StorageService`)

A thin, focused service whose **only** responsibility is business logic directly over the database.

- **Only place the DB is touched** — every SQL query, every `ent` call, every transaction in the domain lives in a Storage handler. DB access from anywhere else (`Service`, `FacadeService`, `lib`, `x`) is a bug. See [structure.md](structure.md) — Implementation Architecture.
- **No validation** — validation is the caller's responsibility (the layer above). The Storage service trusts its input.
- **No cross-service calls** — it touches its own DB and nothing else.
- **Designed to be bent** — because it is minimal and assumption-free, callers can compose it into specific commands, batch operations, or alternative flows without fighting validation or side-effects baked in at the wrong level.

Use when the DB-level operations on a model are worth exposing as a reusable building block — typically for any non-trivial aggregate.

### Layer 2 — Normal service (`<Name>Service`)

The standard gRPC layer — ordinary RPCs with ordinary names. This is the layer clients (including the UI) normally call. It contains validation, authorisation, orchestration of light side-effects (e.g. emitting an event), and composition of business rules.

A `<Name>Service` handler is a **bridge**, not the place where real work happens. DB access is delegated to Storage; meaningful computation is delegated to `lib`/`x` (see [structure.md](structure.md) — Implementation Architecture). A handler that grows past "call Storage, call `lib`, map the result" is a sign its core logic belongs elsewhere.

A `<Name>Service` **may or may not** delegate to a `<Name>StorageService` — that is an implementation choice. Small services with no Storage layer stay entirely here, but the same bridge rule applies: the handler stays thin and the real logic lives in `lib`/`x`.

### Layer 3 — Facade service (`<Name>FacadeService`) — optional, rare

A third, **optional** layer that orchestrates multiple gRPC calls into a single response. Its archetypal use case is a frontend that wants to assemble a larger composite object from several underlying services in one round-trip.

- **Strongly optional** — most projects will never have one.
- **FE-driven** — the shape of a Facade is dictated by a concrete consumer (typically the FE team), so Facades tend to be specific and are not generalised for reuse.
- Consider a Facade only when a real client is making the same N calls every time and the round-trip cost actually matters.

### Naming Summary

| Layer | Suffix | Example | Purpose |
|---|---|---|---|
| Storage | `StorageService` | `TaskStorageService` | Raw DB business logic, no validation |
| Normal | `Service` | `TaskService` | Standard RPCs with validation and rules |
| Facade | `FacadeService` | `TaskFacadeService` | Optional aggregator for composite client responses |

The three can coexist for the same noun without collision — that is the whole point of mandating the suffix.

---

## Handler Implementation — No Wrappers

Everything inside `grpcapi/` — `storage/`, `business/`, `facade/` alike — is the **implementation** of a service whose contract lives in `.proto`. `grpcapi` is the gRPC API; code inside it *is* the gRPC API. There is no dispatch layer above the handler, no wrapper package beneath the client. No hand-written Go type wraps a gRPC server to re-export its RPCs, and no Go type wraps `app.Clients().X()` to re-export them with slightly renamed methods.

This shapes how both sides of every call are written:

- **One RPC, one Go method.** The RPC method's body is the implementation. No `*Internal` helper next to it, no "the real one is over there" delegation. If a handler does nothing but forward to another function, delete the forwarder and put the body in the handler.

- **In-package calls invoke the handler directly.** A handler that needs another RPC in the same service calls `s.OtherRPC(ctx, &proto.OtherRequest{…})` with the proto request. The compiler sees an ordinary method call — no framework, no network, no overhead. That is the only way to reuse RPC behaviour inside a service.

- **Cross-domain calls go through `app.Clients().X()`.** Reaching a service in another domain always uses the generated gRPC client directly — never via a caller-side type that wraps it. In-binary deployment makes it a loopback call, split deployment flips it to TCP, and the call site does not change (see Deployment below).

- **Shared sub-pieces are unexported functions in the same package.** Parsing, mapping, validation — anything that would otherwise tempt a second exported method — lives as `parseX(...)`, `mapX(...)`, `validateX(...)`. They never carry an RPC-shaped name (no `*Internal`, no `*Impl`, no `do*`).

- **Files are split by subject, not by "wrapper vs. logic".** Within `grpcapi/business/` a handler file is named after its service — `task_service.go` for `TaskService` — and contains the full RPC bodies. A second file next to it (`task.go`, `task_logic.go`, `task_impl.go`) holding the "real" code while `task_service.go` becomes a thin forwarder is exactly the antipattern this section exists to prevent.

### The narrow exception

A wrapper over the gRPC surface is acceptable **only** when something concrete cannot be expressed any other way and the price of not wrapping is worse than the price of wrapping: a measurable bottleneck that genuinely needs a concentration point, an observable hot path where default logging produces output nobody can read, behaviour the gRPC layer genuinely does not provide. "It feels cleaner" or "I prefer a nicer API" are not in the exception.

When justified, the wrapper is named after the reason it exists (e.g. `ratelimit`, `tracer`), is scoped to the single RPC or client that requires it, and carries a comment at the top of the file stating which bottleneck or observability gap forced it. A wrapper without that justification is a wrapper that should be deleted.

### Why

At scale — hundreds of services, gRPC's built-in load balancing doing real work — every extra Go indirection is measurable overhead and a wasted stack-trace frame. More importantly, a wrapper-vs-implementation split inside one package is legible only to the person who wrote it and actively lies about what the code does.

> This rule sits *above* the storage / lib+x / business split (see [structure.md](structure.md) — Implementation Architecture). That split says where the bridge goes; this rule says the bridge has no second layer inside its own package and no second layer on the caller's side either.

---

## Deployment — In-Binary by Default, TCP Only When Needed

Splitting a domain into several gRPC services (Storage / normal / Facade) is valuable for **code boundaries** — it is not automatically valuable for **deployment boundaries**. Running every service as its own process up-front means paying real TCP overhead on every internal call before the project has any reason to scale out.

**The default is: all services of a domain run in the same binary.** Write them as proper gRPC services — standard proto definitions, standard server implementations — but at wiring time, clients of those services are bound to **in-process implementations** (no dial, no ports, no network). The service boundary lives in the code; the process boundary does not.

**When scaling actually demands it**, split the binary: the extracted service gets its own `cmd/<service_name>/main.go` and its own deployment, and the clients in the remaining binary are reconfigured to reach it over TCP. The change is confined to three places:

- the **registration layer** — which `cmd/<name>/main.go` starts which gRPC servers
- the **wiring in `application/`** — in-process client vs. dialled TCP client (see [go.md](go.md))
- the **configuration** — service addresses in env (see [tooling.md](tooling.md))

Crucially, **handler implementations, storage services and business logic do not change** — the proto contract is the same, the server-side code is the same, only the client-side binding flips from in-process to TCP. Because the decision is purely a wiring decision, the cost of starting in-binary and splitting later is near-zero; starting split is a cost paid every day the project runs.

> Write services as if they were separate. Run them together until there is a concrete reason not to.
