package ir

// Internal-package tests for small helpers whose negative /
// no-match branches aren't triggered by any existing fixture
// corpus. Keeps per-function coverage honest without requiring
// synthetic IR inputs that bypass validators.

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestDuplicateTableName_NoMatch pins the "no duplicate found"
// return path — existing fixtures only call duplicateTableName
// via the dup-detection path, so the (nil, false) branch sat
// uncovered until now.
func TestDuplicateTableName_NoMatch(t *testing.T) {
	schema := &irpb.Schema{Tables: []*irpb.Table{
		{Name: "users"}, {Name: "orders"},
	}}
	tbl, dup := duplicateTableName(schema, "products")
	if dup || tbl != nil {
		t.Errorf("no-match: got (%v, %v), want (nil, false)", tbl, dup)
	}
}

func TestDuplicateTableName_Match(t *testing.T) {
	schema := &irpb.Schema{Tables: []*irpb.Table{
		{Name: "users"}, {Name: "orders"},
	}}
	tbl, dup := duplicateTableName(schema, "orders")
	if !dup || tbl == nil || tbl.GetName() != "orders" {
		t.Errorf("match: got (%v, %v), want (orders, true)", tbl, dup)
	}
}

func TestDuplicateTableName_Empty(t *testing.T) {
	tbl, dup := duplicateTableName(&irpb.Schema{}, "anything")
	if dup || tbl != nil {
		t.Errorf("empty schema: got (%v, %v), want (nil, false)", tbl, dup)
	}
}
