package ir

import (
	"context"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// borrowFieldDesc returns any FieldDescriptor from the happy.proto
// fixture — validateCarrierSemType only reads `desc.Name()` and
// threads the descriptor through diag.Atf for source anchoring;
// the exact field identity doesn't affect the carrier×sem logic.
// One load per test package covers every unit test below.
func borrowFieldDesc(t *testing.T) protoreflect.FieldDescriptor {
	t.Helper()
	lf, err := loader.Load(context.Background(), "happy.proto", []string{"testdata", "../../../../proto"})
	if err != nil {
		t.Fatalf("borrowFieldDesc: loader.Load: %v", err)
	}
	for _, m := range lf.Messages {
		for _, f := range m.Fields {
			if f.Desc != nil {
				return f.Desc
			}
		}
	}
	t.Fatal("borrowFieldDesc: no field descriptor in happy.proto")
	return nil
}

// TestValidateCarrierSemType walks every (carrier, sem) pair that
// matters — both the OK pairs (nil return) and each rejection
// branch. The SEM_UNSPECIFIED branches on STRING / INT / DOUBLE /
// TIMESTAMP / MAP are dead code in the normal buildColumn flow
// (D14 defaultSemTypeFor fills them in before validate), but the
// branches stay as defensive guards in case a future call-site
// skips the default step. Unit testing them pins the contract.
func TestValidateCarrierSemType(t *testing.T) {
	desc := borrowFieldDesc(t)

	type row struct {
		name       string
		carrier    irpb.Carrier
		sem        irpb.SemType
		wantErr    string // empty = expect nil
	}
	cases := []row{
		// --- BOOL ---
		{"bool + unspecified = OK", irpb.Carrier_CARRIER_BOOL, irpb.SemType_SEM_UNSPECIFIED, ""},
		{"bool + NUMBER rejected", irpb.Carrier_CARRIER_BOOL, irpb.SemType_SEM_NUMBER,
			"bool carrier must not set a semantic type"},
		{"bool + DATETIME rejected", irpb.Carrier_CARRIER_BOOL, irpb.SemType_SEM_DATETIME,
			"bool carrier must not set a semantic type"},

		// --- BYTES ---
		{"bytes + unspecified = OK", irpb.Carrier_CARRIER_BYTES, irpb.SemType_SEM_UNSPECIFIED, ""},
		{"bytes + JSON = OK", irpb.Carrier_CARRIER_BYTES, irpb.SemType_SEM_JSON, ""},
		{"bytes + UUID rejected", irpb.Carrier_CARRIER_BYTES, irpb.SemType_SEM_UUID,
			"bytes carrier accepts only type: JSON"},

		// --- STRING — OK paths ---
		{"string + CHAR", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_CHAR, ""},
		{"string + TEXT", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_TEXT, ""},
		{"string + UUID", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_UUID, ""},
		{"string + EMAIL", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_EMAIL, ""},
		{"string + URL", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_URL, ""},
		{"string + SLUG", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_SLUG, ""},
		{"string + DECIMAL", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_DECIMAL, ""},
		{"string + JSON", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_JSON, ""},
		{"string + IP", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_IP, ""},
		{"string + TSEARCH", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_TSEARCH, ""},
		{"string + ENUM", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_ENUM, ""},
		{"string + MAC", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_MAC, ""},
		{"string + POSIX_PATH", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_POSIX_PATH, ""},
		{"string + FILE_PATH", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_FILE_PATH, ""},
		{"string + IMAGE_PATH", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_IMAGE_PATH, ""},
		// --- STRING — rejection paths ---
		{"string + unspecified rejected (defensive)", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_UNSPECIFIED,
			"string carrier requires a semantic type"},
		{"string + NUMBER rejected", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_NUMBER,
			"is not valid on a string carrier"},
		{"string + COUNTER rejected", irpb.Carrier_CARRIER_STRING, irpb.SemType_SEM_COUNTER,
			"is not valid on a string carrier"},

		// --- INT32 — OK paths ---
		{"int32 + NUMBER", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_NUMBER, ""},
		{"int32 + ID", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_ID, ""},
		{"int32 + ENUM", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_ENUM, ""},
		{"int32 + SMALL_INTEGER", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_SMALL_INTEGER, ""},
		// --- INT32 — rejection paths ---
		{"int32 + unspecified rejected (defensive)", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_UNSPECIFIED,
			"int32 carrier requires a semantic type"},
		{"int32 + COUNTER rejected", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_COUNTER,
			"is not valid on an int32 carrier"},
		{"int32 + MONEY rejected", irpb.Carrier_CARRIER_INT32, irpb.SemType_SEM_MONEY,
			"is not valid on an int32 carrier"},

		// --- INT64 — OK paths ---
		{"int64 + NUMBER", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_NUMBER, ""},
		{"int64 + ID", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_ID, ""},
		{"int64 + COUNTER", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_COUNTER, ""},
		{"int64 + ENUM", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_ENUM, ""},
		// --- INT64 — rejection paths ---
		{"int64 + unspecified rejected (defensive)", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_UNSPECIFIED,
			"int64 carrier requires a semantic type"},
		{"int64 + SMALL_INTEGER rejected (D22c)", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_SMALL_INTEGER,
			"is not valid on an int64 carrier"},
		{"int64 + MONEY rejected", irpb.Carrier_CARRIER_INT64, irpb.SemType_SEM_MONEY,
			"is not valid on an int64 carrier"},

		// --- DOUBLE — OK paths ---
		{"double + NUMBER", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_NUMBER, ""},
		{"double + MONEY", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_MONEY, ""},
		{"double + PERCENTAGE", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_PERCENTAGE, ""},
		{"double + RATIO", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_RATIO, ""},
		// --- DOUBLE — rejection paths ---
		{"double + unspecified rejected (defensive)", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_UNSPECIFIED,
			"double carrier requires a semantic type"},
		{"double + ID rejected", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_ID,
			"is not valid on a double carrier"},
		{"double + DATETIME rejected", irpb.Carrier_CARRIER_DOUBLE, irpb.SemType_SEM_DATETIME,
			"is not valid on a double carrier"},

		// --- TIMESTAMP — OK paths ---
		{"timestamp + DATE", irpb.Carrier_CARRIER_TIMESTAMP, irpb.SemType_SEM_DATE, ""},
		{"timestamp + TIME", irpb.Carrier_CARRIER_TIMESTAMP, irpb.SemType_SEM_TIME, ""},
		{"timestamp + DATETIME", irpb.Carrier_CARRIER_TIMESTAMP, irpb.SemType_SEM_DATETIME, ""},
		// --- TIMESTAMP — rejection paths ---
		{"timestamp + unspecified rejected (defensive)", irpb.Carrier_CARRIER_TIMESTAMP, irpb.SemType_SEM_UNSPECIFIED,
			"Timestamp carrier requires a semantic type"},
		{"timestamp + INTERVAL rejected", irpb.Carrier_CARRIER_TIMESTAMP, irpb.SemType_SEM_INTERVAL,
			"is not valid on a Timestamp carrier"},
		{"timestamp + NUMBER rejected", irpb.Carrier_CARRIER_TIMESTAMP, irpb.SemType_SEM_NUMBER,
			"is not valid on a Timestamp carrier"},

		// --- DURATION ---
		{"duration + INTERVAL", irpb.Carrier_CARRIER_DURATION, irpb.SemType_SEM_INTERVAL, ""},
		{"duration + DATETIME rejected", irpb.Carrier_CARRIER_DURATION, irpb.SemType_SEM_DATETIME,
			"Duration carrier must be INTERVAL"},

		// --- MAP ---
		{"map + AUTO", irpb.Carrier_CARRIER_MAP, irpb.SemType_SEM_AUTO, ""},
		{"map + TEXT rejected", irpb.Carrier_CARRIER_MAP, irpb.SemType_SEM_TEXT,
			"map carrier must be AUTO"},

		// --- LIST — element-level validation happens elsewhere; carrier-
		// level accepts every sem. Covered by positive fixtures.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateCarrierSemType(desc, c.carrier, c.sem)
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q missing substring %q", err.Error(), c.wantErr)
			}
		})
	}
}
