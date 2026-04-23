package redis_test

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/redis"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Compile-time assertion: Emitter satisfies DialectEmitter. Same
// discipline as the PG + SQLite stubs — catches interface drift.
var _ emit.DialectEmitter = redis.Emitter{}

func TestRedisName(t *testing.T) {
	if n := (redis.Emitter{}).Name(); n != "redis" {
		t.Errorf("Name() = %q, want redis", n)
	}
}

// TestEmitAddTableKeyspaceComment — AddTable produces a comment
// documenting the keyspace pattern. Down re-documents + emits a
// drop pattern for the rollback.
func TestEmitAddTableKeyspaceComment(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{
		Table: &irpb.Table{Name: "users", MessageFqn: "shop.User"},
	}}}
	up, down, err := redis.Emitter{}.EmitOp(op)
	if err != nil {
		t.Fatalf("EmitOp: %v", err)
	}
	if !strings.Contains(up, "keyspace pattern 'users:<id>'") {
		t.Errorf("up missing keyspace comment: %q", up)
	}
	if !strings.Contains(down, "UNLINK") {
		t.Errorf("down missing UNLINK pattern: %q", down)
	}
}

// TestEmitDropTablePattern — DropTable emits the SCAN + UNLINK
// pattern.
func TestEmitDropTablePattern(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_DropTable{DropTable: &planpb.DropTable{
		Table: &irpb.Table{Name: "legacy", MessageFqn: "shop.Legacy"},
	}}}
	up, _, err := redis.Emitter{}.EmitOp(op)
	if err != nil {
		t.Fatalf("EmitOp: %v", err)
	}
	if !strings.Contains(up, "UNLINK") || !strings.Contains(up, "'legacy:*'") {
		t.Errorf("up missing DROP pattern: %q", up)
	}
}

// TestEmitRenameTablePattern — RenameTable iterates keys via Lua +
// RENAME.
func TestEmitRenameTablePattern(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_RenameTable{RenameTable: &planpb.RenameTable{
		FromName: "users", ToName: "accounts",
	}}}
	up, down, err := redis.Emitter{}.EmitOp(op)
	if err != nil {
		t.Fatalf("EmitOp: %v", err)
	}
	if !strings.Contains(up, "RENAME") || !strings.Contains(up, "'users:*'") || !strings.Contains(up, "'accounts:'") {
		t.Errorf("up missing RENAME pattern: %q", up)
	}
	if !strings.Contains(down, "'accounts:*'") || !strings.Contains(down, "'users:'") {
		t.Errorf("down missing inverse RENAME: %q", down)
	}
}

// TestEmitColumnOpsAreNoOps — every column / index / FK / CHECK op
// produces a no-op comment. Redis has no concept of these at the
// storage layer.
func TestEmitColumnOpsAreNoOps(t *testing.T) {
	ops := []*planpb.Op{
		{Variant: &planpb.Op_AddColumn{AddColumn: &planpb.AddColumn{Ctx: &planpb.TableCtx{TableName: "t"}, Column: &irpb.Column{Name: "c"}}}},
		{Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{Ctx: &planpb.TableCtx{TableName: "t"}, Column: &irpb.Column{Name: "c"}}}},
		{Variant: &planpb.Op_AddIndex{AddIndex: &planpb.AddIndex{Ctx: &planpb.TableCtx{TableName: "t"}, Index: &irpb.Index{Name: "x"}}}},
		{Variant: &planpb.Op_AddForeignKey{AddForeignKey: &planpb.AddForeignKey{Ctx: &planpb.TableCtx{TableName: "t"}}}},
	}
	for _, op := range ops {
		up, down, err := redis.Emitter{}.EmitOp(op)
		if err != nil {
			t.Errorf("EmitOp(%T): %v", op.GetVariant(), err)
			continue
		}
		if !strings.HasPrefix(up, "# wc:") {
			t.Errorf("EmitOp(%T): up should start with `# wc:` comment, got %q", op.GetVariant(), up)
		}
		if up != down {
			t.Errorf("EmitOp(%T): no-op up/down should match, got up=%q down=%q", op.GetVariant(), up, down)
		}
	}
}

// TestEmitWcMigrationsInsertZadd — applied-state lands in a sorted
// set; ZADD up, ZREM down.
func TestEmitWcMigrationsInsertZadd(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_WcMigrationsInsert{WcMigrationsInsert: &planpb.WcMigrationsInsert{
		Timestamp: "20260423T120000Z",
	}}}
	up, down, err := redis.Emitter{}.EmitOp(op)
	if err != nil {
		t.Fatalf("EmitOp: %v", err)
	}
	if up != "ZADD wc:migrations 0 20260423T120000Z" {
		t.Errorf("up = %q", up)
	}
	if down != "ZREM wc:migrations 20260423T120000Z" {
		t.Errorf("down = %q", down)
	}
}

// TestEmitEmptyOpErrors — empty Op surfaces an informative error.
func TestEmitEmptyOpErrors(t *testing.T) {
	if _, _, err := (redis.Emitter{}).EmitOp(&planpb.Op{}); err == nil {
		t.Fatal("empty Op accepted; want error")
	}
}
