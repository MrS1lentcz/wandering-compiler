// Package classifier dispatches D28 matrix lookups: given a fact-change
// axis + (from, to) pair or axis + case, return the Cell describing the
// migration strategy, USING-cast expression, check.sql template, and
// human rationale.
//
// The authoritative matrices live as YAML under docs/classification/;
// this package is their in-memory index. Build a Classifier once (Load);
// call Carrier / DbType / Constraint / Fold freely afterwards.
//
// Per D30 the classifier is pure: no globals, no caches, no I/O after
// Load returns. Safe for concurrent use.
//
// Missing cells synthesise a CUSTOM_MIGRATION Cell — per the governing
// rule (D28 header, 2026-04-23 user): no silent coercion, if there's
// no explicit rule the author writes the migration SQL.
package classifier

import (
	"fmt"
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Cell is the unit of classifier output: strategy + artifacts for one
// axis transition. Shared across carrier / dbtype / constraint lookups
// so Fold can compare apples to apples.
type Cell struct {
	Strategy  planpb.Strategy
	Using     string // PG USING expression template (empty unless USING-based)
	CheckSQL  string // NEEDS_CONFIRM pre-apply validation template
	Rationale string // human-readable; surfaced in ReviewFinding / diag.Error
}

// Classifier is an immutable index over the D28 matrices. Build via Load;
// call methods freely.
type Classifier struct {
	// strategyRank maps each Strategy to its fold rank (strictest wins).
	// Derived from strategies.yaml; independent of enum declaration order
	// so config changes don't require a rebuild.
	strategyRank map[planpb.Strategy]int32

	carrier    map[carrierKey]Cell
	dbtype     map[dbtypeKey]Cell
	constraint map[constraintKey]Cell
}

type carrierKey struct{ from, to string }          // trimmed enum names (BOOL / STRING / …)
type dbtypeKey struct{ family, from, to string }   // family = STRING/INT/DOUBLE/TIMESTAMP/BYTES/JSON
type constraintKey struct{ axis, caseID string }   // axis = nullable / max_len / …; case = widen / narrow / …

// CarrierEntry is one (from, to, Cell) triple — for iteration use
// by tooling (e2e matrix runner, coverage audits). Returned by
// AllCarrierCells in carrier.yaml declaration order.
type CarrierEntry struct {
	From, To irpb.Carrier
	Cell     Cell
}

// AllCarrierCells returns every carrier transition in the loaded
// matrix. Sorted by (from, to) enum value for deterministic output.
// Synthesised cells (for (from,to) not in YAML) are NOT returned —
// the iterator only emits authored cells.
func (c *Classifier) AllCarrierCells() []CarrierEntry {
	out := make([]CarrierEntry, 0, len(c.carrier))
	for k, cell := range c.carrier {
		from, fromOK := carrierFromName(k.from)
		to, toOK := carrierFromName(k.to)
		if !fromOK || !toOK {
			continue
		}
		out = append(out, CarrierEntry{From: from, To: to, Cell: cell})
	}
	sortCarrierEntries(out)
	return out
}

// DbTypeEntry is one (family, from, to, Cell) row — iterator output
// for the dbType matrix.
type DbTypeEntry struct {
	Family   string
	From, To irpb.DbType
	Cell     Cell
}

// AllDbTypeCells returns every within-carrier dbType transition
// in the loaded matrix, sorted by (family, from, to) for
// determinism.
func (c *Classifier) AllDbTypeCells() []DbTypeEntry {
	out := make([]DbTypeEntry, 0, len(c.dbtype))
	for k, cell := range c.dbtype {
		from, fromOK := dbtypeFromName(k.from)
		to, toOK := dbtypeFromName(k.to)
		if !fromOK || !toOK {
			continue
		}
		out = append(out, DbTypeEntry{Family: k.family, From: from, To: to, Cell: cell})
	}
	sortDbTypeEntries(out)
	return out
}

// ConstraintEntry is one (axis, case, Cell) row — iterator output
// for the constraint matrix.
type ConstraintEntry struct {
	Axis   string
	CaseID string
	Cell   Cell
}

// AllConstraintCells returns every constraint axis case in the
// loaded matrix, sorted by (axis, case) for determinism.
func (c *Classifier) AllConstraintCells() []ConstraintEntry {
	out := make([]ConstraintEntry, 0, len(c.constraint))
	for k, cell := range c.constraint {
		out = append(out, ConstraintEntry{Axis: k.axis, CaseID: k.caseID, Cell: cell})
	}
	sortConstraintEntries(out)
	return out
}

// Carrier returns the Cell for a carrier-axis transition. When the exact
// (from, to) pair isn't in the matrix, returns a synthesised
// CUSTOM_MIGRATION cell (D28 rule: author owns ambiguous paths).
//
// `from == to` returns an explicit SAFE no-op cell — the differ's job
// to skip such calls, but this guards against caller bugs.
func (c *Classifier) Carrier(from, to irpb.Carrier) Cell {
	if from == to {
		return Cell{Strategy: planpb.Strategy_SAFE, Rationale: "no-op: same carrier"}
	}
	key := carrierKey{from: carrierName(from), to: carrierName(to)}
	if cell, ok := c.carrier[key]; ok {
		return cell
	}
	return synthCustom(fmt.Sprintf("carrier %s → %s", key.from, key.to))
}

// DbType returns the Cell for a dbType-axis transition within the
// carrier's family (STRING / INT / DOUBLE / TIMESTAMP / BYTES / JSON).
// Cross-family dbType transitions are misuse — callers should route
// through Carrier first. When unfamiliar, returns CUSTOM_MIGRATION.
func (c *Classifier) DbType(carrier irpb.Carrier, from, to irpb.DbType) Cell {
	if from == to {
		return Cell{Strategy: planpb.Strategy_SAFE, Rationale: "no-op: same dbType"}
	}
	family := carrierFamily(carrier)
	if family == "" {
		return synthCustom(fmt.Sprintf("dbType change on carrier %s (no family)", carrierName(carrier)))
	}
	key := dbtypeKey{family: family, from: dbtypeName(from), to: dbtypeName(to)}
	if cell, ok := c.dbtype[key]; ok {
		return cell
	}
	return synthCustom(fmt.Sprintf("%s dbType %s → %s", family, key.from, key.to))
}

// Constraint returns the Cell for an axis + case pair. Axis is the
// constraint-axis identifier (nullable, default, max_len, pk, …); case
// is the discrete semantic label the caller computed from the fact pair
// (widen / narrow / add / drop / change / relax / tighten / any).
//
// Unknown (axis, case) combinations return CUSTOM_MIGRATION — but
// callers shouldn't rely on that; it's a fallback for classifier
// coverage gaps, not a graceful-degradation feature.
func (c *Classifier) Constraint(axis, caseID string) Cell {
	key := constraintKey{axis: axis, caseID: caseID}
	if cell, ok := c.constraint[key]; ok {
		return cell
	}
	return synthCustom(fmt.Sprintf("constraint %s.%s", axis, caseID))
}

// Fold returns the strictest cell across a multi-axis change set.
// Strictest = highest strategy rank per strategies.yaml ordering.
// Check.sql of the winner is kept; narrative rationale flattens to a
// summary. Empty input returns SAFE no-op.
//
// Multi-axis fold semantics per D28.1 "Strictness fold": when one alter
// touches multiple axes, emitted strategy is the strictest across them.
func (c *Classifier) Fold(cells []Cell) Cell {
	if len(cells) == 0 {
		return Cell{Strategy: planpb.Strategy_SAFE}
	}
	winner := cells[0]
	for _, cell := range cells[1:] {
		if c.strategyRank[cell.Strategy] > c.strategyRank[winner.Strategy] {
			winner = cell
		}
	}
	return winner
}

// Rank returns the fold rank for one strategy. Exposed so diff.go can
// compare strategies without routing through Fold on trivial cases.
func (c *Classifier) Rank(s planpb.Strategy) int32 {
	return c.strategyRank[s]
}

// carrierName trims the "CARRIER_" prefix so the generated enum name
// matches the YAML shorthand (BOOL, STRING, INT32, …).
func carrierName(c irpb.Carrier) string {
	return strings.TrimPrefix(c.String(), "CARRIER_")
}

// carrierFromName reverses carrierName — looks up the irpb.Carrier
// enum for a YAML shorthand. Used by AllCarrierCells iteration.
// Returns (CARRIER_UNSPECIFIED, false) for unknown names so callers
// can filter rather than panic.
func carrierFromName(name string) (irpb.Carrier, bool) {
	full := "CARRIER_" + name
	if v, ok := irpb.Carrier_value[full]; ok {
		return irpb.Carrier(v), true
	}
	return irpb.Carrier_CARRIER_UNSPECIFIED, false
}

// sortCarrierEntries orders carrier entries by (from, to) enum value
// so iteration is deterministic across test runs.
func sortCarrierEntries(entries []CarrierEntry) {
	// Insertion sort — tiny slices (110 entries max), O(n²) fine.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			if carrierLess(entries[j], entries[j-1]) {
				entries[j], entries[j-1] = entries[j-1], entries[j]
				continue
			}
			break
		}
	}
}

