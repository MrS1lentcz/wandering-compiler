# Event Bus

How a domain wires its asynchronous event surface: where the bus is defined, how subscribers are registered, and how publishers emit. The layout is identical in every project so a reader dropping into a new repository finds the bus by name rather than by search.

## The Framework — `protobridge`

The event surface is **not a per-project framework**. It is the Go-only **partial event sourcing framework** shipped by `protobridge` (see [go.md](go.md) — Proto-Driven Tooling). A domain does not implement a bus, does not define subscribe/publish abstractions, does not write a broadcast adapter; it consumes what `protobridge` provides and adds only the minimal integration points this file documents.

Specifically, `protobridge` owns:

- **The event annotation** — `option (protobridge.event) = { kind, subject, visibility, durable_group, ack_wait_seconds, max_deliver }` declared in `.proto`. The annotation *is* the contract; everything else is generated from it.
- **The typed Emit/Subscribe stubs** — `protoc-gen-events-go` generates one `Emit<Event>(ctx, bus, *pb.Event) error` and one `Subscribe<Event>(bus, group, handler) (…, error)` per event into `srcgo/domains/<domain>/gen/events/`. Handlers are typed — no payload marshalling, no string subjects at the call site.
- **The bus interface** — `events.Bus` plus the JetStream-backed implementation `events.NewJetStreamBus(ctx, events.JetStreamConfig{…})`. The domain's `eventbus.New` is a ~5-line wrapper that feeds its own subject list in. NATS/JetStream plumbing, WorkQueue retention, explicit ack, AckWait heartbeats, DLQ population on MaxDeliver — all `protobridge`.
- **The broadcast WS gateway** — the `ForgeEventsBroadcast` service in `.proto` is mounted by `protoc-gen-protobridge`; its `Stream` RPC is implemented by a generated server (`genevents.NewForgeEventsBroadcastServer(bus)`). The domain only registers it on its gRPC server.
- **Label-based routing** — `events.WithLabels(ctx, …)` / `events.LabelsFromContext(ctx)` / `events.LabelsToHeaders(…)`. Publishers tag events; subscribers and the WS gateway fan out per principal without touching the payload.

A domain therefore maintains a very small surface: a subject list, a `RegisterSubscribers` function, and the handlers the subscribers call. That small surface is what the rest of this file describes — but it is only meaningful on top of the `protobridge` framework. If you need bus behaviour that is not in `protobridge`, the change goes into `protobridge`, not into per-project glue.

## The `eventbus/` Package

Every domain that has asynchronous events carries an `eventbus/` package directly inside the domain directory — alongside `grpcapi/`, `application/`, `cmd/`, `ent/`. The package is a **skeleton of exactly two files**, no more:

```
srcgo/domains/<domain>/
└── eventbus/
    ├── eventbus.go      ← Bus construction + durable subject enumeration
    └── subscriber.go    ← RegisterSubscribers + per-event subscribe funcs
```

If a domain has no asynchronous events, the package does not exist. Do not create it preemptively.

### `eventbus.go` — Bus Construction

Contains three things and only three things:

1. **A `durableStreamSubjects` slice** — the complete enumeration of every subject published with `kind=DURABLE` or `kind=BOTH` in the domain's `.proto`, plus the DLQ wildcard sibling. The JetStream stream is created against this list, so pure `BROADCAST` subjects **must** be excluded (WorkQueuePolicy retention would accumulate them forever).
2. **A `New(url, streamName string) (events.Bus, func() error, error)` constructor** — thin wrapper over `events.NewJetStreamBus` that feeds `durableStreamSubjects` in. Returns the bus, its closer, and any error. Idempotent across pod restarts.
3. **A package doc comment** — describes the durable/broadcast split and explicitly calls out the "stream subjects enumerate DURABLE/BOTH only" rule so a reader editing `.proto` events knows the list must be kept in sync.

That is the whole file. It imports `protobridge/runtime/events` and nothing domain-specific.

### `subscriber.go` — Subscription Wiring

