package engine_test

// D37 integration tests — engine.Plan on an alter that drops an
// ENUM value, exercising:
//   (1) NEEDS_CONFIRM → AlterColumn with EnumValuesChange
//       carrying RemovedNames; emit renders 4-statement rebuild.
//   (2) Non-NEEDS_CONFIRM resolution → hard-error with --decide
//       needs_confirm guidance.

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func mkEnumSchema(values ...string) *irpb.Schema {
	numbers := make([]int64, len(values))
	for i := range values {
		numbers[i] = int64(i + 1)
	}
	return &irpb.Schema{Tables: []*irpb.Table{{
		Name: "orders", MessageFqn: "shop.Order",
		Columns: []*irpb.Column{
			{Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
				DbType: irpb.DbType_DBT_BIGINT, Pk: true},
			{Name: "status", ProtoName: "status", FieldNumber: 2,
				Carrier:     irpb.Carrier_CARRIER_STRING,
				Type:        irpb.SemType_SEM_ENUM,
				EnumFqn:     "shop.OrderStatus",
				EnumNames:   values,
				EnumNumbers: numbers},
		},
		PrimaryKey: []string{"id"},
	}}}
}

func TestPlan_EnumValuesRemove_NeedsConfirm(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumSchema("active", "archived", "deprecated")
	curr := mkEnumSchema("active", "archived")

	probe, err := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(probe.Findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(probe.Findings))
	}
	if axis := probe.Findings[0].GetAxis(); axis != "enum_values_remove" {
		t.Fatalf("want axis enum_values_remove, got %s", axis)
	}
	if proposed := probe.Findings[0].GetProposed(); proposed != planpb.Strategy_NEEDS_CONFIRM {
		t.Fatalf("want Proposed=NEEDS_CONFIRM, got %s", proposed)
	}

	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_NEEDS_CONFIRM,
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
	for _, frag := range []string{
		"CREATE TYPE orders_status_new AS ENUM ('active', 'archived');",
		"ALTER TABLE orders ALTER COLUMN status TYPE orders_status_new USING status::text::orders_status_new;",
		"DROP TYPE orders_status;",
		"ALTER TYPE orders_status_new RENAME TO orders_status;",
	} {
		if !strings.Contains(up, frag) {
			t.Errorf("up missing fragment %q\n---\n%s", frag, up)
		}
	}
	down := plan.Migrations[0].GetDownSql()
	if !strings.Contains(down, "'active', 'archived', 'deprecated'") {
		t.Errorf("down should restore 'deprecated'; got:\n%s", down)
	}
}

func TestPlan_EnumValuesRemove_WrongStrategyHardErrors(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumSchema("a", "b", "c")
	curr := mkEnumSchema("a", "b")

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_DROP_AND_CREATE,
		Actor:     "test",
	}}
	_, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err == nil {
		t.Fatal("want hard-error on DROP_AND_CREATE resolution")
	}
	if !strings.Contains(err.Error(), "only NEEDS_CONFIRM") {
		t.Errorf("err should name NEEDS_CONFIRM, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--decide") {
		t.Errorf("err should point at --decide, got: %v", err)
	}
}
