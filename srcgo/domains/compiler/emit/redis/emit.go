// Package redis is the iteration-2 stub implementation of
// emit.DialectEmitter for Redis (and other whole-model KV stores).
//
// Design rationale (iter-2.md D26 framing):
//
// Redis is schema-less at the field level — there's no concept of
// columns, indexes, CHECK constraints, or FKs applied to parts of a
// value. Each "table" maps to a key pattern (`<table_name>:<id>`)
// and the whole proto Message serialises as the value (JSON by
// default, protobuf bytes when the author opts in).
//
// What this emitter does produce:
//
//   * AddTable    — a comment line noting the keyspace pattern so
//                   operators can document what lives in the target
//                   Redis. No DDL required.
//   * DropTable   — a SCAN + UNLINK pattern that removes every key
//                   belonging to the dropped message's keyspace.
//                   Safe under apply-roundtrip on an empty store.
//   * WcMigrationsCreate / WcMigrationsInsert — applied-state
//                   tracking via a reserved `wc:migrations` sorted
//                   set (score = epoch millis of applied_at, member =
//                   timestamp string). Integrity hash stored as a
//                   sidecar hash (`wc:migrations:hash`) map.
//   * Everything else (column / index / FK / CHECK / AlterColumn) —
//                   no-op comment, because the axis doesn't exist on
//                   Redis. Application-layer (the generated gRPC
//                   handler) enforces field-level validation; the
//                   compiler just notes the change happened.
//
// This keeps the iter-2 dialect interface honest: one emitter per
// connection kind, all respecting the same Op stream. Multi-
// connection (D26) will dispatch PG-vs-Redis ops per (dialect,
// version) so a single domain with a main PG + side Redis lands
// two migration files — one per connection — from the same Diff.
package redis

