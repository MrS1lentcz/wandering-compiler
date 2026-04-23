package postgres

import (
	"strings"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestDbTypeKeywordTable exhaustively covers the DbType → PG keyword
// map. Package-internal so the helper stays unexported; table-driven
// so adding a new DbType without a matching case here surfaces
// immediately via an empty keyword.
func TestDbTypeKeywordTable(t *testing.T) {
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
		{irpb.DbType_DBT_BLOB, "BYTEA"}, // PG emitter maps BLOB → BYTEA
		{irpb.DbType_DBT_BOOLEAN, "BOOLEAN"},
		{irpb.DbType_DB_TYPE_UNSPECIFIED, ""},
	}
	for _, c := range cases {
		if got := dbTypeKeyword(c.in); got != c.want {
			t.Errorf("dbTypeKeyword(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestQualToTableSchemaStripping covers both code paths (with and
// without a schema qualifier).
func TestQualToTableSchemaStripping(t *testing.T) {
	cases := map[string]string{
		"users":            "users",
		"auth.users":       "users",
		"reporting.events": "events",
	}
	for in, want := range cases {
		if got := qualToTable(in); got != want {
			t.Errorf("qualToTable(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCommentLiteralEmpty ensures empty comment literal returns NULL
// sentinel for PG.
func TestCommentLiteralEmpty(t *testing.T) {
	if got := commentLiteral(""); got != "NULL" {
		t.Errorf("commentLiteral(\"\") = %q, want NULL", got)
	}
	if got := commentLiteral("hello"); got != "'hello'" {
		t.Errorf("commentLiteral(\"hello\") = %q", got)
	}
}

// TestDbTypeOrEffectiveUnspecified — falls back to columnType on
// the snapshot when the enum is UNSPECIFIED.
func TestDbTypeOrEffectiveUnspecified(t *testing.T) {
	col := &irpb.Column{
		Name: "label", ProtoName: "label",
		Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_TEXT,
	}
	got, err := dbTypeOrEffective(irpb.DbType_DB_TYPE_UNSPECIFIED, col, "configs")
	if err != nil {
		t.Fatalf("dbTypeOrEffective: %v", err)
	}
	if got != "TEXT" {
		t.Errorf("got %q, want TEXT", got)
	}
}

// TestDbTypeOrEffectiveNilSnapshotErrors — defensive branch.
func TestDbTypeOrEffectiveNilSnapshotErrors(t *testing.T) {
	if _, err := dbTypeOrEffective(irpb.DbType_DB_TYPE_UNSPECIFIED, nil, "t"); err == nil {
		t.Fatal("nil column with UNSPECIFIED DbType accepted; want error")
	}
}

// TestModeUsesSchema covers the tiny dispatch helper.
func TestModeUsesSchema(t *testing.T) {
	if !modeUsesSchema(irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA) {
		t.Error("SCHEMA should use schema")
	}
	if modeUsesSchema(irpb.NamespaceMode_NAMESPACE_MODE_PREFIX) {
		t.Error("PREFIX should not use schema")
	}
	if modeUsesSchema(irpb.NamespaceMode_NAMESPACE_MODE_NONE) {
		t.Error("NONE should not use schema")
	}
}

// TestQualifyName covers SCHEMA-with-ns, SCHEMA-empty-ns, and
// non-SCHEMA modes.
func TestQualifyName(t *testing.T) {
	if got := qualifyName(irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA, "auth", "users"); got != "auth.users" {
		t.Errorf("SCHEMA+auth+users = %q", got)
	}
	if got := qualifyName(irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA, "", "users"); got != "users" {
		t.Errorf("SCHEMA+empty+users = %q", got)
	}
	if got := qualifyName(irpb.NamespaceMode_NAMESPACE_MODE_NONE, "", "users"); got != "users" {
		t.Errorf("NONE+empty+users = %q", got)
	}
}

// TestCheckConstraintNameEmpty — checkConstraintName returns "" when
// the Check has no body. Defensive branch.
func TestCheckConstraintNameEmpty(t *testing.T) {
	col := &irpb.Column{Name: "x", ProtoName: "x", Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_TEXT}
	ck := &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{}}}
	if got := checkConstraintName("t", col, ck); got != "" {
		t.Errorf("empty-bound RangeCheck should give empty name, got %q", got)
	}
}

// TestUniqueConstraintName sanity-checks the derived naming
// convention (iter-1 mirror).
func TestUniqueConstraintName(t *testing.T) {
	if got := uniqueConstraintName("users", "email"); got != "users_email_uidx" {
		t.Errorf("got %q", got)
	}
}

// TestFkActionSQLCompleteness — covers every FKAction variant.
func TestFkActionSQLCompleteness(t *testing.T) {
	cases := map[irpb.FKAction]string{
		irpb.FKAction_FK_ACTION_CASCADE:     "CASCADE",
		irpb.FKAction_FK_ACTION_SET_NULL:    "SET NULL",
		irpb.FKAction_FK_ACTION_RESTRICT:    "RESTRICT",
		irpb.FKAction_FK_ACTION_SET_DEFAULT: "SET DEFAULT",
		irpb.FKAction_FK_ACTION_UNSPECIFIED: "",
	}
	for in, want := range cases {
		if got := fkActionSQL(in); got != want {
			t.Errorf("fkActionSQL(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveSqlColNameMissing — defensive branch returns error.
func TestResolveSqlColNameMissing(t *testing.T) {
	_, err := resolveSqlColName([]*irpb.Column{}, "ghost")
	if err == nil {
		t.Fatal("missing column accepted; want error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error doesn't name missing column: %v", err)
	}
}

// TestRenderRawIndexShape — CREATE INDEX / CREATE UNIQUE INDEX
// both branches of the keyword dispatch.
func TestRenderRawIndexShape(t *testing.T) {
	tbl := &irpb.Table{Name: "posts"}
	nonUnique := renderRawIndex(tbl, &irpb.RawIndex{Name: "idx1", Body: "(title)"})
	if nonUnique != "CREATE INDEX idx1 ON posts (title);" {
		t.Errorf("non-unique = %q", nonUnique)
	}
	unique := renderRawIndex(tbl, &irpb.RawIndex{Name: "idx2", Body: "(email)", Unique: true})
	if unique != "CREATE UNIQUE INDEX idx2 ON posts (email);" {
		t.Errorf("unique = %q", unique)
	}
}

// TestRenderWcMigrationsCreate covers the exported public renderer
// (used directly by the CLI wrapper path). Asserts the schema lines
// match the D27 contract.
func TestRenderWcMigrationsCreate(t *testing.T) {
	got := RenderWcMigrationsCreate()
	for _, expect := range []string{
		"CREATE TABLE wc_migrations",
		"timestamp      TIMESTAMPTZ PRIMARY KEY",
		"applied_at     TIMESTAMPTZ NOT NULL DEFAULT now()",
		"content_sha256 BYTEA NOT NULL",
	} {
		if !strings.Contains(got, expect) {
			t.Errorf("missing %q in:\n%s", expect, got)
		}
	}
}

// TestRenderWcMigrationsDrop — DROP TABLE IF EXISTS sentinel.
func TestRenderWcMigrationsDrop(t *testing.T) {
	if got := RenderWcMigrationsDrop(); got != "DROP TABLE IF EXISTS wc_migrations;" {
		t.Errorf("got %q", got)
	}
}

// TestRenderWcMigrationsInsertHexHash — hash renders as PG `\x<hex>`
// bytea literal.
func TestRenderWcMigrationsInsertHexHash(t *testing.T) {
	hash := []byte{0xde, 0xad, 0xbe, 0xef}
	got := RenderWcMigrationsInsert("20260423T120000Z", hash)
	if !strings.Contains(got, `'\xdeadbeef'`) {
		t.Errorf("hash hex not rendered: %q", got)
	}
	if !strings.Contains(got, "INSERT INTO wc_migrations") {
		t.Errorf("missing INSERT: %q", got)
	}
}

// TestRenderWcMigrationsDelete — symmetric DELETE by timestamp PK.
func TestRenderWcMigrationsDelete(t *testing.T) {
	got := RenderWcMigrationsDelete("20260423T120000Z")
	if got != "DELETE FROM wc_migrations WHERE timestamp = '20260423T120000Z';" {
		t.Errorf("got %q", got)
	}
}

// TestEmitWcMigrationsCreateDispatch — covers the Op_WcMigrationsCreate
// EmitOp branch (otherwise 0% because the CLI path calls
// RenderWcMigrationsCreate directly; the Op variant only comes into
// play when a pre-built Plan carries the Op itself).
func TestEmitWcMigrationsCreateDispatch(t *testing.T) {
	e := Emitter{}
	up, down, err := e.emitWcMigrationsCreate(nil)
	if err != nil {
		t.Fatalf("emitWcMigrationsCreate: %v", err)
	}
	if !strings.Contains(up, "CREATE TABLE wc_migrations") {
		t.Errorf("up missing CREATE: %q", up)
	}
	if down != "DROP TABLE IF EXISTS wc_migrations;" {
		t.Errorf("down = %q", down)
	}
}
