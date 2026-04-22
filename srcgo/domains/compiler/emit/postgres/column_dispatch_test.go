package postgres

import (
	"strings"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestColumnTypePerCarrierDispatch exhaustively exercises the non-db_type
// path of columnType — each (carrier, sem) combination that has a
// dedicated branch plus the "no mapping" defensive fallback per
// carrier. Fixtures reach most of these through the full pipeline;
// this unit test pins the contract independently so a regression
// in a single branch fails fast without needing a golden refresh.
func TestColumnTypePerCarrierDispatch(t *testing.T) {
	intPtr := func(i int32) *int32 { return &i }
	cases := []struct {
		name     string
		carrier  irpb.Carrier
		sem      irpb.SemType
		maxLen   int32
		prec     int32
		scale    *int32
		elemCar  irpb.Carrier
		elemMsg  bool
		pg       *irpb.PgOptions
		table    string
		want     string
		wantErr  string
	}{
		// Bool — single branch.
		{name: "BOOL", carrier: irpb.Carrier_CARRIER_BOOL, want: "BOOLEAN"},

		// Bytes — BYTEA default + SEM_JSON → JSONB override.
		{name: "bytes default", carrier: irpb.Carrier_CARRIER_BYTES, want: "BYTEA"},
		{name: "bytes + SEM_JSON", carrier: irpb.Carrier_CARRIER_BYTES, sem: irpb.SemType_SEM_JSON, want: "JSONB"},

		// String — every sem branch.
		{name: "string + TEXT", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_TEXT, want: "TEXT"},
		{name: "string + CHAR", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_CHAR, maxLen: 64, want: "VARCHAR(64)"},
		{name: "string + SLUG", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_SLUG, maxLen: 80, want: "VARCHAR(80)"},
		{name: "string + EMAIL", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_EMAIL, maxLen: 320, want: "VARCHAR(320)"},
		{name: "string + URL", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_URL, maxLen: 2048, want: "VARCHAR(2048)"},
		{name: "string + POSIX_PATH", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_POSIX_PATH, want: "TEXT"},
		{name: "string + FILE_PATH", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_FILE_PATH, want: "TEXT"},
		{name: "string + IMAGE_PATH", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_IMAGE_PATH, want: "TEXT"},
		{name: "string + UUID", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_UUID, want: "UUID"},
		{name: "string + JSON", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_JSON, want: "JSONB"},
		{name: "string + IP", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_IP, want: "INET"},
		{name: "string + MAC", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_MAC, want: "MACADDR"},
		{name: "string + TSEARCH", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_TSEARCH, want: "TSVECTOR"},
		{name: "string + ENUM", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_ENUM, table: "orders", want: "orders_col"},
		{name: "string + DECIMAL precision", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_DECIMAL, prec: 10, want: "NUMERIC(10)"},
		{name: "string + DECIMAL precision + scale", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_DECIMAL, prec: 12, scale: intPtr(4), want: "NUMERIC(12, 4)"},

		// String error branches.
		{name: "string + CHAR without max_len errors", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_CHAR, wantErr: "requires max_len"},
		{name: "string + ENUM without table errors", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_ENUM, wantErr: "requires table context"},
		{name: "string + DECIMAL without precision errors", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_DECIMAL, wantErr: "DECIMAL requires precision"},
		{name: "string + unmapped sem errors", carrier: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_MONEY, wantErr: "no PG type mapping"},

		// Int32 — NUMBER/ID/ENUM + SMALL_INTEGER + COUNTER defensive.
		{name: "int32 + NUMBER", carrier: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_NUMBER, want: "INTEGER"},
		{name: "int32 + ID", carrier: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_ID, want: "INTEGER"},
		{name: "int32 + ENUM", carrier: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_ENUM, want: "INTEGER"},
		{name: "int32 + SMALL_INTEGER", carrier: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_SMALL_INTEGER, want: "SMALLINT"},
		{name: "int32 + COUNTER rejected", carrier: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_COUNTER, wantErr: "COUNTER requires int64"},
		{name: "int32 + unmapped sem errors", carrier: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_TEXT, wantErr: "no PG type mapping"},

		// Int64 — NUMBER/ID/COUNTER/ENUM + unmapped defensive.
		{name: "int64 + NUMBER", carrier: irpb.Carrier_CARRIER_INT64, sem: irpb.SemType_SEM_NUMBER, want: "BIGINT"},
		{name: "int64 + ID", carrier: irpb.Carrier_CARRIER_INT64, sem: irpb.SemType_SEM_ID, want: "BIGINT"},
		{name: "int64 + COUNTER", carrier: irpb.Carrier_CARRIER_INT64, sem: irpb.SemType_SEM_COUNTER, want: "BIGINT"},
		{name: "int64 + ENUM", carrier: irpb.Carrier_CARRIER_INT64, sem: irpb.SemType_SEM_ENUM, want: "BIGINT"},
		{name: "int64 + unmapped sem errors", carrier: irpb.Carrier_CARRIER_INT64, sem: irpb.SemType_SEM_TEXT, wantErr: "no PG type mapping"},

		// Double — NUMBER/MONEY/PERCENTAGE/RATIO + unmapped defensive.
		{name: "double + NUMBER", carrier: irpb.Carrier_CARRIER_DOUBLE, sem: irpb.SemType_SEM_NUMBER, want: "DOUBLE PRECISION"},
		{name: "double + MONEY", carrier: irpb.Carrier_CARRIER_DOUBLE, sem: irpb.SemType_SEM_MONEY, want: "NUMERIC(19, 4)"},
		{name: "double + PERCENTAGE", carrier: irpb.Carrier_CARRIER_DOUBLE, sem: irpb.SemType_SEM_PERCENTAGE, want: "NUMERIC(5, 2)"},
		{name: "double + RATIO", carrier: irpb.Carrier_CARRIER_DOUBLE, sem: irpb.SemType_SEM_RATIO, want: "NUMERIC(5, 4)"},
		{name: "double + unmapped sem errors", carrier: irpb.Carrier_CARRIER_DOUBLE, sem: irpb.SemType_SEM_TEXT, wantErr: "no PG type mapping"},

		// Timestamp — DATE/TIME/DATETIME + unmapped defensive.
		{name: "timestamp + DATE", carrier: irpb.Carrier_CARRIER_TIMESTAMP, sem: irpb.SemType_SEM_DATE, want: "DATE"},
		{name: "timestamp + TIME", carrier: irpb.Carrier_CARRIER_TIMESTAMP, sem: irpb.SemType_SEM_TIME, want: "TIME"},
		{name: "timestamp + DATETIME", carrier: irpb.Carrier_CARRIER_TIMESTAMP, sem: irpb.SemType_SEM_DATETIME, want: "TIMESTAMPTZ"},
		{name: "timestamp + unmapped sem errors", carrier: irpb.Carrier_CARRIER_TIMESTAMP, sem: irpb.SemType_SEM_TEXT, wantErr: "no PG type mapping"},

		// Duration — single branch.
		{name: "duration", carrier: irpb.Carrier_CARRIER_DURATION, want: "INTERVAL"},

		// Map — HSTORE dispatch and JSONB fallback.
		{name: "map string->string (HSTORE)", carrier: irpb.Carrier_CARRIER_MAP, elemCar: irpb.Carrier_CARRIER_STRING, want: "HSTORE"},
		{name: "map string->bytes (JSONB)", carrier: irpb.Carrier_CARRIER_MAP, elemCar: irpb.Carrier_CARRIER_BYTES, want: "JSONB"},
		{name: "map string->Message (JSONB)", carrier: irpb.Carrier_CARRIER_MAP, elemCar: irpb.Carrier_CARRIER_STRING, elemMsg: true, want: "JSONB"},

		// List — message fallback + scalar via pgArrayOf.
		{name: "list<Message> (JSONB)", carrier: irpb.Carrier_CARRIER_LIST, elemMsg: true, want: "JSONB"},
		{name: "list<string> (TEXT[])", carrier: irpb.Carrier_CARRIER_LIST, elemCar: irpb.Carrier_CARRIER_STRING, sem: irpb.SemType_SEM_AUTO, want: "TEXT[]"},
		{name: "list<int32> (INTEGER[])", carrier: irpb.Carrier_CARRIER_LIST, elemCar: irpb.Carrier_CARRIER_INT32, sem: irpb.SemType_SEM_AUTO, want: "INTEGER[]"},

		// pg.custom_type — takes precedence over every dispatch path.
		{name: "pg custom_type override", carrier: irpb.Carrier_CARRIER_STRING, pg: &irpb.PgOptions{CustomType: "vector(512)"}, want: "vector(512)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col := &irpb.Column{
				Name:            "col",
				ProtoName:       "col",
				Carrier:         c.carrier,
				Type:            c.sem,
				MaxLen:          c.maxLen,
				Precision:       c.prec,
				Scale:           c.scale,
				ElementCarrier:  c.elemCar,
				ElementIsMessage: c.elemMsg,
				Pg:              c.pg,
			}
			got, err := columnType(c.table, col)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got SQL %q", c.wantErr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("err %q missing substring %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Errorf("columnType → %q, want %q", got, c.want)
			}
		})
	}
}

