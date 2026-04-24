package engine_test

// Coverage push for D36-D40 risk emission. Exercises the full
// Plan → analyzeRisks → renderRiskComments chain on every NEEDS_CONFIRM
// rebuild path so the per-axis risk profile + factChangeKind branch
// is hit end-to-end. Also covers sortRisksByOpKind (multiple-Op
// migrations) and the mapFindingAxisToProfileKey pk_flip context-
// based dispatch.

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func TestRiskHeader_D37_EnumValuesRemove(t *testing.T) {
	cls := testClassifier(t)
	prev := mkEnumSchema("alpha", "beta", "gamma")
	curr := mkEnumSchema("alpha", "beta")
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
	if !strings.Contains(up, "[enum_values_remove]") {
		t.Errorf("up should carry D37 risk header, got:\n%s", up)
	}
	if !strings.Contains(up, "HIGH") {
		t.Errorf("D37 risk should be HIGH severity:\n%s", up)
	}
}

func TestRiskHeader_D38_DefaultIdentityAdd(t *testing.T) {
	cls := testClassifier(t)
	prev := mkIdentityBaseSchema(nil)
	curr := mkIdentityBaseSchema(&irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_IDENTITY}})
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
	if !strings.Contains(up, "[default_identity_add]") {
		t.Errorf("up should carry D38 identity_add risk header:\n%s", up)
	}
	if !strings.Contains(up, "MEDIUM") {
		t.Errorf("D38 identity_add risk should be MEDIUM severity:\n%s", up)
	}
}

func TestRiskHeader_D38_DefaultIdentityDrop(t *testing.T) {
	cls := testClassifier(t)
	prev := mkIdentityBaseSchema(&irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_IDENTITY}})
	curr := mkIdentityBaseSchema(nil)
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
	if !strings.Contains(up, "[default_identity_drop]") {
		t.Errorf("up should carry D38 identity_drop risk header:\n%s", up)
	}
	if !strings.Contains(up, "LOW") {
		t.Errorf("D38 identity_drop risk should be LOW severity:\n%s", up)
	}
}

func TestRiskHeader_D39_PkFlipEnable(t *testing.T) {
	cls := testClassifier(t)
	prev := mkPkFlipSchema(false)
	curr := mkPkFlipSchema(true)
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
	if !strings.Contains(up, "[pk_flip_enable]") {
		t.Errorf("up should carry D39 pk_flip_enable risk header (not _disable):\n%s", up)
	}
}

func TestRiskHeader_D39_PkFlipDisable(t *testing.T) {
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
	if !strings.Contains(up, "[pk_flip_disable]") {
		t.Errorf("up should carry D39 pk_flip_disable risk header:\n%s", up)
	}
}

