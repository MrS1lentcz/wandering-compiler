package engine_test

// D40 integration tests — engine.Plan on an enum FQN change,
// exercising:
//   (1) Identical-values FQN swap → no-op DDL marker.
//   (2) FQN swap with shrinking value set → rebuild template via
//       the same path as D37 enum_values_remove.
//   (3) FQN swap with growing value set → ALTER TYPE ADD VALUE
//       per added name (pure-add SAFE shape under NEEDS_CONFIRM
//       gate).
//   (4) Wrong strategy → hard-error.

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func mkEnumFqnSchema(fqn string, values ...string) *irpb.Schema {
	numbers := make([]int64, len(values))
	for i := range values {
		numbers[i] = int64(i + 1)
	}
	return &irpb.Schema{Tables: []*irpb.Table{{
		Name: "items", MessageFqn: "shop.Item",
		Columns: []*irpb.Column{
			{Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
				DbType: irpb.DbType_DBT_BIGINT, Pk: true},
			{Name: "kind", ProtoName: "kind", FieldNumber: 2,
				Carrier:     irpb.Carrier_CARRIER_STRING,
				Type:        irpb.SemType_SEM_ENUM,
				EnumFqn:     fqn,
				EnumNames:   values,
				EnumNumbers: numbers},
		},
		PrimaryKey: []string{"id"},
	}}}
}

func TestPlan_EnumFqnChange_IdenticalValues_NoOp(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumFqnSchema("shop.KindV1", "alpha", "beta")
	curr := mkEnumFqnSchema("shop.KindV2", "alpha", "beta")

	probe, err := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(probe.Findings) != 1 || probe.Findings[0].GetAxis() != "enum_fqn_change" {
		t.Fatalf("want 1 enum_fqn_change finding, got %v", probe.Findings)
	}
	if probe.Findings[0].GetProposed() != planpb.Strategy_NEEDS_CONFIRM {
		t.Fatalf("want Proposed=NEEDS_CONFIRM, got %s", probe.Findings[0].GetProposed())
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
	up := plan.Migrations[0].GetUpSql()
	if !strings.Contains(up, "PG type items_kind unchanged") {
		t.Errorf("up should carry no-op marker for identical-values FQN swap:\n%s", up)
	}
	if strings.Contains(up, "CREATE TYPE") || strings.Contains(up, "ALTER TYPE") {
		t.Errorf("up should not emit DDL for identical-values FQN swap:\n%s", up)
	}
}

func TestPlan_EnumFqnChange_ShrinkingValues_Rebuild(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumFqnSchema("shop.KindV1", "alpha", "beta", "gamma")
	curr := mkEnumFqnSchema("shop.KindV2", "alpha", "beta")

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if len(probe.Findings) != 1 || probe.Findings[0].GetAxis() != "enum_fqn_change" {
		t.Fatalf("want enum_fqn_change finding, got %v", probe.Findings)
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
	up := plan.Migrations[0].GetUpSql()
	for _, frag := range []string{
		"CREATE TYPE items_kind_new AS ENUM ('alpha', 'beta');",
		"ALTER TABLE items ALTER COLUMN kind TYPE items_kind_new USING kind::text::items_kind_new;",
		"DROP TYPE items_kind;",
		"ALTER TYPE items_kind_new RENAME TO items_kind;",
	} {
		if !strings.Contains(up, frag) {
			t.Errorf("up missing %q\n---\n%s", frag, up)
		}
	}
}

func TestPlan_EnumFqnChange_GrowingValues_AddValue(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumFqnSchema("shop.KindV1", "alpha", "beta")
	curr := mkEnumFqnSchema("shop.KindV2", "alpha", "beta", "gamma")

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_NEEDS_CONFIRM,
		Actor:     "test",
	}}
	plan, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	up := plan.Migrations[0].GetUpSql()
	if !strings.Contains(up, "ALTER TYPE items_kind ADD VALUE 'gamma';") {
		t.Errorf("up missing ADD VALUE for added name:\n%s", up)
	}
	if strings.Contains(up, "CREATE TYPE") {
		t.Errorf("up should not rebuild on adds-only FQN change:\n%s", up)
	}
}

func TestPlan_EnumFqnChange_WrongStrategyHardErrors(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumFqnSchema("shop.KindV1", "alpha", "beta")
	curr := mkEnumFqnSchema("shop.KindV2", "alpha", "beta")

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
}
