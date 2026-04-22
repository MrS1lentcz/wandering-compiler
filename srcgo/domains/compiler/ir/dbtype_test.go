package ir

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	dbpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/db"
)

// TestDbTypeCompatibleWithCarrier covers every (DbType, Carrier)
// cell of the compatibility matrix — including the defensive
// "return false" fall-through for unknown DbType values. Each
// DbType defines a class of compatible carriers; the matrix is
// the gatekeeper that rejects `db_type: INTEGER` on a string
// field, `db_type: JSONB` on a double field, etc.
func TestDbTypeCompatibleWithCarrier(t *testing.T) {
	cases := []struct {
		dbType  irpb.DbType
		carrier irpb.Carrier
		want    bool
	}{
		// Unspecified always compatible (fall-through).
		{irpb.DbType_DB_TYPE_UNSPECIFIED, irpb.Carrier_CARRIER_STRING, true},

		// Text-shaped — string only.
		{irpb.DbType_DBT_TEXT, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_TEXT, irpb.Carrier_CARRIER_INT64, false},
		{irpb.DbType_DBT_VARCHAR, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_VARCHAR, irpb.Carrier_CARRIER_DOUBLE, false},
		{irpb.DbType_DBT_CITEXT, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_INET, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_CIDR, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_MACADDR, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_TSVECTOR, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_UUID, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_UUID, irpb.Carrier_CARRIER_BYTES, false},

		// JSON family — string OR bytes (opaque payload).
		{irpb.DbType_DBT_JSON, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_JSON, irpb.Carrier_CARRIER_BYTES, true},
		{irpb.DbType_DBT_JSON, irpb.Carrier_CARRIER_INT64, false},
		{irpb.DbType_DBT_JSONB, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_JSONB, irpb.Carrier_CARRIER_BYTES, true},
		{irpb.DbType_DBT_JSONB, irpb.Carrier_CARRIER_DOUBLE, false},
		{irpb.DbType_DBT_HSTORE, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_HSTORE, irpb.Carrier_CARRIER_BYTES, true},

		// Integer-shaped — int32 OR int64.
		{irpb.DbType_DBT_SMALLINT, irpb.Carrier_CARRIER_INT32, true},
		{irpb.DbType_DBT_SMALLINT, irpb.Carrier_CARRIER_INT64, true},
		{irpb.DbType_DBT_SMALLINT, irpb.Carrier_CARRIER_STRING, false},
		{irpb.DbType_DBT_INTEGER, irpb.Carrier_CARRIER_INT32, true},
		{irpb.DbType_DBT_INTEGER, irpb.Carrier_CARRIER_INT64, true},
		{irpb.DbType_DBT_BIGINT, irpb.Carrier_CARRIER_INT32, true},
		{irpb.DbType_DBT_BIGINT, irpb.Carrier_CARRIER_INT64, true},
		{irpb.DbType_DBT_BIGINT, irpb.Carrier_CARRIER_DOUBLE, false},

		// Floating-shaped — double only.
		{irpb.DbType_DBT_REAL, irpb.Carrier_CARRIER_DOUBLE, true},
		{irpb.DbType_DBT_REAL, irpb.Carrier_CARRIER_INT32, false},
		{irpb.DbType_DBT_DOUBLE_PRECISION, irpb.Carrier_CARRIER_DOUBLE, true},

		// NUMERIC — double OR string (DECIMAL on string carrier).
		{irpb.DbType_DBT_NUMERIC, irpb.Carrier_CARRIER_DOUBLE, true},
		{irpb.DbType_DBT_NUMERIC, irpb.Carrier_CARRIER_STRING, true},
		{irpb.DbType_DBT_NUMERIC, irpb.Carrier_CARRIER_INT32, false},

		// Temporal — Timestamp only.
		{irpb.DbType_DBT_DATE, irpb.Carrier_CARRIER_TIMESTAMP, true},
		{irpb.DbType_DBT_DATE, irpb.Carrier_CARRIER_STRING, false},
		{irpb.DbType_DBT_TIME, irpb.Carrier_CARRIER_TIMESTAMP, true},
		{irpb.DbType_DBT_TIMESTAMP, irpb.Carrier_CARRIER_TIMESTAMP, true},
		{irpb.DbType_DBT_TIMESTAMPTZ, irpb.Carrier_CARRIER_TIMESTAMP, true},
		{irpb.DbType_DBT_INTERVAL, irpb.Carrier_CARRIER_DURATION, true},
		{irpb.DbType_DBT_INTERVAL, irpb.Carrier_CARRIER_TIMESTAMP, false},

		// Binary — bytes only.
		{irpb.DbType_DBT_BYTEA, irpb.Carrier_CARRIER_BYTES, true},
		{irpb.DbType_DBT_BYTEA, irpb.Carrier_CARRIER_STRING, false},
		{irpb.DbType_DBT_BLOB, irpb.Carrier_CARRIER_BYTES, true},

		// Boolean — bool only.
		{irpb.DbType_DBT_BOOLEAN, irpb.Carrier_CARRIER_BOOL, true},
		{irpb.DbType_DBT_BOOLEAN, irpb.Carrier_CARRIER_INT32, false},

		// Unknown DbType → defensive false (future enum growth must
		// extend the switch before the IR accepts it).
		{irpb.DbType(9999), irpb.Carrier_CARRIER_STRING, false},
	}
	for _, c := range cases {
		got := dbTypeCompatibleWithCarrier(c.dbType, c.carrier)
		if got != c.want {
			t.Errorf("dbTypeCompatibleWithCarrier(%v, %v) = %v, want %v",
				c.dbType, c.carrier, got, c.want)
		}
	}
}

