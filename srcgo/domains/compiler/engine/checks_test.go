package engine_test

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// TestChecks_NullableTighten — nullable NULL → NOT NULL is the
// canonical NEEDS_CONFIRM case. Plan should emit a NamedSQL with the
// rendered "count NULL rows" query per constraint.yaml.
func TestChecks_NullableTighten(t *testing.T) {
	cls := testClassifier(t)
	// prev nullable, curr NOT NULL → tighten.
	mk := func(nullable bool) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true, Nullable: false},
				{Name: "email", ProtoName: "email", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_EMAIL,
					DbType: irpb.DbType_DBT_TEXT, Nullable: nullable},
			},
			PrimaryKey: []string{"id"},
		}}}
	}
	plan, err := engine.Plan(mk(true), mk(false), cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Migrations) != 1 {
		t.Fatalf("len(Migrations) = %d, want 1", len(plan.Migrations))
	}
	checks := plan.Migrations[0].GetChecks()
	// Expect at least one check for nullable_email.
	found := false
	for _, c := range checks {
		if c.GetName() == "nullable_email" {
			found = true
			if !strings.Contains(c.GetSql(), "IS NULL") {
				t.Errorf("nullable_email SQL doesn't reference IS NULL: %q", c.GetSql())
			}
			if !strings.Contains(c.GetSql(), "users") {
				t.Errorf("nullable_email SQL doesn't reference the table: %q", c.GetSql())
			}
			if !strings.Contains(c.GetSql(), "email") {
				t.Errorf("nullable_email SQL doesn't reference the column: %q", c.GetSql())
			}
		}
	}
	if !found {
		t.Errorf("no nullable_email check emitted; got %+v", checks)
	}
}

// TestChecks_MaxLenNarrow — max_len narrow is NEEDS_CONFIRM with a
// char_length check. Template must interpolate the new max_len.
func TestChecks_MaxLenNarrow(t *testing.T) {
	cls := testClassifier(t)
	mk := func(maxLen int32) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true},
				{Name: "name", ProtoName: "name", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_CHAR,
					DbType: irpb.DbType_DBT_VARCHAR, MaxLen: maxLen},
			},
			PrimaryKey: []string{"id"},
		}}}
	}
	plan, err := engine.Plan(mk(200), mk(50), cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var check *planpb.NamedSQL
	for _, c := range plan.Migrations[0].GetChecks() {
		if c.GetName() == "max_len_name" {
			check = c
			break
		}
	}
	if check == nil {
		t.Fatalf("no max_len_name check emitted; got %v", plan.Migrations[0].GetChecks())
	}
	if !strings.Contains(check.GetSql(), "char_length") {
		t.Errorf("max_len_name SQL missing char_length: %q", check.GetSql())
	}
	if !strings.Contains(check.GetSql(), "> 50") {
		t.Errorf("max_len_name SQL should reference the new bound (50); got %q", check.GetSql())
	}
}

// TestChecks_SafeTransitionEmitsNone — relaxing nullable (NOT NULL →
// NULL) is SAFE; no check should be emitted.
func TestChecks_SafeTransitionEmitsNone(t *testing.T) {
	cls := testClassifier(t)
	mk := func(nullable bool) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true},
				{Name: "email", ProtoName: "email", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_EMAIL,
					DbType: irpb.DbType_DBT_TEXT, Nullable: nullable},
			},
			PrimaryKey: []string{"id"},
		}}}
	}
	// nullable: false → true = relax, SAFE
	plan, err := engine.Plan(mk(false), mk(true), cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range plan.Migrations[0].GetChecks() {
		if c.GetName() == "nullable_email" {
			t.Errorf("SAFE relax should emit no check; got %+v", c)
		}
	}
}
