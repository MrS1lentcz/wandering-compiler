// Package ir holds the dialect-agnostic intermediate representation of a
// user's schema. Loader output lands in ir.Build which validates every
// invariant from docs/iteration-1.md D2/D7/D8 and emits a *Schema.
// Emitters (emit/postgres, emit/sqlite) consume *Schema — they do not see
// proto.
package ir

// Carrier is the proto wire-type underneath a field. Mirrors the subset of
// kinds accepted in iteration-1 (see docs/iteration-1.md "In scope").
//
// Defined as IR-level values (not a re-export of protoreflect.Kind or
// w17pb.Type) because IR is a dialect-independent model of the schema and
// future input surfaces (visual editor, import-from-DB) should not have to
// speak proto to construct a *Schema.
type Carrier int

const (
	CarrierUnspecified Carrier = iota
	CarrierBool
	CarrierString
	CarrierInt32
	CarrierInt64
	CarrierDouble
	CarrierTimestamp
	CarrierDuration
)

func (c Carrier) String() string {
	switch c {
	case CarrierBool:
		return "bool"
	case CarrierString:
		return "string"
	case CarrierInt32:
		return "int32"
	case CarrierInt64:
		return "int64"
	case CarrierDouble:
		return "double"
	case CarrierTimestamp:
		return "google.protobuf.Timestamp"
	case CarrierDuration:
		return "google.protobuf.Duration"
	}
	return "<unspecified>"
}

// SemType is the semantic refinement of a Carrier — what the column
// actually *means*. Drives column type selection in emitters and the
// default CHECK constraints the IR builder attaches.
type SemType int

const (
	SemUnspecified SemType = iota

	// String carriers.
	SemChar
	SemText
	SemUUID
	SemEmail
	SemURL
	SemSlug

	// Numeric carriers (int32/int64/double).
	SemNumber
	SemID
	SemCounter
	SemMoney
	SemPercentage
	SemRatio

	// Arbitrary precision. Carrier: string (lossless wire).
	SemDecimal

	// Temporal carriers.
	SemDate     // Timestamp carrier
	SemTime     // Timestamp carrier
	SemDateTime // Timestamp carrier
	SemInterval // Duration carrier
)

func (s SemType) String() string {
	switch s {
	case SemChar:
		return "CHAR"
	case SemText:
		return "TEXT"
	case SemUUID:
		return "UUID"
	case SemEmail:
		return "EMAIL"
	case SemURL:
		return "URL"
	case SemSlug:
		return "SLUG"
	case SemNumber:
		return "NUMBER"
	case SemID:
		return "ID"
	case SemCounter:
		return "COUNTER"
	case SemMoney:
		return "MONEY"
	case SemPercentage:
		return "PERCENTAGE"
	case SemRatio:
		return "RATIO"
	case SemDecimal:
		return "DECIMAL"
	case SemDate:
		return "DATE"
	case SemTime:
		return "TIME"
	case SemDateTime:
		return "DATETIME"
	case SemInterval:
		return "INTERVAL"
	}
	return "<unspecified>"
}

// FKAction is the resolved ON DELETE behaviour. Derived from
// (w17.field).orphanable + (w17.field).null per D8.
type FKAction int

const (
	FKActionCascade FKAction = iota
	FKActionSetNull
)

func (a FKAction) String() string {
	if a == FKActionSetNull {
		return "SET NULL"
	}
	return "CASCADE"
}
