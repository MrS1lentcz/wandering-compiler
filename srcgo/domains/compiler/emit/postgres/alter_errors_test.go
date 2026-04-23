package postgres_test

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Defensive-branch coverage: each ALTER-family Op has a "nil /
// missing required field" branch that surfaces a clear error rather
// than panicking or emitting malformed SQL. The happy paths are
// covered by fixtures + goldens; these tests nail the remaining
// error-return statements.

func TestEmitAddColumnNilColumn(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddColumn{AddColumn: &planpb.AddColumn{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil AddColumn.Column accepted; want error")
	}
}

func TestEmitDropColumnNilColumn(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil DropColumn.Column accepted; want error")
	}
}

func TestEmitRenameTableNoOp(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_RenameTable{RenameTable: &planpb.RenameTable{
		Ctx:      &planpb.TableCtx{TableName: "users"},
		FromName: "users", ToName: "users",
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("no-op RenameTable accepted; want error")
	}
}

func TestEmitRenameTableEmptyNames(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_RenameTable{RenameTable: &planpb.RenameTable{
		Ctx:      &planpb.TableCtx{TableName: "t"},
		FromName: "", ToName: "x",
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty from accepted; want error")
	}
}

func TestEmitAddForeignKeyNilFK(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddForeignKey{AddForeignKey: &planpb.AddForeignKey{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil FK accepted; want error")
	}
}

func TestEmitAddForeignKeyMissingColumn(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddForeignKey{AddForeignKey: &planpb.AddForeignKey{
		Ctx:            &planpb.TableCtx{TableName: "t"},
		Fk:             &irpb.ForeignKey{Column: "ghost", TargetTable: "x", TargetColumn: "id"},
		ConstraintName: "t_ghost_fkey",
		Columns:        []*irpb.Column{}, // empty — ghost won't resolve
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("missing column in snapshot accepted; want error")
	}
}

func TestEmitDropForeignKeyNilFK(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_DropForeignKey{DropForeignKey: &planpb.DropForeignKey{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil FK accepted; want error")
	}
}

func TestEmitReplaceForeignKeyMissingSide(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_ReplaceForeignKey{ReplaceForeignKey: &planpb.ReplaceForeignKey{
		Ctx:  &planpb.TableCtx{TableName: "t"},
		From: nil,
		To:   &irpb.ForeignKey{Column: "x"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil from accepted; want error")
	}
}

func TestEmitAddCheckNilColumn(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddCheck{AddCheck: &planpb.AddCheck{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil column accepted; want error")
	}
}

func TestEmitDropCheckNilColumn(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_DropCheck{DropCheck: &planpb.DropCheck{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil column accepted; want error")
	}
}

func TestEmitReplaceCheckNilColumn(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_ReplaceCheck{ReplaceCheck: &planpb.ReplaceCheck{
		Ctx: &planpb.TableCtx{TableName: "t"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil column accepted; want error")
	}
}

func TestEmitAddRawIndexEmptyName(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddRawIndex{AddRawIndex: &planpb.AddRawIndex{
		Ctx:   &planpb.TableCtx{TableName: "t"},
		Index: &irpb.RawIndex{}, // empty name
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty-name raw index accepted; want error")
	}
}

func TestEmitDropRawIndexEmptyName(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_DropRawIndex{DropRawIndex: &planpb.DropRawIndex{
		Ctx:   &planpb.TableCtx{TableName: "t"},
		Index: &irpb.RawIndex{},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty-name raw index accepted; want error")
	}
}

func TestEmitReplaceRawIndexMissingSide(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_ReplaceRawIndex{ReplaceRawIndex: &planpb.ReplaceRawIndex{
		Ctx:  &planpb.TableCtx{TableName: "t"},
		From: nil,
		To:   &irpb.RawIndex{Name: "x", Body: "(y)"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil from accepted; want error")
	}
}

func TestEmitAddRawCheckEmptyName(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AddRawCheck{AddRawCheck: &planpb.AddRawCheck{
		Ctx:   &planpb.TableCtx{TableName: "t"},
		Check: &irpb.RawCheck{Expr: "x > 0"}, // empty name
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty-name raw check accepted; want error")
	}
}

func TestEmitReplaceRawCheckMissingSide(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_ReplaceRawCheck{ReplaceRawCheck: &planpb.ReplaceRawCheck{
		Ctx:  &planpb.TableCtx{TableName: "t"},
		From: nil,
		To:   &irpb.RawCheck{Name: "x", Expr: "y > 0"},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil from accepted; want error")
	}
}

func TestEmitReplaceIndexMissingSide(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_ReplaceIndex{ReplaceIndex: &planpb.ReplaceIndex{
		Ctx:  &planpb.TableCtx{TableName: "t"},
		From: nil,
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("nil from accepted; want error")
	}
}

func TestEmitAddCheckEmptyBody(t *testing.T) {
	col := &irpb.Column{Name: "x", ProtoName: "x", Carrier: irpb.Carrier_CARRIER_DOUBLE, Type: irpb.SemType_SEM_NUMBER}
	op := &planpb.Op{Variant: &planpb.Op_AddCheck{AddCheck: &planpb.AddCheck{
		Ctx:    &planpb.TableCtx{TableName: "t"},
		Column: col,
		// RangeCheck with no bounds = empty body — surface error, not empty SQL.
		Check: &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{}}},
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty-body check accepted; want error")
	}
}

func TestEmitAlterColumnEmptyName(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
		Ctx: &planpb.TableCtx{TableName: "t"},
		// ColumnName intentionally empty.
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty column_name accepted; want error")
	}
}

func TestEmitAlterColumnUnknownVariant(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
		Ctx:        &planpb.TableCtx{TableName: "t"},
		ColumnName: "x",
		Changes:    []*planpb.FactChange{{}}, // variant not set
	}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("unknown FactChange variant accepted; want error")
	}
}

func TestEmitWcMigrationsInsertEmptyTimestamp(t *testing.T) {
	op := &planpb.Op{Variant: &planpb.Op_WcMigrationsInsert{WcMigrationsInsert: &planpb.WcMigrationsInsert{}}}
	if _, _, err := (postgres.Emitter{}).EmitOp(op); err == nil {
		t.Fatal("empty timestamp accepted; want error")
	}
}
