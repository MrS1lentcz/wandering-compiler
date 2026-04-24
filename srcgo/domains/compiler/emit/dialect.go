// Package emit is the SQL-emission layer of the compiler. It owns the
// DialectEmitter contract — the narrow seam that lets per-dialect emitters
// render a *planpb.MigrationPlan to up + down SQL without the rest of the
// compiler knowing anything dialect-specific. See docs/iteration-1.md D4.
//
// Iteration-1 ships one real implementation (emit/postgres) plus a stub
// (emit/sqlite) whose existence is acceptance criterion #6: the stub
// compiling against the same interface catches PG-shaped leaks while the
// interface is still small.
package emit

import (
	"fmt"
	"strings"

	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// DialectEmitter renders a single migration Op to up + down SQL. Per-op
// dispatch (rather than whole-plan) keeps the interface stable as new Op
// variants (DropTable, AddColumn, …) land: each implementation type-switches
// on op.GetVariant() and returns an informative error for unknown variants.
//
// The usage parameter collects D16 capability IDs referenced during
// emission. Emitters call usage.Use(cap) at every dispatch site that
// maps to a catalog cap; a nil *Usage turns every call into a no-op
// (Usage.Use handles nil receivers), so tests that don't care about
// tracking can pass nil without boilerplate.
type DialectEmitter interface {
	// Name is the stable short name of the dialect ("postgres", "sqlite", …).
	// Consumed by --dialect flag parsing and diagnostic messages.
	Name() string

	// EmitOp renders one Op to a pair of SQL blocks. Each block must be
	// self-terminating (end with ';') or empty. No trailing newlines — the
	// plan-level Emit adds separators between ops.
	EmitOp(op *planpb.Op, usage *Usage) (up string, down string, err error)
}

// Transactional is an optional capability marker: emitters whose
// dialect has no transactional DDL (Redis and other whole-model KV
// stores, most notably) implement this interface and return false to
// suppress the BEGIN / COMMIT wrapper Emit adds by default. SQL
// emitters (postgres, mysql, sqlite) don't need to implement it —
// the zero-value absence is read as "transactional = true".
type Transactional interface {
	Transactional() bool
}

// Emit orchestrates a whole plan: forward-order up SQL, reverse-order down
// SQL (so rollback applies in the inverse direction of migration). Op blocks
// are separated by one blank line and wrapped in `BEGIN; … COMMIT;` so the
// whole migration is all-or-nothing at apply time — a syntax error or FK
// conflict mid-migration rolls back every CREATE TABLE / CREATE INDEX
// already issued in that script, leaving the target DB in its pre-apply
// state rather than a half-created mess. Postgres's transactional DDL
// makes this safe for every op iter-1 emits (AddTable today, AlterTable
// variants later — non-transactional exceptions like CREATE INDEX
// CONCURRENTLY arrive as opt-outs when iter-2 surfaces them).
//
// Non-transactional emitters (Redis) opt out of the wrapper via the
// Transactional interface, returning false.
//
// The final output carries a trailing newline so file-diff tools behave.
//
// The returned *Usage collects every capability ID the emitter referenced
// during this plan (M4 Layer A). Safe to discard on the call site — nil-
// safe consumers treat an empty collector identically to no tracking.
// TRANSACTIONAL_DDL is recorded for transactional dialects whenever the
// plan produced any output (per-migration, not per-op).
func Emit(e DialectEmitter, plan *planpb.MigrationPlan) (up string, down string, usage *Usage, err error) {
	ops := plan.GetOps()
	ups := make([]string, 0, len(ops))
	downs := make([]string, 0, len(ops))
	usage = NewUsage()

	for i, op := range ops {
		u, d, opErr := e.EmitOp(op, usage)
		if opErr != nil {
			return "", "", nil, fmt.Errorf("emit %s: op[%d]: %w", e.Name(), i, opErr)
		}
		if u != "" {
			ups = append(ups, u)
		}
		if d != "" {
			downs = append(downs, d)
		}
	}

	// Reverse down blocks — rollback undoes ops in reverse application order.
	for i, j := 0, len(downs)-1; i < j; i, j = i+1, j-1 {
		downs[i], downs[j] = downs[j], downs[i]
	}

	if t, ok := e.(Transactional); ok && !t.Transactional() {
		return joinBlocks(ups), joinBlocks(downs), usage, nil
	}
	if len(ups) > 0 || len(downs) > 0 {
		usage.Use(CapTransactionalDDL)
	}
	return wrapTransaction(ups), wrapTransaction(downs), usage, nil
}

// wrapTransaction joins non-empty SQL blocks with one blank line between
// them and wraps the whole script in `BEGIN; … COMMIT;`. Empty input
// returns "" (no wrapping — nothing to commit).
func wrapTransaction(blocks []string) string {
	if len(blocks) == 0 {
		return ""
	}
	return "BEGIN;\n\n" + strings.Join(blocks, "\n\n") + "\n\nCOMMIT;\n"
}

// joinBlocks joins non-empty blocks with blank lines for non-
// transactional dialects (Redis). Trailing newline kept so file
// diffs stay clean.
func joinBlocks(blocks []string) string {
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}
