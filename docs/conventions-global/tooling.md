# Tooling

The operator surface of the project: the `make` commands that drive everything, what `README.md` must contain, and how ENV variables are scoped across domains.

## Makefile

The application is controlled **exclusively via `make` commands**. Docker Compose is always a supported way to run it. A local language installation (Go, Rust, …) is a bonus — each `src<xx>` contains a script (`goenv.sh`, `rsenv.sh`, …) that looks for a local interpreter/compiler. Official Docker images are always the default.

### Core Targets

| Target | Description |
|---|---|
| `configure` | Basically `cp .env.defaults .env`, may do more depending on the project |
| `build` | Build everything in the project |
| `install` | `configure` + `build` |
| `up` | Starts everything needed. Non-code images (DB, …) run with `-d`, others without, so they can be easily restarted via `Ctrl+C` → `make up` |
| `test` | Runs unit tests |
| `audit` | Static analysis, coverage, security scanning |
| `seed` | Installs fixtures. Possible variants: `seed-min`, `seed-<n>`, … |
| `nuke` | Deletes all local state |
| `neoc` | `nuke` + `seed` (mirrors seed suffixes: `neoc-min`, `neoc-<n>`, …) |
| `schemagen` | Generates all schemas (protobuf, OpenAPI, …) |
| `makemigrations` | Generates SQL migrations via Go CLI `ent` schema diff (see [go.md](go.md) — Schema Migrations) |
| `migrate` | Applies generated migrations to the DB |
| `e2e` | Runs end-to-end tests |
| `loadtest` | Load/stability simulation of the entire platform |

---

## README.md

Must contain:

- Short project description (a few sentences)
- Initial setup guide
- Core `make` targets for day-to-day use
- **Quality Gates** section
- **E2E Tests** section
- Links to documentation (`docs/`) with short descriptions
- Description of internal processes (workflow, approval rules, …)

---

## ENV Variables — Scoping

The root `compose.yaml` starts all domains and services at once, so shared variables must have unique names. Each domain prefixes its ENV variables with its name.

### Rule

| Variable type | Prefix | Example |
|---|---|---|
| DB connections, ports, service addresses, feature flags | ✅ domain prefix | `ASSISTANT_POSTGRES_HOST`, `GEOPLATFORM_SERVICE_X_ADDRESS` |
| Universal production settings | ❌ no prefix | `ERROR_LOGGER`, `DEBUG` |

Domain prefix = domain name in uppercase + underscore (`ASSISTANT_`, `GEOPLATFORM_`, …).

### Universal Production Settings (no prefix)

These variables are identical across domains, they serve as a shared base for mixins/base classes in `lib/` and are **never prefixed**:

| Variable | Description |
|---|---|
| `ERROR_LOGGER` | Error reporting backend (`stderr` or `sentry:<dsn>`) |
| `DEBUG` | Debug mode |

### Infra Compose

`infra/<domain>/<stage>/compose.yaml` is isolated per-domain — a domain prefix is not technically required there, but variables **must keep the same names** as in the root `compose.yaml` so configuration is portable.
