# wandering-compiler

A declarative compiler. Developer declares schemas, DB models, queries, events, and mappings (proto + custom DSL); the tool generates compiled gRPC binaries, DB access layer, plain-SQL migrations, gateways (REST/SSE/WS/MCP), JS/TS clients, OpenAPI/AsyncAPI, Docker/k8s/ENV, and a Django-style admin. The only hand-written code is the body of gRPC handler methods. This project is the **productization of conventions already practiced by hand** in this class of projects — see `docs/conventions-global/` [→ D2].

Full spec: `docs/tech-spec.md` [→ D1].

## Documentation Map

| ID | Path | Purpose |
|---|---|---|
| D1 | `docs/tech-spec.md` | Technical specification: architecture, DSL strategy, three-layer gRPC model, generated outputs, strategic decisions, phased implementation plan |
| D2 | `docs/conventions-global/` | **Mandatory** — shared dev conventions across projects in this class. Every design decision and generator output must align with them. |
| D2.1 | `docs/conventions-global/grpc.md` | Storage / Service / Facade gRPC layering — the pattern the compiler generates |
| D2.2 | `docs/conventions-global/eventbus.md` | Event system shape — the event DSL targets this contract |
| D2.3 | `docs/conventions-global/go.md` | Go conventions (`srcgo`, `gox`, `application/`, `protobridge`, `ent`) — generated Go output conforms |
| D2.4 | `docs/conventions-global/structure.md` | Project directory layout, domain and module boundaries |
| D2.5 | `docs/conventions-global/process.md` | `PROJECT_STAGE`, CLAUDE.md format, specs folders, changelog, branch rules |
| D2.6 | `docs/conventions-global/tooling.md` | Makefile targets, README contents, ENV scoping |
| D2.7 | `docs/conventions-global/quality.md` | Resilience, language rules, transactions, locking, acceptance criteria |
| D2.8 | `docs/conventions-global/ui.md` | UI stack, Expo Router layout, mobile distribution |

## PROJECT_STAGE

File **does not exist** → project is in the **skeleton** stage [→ D2.5]. No precommit hooks. No test requirements. Push to `main` allowed. Claude does not warn about missing tests or docs updates. Transition to `poc` when the first generator runs end-to-end against a pilot project.

## Non-negotiable rules

1. **`docs/conventions-global/` is authoritative.** When it disagrees with `tech-spec.md`, the conventions win and the spec gets updated — not the other way around.
2. **Generated output must be indistinguishable from well-written hand-authored code following the conventions.** Storage/Service/Facade naming, in-binary-by-default deployment, two-file eventbus skeleton, `protobridge` integration — every generator respects them.
3. **Escape hatches are mandatory.** Every DSL and generator has a documented fall-back to hand-written code (raw SQL block, Go function, `// wc:keep` regions). A generator without an escape hatch is not ready to ship. See `tech-spec.md` Strategic Decisions [→ D1].
4. **The product is a compiler, not a framework.** Runtime is thin (generated gRPC + `protobridge`). There is no heavyweight runtime library to import. Build-time is where everything happens.

## Known Issues

Skeleton stage is now **populated with iter-1 code**. Tracked deviations
from `docs/conventions-global/`:

- **Core functions over 50 LOC** (`quality.md §Code Structure`) — 18
  functions above the soft cap remain, each registered in
  [`docs/core-functions.md`](docs/core-functions.md) with invariant +
  rationale. Convention permits >50 LOC as core functions *provided*
  they carry special documentation + 100% coverage. The documentation
  half is done; the 100% coverage half is Phase B of the iter-1
  close-out sweep (see `docs/iteration-1-coverage.md`).
- **Makefile surface** ✓ **resolved 2026-04-25.** All conventional
  targets present; `up / seed / neoc / migrate / makemigrations /
  loadtest` keep their conventional name + emit a one-line
  explanation pointing at the right surface (compiler is a CLI
  tool, not a service stack). `audit` target wires `go vet` +
  `cover-all.sh` cross-package coverage report.
- **Shared lib tier absent** (`go.md §srcgo Structure`) — `srcgo/lib/`
  and `srcgo/x/` don't exist yet. `naming/`, `writer/`, `diag/` look
  domain-general and are candidates to lift when a second consumer
  appears (platform + deploy client per
  `project_three_component_platform.md` memory). Deferred to iter-2+.

Not applicable at skeleton stage (no handlers yet): grpc.md three-layer
model, eventbus.md two-file skeleton, protobridge, ent/schema/,
locking/acceptance-criteria.yaml, `PROJECT_STAGE` precommit hooks, auth
resilience rules. These become active when the first real service
domain lands.
