package engine

// D33 — engine-side synthesis of Ops from resolved cross-carrier
// ReviewFindings. Bridges the gap between "classifier says
// LOSSLESS_USING with this template" and "emitter produces an
// ALTER TABLE … TYPE … USING …" without requiring the author to
// supply CUSTOM_MIGRATION SQL manually.
//
// CUSTOM_MIGRATION resolutions are excluded here — they flow
// through spliceCustomMigrations at the string layer.

import (
	"bytes"
	"fmt"
	"sort"
	"text/template"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// injectStrategyOps walks resolved ReviewFindings (minus
// CUSTOM_MIGRATION — handled by spliceCustomMigrations) and
// synthesises the Ops the emitter needs to produce migration
// SQL. Returned Ops are Finding-ID-sorted for idempotence.
//
// Current scope:
//   - axis "carrier_change" → TypeChange FactChange or Drop+Add
//     depending on resolution strategy.
//
// Out of scope today (findings still flow via CUSTOM_MIGRATION
// or error): pk_flip, custom_type_change, enum_fqn_change,
// enum_remove_value, element_carrier_reshape. These axes
// universally require author-written SQL; no template exists in
// classifier.yaml.
func injectStrategyOps(
	pairs []resolvedPair,
	cls *classifier.Classifier,
	bkt *bucket,
) ([]*planpb.Op, error) {
	sorted := make([]resolvedPair, len(pairs))
	copy(sorted, pairs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Finding.GetId() < sorted[j].Finding.GetId()
	})

	var out []*planpb.Op
	for _, p := range sorted {
		if p.Resolution.GetStrategy() == planpb.Strategy_CUSTOM_MIGRATION {
			continue
		}
		switch p.Finding.GetAxis() {
		case "carrier_change":
			ops, err := injectCarrierChange(p, cls, bkt)
			if err != nil {
				return nil, fmt.Errorf("finding %s: %w", p.Finding.GetId(), err)
			}
			out = append(out, ops...)
		default:
			// Other decision-required axes today only accept
			// CUSTOM_MIGRATION. If user resolved them to a non-custom
			// strategy, we have no template to synthesise SQL from —
			// fall through silently; buildManifest still records the
			// AppliedResolution so the audit trail survives.
			continue
		}
	}
	return out, nil
}

// injectCarrierChange synthesises the Op(s) for a resolved
// carrier-change Finding. Strategy dispatch:
//
//   - SAFE / LOSSLESS_USING / NEEDS_CONFIRM → single AlterColumn
//     with a TypeChange FactChange. USING clause rendered from
//     the classifier's forward cell template; reverse rendered
//     from the symmetric cell or a default `col::<from_type>`.
//   - DROP_AND_CREATE → DropColumn + AddColumn pair in FK-safe
//     order (drop first, add second).
func injectCarrierChange(
	p resolvedPair,
	cls *classifier.Classifier,
	bkt *bucket,
) ([]*planpb.Op, error) {
	ref := p.Finding.GetColumn()
	prevTable, prevCol := findColumnByRef(bkt.prev, ref)
	_, currCol := findColumnByRef(bkt.curr, ref)
	if prevCol == nil || currCol == nil {
		return nil, fmt.Errorf("carrier_change: prev/curr column not found via %s/#%d",
			ref.GetTableFqn(), ref.GetFieldNumber())
	}

	ctx := &planpb.TableCtx{
		TableName:     prevTable.GetName(),
		MessageFqn:    prevTable.GetMessageFqn(),
		NamespaceMode: prevTable.GetNamespaceMode(),
		Namespace:     prevTable.GetNamespace(),
	}

	strategy := p.Resolution.GetStrategy()
	switch strategy {
	case planpb.Strategy_DROP_AND_CREATE:
		return []*planpb.Op{
			{Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{
				Ctx: ctx, Column: prevCol,
			}}},
			{Variant: &planpb.Op_AddColumn{AddColumn: &planpb.AddColumn{
				Ctx: ctx, Column: currCol,
			}}},
		}, nil

	case planpb.Strategy_SAFE, planpb.Strategy_LOSSLESS_USING, planpb.Strategy_NEEDS_CONFIRM:
		usingUp, usingDown := renderCarrierUsing(cls, prevCol, currCol, prevTable)
		return []*planpb.Op{{
			Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
				Ctx:        ctx,
				ColumnName: currCol.GetName(),
				Column:     currCol,
				PrevColumn: prevCol,
				Changes: []*planpb.FactChange{{
					Variant: &planpb.FactChange_TypeChange{TypeChange: &planpb.TypeChange{
						FromColumn: prevCol,
						ToColumn:   currCol,
						UsingUp:    usingUp,
						UsingDown:  usingDown,
						Strategy:   strategy,
					}},
				}},
			}},
		}}, nil
	}

	return nil, fmt.Errorf("carrier_change: unhandled strategy %s", strategy)
}

// renderCarrierUsing renders the USING expressions for forward +
// reverse carrier transitions. Forward uses the classifier's
// from→to cell template; reverse uses the to→from cell template
// if its strategy is non-CUSTOM, else falls back to a plain
// "<col>::<type>" cast.
//
// tmpl context keys: {Col, Table} match the classifier docs.
// Template errors fall back to an empty USING — caller's emit
// omits the clause rather than serialising a broken SQL.
func renderCarrierUsing(
	cls *classifier.Classifier,
	prev, curr *irpb.Column,
	table *irpb.Table,
) (string, string) {
	forward := cls.Carrier(prev.GetCarrier(), curr.GetCarrier())
	reverse := cls.Carrier(curr.GetCarrier(), prev.GetCarrier())

	data := struct{ Col, Table string }{
		Col:   curr.GetName(),
		Table: table.GetName(),
	}

	usingUp := renderUsingTemplate(forward.Using, data)
	// Reverse direction: if the symmetric cell is author-SQL-
	// required (CUSTOM_MIGRATION / no template), we can't
	// auto-synthesise a reliable USING for the down. Leave empty
	// and the emitter drops the USING clause — PG may accept the
	// implicit cast or refuse at apply time (which is the correct
	// signal to the operator that rollback needs a custom path).
	usingDown := ""
	if reverse.Strategy != planpb.Strategy_CUSTOM_MIGRATION && reverse.Using != "" {
		data.Col = prev.GetName()
		usingDown = renderUsingTemplate(reverse.Using, data)
	}
	return usingUp, usingDown
}

func renderUsingTemplate(tmpl string, data any) string {
	if tmpl == "" {
		return ""
	}
	t, err := template.New("using").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return ""
	}
	return buf.String()
}

// findColumnByRef locates the (table, column) pair a ColumnRef
// points at within a Schema. Returns (nil, nil) when the ref
// doesn't resolve — callers treat that as a hard error because
// engine produced the ref from the same Schema it's now re-
// reading.
func findColumnByRef(schema *irpb.Schema, ref *planpb.ColumnRef) (*irpb.Table, *irpb.Column) {
	if schema == nil || ref == nil {
		return nil, nil
	}
	for _, t := range schema.GetTables() {
		if t.GetMessageFqn() != ref.GetTableFqn() {
			continue
		}
		for _, c := range t.GetColumns() {
			if c.GetFieldNumber() == ref.GetFieldNumber() {
				return t, c
			}
		}
	}
	return nil, nil
}
