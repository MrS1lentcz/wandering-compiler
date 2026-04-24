package engine_test

// D36 Commit B integration tests — engine.Plan on an alter that
// flips (w17.pg.field).custom_type alias, exercising:
//   (1) Registered conversion path → AlterColumn with rendered
//       USING clause flows through emit.
//   (2) Unregistered path → hard-error identifying the missing
//       convertible_to / convertible_from registration.
//   (3) DROP_AND_CREATE resolution → DropColumn + AddColumn pair.
//   (4) B1 — non-CUSTOM resolution on pk_flip / enum_* etc. axes
//       hard-errors instead of silent-empty migration.

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
	pgpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/pg"
)

// mkCustomTypeSchema synthesises a Schema with one table carrying
// a column with (pg) custom_type = alias + resolved sql_type +
// required_extensions. Registry attached to the Schema reflects
// the registered entries; caller adjusts convertible_to entries
// per test case.
func mkCustomTypeSchema(alias, sqlType string, registry map[string]*pgpb.CustomType) *irpb.Schema {
	return &irpb.Schema{
		Tables: []*irpb.Table{{
			Name: "docs", MessageFqn: "pkg.Doc",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true},
				{Name: "payload", ProtoName: "payload", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_TEXT,
					Pg: &irpb.PgOptions{
						CustomType:      sqlType,
						CustomTypeAlias: alias,
					}},
			},
			PrimaryKey: []string{"id"},
		}},
		PgCustomTypes: registry,
	}
}

func TestPlan_CustomTypeChange_RegisteredPath(t *testing.T) {
	cls := testClassifier(t)
	registry := map[string]*pgpb.CustomType{
		"my_text_v1": {
			Alias: "my_text_v1", SqlType: "my_text_v1",
			ConvertibleTo: []*pgpb.Conversion{
				{Type: "my_text_v2", Cast: "{{.Col}}::my_text_v2",
					Rationale: "Registered PG cast between wrapped domain types."},
			},
		},
		"my_text_v2": {Alias: "my_text_v2", SqlType: "my_text_v2"},
	}
	prev := mkCustomTypeSchema("my_text_v1", "my_text_v1", registry)
	curr := mkCustomTypeSchema("my_text_v2", "my_text_v2", registry)

	// Probe to learn Finding ID, then supply matching LOSSLESS_USING resolution.
	probe, err := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(probe.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(probe.Findings))
	}
	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_LOSSLESS_USING,
		Actor:     "test",
	}}
	plan, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Migrations) != 1 {
		t.Fatalf("want 1 migration, got %d", len(plan.Migrations))
	}
	up := plan.Migrations[0].GetUpSql()
	// Expect ALTER COLUMN TYPE with registered USING cast.
	if !strings.Contains(up, "ALTER COLUMN payload TYPE my_text_v2 USING payload::my_text_v2") {
		t.Errorf("missing registered USING cast in up:\n%s", up)
	}
}

func TestPlan_CustomTypeChange_UnregisteredPath_HardError(t *testing.T) {
	cls := testClassifier(t)
	registry := map[string]*pgpb.CustomType{
		"type_a": {Alias: "type_a", SqlType: "type_a"},
		"type_b": {Alias: "type_b", SqlType: "type_b"},
	}
	prev := mkCustomTypeSchema("type_a", "type_a", registry)
	curr := mkCustomTypeSchema("type_b", "type_b", registry)

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_LOSSLESS_USING,
		Actor:     "test",
	}}
	_, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err == nil {
		t.Fatal("want hard-error on unregistered conversion path")
	}
	if !strings.Contains(err.Error(), "no conversion path registered") {
		t.Errorf("err should mention missing registration, got: %v", err)
	}
	if !strings.Contains(err.Error(), "convertible_to") {
		t.Errorf("err should suggest convertible_to entry, got: %v", err)
	}
}

func TestPlan_CustomTypeChange_DropAndCreate(t *testing.T) {
	cls := testClassifier(t)
	registry := map[string]*pgpb.CustomType{
		"a": {Alias: "a", SqlType: "a"},
		"b": {Alias: "b", SqlType: "b"},
	}
	prev := mkCustomTypeSchema("a", "a", registry)
	curr := mkCustomTypeSchema("b", "b", registry)

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_DROP_AND_CREATE,
		Actor:     "test",
	}}
	plan, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	up := plan.Migrations[0].GetUpSql()
	if !strings.Contains(up, "DROP COLUMN payload") || !strings.Contains(up, "ADD COLUMN payload") {
		t.Errorf("expected DROP+ADD on payload, got:\n%s", up)
	}
}

// TestPlan_B1_PkFlip_NonCustomHardErrors — B1 fix: pk_flip with a
// non-CUSTOM strategy previously silent-empty-migrated; now hard
// errors with a pointer to the --decide custom path.
func TestPlan_B1_PkFlip_NonCustomHardErrors(t *testing.T) {
	cls := testClassifier(t)
	mk := func(pkOnFlag bool) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: !pkOnFlag},
				{Name: "flag", ProtoName: "flag", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_CHAR,
					MaxLen: 32, Pk: pkOnFlag},
			},
			PrimaryKey: func() []string {
				if pkOnFlag {
					return []string{"flag"}
				}
				return []string{"id"}
			}(),
		}}}
	}
	prev := mk(false)
	curr := mk(true)

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if len(probe.Findings) == 0 {
		t.Fatal("want pk_flip finding")
	}
	// Find the pk_flip finding specifically.
	var pkID string
	for _, f := range probe.Findings {
		if f.GetAxis() == "pk_flip" {
			pkID = f.GetId()
			break
		}
	}
	if pkID == "" {
		t.Fatal("no pk_flip finding in probe")
	}
	res := []*planpb.Resolution{{
		FindingId: pkID,
		Strategy:  planpb.Strategy_DROP_AND_CREATE,
		Actor:     "test",
	}}
	_, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err == nil {
		t.Fatal("want B1 hard-error on pk_flip + DROP_AND_CREATE")
	}
	if !strings.Contains(err.Error(), "only CUSTOM_MIGRATION is accepted") {
		t.Errorf("err should mention only CUSTOM_MIGRATION allowed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--decide") {
		t.Errorf("err should point at --decide, got: %v", err)
	}
}
