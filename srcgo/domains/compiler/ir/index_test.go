package ir

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	dbpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/db"
)

// TestIndexMethodToIR — exhaustive mapping from authoring-surface
// IndexMethod enum to IR IndexMethod enum. Missing a branch would
// surface as a silent drop to INDEX_METHOD_UNSPECIFIED (→ BTREE at
// emit), so this table-driven sweep pins every legal input.
func TestIndexMethodToIR(t *testing.T) {
	cases := []struct {
		in   dbpb.IndexMethod
		want irpb.IndexMethod
	}{
		{dbpb.IndexMethod_INDEX_METHOD_UNSPECIFIED, irpb.IndexMethod_INDEX_METHOD_UNSPECIFIED},
		{dbpb.IndexMethod_BTREE, irpb.IndexMethod_IDX_BTREE},
		{dbpb.IndexMethod_GIN, irpb.IndexMethod_IDX_GIN},
		{dbpb.IndexMethod_GIST, irpb.IndexMethod_IDX_GIST},
		{dbpb.IndexMethod_BRIN, irpb.IndexMethod_IDX_BRIN},
		{dbpb.IndexMethod_HASH, irpb.IndexMethod_IDX_HASH},
		{dbpb.IndexMethod_SPGIST, irpb.IndexMethod_IDX_SPGIST},
	}
	for _, c := range cases {
		if got := indexMethodToIR(c.in); got != c.want {
			t.Errorf("indexMethodToIR(%v) = %v, want %v", c.in, got, c.want)
		}
	}

	// Defensive: an out-of-range authoring value falls through to
	// UNSPECIFIED (same outcome as "not provided"). Future enum growth
	// surfaces as BTREE rendering at emit time — acceptable default
	// semantic, but the IR builder's validation layer is expected to
	// flag unknown values before reaching emit.
	if got := indexMethodToIR(dbpb.IndexMethod(9999)); got != irpb.IndexMethod_INDEX_METHOD_UNSPECIFIED {
		t.Errorf("indexMethodToIR(unknown) = %v, want UNSPECIFIED", got)
	}
}

// TestNullsOrderToIR — exhaustive mapping from authoring NullsOrder
// to IR NullsOrder.
func TestNullsOrderToIR(t *testing.T) {
	cases := []struct {
		in   dbpb.NullsOrder
		want irpb.NullsOrder
	}{
		{dbpb.NullsOrder_NULLS_ORDER_UNSPECIFIED, irpb.NullsOrder_NULLS_ORDER_UNSPECIFIED},
		{dbpb.NullsOrder_NULLS_FIRST, irpb.NullsOrder_NULLS_FIRST},
		{dbpb.NullsOrder_NULLS_LAST, irpb.NullsOrder_NULLS_LAST},
	}
	for _, c := range cases {
		if got := nullsOrderToIR(c.in); got != c.want {
			t.Errorf("nullsOrderToIR(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// Unknown value → UNSPECIFIED (same fallback as default).
	if got := nullsOrderToIR(dbpb.NullsOrder(9999)); got != irpb.NullsOrder_NULLS_ORDER_UNSPECIFIED {
		t.Errorf("nullsOrderToIR(unknown) = %v, want UNSPECIFIED", got)
	}
}

// TestIndexMethodDisplay — user-facing names surfaced inside diag
// messages. Must cover every IR IndexMethod variant so future
// method additions don't silently render as "UNSPECIFIED" in
// validation errors.
func TestIndexMethodDisplay(t *testing.T) {
	cases := []struct {
		in   irpb.IndexMethod
		want string
	}{
		{irpb.IndexMethod_INDEX_METHOD_UNSPECIFIED, "UNSPECIFIED"},
		{irpb.IndexMethod_IDX_BTREE, "BTREE"},
		{irpb.IndexMethod_IDX_GIN, "GIN"},
		{irpb.IndexMethod_IDX_GIST, "GIST"},
		{irpb.IndexMethod_IDX_BRIN, "BRIN"},
		{irpb.IndexMethod_IDX_HASH, "HASH"},
		{irpb.IndexMethod_IDX_SPGIST, "SPGIST"},
	}
	for _, c := range cases {
		if got := indexMethodDisplay(c.in); got != c.want {
			t.Errorf("indexMethodDisplay(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// Unknown → UNSPECIFIED fallback.
	if got := indexMethodDisplay(irpb.IndexMethod(9999)); got != "UNSPECIFIED" {
		t.Errorf("indexMethodDisplay(unknown) = %q, want UNSPECIFIED", got)
	}
}

// TestConvertIndexFields — shape fidelity: every authoring field
// entry lowers to the IR entry byte-for-byte, including defaults
// (empty opclass, ASC = false, nulls unspecified).
func TestConvertIndexFields(t *testing.T) {
	if got := convertIndexFields(nil); got != nil {
		t.Errorf("convertIndexFields(nil) = %v, want nil", got)
	}
	if got := convertIndexFields([]*dbpb.IndexField{}); got != nil {
		t.Errorf("convertIndexFields(empty) = %v, want nil", got)
	}
	src := []*dbpb.IndexField{
		{Name: "created_at", Desc: true, Nulls: dbpb.NullsOrder_NULLS_LAST},
		{Name: "id"},
		{Name: "tags", Opclass: "gin_trgm_ops"},
	}
	got := convertIndexFields(src)
	if len(got) != len(src) {
		t.Fatalf("len = %d, want %d", len(got), len(src))
	}
	if got[0].GetName() != "created_at" || !got[0].GetDesc() || got[0].GetNulls() != irpb.NullsOrder_NULLS_LAST {
		t.Errorf("entry[0] = %+v", got[0])
	}
	if got[1].GetName() != "id" || got[1].GetDesc() || got[1].GetNulls() != irpb.NullsOrder_NULLS_ORDER_UNSPECIFIED {
		t.Errorf("entry[1] = %+v", got[1])
	}
	if got[2].GetName() != "tags" || got[2].GetOpclass() != "gin_trgm_ops" {
		t.Errorf("entry[2] = %+v", got[2])
	}
}

// TestCopyStringMap — storage option passthrough with independence
// from the input map. Empty input returns nil (not an empty map).
func TestCopyStringMap(t *testing.T) {
	if got := copyStringMap(nil); got != nil {
		t.Errorf("copyStringMap(nil) = %v, want nil", got)
	}
	if got := copyStringMap(map[string]string{}); got != nil {
		t.Errorf("copyStringMap(empty) = %v, want nil", got)
	}
	src := map[string]string{"fillfactor": "90", "fastupdate": "on"}
	dst := copyStringMap(src)
	if len(dst) != 2 || dst["fillfactor"] != "90" || dst["fastupdate"] != "on" {
		t.Errorf("dst = %v", dst)
	}
	// Mutating src must not affect dst (independent map).
	src["fillfactor"] = "mutated"
	if dst["fillfactor"] != "90" {
		t.Errorf("dst aliased src — got %q, want %q", dst["fillfactor"], "90")
	}
}