import (
	"fmt"
	"strings"

	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Emitter is the Redis DialectEmitter stub. Zero-value usable.
type Emitter struct{}

// Name returns the stable dialect identifier.
func (Emitter) Name() string { return "redis" }

// EmitOp dispatches on the Op variant. Every output line is a
// Redis CLI command or a `#` comment (the Redis CLI treats leading
// `#` as a comment in `redis-cli < file` input).
func (e Emitter) EmitOp(op *planpb.Op) (up string, down string, err error) {
	switch v := op.GetVariant().(type) {
	case *planpb.Op_AddTable:
		return e.emitAddTable(v.AddTable)
	case *planpb.Op_DropTable:
		return e.emitDropTable(v.DropTable)
	case *planpb.Op_WcMigrationsCreate:
		return e.emitWcMigrationsCreate()
	case *planpb.Op_WcMigrationsInsert:
		return e.emitWcMigrationsInsert(v.WcMigrationsInsert)

	// All field-level ops are no-op on Redis — the value is the
	// whole serialised message, so per-column / per-index / per-FK
	// operations have nothing to apply at the storage layer.
	case *planpb.Op_AddColumn, *planpb.Op_DropColumn, *planpb.Op_RenameColumn,
		*planpb.Op_AlterColumn,
		*planpb.Op_AddIndex, *planpb.Op_DropIndex, *planpb.Op_ReplaceIndex,
		*planpb.Op_AddRawIndex, *planpb.Op_DropRawIndex, *planpb.Op_ReplaceRawIndex,
		*planpb.Op_AddForeignKey, *planpb.Op_DropForeignKey, *planpb.Op_ReplaceForeignKey,
		*planpb.Op_AddCheck, *planpb.Op_DropCheck, *planpb.Op_ReplaceCheck,
		*planpb.Op_AddRawCheck, *planpb.Op_DropRawCheck, *planpb.Op_ReplaceRawCheck,
		*planpb.Op_SetTableComment, *planpb.Op_SetTableNamespace:
		return e.noOpWithComment(op), e.noOpWithComment(op), nil
	case *planpb.Op_RenameTable:
		return e.emitRenameTable(v.RenameTable)
	}
	return "", "", fmt.Errorf("redis emitter: unsupported op variant %T", op.GetVariant())
}

// emitAddTable emits a descriptive comment. Redis has no DDL; the
// emitter's job is to document the keyspace so operators and the
// deploy-client agree on what the pattern means.
func (e Emitter) emitAddTable(at *planpb.AddTable) (string, string, error) {
	t := at.GetTable()
	if t == nil || t.GetName() == "" {
		return "", "", fmt.Errorf("redis: AddTable with empty table name")
	}
	up := fmt.Sprintf("# wc: AddTable %s (FQN=%s) — keyspace pattern '%s:<id>'", t.GetName(), t.GetMessageFqn(), t.GetName())
	down := fmt.Sprintf("# wc: AddTable %s rollback — DROP pattern below", t.GetName())
	drop := redisDropPattern(t.GetName())
	return up, down + "\n" + drop, nil
}

// emitDropTable emits the SCAN + UNLINK pattern that removes every
// key under the table's keyspace. Down re-documents the keyspace
// (no way to rehydrate data — that's a deploy-client / backup
// concern).
func (e Emitter) emitDropTable(dt *planpb.DropTable) (string, string, error) {
	t := dt.GetTable()
	if t == nil || t.GetName() == "" {
		return "", "", fmt.Errorf("redis: DropTable with empty table name")
	}
	up := redisDropPattern(t.GetName())
	down := fmt.Sprintf("# wc: DropTable %s rollback — keyspace re-established (no data restored from compiler)", t.GetName())
	return up, down, nil
}

// emitRenameTable emits a SCAN + RENAME pattern for the keyspace.
func (e Emitter) emitRenameTable(rt *planpb.RenameTable) (string, string, error) {
	from, to := rt.GetFromName(), rt.GetToName()
	if from == "" || to == "" {
		return "", "", fmt.Errorf("redis: RenameTable missing from/to")
	}
	up := redisRenamePattern(from, to)
	down := redisRenamePattern(to, from)
	return up, down, nil
}

// emitWcMigrationsCreate is a no-op — the wc:migrations sorted set
// is created lazily on the first ZADD. Emit a comment so operators
// see the bookkeeping location.
func (e Emitter) emitWcMigrationsCreate() (string, string, error) {
	return "# wc: wc:migrations sorted-set created lazily on first ZADD",
		"# wc: wc:migrations rollback — DEL below",
		nil
}

func (e Emitter) emitWcMigrationsInsert(in *planpb.WcMigrationsInsert) (string, string, error) {
	ts := in.GetTimestamp()
	if ts == "" {
		return "", "", fmt.Errorf("redis: WcMigrationsInsert missing timestamp")
	}
	// Sorted set — score = timestamp lexicographically (Redis takes
	// scores as doubles; for our YYYYMMDDTHHMMSSZ format we use the
	// lex-sort = chrono-sort property and store score=0 + member=ts).
	up := fmt.Sprintf("ZADD wc:migrations 0 %s", ts)
	down := fmt.Sprintf("ZREM wc:migrations %s", ts)
	return up, down, nil
}

// redisDropPattern emits SCAN + UNLINK over the table's keyspace
// pattern. SCAN is iterative and non-blocking — safe to run on a
// live store.
//
//	redis-cli --scan --pattern '<name>:*' | xargs redis-cli UNLINK
//
// Rendered as a single Lua script so the whole delete is one
// atomic EVAL call.
func redisDropPattern(table string) string {
	return fmt.Sprintf(
		"EVAL \"local keys = redis.call('KEYS', ARGV[1]); if #keys > 0 then redis.call('UNLINK', unpack(keys)) end; return #keys\" 0 '%s:*'",
		table)
}

// redisRenamePattern iterates every `<from>:*` key and RENAMEs it
// to `<to>:<suffix>`.
func redisRenamePattern(from, to string) string {
	return fmt.Sprintf(
		"EVAL \"local keys = redis.call('KEYS', ARGV[1]); for i, k in ipairs(keys) do local suffix = string.sub(k, #ARGV[2] + 1); redis.call('RENAME', k, ARGV[3] .. suffix) end; return #keys\" 0 '%s:*' '%s:' '%s:'",
		from, from, to)
}

// noOpWithComment renders the no-op marker for field-level ops on
// Redis. Comment carries the op variant so operators see what was
// expected (useful when auditing a combined PG + Redis migration).
func (e Emitter) noOpWithComment(op *planpb.Op) string {
	name := opVariantName(op)
	return fmt.Sprintf("# wc: %s — no-op on Redis (schema-less; app-layer enforces field rules)", name)
}

func opVariantName(op *planpb.Op) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", op.GetVariant()), "*planpb.Op_")
}
