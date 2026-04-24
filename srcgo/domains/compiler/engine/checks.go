package engine

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// collectChecks walks every AlterColumn op in a migration plan and,
// for each FactChange whose classifier strategy is NEEDS_CONFIRM
// with a non-empty CheckSQL template, renders a NamedSQL the deploy
// client can run pre-apply.
//
// Not every FactChange axis produces a check.sql — SAFE axes (comment,
// default add, max_len widen, …) have no check; NEEDS_CONFIRM axes
// without a template (e.g. some DROP EXPRESSION cases) also skip.
// The result is a deterministic slice; empty when no checks applied.
func collectChecks(migPlan *planpb.MigrationPlan, cls *classifier.Classifier) []*planpb.NamedSQL {
	if migPlan == nil || cls == nil {
		return nil
	}
	var out []*planpb.NamedSQL
	for _, op := range migPlan.GetOps() {
		alter := op.GetAlterColumn()
		if alter == nil {
			continue
		}
		qualTable := qualifiedTableName(alter.GetCtx())
		colName := alter.GetColumnName()
		for _, fc := range alter.GetChanges() {
			cell, axis, ok := classifyFactChange(cls, alter, fc)
			if !ok {
				continue
			}
			if cell.Strategy != planpb.Strategy_NEEDS_CONFIRM {
				continue
			}
			if cell.CheckSQL == "" {
				continue
			}
			body, err := renderTemplate(cell.CheckSQL, templateData(qualTable, colName, alter, fc))
			if err != nil {
				// Template error is a developer bug, not a user-
				// facing issue; fall back to the unrendered template
				// so the deploy client at least sees something to
				// inspect.
				body = cell.CheckSQL
			}
			out = append(out, &planpb.NamedSQL{
				Name:      axis + "_" + colName,
				Sql:       body,
				Rationale: cell.Rationale,
			})
		}
	}
	return out
}

// classifyFactChange routes a FactChange variant to the right
// classifier axis + case and returns the Cell + axis ID. Returns
// ok=false for axes we don't classify at this layer (e.g.
// PgOptionsChange is manifest-only SAFE).
func classifyFactChange(
	cls *classifier.Classifier,
	alter *planpb.AlterColumn,
	fc *planpb.FactChange,
) (classifier.Cell, string, bool) {
	switch v := fc.GetVariant().(type) {
	case *planpb.FactChange_Nullable:
		caseID := "relax"
		if v.Nullable.GetTo() {
			// nullable=true means "the column IS nullable"; going
			// false → true is a relax. true → false is tighten.
			// (iter-1 semantics: Nullable bool on Column = allows NULL.)
			caseID = "relax"
		} else {
			caseID = "tighten"
		}
		return cls.Constraint("nullable", caseID), "nullable", true
	case *planpb.FactChange_MaxLen:
		from, to := v.MaxLen.GetFrom(), v.MaxLen.GetTo()
		var caseID string
		switch {
		case from == 0 && to != 0:
			caseID = "add_bound"
		case from != 0 && to == 0:
			caseID = "remove_bound"
		case to > from:
			caseID = "widen"
		default:
			caseID = "narrow"
		}
		return cls.Constraint("max_len", caseID), "max_len", true
	case *planpb.FactChange_NumericPrecision:
		caseID := classifyNumericCase(v.NumericPrecision)
		return cls.Constraint("numeric", caseID), "numeric", true
	case *planpb.FactChange_DbType:
		curr := alter.GetColumn()
		cell := cls.DbType(curr.GetCarrier(), v.DbType.GetFrom(), v.DbType.GetTo())
		return cell, "dbtype", true
	case *planpb.FactChange_TypeChange:
		// D33 — carrier change synthesised by engine.injectStrategyOps.
		// Re-classifies against the carrier matrix so NEEDS_CONFIRM
		// cells emit their check.sql alongside the ALTER.
		from := v.TypeChange.GetFromColumn().GetCarrier()
		to := v.TypeChange.GetToColumn().GetCarrier()
		return cls.Carrier(from, to), "carrier_change", true
	case *planpb.FactChange_GeneratedExpr:
		from, to := v.GeneratedExpr.GetFrom(), v.GeneratedExpr.GetTo()
		var caseID string
		switch {
		case from == "" && to != "":
			caseID = "add"
		case from != "" && to == "":
			caseID = "drop"
		default:
			caseID = "change"
		}
		return cls.Constraint("generated_expr", caseID), "generated_expr", true
	case *planpb.FactChange_Comment:
		return cls.Constraint("comment", "any"), "comment", true
	case *planpb.FactChange_AllowedExtensions:
		caseID := classifyAllowedExtensionsCase(v.AllowedExtensions.GetFrom(), v.AllowedExtensions.GetTo())
		return cls.Constraint("allowed_extensions", caseID), "allowed_extensions", true
	case *planpb.FactChange_DefaultValue:
		return cls.Constraint("default", defaultCaseFor(v.DefaultValue.GetFrom(), v.DefaultValue.GetTo())), "default", true
	}
	// Unknown / manifest-only variants (PgOptions, EnumValues — the
	// latter handled at diff.go since remove produces a Finding, not
	// a FactChange auto-applied).
	return classifier.Cell{}, "", false
}

