package engine

// Internal tests for splitByResolution + spliceCustomMigrations +
// bucketTables edge paths that the public engine.Plan tests don't
// fully exercise. These cover: mixed resolved/unresolved findings,
// multiple simultaneous CUSTOM_MIGRATION splices, empty splice
// pair list, nil bucket sides.

import (
	"strings"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func TestSplitByResolution_MixedResolvedUnresolved(t *testing.T) {
	findings := []*planpb.ReviewFinding{
		{Id: "a", Axis: "carrier_change",
			Column: &planpb.ColumnRef{TableName: "users", ColumnName: "id"}},
		{Id: "b", Axis: "custom_type_change",
			Column: &planpb.ColumnRef{TableName: "posts", ColumnName: "body"}},
		{Id: "c", Axis: "pk_flip",
			Column: &planpb.ColumnRef{TableName: "orders", ColumnName: "uid"}},
	}
	byID := map[string]*planpb.Resolution{
		"a": {FindingId: "a", Strategy: planpb.Strategy_DROP_AND_CREATE, Actor: "cli"},
		"c": {FindingId: "c", Strategy: planpb.Strategy_CUSTOM_MIGRATION, CustomSql: "UPDATE orders SET uid = ...;", Actor: "platform-bot"},
		// "b" is unresolved.
	}

	unresolved, applied, pairs := splitByResolution(findings, byID)

	if len(unresolved) != 1 || unresolved[0].GetId() != "b" {
		t.Errorf("unresolved = %v, want [b]", unresolved)
	}
	if len(applied) != 2 {
		t.Errorf("applied len = %d, want 2", len(applied))
	}
	if len(pairs) != 2 {
		t.Errorf("pairs len = %d, want 2", len(pairs))
	}
	// Applied's FindingId + Actor must mirror the Resolution.
	for _, a := range applied {
		if a.GetFindingId() != "a" && a.GetFindingId() != "c" {
			t.Errorf("unexpected applied.FindingId: %s", a.GetFindingId())
		}
		if a.GetActor() == "" {
			t.Errorf("actor should carry through: %v", a)
		}
	}
}

func TestSpliceCustomMigrations_MultiplePairs(t *testing.T) {
	up, down := "CREATE TABLE x (id INT);", "DROP TABLE x;"
	pairs := []resolvedPair{
		{
			Finding: &planpb.ReviewFinding{Id: "f1", Axis: "carrier_change",
				Column: &planpb.ColumnRef{TableName: "users", ColumnName: "id"}},
			Resolution: &planpb.Resolution{
				FindingId: "f1", Strategy: planpb.Strategy_CUSTOM_MIGRATION,
				CustomSql: "UPDATE users SET id = id::bigint;",
			},
		},
		{
			Finding: &planpb.ReviewFinding{Id: "f2", Axis: "pk_flip",
				Column: &planpb.ColumnRef{TableName: "orders", ColumnName: "uid"}},
			Resolution: &planpb.Resolution{
				FindingId: "f2", Strategy: planpb.Strategy_CUSTOM_MIGRATION,
				CustomSql: "UPDATE orders SET uid = gen_random_uuid();",
			},
		},
	}
	gotUp, gotDown := spliceCustomMigrations(up, down, pairs)
	// Both CUSTOM_MIGRATION bodies must appear in up with attribution.
	if !strings.Contains(gotUp, "UPDATE users SET id = id::bigint;") {
		t.Errorf("up missing first CUSTOM_MIGRATION body: %q", gotUp)
	}
	if !strings.Contains(gotUp, "UPDATE orders SET uid = gen_random_uuid();") {
		t.Errorf("up missing second CUSTOM_MIGRATION body: %q", gotUp)
	}
	if !strings.Contains(gotUp, "-- CUSTOM_MIGRATION: users.id (carrier_change)") {
		t.Errorf("up missing first attribution header: %q", gotUp)
	}
	if !strings.Contains(gotUp, "-- CUSTOM_MIGRATION: orders.uid (pk_flip)") {
		t.Errorf("up missing second attribution header: %q", gotUp)
	}
	// Down must carry a NOTE per pair (rollback is author's job).
	if strings.Count(gotDown, "CUSTOM_MIGRATION applied for") != 2 {
		t.Errorf("down should have 2 rollback notes, got %q", gotDown)
	}
}

func TestSpliceCustomMigrations_NonCustomPassthrough(t *testing.T) {
	// DROP_AND_CREATE resolution should NOT touch up/down body — those
	// get routed through Op emission instead.
	pairs := []resolvedPair{{
		Finding: &planpb.ReviewFinding{Id: "f1", Axis: "carrier_change",
			Column: &planpb.ColumnRef{TableName: "t", ColumnName: "c"}},
		Resolution: &planpb.Resolution{
			FindingId: "f1", Strategy: planpb.Strategy_DROP_AND_CREATE,
		},
	}}
	gotUp, gotDown := spliceCustomMigrations("UP;", "DOWN;", pairs)
	if gotUp != "UP;" || gotDown != "DOWN;" {
		t.Errorf("DROP_AND_CREATE should pass through untouched, got up=%q down=%q", gotUp, gotDown)
	}
}

func TestSpliceCustomMigrations_EmptyCustomSQLSkipped(t *testing.T) {
	// Strategy CUSTOM_MIGRATION with empty CustomSql is a malformed
	// resolution — defensive skip rather than splice an empty body.
	pairs := []resolvedPair{{
		Finding: &planpb.ReviewFinding{Id: "f1",
			Column: &planpb.ColumnRef{TableName: "t", ColumnName: "c"}},
		Resolution: &planpb.Resolution{
			FindingId: "f1", Strategy: planpb.Strategy_CUSTOM_MIGRATION,
			CustomSql: "", // empty
		},
	}}
	gotUp, _ := spliceCustomMigrations("UP;", "DOWN;", pairs)
	if strings.Contains(gotUp, "CUSTOM_MIGRATION") {
		t.Errorf("empty CustomSql should not splice: %q", gotUp)
	}
}

func TestBucketTables_NilSides(t *testing.T) {
	// Both nil.
	empty := &bucket{prev: nil, curr: nil}
	if got := bucketTables(empty); len(got) != 0 {
		t.Errorf("nil/nil bucket should return 0 tables, got %d", len(got))
	}

	table := &irpb.Table{Name: "users", MessageFqn: "shop.User"}

	// Prev set, curr nil (teardown bucket).
	teardown := &bucket{prev: &irpb.Schema{Tables: []*irpb.Table{table}}, curr: nil}
	if got := bucketTables(teardown); len(got) != 1 {
		t.Errorf("teardown bucket should surface prev tables, got %d", len(got))
	}

	// Curr set, prev nil (initial bucket).
	initial := &bucket{prev: nil, curr: &irpb.Schema{Tables: []*irpb.Table{table}}}
	if got := bucketTables(initial); len(got) != 1 {
		t.Errorf("initial bucket should surface curr tables, got %d", len(got))
	}

	// Both set (alter bucket — dup harmless, caller dedupes).
	both := &bucket{
		prev: &irpb.Schema{Tables: []*irpb.Table{table}},
		curr: &irpb.Schema{Tables: []*irpb.Table{table}},
	}
	if got := bucketTables(both); len(got) != 2 {
		t.Errorf("both-sides bucket should union (dup ok), got %d", len(got))
	}
}