// TestSortRisksByOpKind_SameSeverityBucket exercises the inner
// swap branch of sortRisksByOpKind. Two HIGH-severity risks on
// the same migration (enum_values_remove + pk_flip_disable on
// different columns of one table) → both end up in the HIGH
// bucket and sortRisksByOpKind orders them alphabetically by
// op_kind. Without the swap, insertion-order would put pk first
// (since the differ visits columns in declaration order); the
// expected post-sort order has enum_values_remove first.
func TestSortRisksByOpKind_SameSeverityBucket(t *testing.T) {
	cls := testClassifier(t)
	mk := func(targetPk bool, enumValues []string) *irpb.Schema {
		nums := make([]int64, len(enumValues))
		for i := range enumValues {
			nums[i] = int64(i + 1)
		}
		var pk []string
		if targetPk {
			pk = []string{"id"}
		}
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "things", MessageFqn: "mod.Thing",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: targetPk},
				{Name: "kind", ProtoName: "kind", FieldNumber: 2,
					Carrier:     irpb.Carrier_CARRIER_STRING,
					Type:        irpb.SemType_SEM_ENUM,
					EnumFqn:     "mod.Kind",
					EnumNames:   enumValues,
					EnumNumbers: nums},
			},
			PrimaryKey: pk,
		}}}
	}
	prev := mk(true, []string{"alpha", "beta", "gamma"})
	curr := mk(false, []string{"alpha", "beta"})
	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if len(probe.Findings) != 2 {
		t.Fatalf("want 2 findings (pk_flip + enum_values_remove), got %d", len(probe.Findings))
	}
	var res []*planpb.Resolution
	for _, f := range probe.Findings {
		res = append(res, &planpb.Resolution{
			FindingId: f.GetId(),
			Strategy:  planpb.Strategy_NEEDS_CONFIRM,
			Actor:     "test",
		})
	}
	plan, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	up := plan.Migrations[0].GetUpSql()
	enumIdx := strings.Index(up, "[enum_values_remove]")
	pkIdx := strings.Index(up, "[pk_flip_disable]")
	if enumIdx < 0 || pkIdx < 0 {
		t.Fatalf("both risks expected in header; got enumIdx=%d pkIdx=%d\n%s", enumIdx, pkIdx, up)
	}
	if enumIdx > pkIdx {
		t.Errorf("alphabetical sort within HIGH bucket: enum_values_remove < pk_flip_disable;"+
			" got enum at %d, pk at %d", enumIdx, pkIdx)
	}
}

// TestSortRisksByOpKind exercises the multi-risk sort path. Two
// independent NEEDS_CONFIRM transitions on the same migration:
// pk_flip + default_identity_drop. analyzeRisks should emit two
// risks; renderRiskComments orders them deterministically so the
// header is stable across runs.
func TestSortRisksByOpKind(t *testing.T) {
	cls := testClassifier(t)
	mk := func(targetPk bool, idDefault *irpb.Default) *irpb.Schema {
		idCol := &irpb.Column{
			Name: "id", ProtoName: "id", FieldNumber: 1,
			Carrier: irpb.Carrier_CARRIER_INT64,
			Type:    irpb.SemType_SEM_ID,
			DbType:  irpb.DbType_DBT_BIGINT,
			Default: idDefault,
		}
		var pk []string
		if targetPk {
			pk = []string{"target"}
		}
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "things", MessageFqn: "mod.Thing",
			Columns: []*irpb.Column{
				idCol,
				{Name: "target", ProtoName: "target", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: targetPk},
			},
			PrimaryKey: pk,
		}}}
	}
	prev := mk(false, &irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_IDENTITY}})
	curr := mk(true, nil)
	probe, _ := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if len(probe.Findings) != 2 {
		t.Fatalf("want 2 findings (pk_flip + default_identity_drop), got %d", len(probe.Findings))
	}
	var res []*planpb.Resolution
	for _, f := range probe.Findings {
		res = append(res, &planpb.Resolution{
			FindingId: f.GetId(),
			Strategy:  planpb.Strategy_NEEDS_CONFIRM,
			Actor:     "test",
		})
	}
	plan, err := engine.Plan(prev, curr, cls, res, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	up := plan.Migrations[0].GetUpSql()
	// Both risk profile keys should appear; order is deterministic
	// (alphabetical on op_kind via sortRisksByOpKind).
	if !strings.Contains(up, "[default_identity_drop]") {
		t.Errorf("missing default_identity_drop risk:\n%s", up)
	}
	if !strings.Contains(up, "[pk_flip_enable]") {
		t.Errorf("missing pk_flip_enable risk:\n%s", up)
	}
	// sortedByImpact orders HIGH → MEDIUM → LOW; pk_flip_enable HIGH
	// outranks default_identity_drop LOW so pk header appears first.
	defaultIdx := strings.Index(up, "[default_identity_drop]")
	pkIdx := strings.Index(up, "[pk_flip_enable]")
	if pkIdx > defaultIdx {
		t.Errorf("risk order should be severity-first (HIGH pk_flip_enable before LOW default_identity_drop); got pk at %d, default at %d", pkIdx, defaultIdx)
	}
}
