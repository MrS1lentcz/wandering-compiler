package engine_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func testClassifier(t *testing.T) *classifier.Classifier {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "classification")
	c, err := classifier.Load(dir)
	if err != nil {
		t.Fatalf("classifier.Load: %v", err)
	}
	return c
}

// pgOnlyEmitter is a minimal EmitterFor that maps every Connection to
// the Postgres emitter. Sufficient for tests that don't cover dialect
// dispatch.
func pgOnlyEmitter(_ *irpb.Connection) (emit.DialectEmitter, error) {
	return postgres.Emitter{}, nil
}

// singleTableSchema — a minimal IR with one table for initial-migration
// tests. No indexes / FKs / checks — keeps emit SQL deterministic +
// compact for assertions.
func singleTableSchema() *irpb.Schema {
	return &irpb.Schema{Tables: []*irpb.Table{{
		Name:       "users",
		MessageFqn: "shop.User",
		Columns: []*irpb.Column{
			{Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64,
				Type:    irpb.SemType_SEM_ID,
				DbType:  irpb.DbType_DBT_BIGINT,
				Pk:      true},
		},
		PrimaryKey: []string{"id"},
	}}}
}

func TestPlan_InitialMigration(t *testing.T) {
	cls := testClassifier(t)
	plan, err := engine.Plan(nil, singleTableSchema(), cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Migrations) != 1 {
		t.Fatalf("len(Migrations) = %d, want 1", len(plan.Migrations))
	}
	m := plan.Migrations[0]
	if m.UpSql == "" || m.DownSql == "" {
		t.Errorf("migration SQL empty: up=%q down=%q", m.UpSql, m.DownSql)
	}
	if len(plan.Findings) != 0 {
		t.Errorf("unexpected findings on initial migration: %v", plan.Findings)
	}
}

func TestPlan_Noop(t *testing.T) {
	cls := testClassifier(t)
	schema := singleTableSchema()
	plan, err := engine.Plan(schema, schema, cls, nil, pgOnlyEmitter)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Migrations) != 0 {
		t.Errorf("no-op diff should produce no Migration; got %d", len(plan.Migrations))
	}
}

// TestPlan_CarrierChange_Unresolved — decision-required axis without
// a Resolution surfaces as a Finding; the bucket still emits its
// non-carrier-change Ops (here: nothing, because the only column
// changed carrier, so the bucket is otherwise empty).
func TestPlan_CarrierChange_Unresolved(t *testing.T) {
	cls := testClassifier(t)
	mk := func(carrier irpb.Carrier) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: carrier,
					Type:    irpb.SemType_SEM_ID,
					Pk:      true},
			},
			PrimaryKey: []string{"id"},
		}}}
	}
	result, err := engine.Plan(
		mk(irpb.Carrier_CARRIER_INT64),
		mk(irpb.Carrier_CARRIER_STRING),
		cls, nil, pgOnlyEmitter,
	)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("len(Findings) = %d, want 1", len(result.Findings))
	}
	f := result.Findings[0]
	if f.GetAxis() != "carrier_change" {
		t.Errorf("axis = %q, want carrier_change", f.GetAxis())
	}
	if len(result.Migrations) != 0 {
		t.Errorf("expected no Migration for carrier-only-change bucket; got %d", len(result.Migrations))
	}
}

