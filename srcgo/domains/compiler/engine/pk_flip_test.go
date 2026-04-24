package engine_test

// D39 integration tests — engine.Plan on a pk flag transition,
// exercising:
//   (1) pk enable (no PK → col is PK): AlterColumn with
//       PrimaryKeyChange(from=false,to=true); emit ADD PRIMARY KEY.
//   (2) pk disable (col is PK → no PK): AlterColumn with
//       PrimaryKeyChange(from=true,to=false); emit DROP CONSTRAINT
//       <table>_pkey.
//   (3) Wrong strategy (DROP_AND_CREATE / SAFE) → hard-error.
//   (4) PK swap (two pk_flip findings on one table) → hard-error
//       pointing at CUSTOM_MIGRATION. (Covered by
//       TestPlan_PkFlip_SwapHardErrors in custom_type_test.go.)

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// mkPkFlipSchema — two-column table; the `target` column's Pk flag
// is the knob. Prev/curr differ only on that flag and on
// Table.PrimaryKey. `id` is a plain BIGINT counter so the schema is
// valid (AddTable doesn't require a PK column today, but keep the
// shape consistent with other D38/D37 synth).
func mkPkFlipSchema(targetPk bool) *irpb.Schema {
	var pkList []string
	if targetPk {
		pkList = []string{"target"}
	}
	return &irpb.Schema{Tables: []*irpb.Table{{
		Name: "things", MessageFqn: "mod.Thing",
		Columns: []*irpb.Column{
			{Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64,
				Type:    irpb.SemType_SEM_COUNTER,
				DbType:  irpb.DbType_DBT_BIGINT},
			{Name: "target", ProtoName: "target", FieldNumber: 2,
				Carrier: irpb.Carrier_CARRIER_INT64,
				Type:    irpb.SemType_SEM_ID,
				DbType:  irpb.DbType_DBT_BIGINT,
				Pk:      targetPk},
		},
		PrimaryKey: pkList,
	}}}
}

func TestPlan_PkFlipEnable_NeedsConfirm(t *testing.T) {
	cls := testClassifier(t)
	prev := mkPkFlipSchema(false)
	curr := mkPkFlipSchema(true)

	probe, err := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(probe.Findings) != 1 || probe.Findings[0].GetAxis() != "pk_flip" {
		t.Fatalf("want 1 pk_flip finding, got %v", probe.Findings)
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
	if !strings.Contains(up, "ALTER TABLE things ADD PRIMARY KEY (target);") {
		t.Errorf("up missing ADD PRIMARY KEY:\n%s", up)
	}
	down := plan.Migrations[0].GetDownSql()
	if !strings.Contains(down, "ALTER TABLE things DROP CONSTRAINT things_pkey;") {
		t.Errorf("down missing DROP CONSTRAINT things_pkey:\n%s", down)
	}
}

func TestPlan_PkFlipDisable_NeedsConfirm(t *testing.T) {
	cls := testClassifier(t)
	prev := mkPkFlipSchema(true)
	curr := mkPkFlipSchema(false)

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
	if !strings.Contains(up, "ALTER TABLE things DROP CONSTRAINT things_pkey;") {
		t.Errorf("up missing DROP CONSTRAINT:\n%s", up)
	}
	down := plan.Migrations[0].GetDownSql()
	if !strings.Contains(down, "ALTER TABLE things ADD PRIMARY KEY (target);") {
		t.Errorf("down missing ADD PRIMARY KEY:\n%s", down)
	}
}

func TestPlan_PkFlip_WrongStrategyHardErrors(t *testing.T) {
	cls := testClassifier(t)
	prev := mkPkFlipSchema(false)
	curr := mkPkFlipSchema(true)

	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	res := []*planpb.Resolution{{
		FindingId: probe.Findings[0].GetId(),
		Strategy:  planpb.Strategy_SAFE,
		Actor:     "test",
	}}
	_, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err == nil {
		t.Fatal("want hard-error on SAFE resolution")
	}
	if !strings.Contains(err.Error(), "only NEEDS_CONFIRM") {
		t.Errorf("err should name NEEDS_CONFIRM, got: %v", err)
	}
}
