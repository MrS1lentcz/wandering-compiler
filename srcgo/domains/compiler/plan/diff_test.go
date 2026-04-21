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
