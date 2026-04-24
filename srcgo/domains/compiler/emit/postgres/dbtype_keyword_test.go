package postgres

// Internal test for dbTypeKeyword — pins every DbType → PG keyword
// mapping used by the ALTER TABLE … TYPE path. The function sat at
// ~21% coverage because existing goldens only touch a handful of
// db_type change cases; this table-driven test locks every case +
// the default fallthrough (returns "" for DB_TYPE_UNSPECIFIED).

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

func TestDbTypeKeyword_AllCases(t *testing.T) {
	cases := []struct {
		in   irpb.DbType
		want string
	}{
		{irpb.DbType_DBT_TEXT, "TEXT"},
		{irpb.DbType_DBT_VARCHAR, "VARCHAR"},
		{irpb.DbType_DBT_CITEXT, "CITEXT"},
		{irpb.DbType_DBT_JSON, "JSON"},
		{irpb.DbType_DBT_JSONB, "JSONB"},
		{irpb.DbType_DBT_HSTORE, "HSTORE"},
		{irpb.DbType_DBT_INET, "INET"},
		{irpb.DbType_DBT_CIDR, "CIDR"},
		{irpb.DbType_DBT_MACADDR, "MACADDR"},
		{irpb.DbType_DBT_TSVECTOR, "TSVECTOR"},
		{irpb.DbType_DBT_UUID, "UUID"},
		{irpb.DbType_DBT_SMALLINT, "SMALLINT"},
		{irpb.DbType_DBT_INTEGER, "INTEGER"},
		{irpb.DbType_DBT_BIGINT, "BIGINT"},
		{irpb.DbType_DBT_REAL, "REAL"},
		{irpb.DbType_DBT_DOUBLE_PRECISION, "DOUBLE PRECISION"},
		{irpb.DbType_DBT_NUMERIC, "NUMERIC"},
		{irpb.DbType_DBT_DATE, "DATE"},
		{irpb.DbType_DBT_TIME, "TIME"},
		{irpb.DbType_DBT_TIMESTAMP, "TIMESTAMP"},
		{irpb.DbType_DBT_TIMESTAMPTZ, "TIMESTAMPTZ"},
		{irpb.DbType_DBT_INTERVAL, "INTERVAL"},
		{irpb.DbType_DBT_BYTEA, "BYTEA"},
		{irpb.DbType_DBT_BLOB, "BYTEA"}, // BLOB folds into BYTEA
		{irpb.DbType_DBT_BOOLEAN, "BOOLEAN"},
	}
	for _, c := range cases {
		t.Run(c.in.String(), func(t *testing.T) {
			if got := dbTypeKeyword(c.in); got != c.want {
				t.Errorf("dbTypeKeyword(%s) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	// UNSPECIFIED falls through to empty — callers rely on this
	// sentinel to dispatch to columnType() for effective-type
	// derivation.
	if got := dbTypeKeyword(irpb.DbType_DB_TYPE_UNSPECIFIED); got != "" {
		t.Errorf("DB_TYPE_UNSPECIFIED should return \"\", got %q", got)
	}
}

// TestRecordDbTypeCap_AllCases mirrors dbTypeKeyword: every DbType
// that maps to a catalog cap tags its usage, every "universal"
// DbType (TEXT / VARCHAR / SMALLINT / INTEGER / BIGINT / REAL)
// records nothing. Guards the ALTER TYPE path against silently
// dropping caps from the manifest when a fact-change switches a
// column to CITEXT / HSTORE / JSONB / … without the author
// noticing.
func TestRecordDbTypeCap_AllCases(t *testing.T) {
	t.Parallel()
	type expect struct {
		in  irpb.DbType
		cap string // "" = nothing tagged
	}
	cases := []expect{
		{irpb.DbType_DBT_TEXT, ""},
		{irpb.DbType_DBT_VARCHAR, ""},
		{irpb.DbType_DBT_CITEXT, "citext"},
		{irpb.DbType_DBT_JSON, "JSON"},
		{irpb.DbType_DBT_JSONB, "JSONB"},
		{irpb.DbType_DBT_HSTORE, "hstore"},
		{irpb.DbType_DBT_INET, "INET"},
		{irpb.DbType_DBT_CIDR, "CIDR"},
		{irpb.DbType_DBT_MACADDR, "MACADDR"},
		{irpb.DbType_DBT_TSVECTOR, "TSVECTOR"},
		{irpb.DbType_DBT_UUID, "UUID"},
		{irpb.DbType_DBT_SMALLINT, ""},
		{irpb.DbType_DBT_INTEGER, ""},
		{irpb.DbType_DBT_BIGINT, ""},
		{irpb.DbType_DBT_REAL, ""},
		{irpb.DbType_DBT_DOUBLE_PRECISION, "DOUBLE_PRECISION"},
		{irpb.DbType_DBT_NUMERIC, "NUMERIC"},
		{irpb.DbType_DBT_DATE, "DATE"},
		{irpb.DbType_DBT_TIME, "TIME"},
		{irpb.DbType_DBT_TIMESTAMP, "TIMESTAMP"},
		{irpb.DbType_DBT_TIMESTAMPTZ, "TIMESTAMPTZ"},
		{irpb.DbType_DBT_INTERVAL, "INTERVAL"},
		{irpb.DbType_DBT_BYTEA, "BYTEA"},
		{irpb.DbType_DBT_BLOB, "BYTEA"},
		{irpb.DbType_DBT_BOOLEAN, "BOOLEAN"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in.String(), func(t *testing.T) {
			u := emit.NewUsage()
			recordDbTypeCap(u, c.in)
			caps := u.Sorted()
			switch {
			case c.cap == "" && len(caps) > 0:
				t.Errorf("%s should record no cap, got %v", c.in, caps)
			case c.cap != "" && (len(caps) != 1 || caps[0] != c.cap):
				t.Errorf("%s recorded %v, want [%s]", c.in, caps, c.cap)
			}
		})
	}
}
