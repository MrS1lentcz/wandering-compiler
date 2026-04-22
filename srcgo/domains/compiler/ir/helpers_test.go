package ir

import (
	"strings"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	w17pb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17"
)

// TestParseFKRef covers every branch of the parser: happy two-segment
// form + each malformed shape (zero dots, too many dots, empty segments)
// that `findLoadedField`'s caller rejects with a diagnostic.
func TestParseFKRef(t *testing.T) {
	cases := []struct {
		in   string
		want fkRef
		ok   bool
	}{
		{"customers.id", fkRef{table: "customers", column: "id"}, true},
		// Reserved-keyword table name passes the parser — validation
		// happens later in resolveFKs.
		{"order.id", fkRef{table: "order", column: "id"}, true},
		{"", fkRef{}, false},
		{"customers", fkRef{}, false},
		{"a.b.c", fkRef{}, false},
		{".id", fkRef{}, false},
		{"customers.", fkRef{}, false},
	}
	for _, c := range cases {
		got, ok := parseFKRef(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseFKRef(%q) = (%+v, %v), want (%+v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestHasSingleColUniqueIndex — positive match + rejected on non-unique,
// multi-column, and wrong-column.
func TestHasSingleColUniqueIndex(t *testing.T) {
	idx := []*irpb.Index{
		{Unique: true, Fields: []*irpb.IndexField{{Name: "email"}}},
		{Unique: false, Fields: []*irpb.IndexField{{Name: "username"}}},
		{Unique: true, Fields: []*irpb.IndexField{{Name: "a"}, {Name: "b"}}},
	}
	if !hasSingleColUniqueIndex(idx, "email") {
		t.Error("email should match single-col unique")
	}
	if hasSingleColUniqueIndex(idx, "username") {
		t.Error("username is non-unique; should not match")
	}
	if hasSingleColUniqueIndex(idx, "a") {
		t.Error("a is part of composite; should not match")
	}
	if hasSingleColUniqueIndex(idx, "ghost") {
		t.Error("ghost column should not match")
	}
	if hasSingleColUniqueIndex(nil, "anything") {
		t.Error("nil index slice should not match")
	}
}

// TestHasSingleColIndex — same table, but the uniqueness flag doesn't
// matter; any single-field index counts.
func TestHasSingleColIndex(t *testing.T) {
	idx := []*irpb.Index{
		{Fields: []*irpb.IndexField{{Name: "name"}}},
		{Fields: []*irpb.IndexField{{Name: "a"}, {Name: "b"}}},
	}
	if !hasSingleColIndex(idx, "name") {
		t.Error("name should match single-col")
	}
	if hasSingleColIndex(idx, "a") {
		t.Error("a is composite; should not match")
	}
	if hasSingleColIndex(idx, "ghost") {
		t.Error("ghost should not match")
	}
}

// TestHasColumn — Table lookup by proto name.
func TestHasColumn(t *testing.T) {
	tbl := &irpb.Table{Columns: []*irpb.Column{
		{ProtoName: "id"},
		{ProtoName: "email"},
	}}
	if !hasColumn(tbl, "id") {
		t.Error("id should match")
	}
	if !hasColumn(tbl, "email") {
		t.Error("email should match")
	}
	if hasColumn(tbl, "ghost") {
		t.Error("ghost should not match")
	}
	if hasColumn(&irpb.Table{}, "anything") {
		t.Error("empty table should not match")
	}
}

// (indexMethodToIR, nullsOrderToIR, convertIndexFields, copyStringMap
// are exhaustively covered by index_test.go.)

// TestProtoAutoToKind — every AutoDefault value + defensive fallback.
func TestProtoAutoToKind(t *testing.T) {
	cases := map[w17pb.AutoDefault]irpb.AutoKind{
		w17pb.AutoDefault_NOW:               irpb.AutoKind_AUTO_NOW,
		w17pb.AutoDefault_UUID_V4:           irpb.AutoKind_AUTO_UUID_V4,
		w17pb.AutoDefault_UUID_V7:           irpb.AutoKind_AUTO_UUID_V7,
		w17pb.AutoDefault_EMPTY_JSON_ARRAY:  irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY,
		w17pb.AutoDefault_EMPTY_JSON_OBJECT: irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT,
		w17pb.AutoDefault_TRUE:              irpb.AutoKind_AUTO_TRUE,
		w17pb.AutoDefault_FALSE:             irpb.AutoKind_AUTO_FALSE,
		w17pb.AutoDefault_IDENTITY:          irpb.AutoKind_AUTO_IDENTITY,
		// Unspecified + unknown fall through to AUTO_UNSPECIFIED.
		w17pb.AutoDefault_AUTO_DEFAULT_UNSPECIFIED: irpb.AutoKind_AUTO_UNSPECIFIED,
		w17pb.AutoDefault(9999):                    irpb.AutoKind_AUTO_UNSPECIFIED,
	}
	for in, want := range cases {
		if got := protoAutoToKind(in); got != want {
			t.Errorf("protoAutoToKind(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestDefaultSemTypeFor — every carrier including the UNSPECIFIED
// fallback (carriers with no pre-picked default such as BOOL / BYTES
// fall through to SEM_UNSPECIFIED; the caller then validates explicit
// type: annotations picked out by the user).
func TestDefaultSemTypeFor(t *testing.T) {
	cases := map[irpb.Carrier]irpb.SemType{
		irpb.Carrier_CARRIER_STRING:    irpb.SemType_SEM_TEXT,
		irpb.Carrier_CARRIER_INT32:     irpb.SemType_SEM_NUMBER,
		irpb.Carrier_CARRIER_INT64:     irpb.SemType_SEM_NUMBER,
		irpb.Carrier_CARRIER_DOUBLE:    irpb.SemType_SEM_NUMBER,
		irpb.Carrier_CARRIER_TIMESTAMP: irpb.SemType_SEM_DATETIME,
		irpb.Carrier_CARRIER_DURATION:  irpb.SemType_SEM_INTERVAL,
		irpb.Carrier_CARRIER_MAP:       irpb.SemType_SEM_AUTO,
		irpb.Carrier_CARRIER_LIST:      irpb.SemType_SEM_AUTO,
		// Carriers without a pre-picked default fall through.
		irpb.Carrier_CARRIER_BOOL:        irpb.SemType_SEM_UNSPECIFIED,
		irpb.Carrier_CARRIER_BYTES:       irpb.SemType_SEM_UNSPECIFIED,
		irpb.Carrier_CARRIER_UNSPECIFIED: irpb.SemType_SEM_UNSPECIFIED,
	}
	for c, want := range cases {
		if got := defaultSemTypeFor(c); got != want {
			t.Errorf("defaultSemTypeFor(%v) = %v, want %v", c, got, want)
		}
	}
}

// TestRegexpQuote — pins the escape-set used by path extension regexes
// (D22d). Each meta-character gets a leading backslash; non-meta ASCII
// + unicode pass through untouched.
func TestRegexpQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{".", `\.`},
		{"a.b", `a\.b`},
		{"foo+bar", `foo\+bar`},
		{"a*b", `a\*b`},
		{"a?", `a\?`},
		{"(x)", `\(x\)`},
		{"[y]", `\[y\]`},
		{"{z}", `\{z\}`},
		{"a|b", `a\|b`},
		{"^start$", `\^start\$`},
		{`a\b`, `a\\b`},
		{"česky.txt", `česky\.txt`}, // unicode passes through, dot still escaped
	}
	for _, c := range cases {
		if got := regexpQuote(c.in); got != c.want {
			t.Errorf("regexpQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestValidateIdentifier — every return branch: empty, too-long, reserved
// keyword (both ASCII-cased and upper-cased), valid.
func TestValidateIdentifier(t *testing.T) {
	// The max identifier length is NAMEDATALEN - 1 = 63 bytes. Construct
	// boundary values.
	goodMaxLen := strings.Repeat("a", 63)
	overLen := strings.Repeat("a", 64)

	cases := []struct {
		name     string
		in       string
		wantWhy  string // empty if expected valid
	}{
		{"valid snake_case", "my_table", ""},
		{"valid length = 63", goodMaxLen, ""},
		{"empty", "", "identifier is empty"},
		{"length 64", overLen, "63 bytes"},
		{"reserved lowercase", "select", "reserved keyword"},
		{"reserved uppercase", "SELECT", "reserved keyword"},
		{"reserved mixed case", "Order", "reserved keyword"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateIdentifier(c.in)
			if c.wantWhy == "" {
				if got != "" {
					t.Errorf("validateIdentifier(%q) = %q, want empty", c.in, got)
				}
				return
			}
			if !strings.Contains(got, c.wantWhy) {
				t.Errorf("validateIdentifier(%q) = %q, want containing %q", c.in, got, c.wantWhy)
			}
		})
	}
}

// TestIsReservedPgSchema — every documented reserved prefix + normal
// identifiers.
func TestIsReservedPgSchema(t *testing.T) {
	reserved := []string{"pg_catalog", "pg_toast", "pg_temp_1", "information_schema", "pg_any"}
	ok := []string{"reporting", "billing", "auth", "public"}
	for _, n := range reserved {
		if !isReservedPgSchema(n) {
			t.Errorf("%q should be reserved", n)
		}
	}
	for _, n := range ok {
		if isReservedPgSchema(n) {
			t.Errorf("%q should not be reserved", n)
		}
	}
}
