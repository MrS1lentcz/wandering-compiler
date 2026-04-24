//go:build e2e

package e2e

// Maps the classifier's carrier.yaml entries into Cells the
// harness can run. Handles per-carrier sem defaults and
// per-strategy resolution (CUSTOM_MIGRATION cells get a stub
// SQL body that lands cleanly on the synthesized empty table).

import (
	"fmt"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// allDbTypeCells iterates classifier.AllDbTypeCells() and builds
// a runnable Cell for each. DbType transitions are within-carrier
// in-axis changes — they flow through FactChange_DbType, not
// ReviewFindings, so no --decide is required and the harness
// runs them without the probe/resolution phase.
func allDbTypeCells(cls *classifier.Classifier) []Cell {
	entries := cls.AllDbTypeCells()
	out := make([]Cell, 0, len(entries))
	for _, e := range entries {
		out = append(out, dbtypeCell(e, cls))
	}
	return out
}

func dbtypeCell(e classifier.DbTypeEntry, cls *classifier.Classifier) Cell {
	carrier := familyCarrier(e.Family)
	c := Cell{
		Axis:     "dbtype",
		Name:     fmt.Sprintf("%s_%s_to_%s", e.Family, trimDbType(e.From), trimDbType(e.To)),
		Strategy: e.Cell.Strategy,
	}
	if carrier == irpb.Carrier_CARRIER_UNSPECIFIED {
		c.SkipReason = fmt.Sprintf("synth skip: family %s has no default carrier mapping", e.Family)
		return c
	}
	// JSON family uses MAP/LIST carrier which brings element-carrier
	// invariants the stub synth doesn't produce cleanly. Skip.
	if e.Family == "JSON" {
		c.SkipReason = "synth skip: JSON family requires map/list element context"
		return c
	}
	c.Prev = dbtypeSchema(carrier, e.From)
	c.Curr = dbtypeSchema(carrier, e.To)
	// Reverse-direction check: if classifier's reverse lookup is
	// CUSTOM_MIGRATION (no authored template), mark as forward-only —
	// harness skips diff.down apply (PG's implicit cast fails; real
	// rollback would need --decide-reverse).
	reverse := cls.DbType(carrier, e.To, e.From)
	if reverse.Strategy == planpb.Strategy_CUSTOM_MIGRATION {
		c.ForwardOnly = true
	}
	return c
}

// dbtypeSchema builds a minimal schema with a target column at
// the given (carrier, db_type) pair. Semantic defaults pulled
// from applyCarrierDefaults; db_type override wins at emit.
func dbtypeSchema(carrier irpb.Carrier, dbType irpb.DbType) *irpb.Schema {
	col := &irpb.Column{
		Name: "target", ProtoName: "target", FieldNumber: 2,
		Carrier:  carrier,
		DbType:   dbType,
		Nullable: true,
	}
	applyCarrierDefaults(col)
	// Specific invariants for dbtypes that require length / precision
	// beyond the generic defaults applied above.
	switch dbType {
	case irpb.DbType_DBT_VARCHAR:
		if col.GetMaxLen() == 0 {
			col.MaxLen = 64
		}
	case irpb.DbType_DBT_NUMERIC:
		if col.GetPrecision() == 0 {
			col.Precision = 12
		}
	}
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

func familyCarrier(family string) irpb.Carrier {
	switch family {
	case "STRING":
		return irpb.Carrier_CARRIER_STRING
	case "INT":
		return irpb.Carrier_CARRIER_INT64
	case "DOUBLE":
		return irpb.Carrier_CARRIER_DOUBLE
	case "TIMESTAMP":
		return irpb.Carrier_CARRIER_TIMESTAMP
	case "BYTES":
		return irpb.Carrier_CARRIER_BYTES
	}
	return irpb.Carrier_CARRIER_UNSPECIFIED
}

func trimDbType(d irpb.DbType) string {
	s := d.String()
	if len(s) > 4 && s[:4] == "DBT_" {
		return s[4:]
	}
	return s
}

// allCarrierCells iterates classifier.AllCarrierCells() and builds
// a runnable Cell for each. Unsupported synthesis shapes (LIST,
// MAP, MESSAGE — element-level invariants need extra work) are
// emitted with SkipReason so the sub-test is visible but doesn't
// fail. Skip list graduates to actual runs as the synthesizer
// learns those shapes.
func allCarrierCells(cls *classifier.Classifier) []Cell {
	entries := cls.AllCarrierCells()
	out := make([]Cell, 0, len(entries))
	for _, e := range entries {
		out = append(out, carrierCell(e, cls))
	}
	return out
}

func carrierCell(e classifier.CarrierEntry, cls *classifier.Classifier) Cell {
	c := Cell{
		Axis:        "carrier",
		Name:        carrierLabel(e.From, e.To),
		Strategy:    e.Cell.Strategy,
		FromCarrier: e.From,
		ToCarrier:   e.To,
	}
	if skip := carrierSynthSkip(e.From, e.To); skip != "" {
		c.SkipReason = skip
		return c
	}
	c.Prev = carrierSchema(e.From, defaultSemFor(e.From))
	c.Curr = carrierSchema(e.To, defaultSemFor(e.To))
	if e.Cell.Strategy == planpb.Strategy_CUSTOM_MIGRATION {
		c.CustomSQL = stubCustomSQL(e.From, e.To)
	} else {
		// Reverse-strategy check: when to→from is CUSTOM_MIGRATION
		// the roundtrip can't round-trip automatically. Mark as
		// forward-only so the harness skips diff.down + prev.down.
		reverse := cls.Carrier(e.To, e.From)
		if reverse.Strategy == planpb.Strategy_CUSTOM_MIGRATION {
			c.ForwardOnly = true
		}
	}
	return c
}

// carrierSynthSkip flags (from, to) pairs the synthesizer can't
// reliably stand up. These become Skip sub-tests — visible in the
// matrix, not failures. Categories:
//
//   - element-carrier carriers (LIST/MAP): valid shape needs an
//     element type matching the source side's element, which
//     the harness doesn't persist across "flips" — graduates to
//     a proper run when synth.go grows list/map awareness.
//   - MESSAGE ↔ non-MESSAGE: proto-FQN metadata doesn't survive
//     the flip meaningfully without author intent.
//
// Same-side LIST↔LIST / MAP↔MAP isn't in the carrier matrix (that
// axis is element-carrier reshape, a separate finding type).
func carrierSynthSkip(from, to irpb.Carrier) string {
	needsElem := func(c irpb.Carrier) bool {
		return c == irpb.Carrier_CARRIER_LIST || c == irpb.Carrier_CARRIER_MAP
	}
	if needsElem(from) || needsElem(to) {
		return fmt.Sprintf("synth skip: LIST/MAP carrier requires element-type context (%s ↔ %s)", from, to)
	}
	if from == irpb.Carrier_CARRIER_MESSAGE || to == irpb.Carrier_CARRIER_MESSAGE {
		return fmt.Sprintf("synth skip: MESSAGE carrier requires proto FQN context (%s ↔ %s)", from, to)
	}
	return ""
}

// defaultSemFor picks a sem that makes the synthesized column
// emittable for each carrier. Matches ir.Build's preset defaults
// so the synthesized IR renders without invariant errors.
func defaultSemFor(c irpb.Carrier) irpb.SemType {
	switch c {
	case irpb.Carrier_CARRIER_STRING:
		return irpb.SemType_SEM_CHAR // VARCHAR(32) via MaxLen default
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		return irpb.SemType_SEM_NUMBER
	case irpb.Carrier_CARRIER_DOUBLE:
		return irpb.SemType_SEM_NUMBER
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return irpb.SemType_SEM_DATETIME
	case irpb.Carrier_CARRIER_DURATION:
		return irpb.SemType_SEM_UNSPECIFIED
	case irpb.Carrier_CARRIER_BYTES:
		return irpb.SemType_SEM_UNSPECIFIED
	case irpb.Carrier_CARRIER_BOOL:
		return irpb.SemType_SEM_UNSPECIFIED
	}
	return irpb.SemType_SEM_UNSPECIFIED
}

// stubCustomSQL produces a trivial SQL body for CUSTOM_MIGRATION
// cells. Strategy: DROP the target column + re-ADD it with the new
// carrier's PG type. Works on the synthesized empty table; real
// projects supply a data-preserving CASE/UPDATE/whatever via
// --decide target=custom:<path>, but the harness just needs
// something that applies cleanly.
//
// Uses static PG-type keywords per carrier since the synthesizer's
// defaults are known. Sem fidelity isn't tested at the SQL layer
// here — that's the USING-clause cells' job (LOSSLESS_USING).
func stubCustomSQL(from, to irpb.Carrier) string {
	return fmt.Sprintf(
		"ALTER TABLE t DROP COLUMN target;\nALTER TABLE t ADD COLUMN target %s;",
		carrierStubPGType(to))
}

func carrierStubPGType(c irpb.Carrier) string {
	switch c {
	case irpb.Carrier_CARRIER_BOOL:
		return "BOOLEAN"
	case irpb.Carrier_CARRIER_STRING:
		return "VARCHAR(32)"
	case irpb.Carrier_CARRIER_INT32:
		return "INTEGER"
	case irpb.Carrier_CARRIER_INT64:
		return "BIGINT"
	case irpb.Carrier_CARRIER_DOUBLE:
		return "DOUBLE PRECISION"
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return "TIMESTAMPTZ"
	case irpb.Carrier_CARRIER_DURATION:
		return "INTERVAL"
	case irpb.Carrier_CARRIER_BYTES:
		return "BYTEA"
	}
	return "TEXT"
}
