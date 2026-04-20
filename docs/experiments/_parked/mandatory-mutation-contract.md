# Parked experiment — Mandatory mutation contract

Parked 2026-04-20. Requires the query DSL (iter-2+ minimum); write this
up as a design sketch so the idea doesn't evaporate.

## Motivation

Auto-update side-effects (Django `auto_now=True`, DB `ON UPDATE NOW()`
triggers, ORM `save()` hooks) are cheap to add and expensive to escape.
Archive copies, backfill jobs, admin corrections, data migrations —
every one of those is a case where the developer explicitly does not
want the "magic" to fire, and every one of them fights the framework
instead of cooperating with it.

wandering-compiler's generated storage layer has a property Django does
not: every mutation to every table is a declared storage RPC. We know
statically which methods mutate which fields. That means we can move
from "automatic-with-escape" to "explicit-with-validation":

1. Schema declares a mutation contract per table — "every write RPC must
   update these fields".
2. Compiler walks every storage RPC's DQL (query DSL) body.
3. Any RPC that writes the table but misses a contracted field → build
   error with the RPC location.
4. RPCs that legitimately skip the update declare an explicit exemption
   on themselves; the exemption is visible at the call site.

This buys us (a) no silent side-effects, (b) a static audit of "which
write paths keep updated_at honest" visible in the platform UI, (c) a
cheap hook for domain-specific invariants beyond timestamps: optimistic
version counters, audit trails, row-level cache epochs, soft-delete
timestamps.

## Sketch shape

On the table annotation:

    option (w17.db.table) = {
      name: "articles"
      update_contract: [
        { field: "updated_at", on: ANY_WRITE },
        { field: "version",    on: ANY_WRITE, mode: INCREMENT }
      ]
    };

On a storage RPC that legitimately skips:

    rpc ArchiveArticle(ArchiveArticleRequest) returns (ArchiveArticleResponse) {
      option (w17.rpc) = {
        update_contract_exempt: ["articles.updated_at", "articles.version"]
      };
    }

## Open questions (resolve in the iteration that builds this)

- Exemption scope: per-field or whole-table? Start per-field.
- Granularity of "write": any UPDATE, or any "real" mutation (ignoring
  idempotent no-ops)? Probably any UPDATE — simpler and no one writes
  a no-op update on purpose.
- Exemption visibility: surface in platform UI as a list of "dangerous"
  RPCs so reviewers see them.
- Cross-table writes (`UPDATE joins`): probably future; start with
  single-table UPDATE/DELETE.

## Not this iteration

Iter-1 has no query DSL, so there is nothing to check. This experiment
lands when we have storage RPCs to analyse — iter-2 at the earliest.

No work starts here until an actual project needs it; it is not a gating
item for any iteration.