Contains a single public function and a flat list of small private helpers:

1. **`RegisterSubscribers(app application.Application)`** — top-level entry point. Called once from `cmd/<service>/main.go` after `application.New` has returned. Receives the full `Application` DI facade — never a subset, never raw dependencies. If `app.Bus() == nil`, returns immediately (no NATS configured — tests, CLI tools).
2. **One `subscribe<Event>` helper per subscription** — each is a ~5-line function that calls the generated `genevents.Subscribe<Event>` stub, passes a consumer-group string, and installs a handler. Handler errors are returned so the runtime can nack and redeliver per the proto policy. Subscription errors at wire-up time are logged as warnings; they never abort startup.

No other functions belong in this file. Handler bodies that grow past a few lines belong in a **domain-local orchestrator** (see [structure.md](structure.md) — Exception: Domain-Local Packages), and `subscriber.go` only calls the orchestrator's `On<Event>` method.

## Where Handler Logic Lives

`subscriber.go` is wiring. It never owns business logic. Every subscriber routes into one of three targets:

- **A gRPC RPC on a service in this domain** — the subscriber calls `app.Clients().<Service>().<RPC>(ctx, req)`. This is the default for any event whose reaction is already a handler the UI or another service would trigger: the subscriber is just a second caller of the same RPC. It makes the backend structurally identical to any future out-of-process consumer — the handler does not know whether the call came from the bus or a client.
- **A domain-local orchestrator** — for events that drive a state machine spanning multiple RPCs (validation cycles, repair loops, multi-step spawn pipelines). The orchestrator lives in its own package (`validation/`, `sessionspawn/`, …) and exposes one `On<Event>` method per subject. It reaches the DB exclusively through Storage clients — no direct `ent` access, ever. See [structure.md](structure.md) — Exception: Domain-Local Packages.
- **A single domain-local consumer** — a small stateless type whose only job is one side-effect (spawn a container, post to GitHub, emit a follow-up event). Same package layout as an orchestrator, but smaller surface.

A subscriber **never** reaches directly into a gRPC server struct, never takes a handler as a parameter, and never holds the raw DB handle.

## Publishers

Publishers live wherever the business logic that emits the event lives — typically a `grpcapi/business/` handler or a domain-local orchestrator. They use the generated `genevents.Emit<Event>(ctx, app.Bus(), &pbev.Event{…})` stub from `gen/events/`.

**Rules:**

- Publishers reach the bus through `app.Bus()` — never via a parameter threaded through the constructor. The DI facade already exposes it.
- `app.Bus()` returns `nil` when the process runs without NATS (tests, CLI tools). Code that unconditionally publishes must nil-check; code on the hot path typically does not bother, because startup has either configured the bus or the test's code path does not reach the publish.
- Labels (`events.WithLabels`) carry principal-scoping metadata for broadcast fan-out. The broadcast adapter reads them from ctx and forwards as message headers; subscribers and the WS gateway route on them.
- Events are emitted **after** the state change is committed — never before — so a redelivery observes the world the event describes.

## Event Definitions — Proto as the Source of Truth

Every event is a message in `proto/domains/<domain>/events/<domain>_events.proto` annotated with `(protobridge.event)` (annotation schema owned by `protobridge` — see the framework section above). The fields the domain fills in:

- **`kind`** — `DURABLE` (JetStream WorkQueue), `BROADCAST` (core NATS fan-out), or `BOTH` (emitted on both transports).
- **`subject`** — three-token name: `<project>.<category>.<verb>` (e.g. `forge.session.spawn_requested`). Four tokens are reserved for DLQ siblings (`<subject>.dlq`).
- **`visibility`** — `PUBLIC` (surfaced by the broadcast WS gateway to clients) or `INTERNAL` (consumed only inside the backend).
- **`durable_group`** — default consumer group name for `SubscribeX`. Individual `subscribe<Event>` helpers may pass a different group to register a second consumer.
- **`ack_wait_seconds`** + **`max_deliver`** — per-event redelivery policy (required for `DURABLE`).

