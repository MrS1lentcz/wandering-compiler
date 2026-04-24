package engine_test

// End-to-end smoke: engine.Plan on a realistic alter migration
// emits RISK comments in up/down SQL and populates
// Manifest.RiskFindings. Pins the D35 integration point without
// spinning up Docker (that's what the e2e package does — this
// is the in-process cheap check).

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

func TestPlan_EmitsRiskCommentsOnAlter(t *testing.T) {
	cls := testClassifier(t)
	mk := func(nullable bool) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true},
				{Name: "email", ProtoName: "email", FieldNumber: 2,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_CHAR,
					MaxLen: 255, Nullable: nullable},
			},
			PrimaryKey: []string{"id"},
		}}}
	}
	prev := mk(true)  // email nullable
	curr := mk(false) // email NOT NULL → nullable_tighten risk

	plan, err := engine.Plan(prev, curr, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Migrations) != 1 {
		t.Fatalf("want 1 migration, got %d", len(plan.Migrations))
	}

	// (1) up.sql carries the RISK comment header.
	up := plan.Migrations[0].GetUpSql()
	if !strings.Contains(up, "-- RISK MEDIUM [nullable_tighten]") {
		t.Errorf("up.sql missing RISK header for nullable_tighten:\n%s", up)
	}
	if !strings.Contains(up, "Migration risk analysis") {
		t.Errorf("up.sql missing analysis banner:\n%s", up)
	}

	// (2) down.sql also carries a RISK header (relaxing NOT NULL is LOW,
	// but header still emits because analysis reports both directions).
	down := plan.Migrations[0].GetDownSql()
	if !strings.Contains(down, "Migration risk analysis") {
		t.Errorf("down.sql missing analysis banner:\n%s", down)
	}

	// (3) Manifest carries structured RiskFindings.
	m := plan.Migrations[0].GetManifest()
	if m == nil {
		t.Fatal("manifest is nil; want non-nil with RiskFindings populated")
	}
	rfs := m.GetRiskFindings()
	if len(rfs) == 0 {
		t.Fatal("Manifest.RiskFindings is empty; want the nullable_tighten entry")
	}
	var found bool
	for _, rf := range rfs {
		if rf.GetOpKind() == "nullable_tighten" && rf.GetSeverity() == "MEDIUM" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected nullable_tighten MEDIUM in manifest, got %v", rfs)
	}
}

func TestPlan_NoRiskCommentsOnInitialMigration(t *testing.T) {
	cls := testClassifier(t)
	// Initial migration (prev=nil) — all AddTable, no alters → no risks.
	plan, err := engine.Plan(nil, singleTableSchema(), cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	up := plan.Migrations[0].GetUpSql()
	if strings.Contains(up, "-- RISK") || strings.Contains(up, "Migration risk analysis") {
		t.Errorf("initial migration should have no RISK header, got:\n%s", up)
	}
}
