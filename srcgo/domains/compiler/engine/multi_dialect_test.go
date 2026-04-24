package engine_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/redis"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestPlan_MultiDialect_HappyPath — two tables on two different
// connections (PG + Redis), one Plan call. Asserts the engine
// buckets correctly + dispatches each bucket through its own
// emitter + builds a per-bucket Manifest.
//
// This is the orchestration test that should give Layer C (MySQL)
// a safety net: when MySQL lands, just add a third bucket here and
// the test exercises three dialects in one Plan invocation.
func TestPlan_MultiDialect_HappyPath(t *testing.T) {
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
				{Name: "payload", ProtoName: "payload", FieldNumber: 2, Nullable: true,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_JSON},
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

	calls := map[irpb.Dialect]int{}
	emitterFor := func(c *irpb.Connection) (emit.DialectEmitter, error) {
		if c == nil {
			return nil, fmt.Errorf("multi-dialect test must not see a nil connection bucket")
		}
		calls[c.GetDialect()]++
		switch c.GetDialect() {
		case irpb.Dialect_POSTGRES:
			return postgres.Emitter{}, nil
		case irpb.Dialect_REDIS:
			return redis.Emitter{}, nil
		}
		return nil, fmt.Errorf("unexpected dialect: %s", c.GetDialect())
	}

	plan, err := engine.Plan(nil, schema, cls, nil, emitterFor)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if len(plan.Migrations) != 2 {
		t.Fatalf("want 2 migrations (one per connection), got %d", len(plan.Migrations))
	}
	if calls[irpb.Dialect_POSTGRES] != 1 || calls[irpb.Dialect_REDIS] != 1 {
		t.Errorf("EmitterFor calls: want PG=1 Redis=1, got %v", calls)
	}

	byDialect := map[irpb.Dialect]bool{}
	for _, m := range plan.Migrations {
		conn := m.GetConnection()
		if conn == nil {
			t.Errorf("migration carries nil Connection")
			continue
		}
		byDialect[conn.GetDialect()] = true

		switch conn.GetDialect() {
		case irpb.Dialect_POSTGRES:
			if !strings.Contains(m.GetUpSql(), "CREATE TABLE") {
				t.Errorf("PG migration missing CREATE TABLE: %q", m.GetUpSql())
			}
			if !strings.HasPrefix(m.GetUpSql(), "BEGIN;") {
				t.Errorf("PG migration not transactional-wrapped: %q", m.GetUpSql())
			}
			if m.GetManifest() == nil {
				t.Error("PG migration missing manifest")
			} else {
				if !containsCap(m.GetManifest().GetCapabilities(), "JSONB") {
					t.Errorf("PG manifest missing JSONB cap: %v", m.GetManifest().GetCapabilities())
				}
				if !containsCap(m.GetManifest().GetCapabilities(), "TRANSACTIONAL_DDL") {
					t.Errorf("PG manifest missing TRANSACTIONAL_DDL cap: %v", m.GetManifest().GetCapabilities())
				}
			}
		case irpb.Dialect_REDIS:
			if !strings.Contains(m.GetUpSql(), "# wc:") {
				t.Errorf("Redis migration missing `# wc:` marker: %q", m.GetUpSql())
			}
			if strings.HasPrefix(m.GetUpSql(), "BEGIN;") {
				t.Errorf("Redis migration should NOT be transactional-wrapped: %q", m.GetUpSql())
			}
			// Redis emitter doesn't implement DialectCapabilities + records
			// no caps via Use(); manifest stays nil per buildManifest's
			// "all slots empty → nil" rule.
			if m.GetManifest() != nil {
				t.Errorf("Redis migration should have nil manifest, got %v", m.GetManifest())
			}
		}
	}
	if !byDialect[irpb.Dialect_POSTGRES] || !byDialect[irpb.Dialect_REDIS] {
		t.Errorf("missing per-dialect migration: byDialect=%v", byDialect)
	}
}

// TestPlan_MultiDialect_PerBucketIsolation — a finding in the PG
// bucket must not contaminate the Redis bucket's emit (and vice
// versa). Engine.Plan iterates buckets independently; this pins
// that nothing leaks across the boundary.
func TestPlan_MultiDialect_PerBucketIsolation(t *testing.T) {
	cls := testClassifier(t)
	pgConn := &irpb.Connection{Name: "main", Dialect: irpb.Dialect_POSTGRES, Version: "18"}
	redisConn := &irpb.Connection{Name: "cache", Dialect: irpb.Dialect_REDIS, Version: "7"}

	// PG bucket carries an unresolved carrier change. Redis bucket
	// carries an additive AddTable (no decisions).
	prev := &irpb.Schema{Tables: []*irpb.Table{
		{
			Name: "users", MessageFqn: "shop.User", Connection: pgConn,
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT32, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_INTEGER, Pk: true},
			},
			PrimaryKey: []string{"id"},
		},
	}}
	curr := &irpb.Schema{Tables: []*irpb.Table{
		{
			Name: "users", MessageFqn: "shop.User", Connection: pgConn,
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_INT64, Type: irpb.SemType_SEM_ID,
					DbType: irpb.DbType_DBT_BIGINT, Pk: true},
			},
			PrimaryKey: []string{"id"},
		},
		{
			Name: "sessions", MessageFqn: "shop.Session", Connection: redisConn,
			Columns: []*irpb.Column{
				{Name: "id", ProtoName: "id", FieldNumber: 1,
					Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_ID, Pk: true},
			},
			PrimaryKey: []string{"id"},
		},
	}}

	plan, err := engine.Plan(prev, curr, cls,
		nil, // no resolutions — PG carrier change should surface as a Finding
		func(c *irpb.Connection) (emit.DialectEmitter, error) {
			switch c.GetDialect() {
			case irpb.Dialect_POSTGRES:
				return postgres.Emitter{}, nil
			case irpb.Dialect_REDIS:
				return redis.Emitter{}, nil
			}
			return nil, fmt.Errorf("unexpected dialect: %s", c.GetDialect())
		})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Findings are PG-only (the carrier change). Redis bucket
	// produces no findings because AddTable doesn't go through
	// classifier dispatch.
	if len(plan.Findings) == 0 {
		t.Fatal("want at least one PG carrier-change finding, got none")
	}
	for _, f := range plan.Findings {
		if !strings.HasPrefix(f.GetColumn().GetTableFqn(), "shop.User") &&
			f.GetColumn().GetTableName() != "users" {
			t.Errorf("finding leaked into wrong bucket: %v", f)
		}
	}

	// Redis migration should still emit (independent bucket).
	var redisMigration bool
	for _, m := range plan.Migrations {
		if m.GetConnection().GetDialect() == irpb.Dialect_REDIS {
			redisMigration = true
			if !strings.Contains(m.GetUpSql(), "# wc:") {
				t.Errorf("Redis migration body unexpected: %q", m.GetUpSql())
			}
		}
	}
	if !redisMigration {
		t.Error("Redis bucket should produce a migration despite PG bucket having unresolved findings")
	}
}