// TestElementDefaultSemFor pins the per-carrier default sem table that
// pgArrayOf consults when a list's element sem is SEM_AUTO. Covers all
// carriers including the UNSPECIFIED fallback for carriers that can't
// appear as list elements (MAP, LIST, BOOL, BYTES).
func TestElementDefaultSemFor(t *testing.T) {
	cases := map[irpb.Carrier]irpb.SemType{
		irpb.Carrier_CARRIER_STRING:    irpb.SemType_SEM_TEXT,
		irpb.Carrier_CARRIER_INT32:     irpb.SemType_SEM_NUMBER,
		irpb.Carrier_CARRIER_INT64:     irpb.SemType_SEM_NUMBER,
		irpb.Carrier_CARRIER_DOUBLE:    irpb.SemType_SEM_NUMBER,
		irpb.Carrier_CARRIER_TIMESTAMP: irpb.SemType_SEM_DATETIME,
		irpb.Carrier_CARRIER_DURATION:  irpb.SemType_SEM_INTERVAL,
		// Carriers with no list-element convention fall through to
		// UNSPECIFIED (caller then hits the carrier dispatch's
		// defensive branch).
		irpb.Carrier_CARRIER_BOOL:  irpb.SemType_SEM_UNSPECIFIED,
		irpb.Carrier_CARRIER_BYTES: irpb.SemType_SEM_UNSPECIFIED,
	}
	for carrier, want := range cases {
		if got := elementDefaultSemFor(carrier); got != want {
			t.Errorf("elementDefaultSemFor(%s) = %s, want %s",
				displayCarrier(carrier), displaySemType(got), displaySemType(want))
		}
	}
}