Alongside events, each project declares one broadcast service — conventionally `<Project>EventsBroadcast` — whose `Stream` RPC is auto-mounted by `protoc-gen-protobridge`. The envelope's `oneof` lists exactly the `PUBLIC` events; `INTERNAL` events are excluded by construction. The domain does not implement the RPC — it only registers the generated server on its gRPC surface.

Adding a new event is therefore:

1. Add the `message` + `option (protobridge.event)` to `<domain>_events.proto`.
2. If the event is `PUBLIC`, add a branch to the `ForgeEventEnvelope` oneof.
3. Run `make schemagen` — `gen/events/` and the broadcast server are regenerated.
4. If `kind=DURABLE` or `kind=BOTH`, append the subject to `durableStreamSubjects` in `eventbus/eventbus.go`.
5. Emit from the producing handler (`genevents.EmitX`) and subscribe from `subscriber.go`.

Forgetting step 4 is the most common mistake: the subscriber compiles, the publish succeeds, but JetStream rejects the message because the stream has no matching subject. Keep the proto annotation and the subject list in the same commit.

## DLQ

Dead-letter subjects are a uniform `<subject>.dlq` sibling of every durable subject. The stream captures them via a single wildcard — typically `<project>.*.*.dlq` — so no per-event DLQ entry is needed. Consumers of DLQ content are tooling (inspection, replay); they are not subscribed from `subscriber.go`.

## Wiring in `cmd/<service>/main.go`

The daemon binary constructs the bus, passes it to `application.New` via `application.WithEventBus(bus)`, then — after `New` returns — calls `eventbus.RegisterSubscribers(app)`. No other sequence is correct: subscribers depend on the Application facade, which itself may depend on the bus (publishers wired into handlers).

```go
bus, closer, err := eventbus.New(cfg.NatsURL, "<stream-name>")
// …
app, err := application.New(cfg, …, application.WithEventBus(bus))
// …
eventbus.RegisterSubscribers(app)
```

`RegisterSubscribers` is also the point where domain-local orchestrators and consumers are **constructed** — they are stateless w.r.t. subscriptions and lifecycle, so the constructor is trivial (`validation.NewOrchestrator(app)`, `sessionspawn.NewSpawner(app)`). The returned value is held in a local variable inside `RegisterSubscribers` and captured by the `subscribe<Event>` helpers. There is no DI module for them — they are not startup infrastructure, they are subscribers.

## Testing

Unit tests for handler logic never touch the bus. They:

- Call the orchestrator's `On<Event>` method directly with a constructed `*pbev.Event` and a test context that holds a `ClientsModule` backed by in-process Storage servers (see [go.md](go.md) for the bufconn pattern).
- Pass `app.Bus()` as `nil` when the test does not care about follow-up publishes; the production code already nil-checks.
- When a test does want to assert a follow-up publish, it injects a bus fake that records `Publish` calls — never a real JetStream connection.

End-to-end tests use the real bus spun up by `compose.yaml`. Subscription wiring is exercised by starting the full `cmd/<service>/main.go` — never by poking `RegisterSubscribers` from a unit test.

## Summary

| Piece | File | Rule |
|---|---|---|
| Proto event + annotation | `proto/domains/<domain>/events/<domain>_events.proto` | Source of truth |
| Generated stubs | `srcgo/domains/<domain>/gen/events/` | Never hand-edited |
| Bus construction | `eventbus/eventbus.go` | One constructor, one subject list |
| Subscription wiring | `eventbus/subscriber.go` | `RegisterSubscribers(app)` + `subscribe<Event>` helpers only |
| Handler logic | domain-local package or gRPC RPC | Never in `subscriber.go` |
| Startup | `cmd/<service>/main.go` | Bus built → `application.New` → `RegisterSubscribers(app)` |
| DB access from subscribers | Storage clients only | Same invariant as `grpcapi/business/` |

The skeleton is small on purpose: everything structural sits in two files so a reader new to the repository learns the full event topology in under a minute.
