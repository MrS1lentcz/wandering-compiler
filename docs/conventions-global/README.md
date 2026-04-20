# Project Dev Conventions

Global conventions shared across projects in this class. Split into thematic files so only the relevant subset is loaded when working on a specific part of the project.

## Files

- [structure.md](structure.md) — Project directory layout, domain and module boundaries, volumes, and the Network/Backoffice technology decisions that shape deployment.
- [process.md](process.md) — Project lifecycle: `PROJECT_STAGE`, CLAUDE.md format, spec folders, changelog, branch conventions and precommit hooks.
- [tooling.md](tooling.md) — Operator surface: Makefile targets, README.md contents, ENV variable scoping.
- [go.md](go.md) — Go (`srcgo`) conventions, `gox` foundation, domain `application/` pattern, `cmd/` classification, CLI, and the `ent` / schema migrations workflow.
- [grpc.md](grpc.md) — gRPC service layers (Storage / Service / Facade) and the in-binary-by-default deployment convention.
- [eventbus.md](eventbus.md) — Asynchronous event surface: the `eventbus/` two-file skeleton, subscriber wiring, publisher rules, and the proto-as-source-of-truth event definitions.
- [ui.md](ui.md) — UI (`ui/`) stack, web-first policy, Expo Router layout, Docker/compose integration, mobile distribution.
- [quality.md](quality.md) — Resilience (timeouts, retries, Sentry), quality rules (language, structure, coverage, transactions, locking), and `acceptance-criteria.yaml`.

## How to use

Load only the file relevant to the current task. Cross-file references use the short form `(see [grpc.md](grpc.md))` — no numeric IDs, no stable anchors beyond section headings.
