# Quality

Cross-cutting quality and non-functional rules: resilience (timeouts, retries, Sentry), code and data-integrity rules, and the `acceptance-criteria.yaml` format that feeds e2e tests.

## Resilience

### Timeouts, Retry and Backoff

Every network connection and every async task must have explicitly configured:

| Parameter | Required | Note |
|---|---|---|
| **Timeout** | ✅ always | According to connection type (DB, queue, external API, object storage, …) |
| **Retry** | ✅ when it makes sense | Not all operations are retriable — see *Where retry applies* below |
| **Backoff** | ✅ when retry is used | Exponential or other — **max backoff** is a cap on the interval between attempts, not on the number of attempts |
| **Error policy** | ✅ always | What gets reported to Sentry and when |

> HTTP/gRPC **ingress** (the public surface exposed by the service — Read/Write/Idle timeouts, keep-alive, request limits) is not in scope here. It is owned by `protobridge` (see [go.md](go.md) — Proto-Driven Tooling) and is not configured per project.

Retry is **infinite by default** — the application must be able to recover on its own even after a long outage (e.g. 24h server downtime). Max backoff only limits how long to wait between attempts.

#### Where retry applies

- **Startup dependencies** (DB connect, gRPC client dial, object-storage reachability) — infinite retry; the service must eventually recover from any outage
- **Outbound calls to other gRPC services / external APIs** — retry when the call is **idempotent** or the request carries a server-side dedup key
- **Object storage reads/writes** — retry (operations are idempotent at the object level)
- **Background workers and reconcilers** — retry indefinitely; the next tick always picks up whatever the previous one failed to complete

#### Where retry does NOT apply

| Case | Reason |
|---|---|
| Synchronous request handlers | The caller is blocked; silent retries hide errors and blow deadlines — surface the failure and let the caller decide |
| Authentication / token issuance | Retrying rejected credentials is a security risk |
| Non-idempotent one-shots without a dedup key | Duplicate side-effects are worse than a visible failure |
| Periodic tasks (NATS, cron, …) | A skipped tick has a clear meaning; the next tick is the recovery mechanism |

#### Backoff configuration

- **Exponential with jitter** — add ±20% jitter to the computed interval to avoid thundering-herd when many clients recover from a shared outage simultaneously
- Every retry loop **must respect the parent `context`** — cancel immediately when the context is cancelled (request deadline, shutdown signal). A retry loop that ignores context is a bug.

### Sentry Error Reporting

- **Every error that is not noise must be reported to Sentry**
    - Noise = transient failure that resolved itself (connection drop where retry succeeded, expected 4xx client errors, expected terminal states of long-running entities)
    - Not noise = retries exhausted, unexpected 5xx, panic recovered by middleware, startup errors, non-retriable external failures
- After all retry attempts are exhausted → **always report to Sentry** without exception
- **Intermediate retry attempts are not reported** — only the final exhaustion. Reporting every attempt drowns the signal.

### Typed Config and Bootstrap Validation

- Every service must have a **typed config** loaded at startup
- Config is **validated in the bootstrap phase** — before the service reaches a healthy state
- If any required value is missing or invalid → **the service will not start**
- Behaviour on config error:

| Situation | Behaviour |
|---|---|
| `ERROR_LOGGER` invalid format | Log error + exit |
| Other required value missing/invalid | Report error + exit |

- `.env.defaults` must contain sufficient defaults for basic application operation — after `cp .env.defaults .env` the application must start

---

## Quality Rules

### Code Language

- **All code must be written in English** — applies to all languages (Go, Rust, Python, …)
- This includes: variable names, function names, type names, struct field names, comments, log messages, error messages, and any other text appearing in source code
- Documentation files (`docs/`, `README.md`, `CLAUDE.md`, …) may be written in the team's language of choice

### Code Structure