// classifyNumericCase decides which numeric axis case applies.
func classifyNumericCase(c *planpb.NumericPrecisionChange) string {
	fromP, toP := c.GetFromPrecision(), c.GetToPrecision()
	fromS, fromSOk := scaleOf(c.FromScale)
	toS, toSOk := scaleOf(c.ToScale)

	switch {
	case fromP == 0 && toP != 0:
		return "add_bound"
	case fromP != 0 && toP == 0:
		return "remove_bound"
	case toP >= fromP && (!toSOk || !fromSOk || toS >= fromS):
		return "widen_both"
	case toP < fromP:
		return "precision_narrow"
	default:
		return "scale_narrow"
	}
}

func scaleOf(p *int32) (int32, bool) {
	if p == nil {
		return 0, false
	}
	return *p, true
}

// classifyAllowedExtensionsCase compares from/to lists and picks the
// matching constraint.yaml case.
func classifyAllowedExtensionsCase(from, to []string) string {
	if contains(to, "*") {
		return "to_wildcard"
	}
	if contains(from, "*") {
		return "from_wildcard"
	}
	toSet := stringSet(to)
	fromSet := stringSet(from)
	fromSuperset := true
	toSuperset := true
	for _, f := range from {
		if _, ok := toSet[f]; !ok {
			toSuperset = false
		}
	}
	for _, t := range to {
		if _, ok := fromSet[t]; !ok {
			fromSuperset = false
		}
	}
	switch {
	case fromSuperset && toSuperset:
		return "widen" // no-op really, but classify as widen = SAFE
	case toSuperset:
		return "widen"
	case fromSuperset:
		return "narrow"
	default:
		return "disjoint"
	}
}

// defaultCaseFor picks a "default" axis case. Coarse but covers the
// common paths: no → yes = add, yes → no = drop, else change.
// Identity toggles are out of scope here; they ride on the IDENTITY
// axis detection (not wired into FactChange today).
func defaultCaseFor(from, to *irpb.Default) string {
	hasFrom := from != nil && !isEmptyDefault(from)
	hasTo := to != nil && !isEmptyDefault(to)
	switch {
	case !hasFrom && hasTo:
		return "add"
	case hasFrom && !hasTo:
		return "drop"
	default:
		return "change_literal"
	}
}

func isEmptyDefault(d *irpb.Default) bool {
	return d == nil || d.GetVariant() == nil
}

// templateData bundles the context values referenced by check_sql
// templates (Go text/template style). Per-axis fields are populated
// from the FactChange; unset fields render as zero values.
func templateData(table, col string, alter *planpb.AlterColumn, fc *planpb.FactChange) any {
	data := struct {
		Table, Col                            string
		NewMaxLen, NewPrecision, NewScale     int32
		IntMin, IntMax                        int64
	}{
		Table: table,
		Col:   col,
	}
	switch v := fc.GetVariant().(type) {
	case *planpb.FactChange_MaxLen:
		data.NewMaxLen = v.MaxLen.GetTo()
	case *planpb.FactChange_NumericPrecision:
		data.NewPrecision = v.NumericPrecision.GetToPrecision()
		if s, ok := scaleOf(v.NumericPrecision.ToScale); ok {
			data.NewScale = s
		}
	}
	return data
}

// renderTemplate runs a Go text/template over the data bag and
// returns the rendered string. Strict mode — missing keys error
// rather than silently blank.
func renderTemplate(tmpl string, data any) (string, error) {
	t, err := template.New("check").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}
	return buf.String(), nil
}

// qualifiedTableName assembles the SQL-qualified table name from a
// TableCtx. Matches emit's rendering so check.sql references the
// same table identifier the ALTER does.
func qualifiedTableName(ctx *planpb.TableCtx) string {
	if ctx == nil {
		return ""
	}
	if ctx.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA && ctx.GetNamespace() != "" {
		return ctx.GetNamespace() + "." + ctx.GetTableName()
	}
	return ctx.GetTableName()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func stringSet(ss []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		out[s] = struct{}{}
	}
	return out
}

