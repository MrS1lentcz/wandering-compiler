package engine_test

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestPlan_ManifestPopulatedFromEmitterUsage — running Plan against a
// schema with a JSONB column + required extension surfaces both on
// Migration.Manifest: JSONB as a Capability (recorded by PG emitter),
// no catalog-derived extensions (JSONB has none), and the IR-level
// required_extensions from PgOptions flow through verbatim.
func TestPlan_ManifestPopulatedFromEmitterUsage(t *testing.T) {
	cls := testClassifier(t)
	schema := &irpb.Schema{Tables: []*irpb.Table{{
		Name:       "documents",
		MessageFqn: "pkg.Document",
		Columns: []*irpb.Column{
			{Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
				DbType: irpb.DbType_DBT_BIGINT, Pk: true},
			{Name: "payload", ProtoName: "payload", FieldNumber: 2, Nullable: true,
				Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_JSON,
				Pg: &irpb.PgOptions{
					RequiredExtensions: []string{"pg_jsonschema"},
				}},
		},
		PrimaryKey: []string{"id"},
	}}}

	plan, err := engine.Plan(nil, schema, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Migrations) != 1 {
		t.Fatalf("want 1 migration, got %d", len(plan.Migrations))
	}
	m := plan.Migrations[0].GetManifest()
	if m == nil {
		t.Fatal("manifest is nil; want non-empty after JSONB column + required_extensions")
	}

	if !containsCap(m.GetCapabilities(), "JSONB") {
		t.Errorf("Capabilities missing JSONB: %v", m.GetCapabilities())
	}
	if !containsCap(m.GetCapabilities(), "TRANSACTIONAL_DDL") {
		t.Errorf("Capabilities missing TRANSACTIONAL_DDL: %v", m.GetCapabilities())
	}
	if !containsCap(m.GetRequiredExtensions(), "pg_jsonschema") {
		t.Errorf("RequiredExtensions missing pg_jsonschema (from IR): %v", m.GetRequiredExtensions())
	}
	// Sorted + deduped invariant: Capabilities must be monotonic.
	prev := ""
	for _, c := range m.GetCapabilities() {
		if c <= prev {
			t.Errorf("Capabilities not sorted/unique: %v", m.GetCapabilities())
			break
		}
		prev = c
	}
}

// TestPlan_ManifestNilForBareSchemaNoCaps — a schema whose columns
// touch zero tagged caps (plain INTEGER BIGINT only, no PG
// extensions, no resolutions) still surfaces the TRANSACTIONAL_DDL
// cap because Emit() wrapped a non-empty migration. The manifest
// therefore is non-nil — but contains zero RequiredExtensions, zero
// AppliedResolutions.
func TestPlan_ManifestTransactionalOnlyForPlainSchema(t *testing.T) {
	cls := testClassifier(t)
	plan, err := engine.Plan(nil, singleTableSchema(), cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	m := plan.Migrations[0].GetManifest()
	if m == nil {
		t.Fatal("manifest is nil; want TRANSACTIONAL_DDL cap recorded")
	}
	if !containsCap(m.GetCapabilities(), "TRANSACTIONAL_DDL") {
		t.Errorf("Capabilities missing TRANSACTIONAL_DDL: %v", m.GetCapabilities())
	}
	if len(m.GetRequiredExtensions()) != 0 {
		t.Errorf("RequiredExtensions should be empty, got %v", m.GetRequiredExtensions())
	}
	if len(m.GetAppliedResolutions()) != 0 {
		t.Errorf("AppliedResolutions should be empty, got %v", m.GetAppliedResolutions())
	}
}

func containsCap(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