func carrierLess(a, b CarrierEntry) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	return a.To < b.To
}

// dbtypeFromName reverses dbtypeName. Returns
// (DB_TYPE_UNSPECIFIED, false) for unknown names.
func dbtypeFromName(name string) (irpb.DbType, bool) {
	full := "DBT_" + name
	if v, ok := irpb.DbType_value[full]; ok {
		return irpb.DbType(v), true
	}
	return irpb.DbType_DB_TYPE_UNSPECIFIED, false
}

func sortDbTypeEntries(entries []DbTypeEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			if dbtypeLess(entries[j], entries[j-1]) {
				entries[j], entries[j-1] = entries[j-1], entries[j]
				continue
			}
			break
		}
	}
}

func dbtypeLess(a, b DbTypeEntry) bool {
	if a.Family != b.Family {
		return a.Family < b.Family
	}
	if a.From != b.From {
		return a.From < b.From
	}
	return a.To < b.To
}

func sortConstraintEntries(entries []ConstraintEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0; j-- {
			if constraintLess(entries[j], entries[j-1]) {
				entries[j], entries[j-1] = entries[j-1], entries[j]
				continue
			}
			break
		}
	}
}

func constraintLess(a, b ConstraintEntry) bool {
	if a.Axis != b.Axis {
		return a.Axis < b.Axis
	}
	return a.CaseID < b.CaseID
}

// dbtypeName trims the "DBT_" prefix (ir.proto DbType convention).
func dbtypeName(d irpb.DbType) string {
	return strings.TrimPrefix(d.String(), "DBT_")
}

// carrierFamily groups carriers into dbType-matrix families. See
// dbtype.yaml per-family grid headers (C1-C6).
func carrierFamily(c irpb.Carrier) string {
	switch c {
	case irpb.Carrier_CARRIER_STRING:
		return "STRING"
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		return "INT"
	case irpb.Carrier_CARRIER_DOUBLE:
		return "DOUBLE"
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return "TIMESTAMP"
	case irpb.Carrier_CARRIER_BYTES:
		return "BYTES"
	case irpb.Carrier_CARRIER_MAP, irpb.Carrier_CARRIER_LIST:
		return "JSON"
	}
	return ""
}

// synthCustom builds a fallback Cell for coverage gaps. D28 rule: no
// silent coercion, author writes when compiler has no pinned rule.
func synthCustom(context string) Cell {
	return Cell{
		Strategy:  planpb.Strategy_CUSTOM_MIGRATION,
		Rationale: "No explicit classification rule for " + context + "; author writes migration SQL (--decide col=custom) or opts into drop_and_create.",
	}
}