// TestPgArrayOfElementError — pgArrayOf propagates columnType errors
// from the synthetic element through its own wrap. Construct a LIST
// column whose element would fail columnType (DECIMAL with no
// precision) and assert the wrapped error.
func TestPgArrayOfElementError(t *testing.T) {
	col := &irpb.Column{
		Name:           "amounts",
		ProtoName:      "amounts",
		Carrier:        irpb.Carrier_CARRIER_LIST,
		ElementCarrier: irpb.Carrier_CARRIER_STRING,
		Type:           irpb.SemType_SEM_DECIMAL,
		// No Precision — element columnType errors out.
	}
	_, err := pgArrayOf(col)
	if err == nil {
		t.Fatal("expected err when element has no precision, got nil")
	}
	if !strings.Contains(err.Error(), "pgArrayOf: element:") {
		t.Errorf("err %q missing pgArrayOf wrap", err.Error())
	}
}

// TestPgEnumTypeName pins the <table>_<col> convention for string-backed
// SEM_ENUM types. IR validates the resulting identifier against
// NAMEDATALEN at build time; the emitter trusts that validation.
func TestPgEnumTypeName(t *testing.T) {
	if got := pgEnumTypeName("orders", "status"); got != "orders_status" {
		t.Errorf("pgEnumTypeName = %q, want %q", got, "orders_status")
	}
	// Works with prefix-baked table names (D19 PREFIX mode).
	if got := pgEnumTypeName("billing_invoices", "state"); got != "billing_invoices_state" {
		t.Errorf("pgEnumTypeName = %q, want %q", got, "billing_invoices_state")
	}
}

// TestDefaultExprVariants covers each oneof variant of Default including
// the "unknown variant" defensive branch. Fixture coverage touches the
// happy paths; this pins every branch including the error defaults.
func TestDefaultExprVariants(t *testing.T) {
	col := &irpb.Column{Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_TEXT}

	cases := []struct {
		name string
		def  *irpb.Default
		want string
	}{
		{name: "literal string", def: &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "foo"}}, want: "'foo'"},
		{name: "literal string with apostrophe", def: &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "Jane's"}}, want: "'Jane''s'"},
		{name: "literal int", def: &irpb.Default{Variant: &irpb.Default_LiteralInt{LiteralInt: 42}}, want: "42"},
		{name: "literal int negative", def: &irpb.Default{Variant: &irpb.Default_LiteralInt{LiteralInt: -7}}, want: "-7"},
		{name: "literal double", def: &irpb.Default{Variant: &irpb.Default_LiteralDouble{LiteralDouble: 0.5}}, want: "0.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := defaultExpr(col, c.def)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("defaultExpr → %q, want %q", got, c.want)
			}
		})
	}

	// Unknown variant defensive — proto allows future oneof growth;
	// the emitter must surface a clear error instead of silently
	// returning empty SQL.
	t.Run("unknown variant", func(t *testing.T) {
		_, err := defaultExpr(col, &irpb.Default{})
		if err == nil {
			t.Fatal("want error for unset variant, got nil")
		}
	})
}

