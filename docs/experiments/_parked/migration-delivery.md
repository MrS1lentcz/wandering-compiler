# Parked: Migration delivery model

Captured 2026-04-20 while closing iteration-1. **Not scheduled for a specific
iteration.** Design space for how migrations flow from "compiler output" to
"applied against a deployed database". Iteration-1 only produces SQL to a
local `out/` directory; everything in this document is what happens *after*
iteration-1 is done.

---

## The three-component picture

wandering-compiler is not just a CLI compiler. The full product is:

1. **CLI compiler** (`wc`). Dev-time tool. Reads proto, builds IR, diffs
   against the previous schema state, emits SQL via a dialect emitter.
   Iteration-1 scope.
2. **Hosted migration platform.** Ops-time service. A full product in its
   own right, significantly larger than the compiler itself. Stores every
   migration with:
   - full history (which schema version → which schema version);
   - **change requests** as discrete reviewable units (think GitHub PRs, but
     for schema changes) with discussion, approval, and merge semantics;
   - audit trail (who proposed, who approved, when, for which environment);
   - UI with a properly-UX'd visual diff (table-level, column-level,
     dependency graph, lint warnings, changelog preview).
   The platform is the authoritative storage for migrations. They are NOT
   committed to the user's git repo.

   **Two interfaces, both first-class:**
   - **UI** (click-through) for humans reviewing, approving, and observing
     change requests.
   - **Programmatic client + proto push** for automated agents. This is the
     *primary* interface in practice — schema changes will increasingly be
     produced by code-generation tools (Claude Code, similar agents) that
     push proto updates as change requests via the client. The API surface
     for this is not an afterthought; it is co-equal with the UI.
3. **Deploy client.** Lightweight binary called by the user's deploy pipeline.
   At deploy time it talks to the platform, pulls the right migration(s) for
   the target environment's current state, and applies them. Runs **either
   as a CLI on the deploy host** (operator invokes it from their pipeline),
   **or driven directly by the platform over a server-side channel** for
   cases where the platform orchestrates the deploy itself (see next point).

   **Multi-stage deploy orchestration.** The platform is not just a migration
   store — it orchestrates the deploy *sequence* that a single schema change
   often requires. The canonical example: renaming a `NOT NULL` column
   safely across a running fleet requires three releases, not one:
     a. Add new nullable column + start dual-writing from application code.
     b. Backfill the new column from the old.
     c. Cut readers over, drop the old column, and `NOT NULL` the new one.
   Today, developers stitch this together by hand (three separate PRs, three
   separate deploys, a coordination note in a doc). The platform plans the
   sequence from one proto change, gates each stage on the previous being
   observed healthy, and the deploy client (whether CLI-invoked or
   platform-driven) executes one stage at a time. This is a significant
   chunk of the platform's value and is something existing migration tools
   (Django, Flyway, Atlas) do not attempt.

The user's git repo holds only the authoring proto. Everything downstream —
SQL, migration history, applied-state tracking — lives in the platform or
inside each deployed environment's database.

## Why this shape instead of git-checked-in migrations

- **Review focus.** PR review is about intent (proto change), not the
  mechanical SQL output. Having 800 lines of generated SQL in a PR buries
  the one-line proto change that caused it.
- **Audit trail belongs out of git.** "Who approved migration 0042 for the
  production deployment on 2026-05-17" is not a git question. It is an
  authorization event with tenant, environment, and human-approver metadata.
  Git would be a weak substitute.
- **UI visualization needs structured data.** The plan visualization (diff
  graph, lint warnings, changelog) operates on the IR / `MigrationPlan`, not
  on SQL text. The platform is where that IR lives server-side for review.
- **Single source of truth.** Proto in git defines the schema. Migrations are
  derivatives. Storing both is redundancy that can drift.
- **Terraform's model applied to schema.** Declarative state → computed plan
  → review in UI → apply. The analogy is deliberate.

## Open questions (not yet decided)

These are **parked**, not urgent. They come alive when we start building
component 2 (platform) or component 3 (client).

### A. Versioning & identity of a migration

- What uniquely identifies a migration? Git commit SHA of the proto change?
  A platform-internal monotonic ID? A hash of the `MigrationPlan.Ops`?
- Does the platform re-derive the migration from proto, or store the rendered
  SQL alongside? (Probably both: store ops + rendered SQL per dialect, so the
  platform never has to re-render at apply time.)

### B. Applied-state tracking in the deployed DB