- Every entry file (`main.rs`, `main.go`, `main.py`) contains **only one `main` function** — no additional function definitions
- Aim for small functions: **ideal < 20 lines, max < 50 lines**
- Functions over 50 lines must be documented as **core functions** — special description in the documentation + 100% coverage

### Coverage

- Applications marked as **critical** → **100% unit test coverage**
- Recommended skipping of LLVM false positive artefacts:

```
skip = src in ('}', '});', '}});', 'return;')
    or src.startswith((
        '_ => panic!',
        'Err(_) => break',
        'Err(_) => return',
        'other => panic!',
        'eprintln!',
        'std::process::exit'
    ))
```

- Applications with an API → **all handlers must be in their own package** — 100% coverage automatically applies to APIs (all handler branches must be tested)

### Data Integrity

#### Transactions

- **Every data mutation must happen inside a transaction** — primarily applies to HTTP handlers but also anywhere else in the code
- Transactions **must not contain any TCP/IP calls** (HTTP requests, DB queries outside the transaction, message queue, …)
- Transactions **must not contain any heavy operations** — light mapping or simple algorithms are fine, heavy computation is not

#### Race Conditions — SELECT FOR UPDATE

- **Every mutation must handle race conditions**, primarily via `SELECT FOR UPDATE`
    - Applies to **every mutation RPC** — in Go projects this means every mutation method on a `<Name>StorageService` (see [grpc.md](grpc.md)), and every mutation-carrying method on `<Name>Service` when no Storage layer exists
    - Pure reads (non-mutation RPCs) do not lock
- **Exactly one resource is locked per mutation.** If an operation logically needs several rows to be serialised, lift the lock to a **higher-level aggregate** that encompasses them (e.g. lock the owning `Workspace`, not the two child `Task` rows).
    - The higher the aggregate, the fewer lock records exist and the smaller the surface for lock-ordering / deadlock problems
    - A mutation never acquires a second row-level lock inside the same transaction
- **Exceptions may use a different lock type** (advisory lock, `SELECT FOR NO KEY UPDATE`, application-level distributed lock, …) when the semantics require it — every exception must be **explicitly listed** in the project's `docs/locking-strategy.md` with its reason
- Every project must define its own **locking strategy**, captured in `docs/locking-strategy.md`. The file is **authored inside the project**, not provided by this conventions library — the conventions only require that the file exist and that every mutation in the codebase maps to one of its entries.
- The strategy defines a **narrow set of models that get locked** — typically:
    - `User`
    - `Organization`
    - the main aggregate model of the domain (e.g. `Account`, `Workspace`, …)
- For each mutation surface, `docs/locking-strategy.md` must state the **resource type** that is locked, the **lock mode** (`SELECT FOR UPDATE` by default; any other mode requires a reason), and the **key** used to identify the locked row (usually the aggregate ID)
- The intent is to prevent problems with **lock ordering** and **cross-locking** — the fewer models are locked, the lower the deadlock risk

> ⚠️ `docs/locking-strategy.md` must exist before the first mutation is implemented in the project.

### Audit Tools

| Language | Tools |
|---|---|
| Go | `golangci-lint`, `go vet`, `staticcheck`, `trivy`, `govulncheck` |
| Rust | Equivalent tools (`cargo clippy`, `cargo audit`, …) |

---

## Acceptance Criteria (`acceptance-criteria.yaml`)

The file serves as a **basis for generating e2e tests and as a production checklist**. It exists in two variants — central (`docs/acceptance-criteria.yaml`) and per-domain (`docs/<domain>/acceptance-criteria.yaml`). The per-domain file is only written when a domain or component has specifics that do not belong in the central file.

### File Structure

The file is divided into two parts:

**Part 1 — Technical requirements** (`per-component`, must always hold)

Describes system invariants — what must always be true, regardless of the specific scenario. Each requirement has:

```yaml
<component_name>:
  <requirement_name>:
    requirement: "Brief description of what must hold"
    detail: |
      Detailed description — why, how, edge cases.
      Can be multi-line.
    status: "implemented" | "partial" | "not_implemented"
    impl: "Where and how it is implemented (file, pattern)"  # if implemented/partial
    todo: "What remains"                                      # if partial/not_implemented
    tests: "Link to tests or their count"                     # optional
```

Status model for `status`:

| Status | Meaning |
|---|---|
| `implemented` | Done, covered by tests |
| `partial` | Partially implemented — `todo` is required |
| `not_implemented` | Does not exist yet — `tests` field contains the list of tests that must pass |

**Part 2 — Scenarios** (mutation + expected UI/system state)

Each scenario describes one mutation and what should happen — in the UI, in the HTTP layer, in other components. Used directly as input for e2e tests.

```yaml
scenarios:
  - name: "Brief action name"
    mutation: "HTTP method + endpoint (or description of a system event)"
    http:
      target_app: "before → during → after"  # during only if a transitional state exists
      other_apps:  "before → after"
    precondition: "Optional — required state before the mutation"
    expect_error:  "Optional — if the mutation should fail (e.g. 409)"
    expect:
      <ui_component>: "<legend>"
```

### HTTP State — Format

```yaml
# Without transitional state:
http: {target_app: "200 → 200", other_apps: "200 → 200"}

# With transitional state (during):
http: {target_app: "000 → 503 → 200", other_apps: "200 → 200"}

# No app context (auth, org operations):
http: {target_app: "- → -", other_apps: "- → -"}
```

| Code | Meaning |
|---|---|
| `200` | App running, healthy, serving traffic |
| `503` | Starting/deploying — health check not yet passing |
| `000` | Unreachable — no route, app does not exist, throttled |
| `-` | Not applicable — no app context |

### UI Component State Legend

| Symbol | Meaning |
|---|---|
| `-` | No change |
| `U` | Updated — data changed, UI reflects it |
| `E` | Empty state — no data |
| `D` | Disabled — component inactive/locked |
| `N` | New item — a record was added |
| `R` | Removed — a record disappeared |
| `ERR` | Error state — displaying an error |
| `LOAD` | Loading — operation in progress |
| `B` | Banner — notice/warning |
| `H` | Hidden — component not visible |

### Rules for Writing Scenarios

**What constitutes one scenario:** one atomic mutation — one HTTP request or one system event (agent event, sweeper, background job). If an action triggers a chain (e.g. deploy → agent pull → health check → LB reconcile), the scenario describes the final state after the entire chain, not an intermediate state.

**Naming:** the scenario name is a human-readable description from the user's or operator's perspective — not an endpoint name. `"Trigger first deploy"` not `"POST /deployments"`.

**System events** (not HTTP) are described in `mutation` as free text — e.g. `"Agent detects container exit, triggers backoff restart"`.

**Negative scenarios** — if the mutation should fail, add `expect_error` with the HTTP code and reason. `expect` then describes the state after rejection (typically all `-`).

**YAML anchors** — repeated blocks are defined as an anchor and referenced via `*` to avoid copy-paste:

```yaml
_defaults:
  no_change_app_detail: &no_change_app_detail
    header_panel: "-"
    resources_tab: "-"
    # ...

scenarios:
  - name: "..."
    expect:
      app_detail: *no_change_app_detail
```

**`expect` granularity:** list all components — including those with no change (`-`). Always list hidden components (`H`) explicitly — it is important to say a component should not be visible, not silently skip it.

### Where the File Lives

```
docs/
├── acceptance-criteria.yaml        # Central — cross-cutting, shared scenarios
└── <domain>/
    └── acceptance-criteria.yaml    # Per-domain — only if it has specific requirements or scenarios
```

The per-domain file **does not duplicate** the central one — it extends it. Technical requirements for per-component entries (`node_agent`, `control_plane`, …) belong in the file of the domain that owns that component.