// TestAutoExprVariants covers every AutoKind branch including NOW's
// per-sem-type dispatch and the defensive "AUTO_IDENTITY as DEFAULT"
// + "unknown AutoKind" rejects.
func TestAutoExprVariants(t *testing.T) {
	cases := []struct {
		name    string
		sem     irpb.SemType
		kind    irpb.AutoKind
		want    string
		wantErr string
	}{
		{name: "NOW + DATETIME", sem: irpb.SemType_SEM_DATETIME, kind: irpb.AutoKind_AUTO_NOW, want: "NOW()"},
		{name: "NOW + DATE", sem: irpb.SemType_SEM_DATE, kind: irpb.AutoKind_AUTO_NOW, want: "CURRENT_DATE"},
		{name: "NOW + TIME", sem: irpb.SemType_SEM_TIME, kind: irpb.AutoKind_AUTO_NOW, want: "CURRENT_TIME"},
		{name: "NOW on unsupported sem errors", sem: irpb.SemType_SEM_TEXT, kind: irpb.AutoKind_AUTO_NOW, wantErr: "AUTO_NOW on unsupported"},
		{name: "UUID_V4", kind: irpb.AutoKind_AUTO_UUID_V4, want: "gen_random_uuid()"},
		{name: "UUID_V7", kind: irpb.AutoKind_AUTO_UUID_V7, want: "uuidv7()"},
		{name: "EMPTY_JSON_ARRAY", kind: irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY, want: "'[]'"},
		{name: "EMPTY_JSON_OBJECT", kind: irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT, want: "'{}'"},
		{name: "TRUE", kind: irpb.AutoKind_AUTO_TRUE, want: "TRUE"},
		{name: "FALSE", kind: irpb.AutoKind_AUTO_FALSE, want: "FALSE"},
		{name: "IDENTITY rejected", kind: irpb.AutoKind_AUTO_IDENTITY, wantErr: "IDENTITY is a column modifier"},
		{name: "unknown AutoKind", kind: irpb.AutoKind(9999), wantErr: "unknown AutoKind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			col := &irpb.Column{Type: c.sem}
			got, err := autoExpr(col, c.kind)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got SQL %q", c.wantErr, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("err %q missing substring %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Errorf("autoExpr → %q, want %q", got, c.want)
			}
		})
	}
}

// TestFkActionSQL pins every FKAction → SQL keyword mapping plus the
// defensive empty-string default (unreachable under valid IR because
// resolveFKAction populates one of the four known variants).
func TestFkActionSQL(t *testing.T) {
	cases := map[irpb.FKAction]string{
		irpb.FKAction_FK_ACTION_CASCADE:     "CASCADE",
		irpb.FKAction_FK_ACTION_SET_NULL:    "SET NULL",
		irpb.FKAction_FK_ACTION_RESTRICT:    "RESTRICT",
		irpb.FKAction_FK_ACTION_SET_DEFAULT: "SET DEFAULT",
		irpb.FKAction_FK_ACTION_UNSPECIFIED: "", // defensive
	}
	for action, want := range cases {
		if got := fkActionSQL(action); got != want {
			t.Errorf("fkActionSQL(%v) = %q, want %q", action, got, want)
		}
	}
}

// TestIsIdentity covers the three states: no default, default with
// AUTO_IDENTITY, default with another Auto variant.
func TestIsIdentity(t *testing.T) {
	if isIdentity(&irpb.Column{}) {
		t.Error("isIdentity on bare column should be false")
	}
	idCol := &irpb.Column{
		Default: &irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_IDENTITY}},
	}
	if !isIdentity(idCol) {
		t.Error("isIdentity on AUTO_IDENTITY default should be true")
	}
	nonIdCol := &irpb.Column{
		Default: &irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_NOW}},
	}
	if isIdentity(nonIdCol) {
		t.Error("isIdentity on AUTO_NOW default should be false")
	}
	litCol := &irpb.Column{
		Default: &irpb.Default{Variant: &irpb.Default_LiteralInt{LiteralInt: 0}},
	}
	if isIdentity(litCol) {
		t.Error("isIdentity on literal_int default should be false")
	}
}
