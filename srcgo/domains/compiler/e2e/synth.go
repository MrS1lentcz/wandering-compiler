//go:build e2e

package e2e

// Per-axis IR synthesizers. Each cell of a classifier matrix is
// reducible to "a table with a single target column of shape X
// before and Y after"; these helpers produce the (prev, curr)
// IR pair so the harness can run it through engine.Plan.
//
// Conventions:
//   Table name           = "t"
//   MessageFqn           = "e2e.T"
//   Integer PK column    = "id" (INT64 / BIGINT), proto field #1
//   Target column        = "target", proto field #2
//
// The target column's carrier / sem / db_type / etc. is what the
// cell flips. Proto field number stays stable across the flip —
// D10 / D24 identity key so plan.Diff classifies it as an alter
// (not drop+add).

import (
	"fmt"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// carrierSchema builds a minimal Schema with a single target
// column of the given carrier + sem. Required fills (MaxLen /
// Precision / element carriers) pick sensible defaults so the IR
// passes ir.Build-equivalent validation.
func carrierSchema(carrier irpb.Carrier, sem irpb.SemType) *irpb.Schema {
	col := &irpb.Column{
		Name:      "target",
		ProtoName: "target",
		FieldNumber: 2,
		Carrier:   carrier,
		Type:      sem,
		Nullable:  true,
	}
	applyCarrierDefaults(col)
	return &irpb.Schema{Tables: []*irpb.Table{{
		Name:       "t",
		MessageFqn: "e2e.T",
		Columns: []*irpb.Column{
			{
				Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64,
				Type:    irpb.SemType_SEM_ID,
				DbType:  irpb.DbType_DBT_BIGINT,
				Pk:      true,
			},
			col,
		},
		PrimaryKey: []string{"id"},
	}}}
}

// applyCarrierDefaults fills carrier-invariant fields so the IR
// is emittable. Matches the defaults ir.Build would apply when
// presets / sems don't specify.
func applyCarrierDefaults(c *irpb.Column) {
	switch c.GetCarrier() {
	case irpb.Carrier_CARRIER_STRING:
		if c.GetType() == irpb.SemType_SEM_UNSPECIFIED {
			c.Type = irpb.SemType_SEM_TEXT
		}
		// Length-bounded sems need a MaxLen; fall back to 32 so
		// the emitter can render VARCHAR(32).
		switch c.GetType() {
		case irpb.SemType_SEM_CHAR, irpb.SemType_SEM_SLUG,
			irpb.SemType_SEM_EMAIL, irpb.SemType_SEM_URL:
			if c.GetMaxLen() == 0 {
				c.MaxLen = 32
			}
		case irpb.SemType_SEM_DECIMAL:
			if c.GetPrecision() == 0 {
				c.Precision = 12
			}
		}
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		if c.GetType() == irpb.SemType_SEM_UNSPECIFIED {
			c.Type = irpb.SemType_SEM_NUMBER
		}
	case irpb.Carrier_CARRIER_DOUBLE:
		if c.GetType() == irpb.SemType_SEM_UNSPECIFIED {
			c.Type = irpb.SemType_SEM_NUMBER
		}
	case irpb.Carrier_CARRIER_TIMESTAMP:
		if c.GetType() == irpb.SemType_SEM_UNSPECIFIED {
			c.Type = irpb.SemType_SEM_DATETIME
		}
	case irpb.Carrier_CARRIER_LIST:
		if c.GetElementCarrier() == irpb.Carrier_CARRIER_UNSPECIFIED {
			c.ElementCarrier = irpb.Carrier_CARRIER_STRING
		}
		if c.GetType() == irpb.SemType_SEM_UNSPECIFIED {
			c.Type = irpb.SemType_SEM_TEXT
		}
	case irpb.Carrier_CARRIER_MAP:
		if c.GetElementCarrier() == irpb.Carrier_CARRIER_UNSPECIFIED {
			c.ElementCarrier = irpb.Carrier_CARRIER_STRING
		}
		if c.GetType() == irpb.SemType_SEM_UNSPECIFIED {
			c.Type = irpb.SemType_SEM_TEXT
		}
	case irpb.Carrier_CARRIER_MESSAGE:
		// Message carrier needs an FQN — use a stub; emit treats it
		// as JSONB so the pre-resolved FQN doesn't matter at apply.
		if c.GetMessageFqn() == "" {
			c.MessageFqn = "e2e.Inner"
		}
	}
}

// carrierLabel renders a compact "BOOL→STRING" style identifier
// for sub-test names + error messages.
func carrierLabel(from, to irpb.Carrier) string {
	return fmt.Sprintf("%s_to_%s",
		trimPrefix(from.String(), "CARRIER_"),
		trimPrefix(to.String(), "CARRIER_"))
}

func trimPrefix(s, prefix string) string {
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
