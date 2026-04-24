package classifier

// Internal-package helpers coverage — carrierFromName /
// dbtypeFromName unknown-name paths (returns false) and the
// insertion-sort helpers on empty / single-element inputs.
// Exterior iter_test covers full-matrix iteration; these pin
// the corner cases.

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

func TestCarrierFromName_UnknownReturnsFalse(t *testing.T) {
	got, ok := carrierFromName("NOT_A_CARRIER")
	if ok {
		t.Errorf("unknown carrier should return ok=false, got ok=true value=%v", got)
	}
	if got != irpb.Carrier_CARRIER_UNSPECIFIED {
		t.Errorf("unknown carrier should return CARRIER_UNSPECIFIED, got %v", got)
	}
}

func TestCarrierFromName_Known(t *testing.T) {
	got, ok := carrierFromName("STRING")
	if !ok || got != irpb.Carrier_CARRIER_STRING {
		t.Errorf("STRING lookup: got (%v, %v), want (STRING, true)", got, ok)
	}
}

func TestDbTypeFromName_UnknownReturnsFalse(t *testing.T) {
	got, ok := dbtypeFromName("NOT_A_TYPE")
	if ok {
		t.Errorf("unknown dbtype should return ok=false, got value=%v", got)
	}
	if got != irpb.DbType_DB_TYPE_UNSPECIFIED {
		t.Errorf("unknown dbtype should return DB_TYPE_UNSPECIFIED, got %v", got)
	}
}

func TestDbTypeFromName_Known(t *testing.T) {
	got, ok := dbtypeFromName("TEXT")
	if !ok || got != irpb.DbType_DBT_TEXT {
		t.Errorf("TEXT lookup: got (%v, %v), want (DBT_TEXT, true)", got, ok)
	}
}

func TestSortCarrierEntries_EmptyAndSingle(t *testing.T) {
	// Empty slice: no-op, no panic.
	var empty []CarrierEntry
	sortCarrierEntries(empty)
	if len(empty) != 0 {
		t.Errorf("empty stays empty, got %d", len(empty))
	}
	// Single entry: no-op, insertion sort's outer loop never runs.
	one := []CarrierEntry{{From: irpb.Carrier_CARRIER_STRING, To: irpb.Carrier_CARRIER_INT32}}
	sortCarrierEntries(one)
	if len(one) != 1 || one[0].From != irpb.Carrier_CARRIER_STRING {
		t.Errorf("single entry shouldn't reorder, got %v", one)
	}
	// Reverse-sorted → fully-sorted. Exercises the inner j-- loop.
	rev := []CarrierEntry{
		{From: irpb.Carrier_CARRIER_INT64, To: irpb.Carrier_CARRIER_INT32},
		{From: irpb.Carrier_CARRIER_INT32, To: irpb.Carrier_CARRIER_BOOL},
		{From: irpb.Carrier_CARRIER_BOOL, To: irpb.Carrier_CARRIER_STRING},
	}
	sortCarrierEntries(rev)
	if rev[0].From != irpb.Carrier_CARRIER_BOOL {
		t.Errorf("sort failed, got %v", rev)
	}
}

func TestSortDbTypeEntries_EmptyAndReverse(t *testing.T) {
	var empty []DbTypeEntry
	sortDbTypeEntries(empty)
	// Reverse-sorted across families → sorted.
	rev := []DbTypeEntry{
		{Family: "STRING", From: irpb.DbType_DBT_VARCHAR, To: irpb.DbType_DBT_TEXT},
		{Family: "INT", From: irpb.DbType_DBT_BIGINT, To: irpb.DbType_DBT_INTEGER},
	}
	sortDbTypeEntries(rev)
	if rev[0].Family != "INT" {
		t.Errorf("sort failed, got %v", rev)
	}
}

func TestSortConstraintEntries_EmptyAndReverse(t *testing.T) {
	var empty []ConstraintEntry
	sortConstraintEntries(empty)
	rev := []ConstraintEntry{
		{Axis: "nullable", CaseID: "tighten"},
		{Axis: "max_len", CaseID: "widen"},
	}
	sortConstraintEntries(rev)
	if rev[0].Axis != "max_len" {
		t.Errorf("sort failed, got %v", rev)
	}
}