- How does the deploy client know which migrations have been applied to its
  target DB? Django uses a `django_migrations` table; Flyway uses
  `flyway_schema_history`.
- Minimum shape: a table in the deployed DB like `wc_schema_history (migration_id, applied_at, applied_by, checksum)`.
- Should we write this ourselves (fits D4 "own the pipeline" philosophy) or
  reuse an existing convention? Decision: write our own, name it distinctly
  (`wc_schema_history`), but keep the shape boring so operators can query it
  the same way they would Flyway.

### C. Multi-environment workflow

- A change goes through environments (dev → staging → prod). Does the
  platform gate each transition separately ("approved for staging" ≠
  "approved for prod"), or is approval global?
- How do we handle divergence — a hotfix applied directly to prod without
  going through staging?
- Hypothesis: per-environment approval, divergence is flagged in UI as
  "environment is ahead/behind expected version".

### D. Deploy client ↔ platform protocol

- Auth: tokens per environment? OIDC per operator?
- Transport: HTTPS pull model (client polls/requests) is simplest; push
  (platform notifies client) adds complexity without obvious benefit.
- Offline deploys: if the platform is unreachable, can the client apply a
  migration it already cached? Probably yes, with strict "you're applying
  migration X whose approval status was Y at the time you cached it" banner.

### E. Rollback

- Does "rollback" mean running the `.down.sql` of the last migration, or does
  it mean planning a fresh migration from current state to the desired
  previous state? The second is strictly safer for non-trivial schemas.
- For iteration-1 the `.down.sql` is still emitted, but only for the trivial
  "drop what we just created" case. Real rollback is a later design problem.

### E2. Multi-stage deploy planning

- How does the platform decide a change **needs** multi-stage orchestration?
  Candidate triggers: adding `NOT NULL` to an existing populated column,
  column renames, type-narrowing changes, dropping a column that downstream
  services still read.
- How are stages represented? Probably a `DeployPlan` = ordered list of
  `Stage`s, each stage carrying one or more `MigrationPlan`s plus gates
  ("wait for all replicas to be on version X before proceeding").
- How does the platform observe that stage N is healthy before releasing
  stage N+1? Hooks into the deploy tooling? Manual "I'm good" button in UI?
  Metrics-based auto-advance? Open.
- What is the interaction with application-code releases that need to land
  between stages (dual-writing, reader-cutover)? The platform probably needs
  to know about *application* versions too, not just schema versions.

### F2. AI agents as platform users

- Claude Code and similar agents will be first-class producers of schema
  change requests. The programmatic API must be designed for them, not
  retrofitted from a human-centric UI.
- Implications: every field a human can set in the UI, an agent must be able
  to set via API — including the PR description, justification, migration
  strategy choice (single-stage vs multi-stage), and merge intent.
- Audit logs distinguish agent-authored from human-authored proposals, and
  approval policies may differ ("any agent proposal needs at least one human
  reviewer" is a plausible default).
- This is not an accessibility or "nice to have" axis — it is likely the
  *primary* way proposals are authored in practice.

### F. Multi-tenancy of the platform

- SaaS-hosted for all projects in this class? Self-hosted on-prem option?
- If SaaS: tenant per organization, project per tenant, environment per
  project. Permissions roll up the tree.
- If self-hosted: single-tenant simpler. Start there, add multi-tenant later
  only if the hosting story materializes.

### G. Relationship to the admin / Django-style UI the project is building

- The project's broader deliverable includes a "Django-style admin" for each
  compiled service. Is the migration platform UI part of that, or a separate
  product? Open.

### H. Pilot project workflow in the absence of a platform

- Iteration-1 doesn't yet ship components 2 or 3. How does a pilot project
  consume iteration-1 output? Proposal: pilot deletes its hand-written
  `migrations/` folder, runs `wc generate` to produce SQL into `out/`, and
  applies that SQL manually for the scope of the pilot. No audit trail, no
  approval workflow — those arrive with components 2 and 3.

---

## Unparking criteria

This document becomes active work when at least one of the following is true:

- Iteration-1 is done and at least one pilot project is successfully using
  its SQL output, so the "what comes after the compiler" question is now the
  bottleneck.
- A pilot operator asks us for the audit/approval story (the implicit demand
  for component 2).
- We start designing the broader "admin-style UI" and realize its scope
  overlaps with the migration platform UI (question G).

Until then: leave it parked. Do not let the platform design shape iteration-1
decisions — the compiler must produce usable SQL regardless of where that
SQL is ultimately stored.
