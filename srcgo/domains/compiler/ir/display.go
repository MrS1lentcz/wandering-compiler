package ir

import (
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// displayCarrier renders a Carrier as the proto author wrote it in the
// source .proto ("string", "int32", "google.protobuf.Timestamp", …).
// The proto-generated Stringer returns "CARRIER_STRING" which would confuse
// end-user errors.
func displayCarrier(c irpb.Carrier) string {
	switch c {
	case irpb.Carrier_CARRIER_BOOL:
		return "bool"
	case irpb.Carrier_CARRIER_STRING:
		return "string"
	case irpb.Carrier_CARRIER_INT32:
		return "int32"
	case irpb.Carrier_CARRIER_INT64:
		return "int64"
	case irpb.Carrier_CARRIER_DOUBLE:
		return "double"
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return "google.protobuf.Timestamp"
	case irpb.Carrier_CARRIER_DURATION:
		return "google.protobuf.Duration"
	}
	return "<unspecified>"
}

// displaySemType strips the "SEM_" prefix from the proto enum name so error
// messages read like the authoring vocabulary ("CHAR", "DECIMAL", …).
func displaySemType(s irpb.SemType) string {
	if s == irpb.SemType_SEM_UNSPECIFIED {
		return "<unspecified>"
	}
	return strings.TrimPrefix(s.String(), "SEM_")
}

// displayAutoKind strips the "AUTO_" prefix for user-facing messages ("NOW",
// "UUID_V4", "IDENTITY", …).
func displayAutoKind(k irpb.AutoKind) string {
	if k == irpb.AutoKind_AUTO_UNSPECIFIED {
		return "<unspecified>"
	}
	return strings.TrimPrefix(k.String(), "AUTO_")
}
