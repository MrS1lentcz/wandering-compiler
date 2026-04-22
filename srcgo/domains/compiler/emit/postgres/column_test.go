package postgres

import (
	"strings"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestPgColumnFromDbType — every DbType enum value lands on the
// correct SQL column type via columnType(). Coverage before this
// test was 21.9% because only a handful of db_type overrides
// appeared in fixture SQL; this sweep ensures D14's enumerated
// storage override is exhaustively reachable.
func TestPgColumnFromDbType(t *testing.T) {
	intPtr := func(i int32) *int32 { return &i }
	cases := []struct {
		name     string
		dbType   irpb.DbType
		maxLen   int32
		prec     int32
		scale    *int32
		carrier  irpb.Carrier
		wantSQL  string
		wantErr  string
	}{
		{name: "TEXT", dbType: irpb.DbType_DBT_TEXT, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "TEXT"},
		{name: "VARCHAR with max_len", dbType: irpb.DbType_DBT_VARCHAR, maxLen: 120, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "VARCHAR(120)"},
		{name: "VARCHAR without max_len errors", dbType: irpb.DbType_DBT_VARCHAR, carrier: irpb.Carrier_CARRIER_STRING, wantErr: "VARCHAR requires max_len"},
		{name: "CITEXT", dbType: irpb.DbType_DBT_CITEXT, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "CITEXT"},
		{name: "JSON (text-stored)", dbType: irpb.DbType_DBT_JSON, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "JSON"},
		{name: "JSONB (binary)", dbType: irpb.DbType_DBT_JSONB, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "JSONB"},
		{name: "HSTORE", dbType: irpb.DbType_DBT_HSTORE, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "HSTORE"},
		{name: "INET", dbType: irpb.DbType_DBT_INET, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "INET"},
		{name: "CIDR", dbType: irpb.DbType_DBT_CIDR, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "CIDR"},
		{name: "MACADDR", dbType: irpb.DbType_DBT_MACADDR, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "MACADDR"},
		{name: "TSVECTOR", dbType: irpb.DbType_DBT_TSVECTOR, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "TSVECTOR"},
		{name: "UUID", dbType: irpb.DbType_DBT_UUID, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "UUID"},
		{name: "SMALLINT", dbType: irpb.DbType_DBT_SMALLINT, carrier: irpb.Carrier_CARRIER_INT32, wantSQL: "SMALLINT"},
		{name: "INTEGER", dbType: irpb.DbType_DBT_INTEGER, carrier: irpb.Carrier_CARRIER_INT32, wantSQL: "INTEGER"},
		{name: "BIGINT", dbType: irpb.DbType_DBT_BIGINT, carrier: irpb.Carrier_CARRIER_INT64, wantSQL: "BIGINT"},
		{name: "REAL", dbType: irpb.DbType_DBT_REAL, carrier: irpb.Carrier_CARRIER_DOUBLE, wantSQL: "REAL"},
		{name: "DOUBLE_PRECISION", dbType: irpb.DbType_DBT_DOUBLE_PRECISION, carrier: irpb.Carrier_CARRIER_DOUBLE, wantSQL: "DOUBLE PRECISION"},
		{name: "NUMERIC precision only", dbType: irpb.DbType_DBT_NUMERIC, prec: 10, carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "NUMERIC(10)"},
		{name: "NUMERIC precision + scale", dbType: irpb.DbType_DBT_NUMERIC, prec: 12, scale: intPtr(4), carrier: irpb.Carrier_CARRIER_STRING, wantSQL: "NUMERIC(12, 4)"},
		{name: "NUMERIC without precision errors", dbType: irpb.DbType_DBT_NUMERIC, carrier: irpb.Carrier_CARRIER_STRING, wantErr: "NUMERIC requires precision"},
		{name: "DATE", dbType: irpb.DbType_DBT_DATE, carrier: irpb.Carrier_CARRIER_TIMESTAMP, wantSQL: "DATE"},
		{name: "TIME", dbType: irpb.DbType_DBT_TIME, carrier: irpb.Carrier_CARRIER_TIMESTAMP, wantSQL: "TIME"},
		{name: "TIMESTAMP", dbType: irpb.DbType_DBT_TIMESTAMP, carrier: irpb.Carrier_CARRIER_TIMESTAMP, wantSQL: "TIMESTAMP"},
		{name: "TIMESTAMPTZ", dbType: irpb.DbType_DBT_TIMESTAMPTZ, carrier: irpb.Carrier_CARRIER_TIMESTAMP, wantSQL: "TIMESTAMPTZ"},
		{name: "INTERVAL", dbType: irpb.DbType_DBT_INTERVAL, carrier: irpb.Carrier_CARRIER_DURATION, wantSQL: "INTERVAL"},
		{name: "BYTEA", dbType: irpb.DbType_DBT_BYTEA, carrier: irpb.Carrier_CARRIER_BYTES, wantSQL: "BYTEA"},
		{name: "BLOB folds to BYTEA on PG", dbType: irpb.DbType_DBT_BLOB, carrier: irpb.Carrier_CARRIER_BYTES, wantSQL: "BYTEA"},
		{name: "BOOLEAN", dbType: irpb.DbType_DBT_BOOLEAN, carrier: irpb.Carrier_CARRIER_BOOL, wantSQL: "BOOLEAN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col := &irpb.Column{
				Name:      "col",
				ProtoName: "col",
				Carrier:   c.carrier,
				DbType:    c.dbType,
				MaxLen:    c.maxLen,
				Precision: c.prec,
				Scale:     c.scale,
			}
			got, err := columnType("t", col)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got SQL %q", c.wantErr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error %q missing substring %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.wantSQL {
				t.Errorf("columnType → %q, want %q", got, c.wantSQL)
			}
		})
	}
}

// TestDisplayHelpers covers the package-local displayCarrier /
// displaySemType helpers. They're defensive — only fire inside
// "ir invariant violated" error messages — so normal compilation
// never reaches them. Unit tests pin the contract against future
// enum reshuffling.
func TestDisplayHelpers(t *testing.T) {
	if got := displayCarrier(irpb.Carrier_CARRIER_STRING); got != "STRING" {
		t.Errorf("displayCarrier(STRING) = %q, want STRING", got)
	}
	if got := displayCarrier(irpb.Carrier_CARRIER_INT64); got != "INT64" {
		t.Errorf("displayCarrier(INT64) = %q, want INT64", got)
	}
	if got := displaySemType(irpb.SemType_SEM_TEXT); got != "TEXT" {
		t.Errorf("displaySemType(TEXT) = %q, want TEXT", got)
	}
	if got := displaySemType(irpb.SemType_SEM_NUMBER); got != "NUMBER" {
		t.Errorf("displaySemType(NUMBER) = %q, want NUMBER", got)
	}
}

// TestPgColumnFromDbType_Unknown exercises the "unknown db_type"
// defensive branch — pgColumnFromDbType returns an error when a
// future enum value arrives before the switch is updated.
func TestPgColumnFromDbType_Unknown(t *testing.T) {
	col := &irpb.Column{
		Carrier: irpb.Carrier_CARRIER_STRING,
		// A deliberately out-of-range value — the real enum tops out at
		// DBT_BOOLEAN = 90 in the spec, but proto enums accept any
		// int32 at the wire level. This exercises the defensive default
		// branch so a missed switch case surfaces with a clear error
		// instead of silently returning empty SQL.
		DbType: irpb.DbType(9999),
	}
	_, err := pgColumnFromDbType(col)
	if err == nil {
		t.Fatal("expected error for unknown db_type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown db_type") {
		t.Errorf("error %q missing 'unknown db_type'", err.Error())
	}
}
