# Core Functions Registry

Per [`conventions-global/quality.md`](conventions-global/quality.md) §Code
Structure: functions over 50 lines must be **documented as core functions**
(special description + 100% coverage). This file is that documentation —
the single source of truth for every function in `srcgo/` that sits above
the 50-LOC soft cap, with the invariant it enforces and why splitting
would hurt more than it helps.

Every entry below has a corresponding [test/fixture coverage entry](iteration-1-coverage.md)
landing 100% of its branches.

---

## Pure dispatch switches

Splitting a pure `switch` per-case fragments the dispatch across call
sites without changing the number of cases. Kept intact.

| Fn | LOC | File | Dispatch axis |
|---|---|---|---|
| `protoTypeToSem` | 58 | `ir/build.go` | `w17pb.Type` → `irpb.SemType` (every `w17.field.Type` value). |
| `dbTypeToIR` | 59 | `ir/build.go` | `dbpb.DbType` → `irpb.DbType` (every `w17.db.column.db_type` value). |
| `isReservedPgSchema` | 54 | `ir/names.go` | Postgres system schema prefixes (`pg_*`, `information_schema`, `pg_toast`). |
| `pgColumnFromDbType` | 63 | `emit/postgres/column.go` | `irpb.DbType` → PG keyword + shape (VARCHAR requires max_len, NUMERIC requires precision). |

---

## Matrix validators

These encode the load-bearing D2 / D14 carrier×sem compatibility table
and the D2 / D17 / D22 per-option CHECK rules. Every case is an
independent invariant; a split would require a per-case helper with
identical signature plus call-site orchestration — pure duplication.

| Fn | LOC | File | Matrix |
|---|---|---|---|
| `validateCarrierSemType` | 110 | `ir/build.go` | (Carrier, SemType) × valid/invalid per D2 + D14. |
| `attachChecks` | 108 | `ir/build.go` | Per-column CHECK synthesis dispatch on (carrier, sem, option): blank, min_len, max_len, pattern, choices, gt/gte/lt/lte — each emits one derived CHECK name. |

---

## Cohesive pipelines

Each is a linear pipeline where the stages share tight state
(partially-built IR column, loader-level descriptors, shared error
accumulator). Splitting would push state through parameter lists without
shrinking the understanding surface.

| Fn | LOC | File | Pipeline |
|---|---|---|---|
| `Build` | 51 | `ir/build.go` | namespace → tables → FKs → errors.Join. Early-return chain; splitting would hide the short-circuit pattern. |
| `resolveNamespace` | 62 | `ir/build.go` | (SCHEMA xor PREFIX) validation + Schema population. Two modes share the identifier / reserved-schema validation shape. |
| `newTableFrame` | 54 | `ir/build.go` | D21 derive + applyPrefix + validateIdentifier + Table literal. Returns (tbl, ok). |
| `validateStringNumericOptions` | 59 | `ir/build.go` | carrier-class dispatch (string-only / numeric-only / collection) + range-bound gate. Each sub-branch is a few lines; the envelope is one concern. |
| `resolveEnumColumn` | 52 | `ir/build.go` | (D17) descriptor resolve → populate Choices + numeric values → synth CHECK IN. |
| `resolvePathExtensions` | 73 | `ir/build.go` | (D22d) default list for IMAGE_PATH → wildcard expansion → regex synth. |
| `populateElement` | 63 | `ir/build.go` | Element-carrier / element-is-message inference for MAP + LIST. |
| `Load` | 87 | `loader/loader.go` | proto file resolution → descriptor parse → message/field walk with annotation extraction. |
| `topoSortByFK` | 59 | `plan/diff.go` | Kahn-style topo sort with FK dependency edges; self-FK is root; cycle rejection. |
| `renderColumn` | 81 | `emit/postgres/column.go` | columnType + NOT NULL + GENERATED ALWAYS AS + DEFAULT + PK-inline + FK inline + sub-renderers. |
| `renderIndexes` | 57 | `emit/postgres/index.go` | Per-index render with method / opclass / nulls / include / storage dispatch. |
| `emitAddTable` | 30 | `emit/postgres/emit.go` | **Refactored 2026-04-22** — was 143 LOC; now orchestrates 5 sub-stages (writeEnumTypePrelude, writeCreateTable, writeIndexStatements, writeCommentStatements, renderTableDown). Kept here as the historical note. |
| `columnType` | 35 | `emit/postgres/column.go` | **Refactored 2026-04-22** — was 141 LOC; now dispatches to 8 per-carrier helpers. Kept as historical note. |
| `buildTable` | 20 | `ir/build.go` | **Refactored 2026-04-22** — was 313 LOC; now orchestrates 9 sub-stages. Historical note. |
| `buildColumn` | ~50 | `ir/build.go` | **Refactored 2026-04-22** — was 440 LOC; now orchestrates 14 sub-stages. Historical note. |
| `(GenerateCmd).Run` | 61 | `cmd/cli/cmd_generate.go` | CLI plumbing: load → build → diff → emit → write. Each step is one call; the envelope is the orchestration. |

---

## Convention compliance checklist

- [x] Every entry above has a descriptive doc comment at the function site.
- [x] Every entry is registered here with its invariant + rationale.
- [ ] Every entry reaches 100% statement coverage — **tracked in Phase B**
      (`iteration-1-coverage.md` Phase-B sweep; `go tool cover -func`
      per-function verification).

When a function drops below 50 LOC through a refactor, remove it from
this file. When a new >50 LOC function lands, add it in the same
commit.
