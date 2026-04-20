# Process

Project lifecycle: stages, the `CLAUDE.md` format Claude reads at every session start, feature specifications, the changelog, and the branch-and-precommit rules that govern regular development.

## Project Stage

The project goes through three stages that control which rules and hooks are active.

### Stages

| Stage | `PROJECT_STAGE` file | Description |
|---|---|---|
| **skeleton** | file does not exist | Clean structure, no rules, no hooks |
| **poc** | `poc` | Hooks active, push to main allowed, e2e tests not required |
| **production** | `production` | All rules apply, push to main forbidden |

### The `PROJECT_STAGE` File

- Lives in the project root, git-tracked
- Contains a single line: the stage name (`poc` or `production`)
- **Absence of the file = skeleton stage** — project in early days, no rules
- Readable from scripts: `cat PROJECT_STAGE`

### Stage Transitions

Transitions are **one-way** — you cannot go back to a previous stage without a deliberate team decision.

```
skeleton → poc → production
```

Each transition = a standalone commit changing `PROJECT_STAGE`. This creates a clear history of when the project reached each stage.

### What Changes at Each Stage

| Rule | skeleton | poc | production |
|---|---|---|---|
| Claude precommit hooks | ❌ | ✅ | ✅ |
| Push to main | ✅ | ✅ | ❌ |
| E2E tests required | ❌ | ❌ | ✅ |
| All quality rules | ❌ | ✅ | ✅ |

### CLAUDE.md and PROJECT_STAGE

CLAUDE.md must explicitly mention `PROJECT_STAGE` and its role. Claude reads the file at the start of every session and adapts its behaviour to the current stage — for example, in the `skeleton` stage it does not warn about missing tests or docs updates; in `production` it refuses to push to main.

---

## CLAUDE.md

Contains:

- Brief application description with references to detailed documentation in the style `[→ D2.1]` (regex-friendly format)
- Feature overview with links to specific files in `docs/`
- Reference to `PROJECT_STAGE` and a description of how Claude behaves based on it
- **Known issues / TODO list** — tracked violations, tech debt, and unimplemented spec items (see below)

### Known Issues / TODO List

Every CLAUDE.md must contain a `## Known Issues` section. Its purpose is to give Claude (and developers) an immediate picture of what is intentionally deferred, broken, or incomplete — without having to read the entire codebase.

**What belongs here:**

| Category | Examples |
|---|---|
| Convention violations | No transactions, Czech text in Go source, function >50 lines |
| Tech debt | Missing tests, unimplemented locking, legacy `cmd/seed/` |
| Unimplemented spec items | PR merge tracking cron, GitHub API integration |
| Spec/doc gaps | Missing fields in data model table, wrong endpoint list |

**Format:**

```markdown
## Known Issues

Each item links to the relevant doc using the `[→ D2.1]` reference format defined in
the doc map at the top of CLAUDE.md. Items are removed when resolved.

- [ ] **Short label** — one-line description. [→ D2]
```

**Rules:**

- Every item found during a review or code session that cannot be fixed immediately **must** be added here.
- Items are removed (or marked done) when actually resolved — not when a workaround is applied.
- The list is not a backlog — it is a snapshot of known deviations from the current conventions and spec.
- Claude reads this list at the start of every session and must not introduce new work that conflicts with an open item without acknowledging it.

---

## Feature Specifications

Larger features that require planning before implementation get their own spec folder:

- **Location:** `docs/specs/<initiative-name>/`
- **Naming:** kebab-case, descriptive noun phrase (e.g. `task-first/`, `auth-refactor/`)
- **Files per folder:**
    - `specification.md` — design, goals, data model, API changes
    - `implementation_plan.md` — step-by-step implementation plan, test strategy
- **When to create:** Any `feat/` that involves a non-trivial design decision, multiple moving parts, or coordination across domains. Single-file changes or obvious extensions do not need a spec.

### Referencing Specs from CLAUDE.md Known Issues

Since one spec = one folder, reference the folder directly:

```markdown
- [ ] **Task-first UI** — sessions must expose a task list before terminal. [→ docs/specs/task-first/]
```

Do **not** assign a `Dx` ID to spec folders — the `Dx` table is reserved for stable, project-wide reference docs. Spec folders are initiative-scoped and short-lived.

---

## Changelog

Projects that use **audevio.com** for session tracking maintain a changelog at `docs/changelog/CHANGELOG.md`.

### Format

```markdown
# Changelog

## 2026-03-26 · User authentication refactor
[Implementation →](https://audevio.com/sessions/<uuid>)

## 2026-03-20 · Add specs workflow
[Technical Specification →](https://audevio.com/sessions/<uuid>)
[Concept & Tests →](https://audevio.com/sessions/<uuid>)
[Implementation →](https://audevio.com/sessions/<uuid>)
```

### Entry Rules

| Branch type | Expected logs |
|---|---|
| `feat/` | Technical Specification + Concept & Tests + Implementation (3 sessions) |
| `fix/`, `cleanup/`, `chore/`, `docs/` | Implementation only (1 session) |

**Rationale:** `feat/` branches go through three distinct phases — upfront design, test design, and coding — each worth its own audit trail. Smaller changes do not have this lifecycle and a single implementation log is sufficient.

- Each entry is added when the PR is merged, not when work starts.
- The entry title matches the PR title.
- Entries are ordered newest-first.

---

## Core Processes

### 1. Bugfixing — TDD

1. Write a test that **reproduces the bug**
2. Implement the fix
3. Verify the test passes

### 2. Regular Development

#### Branch Prefixes

| Prefix | Description | Precommit hooks |
|---|---|---|
| `feat/` | New functionality | ✅ Yes |
| `fix/` | Bug fix | ✅ Yes |
| `cleanup/` | Refactoring, code cleanup | ❌ No |
| `docs/` | Documentation | ❌ No |
| `chore/` | Config, deps, build, infrastructure | ❌ No |

#### Claude Precommit Hooks

Run **only on `feat/` and `fix/` branches** and **only when `PROJECT_STAGE` is `poc` or `production`** (see Project Stage above):

- **Code change** (`.go`, `.rs`, `.py`, …) → the corresponding test file must also change (where possible; in Rust tests live in the same file)
- **Every PR** must include a change to `CLAUDE.md` and `docs/`
- **Audit must pass** before every push (practical substitute for CI/CD which only runs on push to `main`)

On `cleanup/`, `docs/` and `chore/` branches these hooks are skipped — these are changes where strict rules (mandatory tests, docs update) are not relevant.