// TestPlan_CarrierChange_Resolved — same carrier change, but caller
// supplies a Resolution matching the finding ID. The finding moves
// from Plan.Findings into the Migration's Manifest.AppliedResolutions;
// Plan.Findings is now empty.
func TestPlan_CarrierChange_Resolved(t *testing.T) {
	cls := testClassifier(t)
	mk := func(carrier irpb.Carrier) *irpb.Schema {
		return &irpb.Schema{Tables: []*irpb.Table{{
			Name: "users", MessageFqn: "shop.User",
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: carrier,
					Type:    irpb.SemType_SEM_ID,
					Pk:      true},
			},
			PrimaryKey: []string{"id"},
		}}}
	}
	// Round 1: probe to learn the finding ID.
	probe, _ := engine.Plan(
		mk(irpb.Carrier_CARRIER_INT64),
		mk(irpb.Carrier_CARRIER_STRING),
		cls, nil, pgOnlyEmitter,
	)
	if len(probe.Findings) != 1 {
		t.Fatalf("probe: expected 1 finding, got %d", len(probe.Findings))
	}
	findingID := probe.Findings[0].GetId()

	// Round 2: supply a matching Resolution.
	resolutions := []*planpb.Resolution{{
		FindingId: findingID,
		Strategy:  planpb.Strategy_DROP_AND_CREATE,
		Actor:     "test",
	}}
	result, err := engine.Plan(
		mk(irpb.Carrier_CARRIER_INT64),
		mk(irpb.Carrier_CARRIER_STRING),
		cls, resolutions, pgOnlyEmitter,
	)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected Findings to clear after Resolution; got %d", len(result.Findings))
	}
	// The bucket had no other changes than the carrier flip, so the
	// Migration is still empty (no SQL to emit). Manifest-only effect
	// of Resolution lives in the Migration envelope — only present
	// when a Migration was emitted for other changes.
}

// TestPlan_Idempotence — same inputs produce byte-identical output.
// Underpins D30 idempotence claim + the cache-warming story for
// re-runs during resolution loops.
func TestPlan_Idempotence(t *testing.T) {
	cls := testClassifier(t)
	schema := singleTableSchema()
	r1, _ := engine.Plan(nil, schema, cls, nil, pgOnlyEmitter)
	r2, _ := engine.Plan(nil, schema, cls, nil, pgOnlyEmitter)
	if r1.Migrations[0].UpSql != r2.Migrations[0].UpSql {
		t.Errorf("non-deterministic up SQL")
	}
}

func TestPlan_MultiConnection(t *testing.T) {
	cls := testClassifier(t)
	pgConn := &irpb.Connection{Name: "main", Dialect: irpb.Dialect_POSTGRES, Version: "18"}
	redisConn := &irpb.Connection{Name: "cache", Dialect: irpb.Dialect_REDIS, Version: "7"}

	schema := &irpb.Schema{Tables: []*irpb.Table{
		{
			Name: "users", MessageFqn: "shop.User",
			Connection: pgConn,
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true},
			},
			PrimaryKey: []string{"id"},
		},
		{
			Name: "sessions", MessageFqn: "shop.Session",
			Connection: redisConn,
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_ID, Pk: true},
			},
			PrimaryKey: []string{"id"},
		},
	}}

	dispatched := map[irpb.Dialect]emit.DialectEmitter{
		irpb.Dialect_POSTGRES: postgres.Emitter{},
	}
	emitterFor := func(c *irpb.Connection) (emit.DialectEmitter, error) {
		if c == nil {
			return postgres.Emitter{}, nil
		}
		em, ok := dispatched[c.GetDialect()]
		if !ok {
			return nil, fmt.Errorf("unsupported dialect %s (test is PG-only)", c.GetDialect())
		}
		return em, nil
	}
	// Only PG is wired; Redis bucket should error.
	_, err := engine.Plan(nil, schema, cls, nil, emitterFor)
	if err == nil {
		t.Error("unsupported dialect should surface as Plan error")
	}
}

func TestPlan_RequiresClassifier(t *testing.T) {
	_, err := engine.Plan(nil, singleTableSchema(), nil, nil, pgOnlyEmitter)
	if err == nil {
		t.Error("nil classifier should error")
	}
}

func TestPlan_RequiresEmitterFor(t *testing.T) {
	cls := testClassifier(t)
	_, err := engine.Plan(nil, singleTableSchema(), cls, nil, nil)
	if err == nil {
		t.Error("nil emitterFor should error")
	}
}