// TestDbTypeToIR — exhaustive authoring → IR enum mapping. One
// slip here (missing case, wrong constant) silently degrades
// db_type overrides into UNSPECIFIED, which then renders as the
// sem-type default at emit — hard to spot in output. Pin every
// value.
func TestDbTypeToIR(t *testing.T) {
	cases := []struct {
		in   dbpb.DbType
		want irpb.DbType
	}{
		{dbpb.DbType_DB_TYPE_UNSPECIFIED, irpb.DbType_DB_TYPE_UNSPECIFIED},
		{dbpb.DbType_TEXT, irpb.DbType_DBT_TEXT},
		{dbpb.DbType_VARCHAR, irpb.DbType_DBT_VARCHAR},
		{dbpb.DbType_CITEXT, irpb.DbType_DBT_CITEXT},
		{dbpb.DbType_JSON, irpb.DbType_DBT_JSON},
		{dbpb.DbType_JSONB, irpb.DbType_DBT_JSONB},
		{dbpb.DbType_HSTORE, irpb.DbType_DBT_HSTORE},
		{dbpb.DbType_INET, irpb.DbType_DBT_INET},
		{dbpb.DbType_CIDR, irpb.DbType_DBT_CIDR},
		{dbpb.DbType_MACADDR, irpb.DbType_DBT_MACADDR},
		{dbpb.DbType_TSVECTOR, irpb.DbType_DBT_TSVECTOR},
		{dbpb.DbType_UUID, irpb.DbType_DBT_UUID},
		{dbpb.DbType_SMALLINT, irpb.DbType_DBT_SMALLINT},
		{dbpb.DbType_INTEGER, irpb.DbType_DBT_INTEGER},
		{dbpb.DbType_BIGINT, irpb.DbType_DBT_BIGINT},
		{dbpb.DbType_REAL, irpb.DbType_DBT_REAL},
		{dbpb.DbType_DOUBLE_PRECISION, irpb.DbType_DBT_DOUBLE_PRECISION},
		{dbpb.DbType_NUMERIC, irpb.DbType_DBT_NUMERIC},
		{dbpb.DbType_DATE, irpb.DbType_DBT_DATE},
		{dbpb.DbType_TIME, irpb.DbType_DBT_TIME},
		{dbpb.DbType_TIMESTAMP, irpb.DbType_DBT_TIMESTAMP},
		{dbpb.DbType_TIMESTAMPTZ, irpb.DbType_DBT_TIMESTAMPTZ},
		{dbpb.DbType_INTERVAL, irpb.DbType_DBT_INTERVAL},
		{dbpb.DbType_BYTEA, irpb.DbType_DBT_BYTEA},
		{dbpb.DbType_BLOB, irpb.DbType_DBT_BLOB},
		{dbpb.DbType_BOOLEAN, irpb.DbType_DBT_BOOLEAN},
	}
	for _, c := range cases {
		if got := dbTypeToIR(c.in); got != c.want {
			t.Errorf("dbTypeToIR(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// Unknown value → UNSPECIFIED (safe default; downstream rejects).
	if got := dbTypeToIR(dbpb.DbType(9999)); got != irpb.DbType_DB_TYPE_UNSPECIFIED {
		t.Errorf("dbTypeToIR(unknown) = %v, want UNSPECIFIED", got)
	}
}
