package plan_test

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/plan"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Input is intentionally given in reverse alphabetical order to prove the
// differ sorts before emitting.
func schemaTwoTables() *irpb.Schema {
	return &irpb.Schema{
		Tables: []*irpb.Table{
			{Name: "orders", MessageFqn: "shop.Order"},
			{Name: "customers", MessageFqn: "shop.Customer"},
		},
	}
}

func TestDiffNilPrevTwoTables(t *testing.T) {
	got, err := plan.Diff(nil, schemaTwoTables())
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if len(ops) != 2 {
		t.Fatalf("len(ops) = %d, want 2", len(ops))
	}
	if n := ops[0].GetAddTable().GetTable().GetName(); n != "customers" {
		t.Errorf("ops[0] table = %q, want customers (alphabetical)", n)
	}
	if n := ops[1].GetAddTable().GetTable().GetName(); n != "orders" {
		t.Errorf("ops[1] table = %q, want orders", n)
	}
}

func TestDiffNilSchemas(t *testing.T) {
	got, err := plan.Diff(nil, nil)
	if err != nil {
		t.Fatalf("Diff(nil, nil): %v", err)
	}
	if len(got.GetOps()) != 0 {
		t.Errorf("len(ops) = %d, want 0 on empty input", len(got.GetOps()))
	}
}

func TestDiffNonNilPrevRejected(t *testing.T) {
	_, err := plan.Diff(&irpb.Schema{}, &irpb.Schema{})
	if err == nil {
		t.Fatal("Diff(non-nil, …) succeeded; expected iter-1 rejection")
	}
}

// AC #4 — byte-identical on re-run. Run the differ twice and compare
// deterministic proto-wire bytes. Any map iteration or non-stable ordering
// in Diff would surface here.
func TestDiffDeterministic(t *testing.T) {
	in := schemaTwoTables()

	run := func() []byte {
		t.Helper()
		p, err := plan.Diff(nil, in)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(p)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return b
	}

	a, b := run(), run()
	if !bytes.Equal(a, b) {
		t.Fatalf("plan wire bytes differ across runs (len a=%d b=%d)", len(a), len(b))
	}
}

// Assert the plan contains AddTable ops (not some other variant). Regression
// guard for future Op additions — breaks if someone re-tags the oneof.
func TestDiffOpVariantIsAddTable(t *testing.T) {
	got, err := plan.Diff(nil, schemaTwoTables())
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for i, op := range got.GetOps() {
		if _, ok := op.GetVariant().(*planpb.Op_AddTable); !ok {
			t.Errorf("ops[%d] variant = %T, want *planpb.Op_AddTable", i, op.GetVariant())
		}
	}
}

// FK-dependency topo sort: `product_tags` sorts lexically before `products`
// (because '_' 0x5F < 's' 0x73), but m2m tables must come AFTER the tables
// they reference or CREATE TABLE … REFERENCES breaks at apply time. The
// differ's topological order must override lexical here.
func TestDiffTopoOrderReferencedBeforeReferencer(t *testing.T) {
	schema := &irpb.Schema{
		Tables: []*irpb.Table{
			{
				Name:       "product_tags",
				MessageFqn: "shop.ProductTag",
				ForeignKeys: []*irpb.ForeignKey{
					{Column: "product_id", TargetTable: "products", TargetColumn: "id"},
					{Column: "tag_id", TargetTable: "tags", TargetColumn: "id"},
				},
			},
			{Name: "products", MessageFqn: "shop.Product"},
			{Name: "tags", MessageFqn: "shop.Tag"},
		},
	}
	got, err := plan.Diff(nil, schema)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if len(ops) != 3 {
		t.Fatalf("len(ops) = %d, want 3", len(ops))
	}
	order := []string{
		ops[0].GetAddTable().GetTable().GetName(),
		ops[1].GetAddTable().GetTable().GetName(),
		ops[2].GetAddTable().GetTable().GetName(),
	}
	// Expected: products & tags (no deps, lexical tiebreak) then product_tags.
	want := []string{"products", "tags", "product_tags"}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("ops[%d] table = %q, want %q (full order got=%v want=%v)", i, order[i], w, order, want)
		}
	}
}

// Self-FKs create no ordering constraint — a table with fk → itself should
// still sort lexically among other root-independent tables.
func TestDiffSelfFKIsRoot(t *testing.T) {
	schema := &irpb.Schema{
		Tables: []*irpb.Table{
			{
				Name:       "categories",
				MessageFqn: "shop.Category",
				ForeignKeys: []*irpb.ForeignKey{
					{Column: "parent_id", TargetTable: "categories", TargetColumn: "id"},
				},
			},
			{Name: "customers", MessageFqn: "shop.Customer"},
		},
	}
	got, err := plan.Diff(nil, schema)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if n := ops[0].GetAddTable().GetTable().GetName(); n != "categories" {
		t.Errorf("ops[0] = %q, want categories (lexical order; self-FK is not a dep)", n)
	}
	if n := ops[1].GetAddTable().GetTable().GetName(); n != "customers" {
		t.Errorf("ops[1] = %q, want customers", n)
	}
}

// Multi-table FK cycles are explicitly out of scope in iter-1; Diff must
// reject rather than loop or produce partial output.
func TestDiffFKCycleRejected(t *testing.T) {
	schema := &irpb.Schema{
		Tables: []*irpb.Table{
			{
				Name:       "a",
				MessageFqn: "x.A",
				ForeignKeys: []*irpb.ForeignKey{{Column: "b_id", TargetTable: "b", TargetColumn: "id"}},
			},
			{
				Name:       "b",
				MessageFqn: "x.B",
				ForeignKeys: []*irpb.ForeignKey{{Column: "a_id", TargetTable: "a", TargetColumn: "id"}},
			},
		},
	}
	_, err := plan.Diff(nil, schema)
	if err == nil {
		t.Fatal("Diff succeeded on 2-table FK cycle; expected rejection")
	}
}
