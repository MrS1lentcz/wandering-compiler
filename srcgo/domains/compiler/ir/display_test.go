package ir

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestDisplayCarrier — user-facing names for every carrier. Proto's
// own Stringer returns "CARRIER_STRING" etc. which is not how
// authors write the proto — these helpers render "string",
// "int32", "google.protobuf.Timestamp", …. Every carrier gets a
// test entry so future additions don't silently land with an
// "<unspecified>" fallback in error text.
func TestDisplayCarrier(t *testing.T) {
	cases := []struct {
		in   irpb.Carrier
		want string
	}{
		{irpb.Carrier_CARRIER_UNSPECIFIED, "<unspecified>"},
		{irpb.Carrier_CARRIER_BOOL, "bool"},
		{irpb.Carrier_CARRIER_STRING, "string"},
		{irpb.Carrier_CARRIER_INT32, "int32"},
		{irpb.Carrier_CARRIER_INT64, "int64"},
		{irpb.Carrier_CARRIER_DOUBLE, "double"},
		{irpb.Carrier_CARRIER_TIMESTAMP, "google.protobuf.Timestamp"},
		{irpb.Carrier_CARRIER_DURATION, "google.protobuf.Duration"},
		{irpb.Carrier_CARRIER_BYTES, "bytes"},
		{irpb.Carrier_CARRIER_MAP, "MAP"},
		{irpb.Carrier_CARRIER_LIST, "LIST"},
	}
	for _, c := range cases {
		if got := displayCarrier(c.in); got != c.want {
			t.Errorf("displayCarrier(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// Unknown values also fall through to the fallback so future
	// enum growth doesn't break diag formatting silently.
	if got := displayCarrier(irpb.Carrier(9999)); got != "<unspecified>" {
		t.Errorf("displayCarrier(unknown) = %q, want <unspecified>", got)
	}
}

// TestDisplaySemType — strips the "SEM_" prefix for every sem
// type. Checks both the shared fallback (UNSPECIFIED) and a
// representative set; the `TrimPrefix(s.String(), "SEM_")` path
// trivially covers the rest provided the enum names stay the
// SEM_XX shape.
func TestDisplaySemType(t *testing.T) {
	cases := []struct {
		in   irpb.SemType
		want string
	}{
		{irpb.SemType_SEM_UNSPECIFIED, "<unspecified>"},
		{irpb.SemType_SEM_CHAR, "CHAR"},
		{irpb.SemType_SEM_TEXT, "TEXT"},
		{irpb.SemType_SEM_UUID, "UUID"},
		{irpb.SemType_SEM_EMAIL, "EMAIL"},
		{irpb.SemType_SEM_URL, "URL"},
		{irpb.SemType_SEM_SLUG, "SLUG"},
		{irpb.SemType_SEM_DECIMAL, "DECIMAL"},
		{irpb.SemType_SEM_JSON, "JSON"},
		{irpb.SemType_SEM_IP, "IP"},
		{irpb.SemType_SEM_TSEARCH, "TSEARCH"},
		{irpb.SemType_SEM_ENUM, "ENUM"},
		{irpb.SemType_SEM_MAC, "MAC"},
		{irpb.SemType_SEM_POSIX_PATH, "POSIX_PATH"},
		{irpb.SemType_SEM_FILE_PATH, "FILE_PATH"},
		{irpb.SemType_SEM_IMAGE_PATH, "IMAGE_PATH"},
		{irpb.SemType_SEM_NUMBER, "NUMBER"},
		{irpb.SemType_SEM_ID, "ID"},
		{irpb.SemType_SEM_COUNTER, "COUNTER"},
		{irpb.SemType_SEM_MONEY, "MONEY"},
		{irpb.SemType_SEM_PERCENTAGE, "PERCENTAGE"},
		{irpb.SemType_SEM_RATIO, "RATIO"},
		{irpb.SemType_SEM_SMALL_INTEGER, "SMALL_INTEGER"},
		{irpb.SemType_SEM_DATE, "DATE"},
		{irpb.SemType_SEM_TIME, "TIME"},
		{irpb.SemType_SEM_DATETIME, "DATETIME"},
		{irpb.SemType_SEM_INTERVAL, "INTERVAL"},
		{irpb.SemType_SEM_AUTO, "AUTO"},
	}
	for _, c := range cases {
		if got := displaySemType(c.in); got != c.want {
			t.Errorf("displaySemType(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDisplayAutoKind — strips the "AUTO_" prefix; covers
// UNSPECIFIED fallback + every legit AutoKind shipped today.
func TestDisplayAutoKind(t *testing.T) {
	cases := []struct {
		in   irpb.AutoKind
		want string
	}{
		{irpb.AutoKind_AUTO_UNSPECIFIED, "<unspecified>"},
		{irpb.AutoKind_AUTO_NOW, "NOW"},
		{irpb.AutoKind_AUTO_UUID_V4, "UUID_V4"},
		{irpb.AutoKind_AUTO_UUID_V7, "UUID_V7"},
		{irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY, "EMPTY_JSON_ARRAY"},
		{irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT, "EMPTY_JSON_OBJECT"},
		{irpb.AutoKind_AUTO_TRUE, "TRUE"},
		{irpb.AutoKind_AUTO_FALSE, "FALSE"},
		{irpb.AutoKind_AUTO_IDENTITY, "IDENTITY"},
	}
	for _, c := range cases {
		if got := displayAutoKind(c.in); got != c.want {
			t.Errorf("displayAutoKind(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSuggestedTypeFor — per-carrier "what type would you want"
// recommendation threaded into error fixes. Every carrier returns
// a concrete sem suggestion; the fallback covers unknown values.
func TestSuggestedTypeFor(t *testing.T) {
	cases := []struct {
		in   irpb.Carrier
		want string
	}{
		{irpb.Carrier_CARRIER_STRING, "CHAR, max_len: 255"},
		{irpb.Carrier_CARRIER_INT32, "NUMBER"},
		{irpb.Carrier_CARRIER_INT64, "NUMBER"},
		{irpb.Carrier_CARRIER_DOUBLE, "NUMBER"},
		{irpb.Carrier_CARRIER_TIMESTAMP, "DATETIME"},
		{irpb.Carrier_CARRIER_DURATION, "INTERVAL"},
		// Fallback for carriers without a canonical suggestion.
		{irpb.Carrier_CARRIER_UNSPECIFIED, "CHAR"},
		{irpb.Carrier_CARRIER_BOOL, "CHAR"},
		{irpb.Carrier_CARRIER_BYTES, "CHAR"},
		{irpb.Carrier_CARRIER_MAP, "CHAR"},
		{irpb.Carrier_CARRIER_LIST, "CHAR"},
	}
	for _, c := range cases {
		if got := suggestedTypeFor(c.in); got != c.want {
			t.Errorf("suggestedTypeFor(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
