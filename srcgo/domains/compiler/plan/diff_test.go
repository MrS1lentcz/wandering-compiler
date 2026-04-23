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

// TestDiffPrevEqualsCurrEmptyPlan — when prev and curr are structurally
// equivalent (same FQNs, same table content), the differ emits no Ops.
// AC #1 of iter-2 M1 (`alter_noop` fixture's basis).
func TestDiffPrevEqualsCurrEmptyPlan(t *testing.T) {
	got, err := plan.Diff(schemaTwoTables(), schemaTwoTables())
	if err != nil {
		t.Fatalf("Diff(equal, equal): %v", err)
	}
	if len(got.GetOps()) != 0 {
		t.Errorf("len(ops) = %d, want 0 on equal prev/curr", len(got.GetOps()))
	}
}

// TestDiffDropTableOnly — table FQN present in prev but not in curr
// becomes a DropTable Op. Reverse FK topological order: a referencer
// drops before its referencee.
func TestDiffDropTableOnly(t *testing.T) {
	prev := schemaTwoTables()                       // shop.Order, shop.Customer
	curr := &irpb.Schema{Tables: prev.GetTables()[:1]} // keeps shop.Order only
	got, err := plan.Diff(prev, curr)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if len(ops) != 1 {
		t.Fatalf("len(ops) = %d, want 1", len(ops))
	}
	dt := ops[0].GetDropTable()
	if dt == nil {
		t.Fatalf("ops[0] variant = %T, want *planpb.Op_DropTable", ops[0].GetVariant())
	}
	if n := dt.GetTable().GetName(); n != "customers" {
		t.Errorf("dropped table = %q, want customers", n)
	}
}

// TestDiffDropOrderReverseTopo — when dropping multiple tables related
// by FK, the referencer drops first so the referencee's drop doesn't
// hit "still referenced by …". Inverse of TestDiffTopoOrderReferencedBeforeReferencer.
func TestDiffDropOrderReverseTopo(t *testing.T) {
	prev := &irpb.Schema{
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
	curr := &irpb.Schema{} // drop everything
	got, err := plan.Diff(prev, curr)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if len(ops) != 3 {
		t.Fatalf("len(ops) = %d, want 3", len(ops))
	}
	order := []string{
		ops[0].GetDropTable().GetTable().GetName(),
		ops[1].GetDropTable().GetTable().GetName(),
		ops[2].GetDropTable().GetTable().GetName(),
	}
	// Forward topo: [products, tags, product_tags] (referenced-first;
	// products/tags lexical tiebreak between independents). Reversed
	// for drops: product_tags first (it references products + tags),
	// then tags, then products. The order of products vs tags here
	// is the reverse of the forward lexical tiebreak — both orders
	// are FK-correct, and reverse-of-forward is what determinism
	// gives us.
	want := []string{"product_tags", "tags", "products"}
	for i, w := range want {
		if order[i] != w {
			t.Errorf("ops[%d] = %q, want %q (full order got=%v want=%v)", i, order[i], w, order, want)
		}
	}
}

// TestDiffDropAndAddCombined — prev has A; curr has B. Plan must drop
// A first, then add B. (B has no FQN match in prev, A has no FQN match
// in curr.)
func TestDiffDropAndAddCombined(t *testing.T) {
	prev := &irpb.Schema{
		Tables: []*irpb.Table{{Name: "old_table", MessageFqn: "shop.Old"}},
	}
	curr := &irpb.Schema{
		Tables: []*irpb.Table{{Name: "new_table", MessageFqn: "shop.New"}},
	}
	got, err := plan.Diff(prev, curr)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if len(ops) != 2 {
		t.Fatalf("len(ops) = %d, want 2", len(ops))
	}
	if dt := ops[0].GetDropTable(); dt == nil || dt.GetTable().GetName() != "old_table" {
		t.Errorf("ops[0] = %v, want DropTable(old_table)", ops[0].GetVariant())
	}
	if at := ops[1].GetAddTable(); at == nil || at.GetTable().GetName() != "new_table" {
		t.Errorf("ops[1] = %v, want AddTable(new_table)", ops[1].GetVariant())
	}
}

// TestDiffMessageRenameIsDropPlusAdd — D24: changing the proto message
// name (= changing FQN) is semantically a destroy + create, not an
// in-place rename. Even if the SQL `name` is identical, FQN difference
// drives drop+add.
func TestDiffMessageRenameIsDropPlusAdd(t *testing.T) {
	prev := &irpb.Schema{
		Tables: []*irpb.Table{{Name: "users", MessageFqn: "shop.OldUser"}},
	}
	curr := &irpb.Schema{
		Tables: []*irpb.Table{{Name: "users", MessageFqn: "shop.NewUser"}},
	}
	got, err := plan.Diff(prev, curr)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	ops := got.GetOps()
	if len(ops) != 2 {
		t.Fatalf("len(ops) = %d, want 2", len(ops))
	}
	if ops[0].GetDropTable() == nil {
		t.Errorf("ops[0] = %T, want DropTable", ops[0].GetVariant())
	}
	if ops[1].GetAddTable() == nil {
		t.Errorf("ops[1] = %T, want AddTable", ops[1].GetVariant())
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
