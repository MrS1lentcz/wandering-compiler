package postgres

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// renderCheck returns one table-body line of the form
//
//	CONSTRAINT <table>_<col>_<suffix> CHECK (<expr>)
//
// or "" if the check has no SQL surface (e.g. empty RangeCheck). suffix is
// fixed per variant for stable naming across re-runs.
func renderCheck(table string, col *irpb.Column, ck *irpb.Check) (string, error) {
	name, expr, err := renderCheckBody(col, ck)
	if err != nil {
		return "", err
	}
	if expr == "" {
		return "", nil
	}
	return fmt.Sprintf("CONSTRAINT %s_%s_%s CHECK (%s)", table, col.GetName(), name, expr), nil
}

// renderCheckBody returns (suffix, expr, err). suffix drives the constraint
// name; expr is the raw SQL boolean inside CHECK (...).
func renderCheckBody(col *irpb.Column, ck *irpb.Check) (string, string, error) {
	sqlCol := col.GetName()
	switch v := ck.GetVariant().(type) {
	case *irpb.Check_Length:
		return "len", renderLength(sqlCol, v.Length), nil
	case *irpb.Check_Blank:
		return "blank", fmt.Sprintf("%s <> ''", sqlCol), nil
	case *irpb.Check_Range:
		return "range", renderRange(sqlCol, v.Range), nil
	case *irpb.Check_Regex:
		return "format", fmt.Sprintf("%s ~ %s", sqlCol, sqlStringLiteral(v.Regex.GetPattern())), nil
	case *irpb.Check_Choices:
		return "choices", renderChoices(sqlCol, v.Choices), nil
	}
	return "", "", fmt.Errorf("unknown Check variant %T on column %q", ck.GetVariant(), col.GetProtoName())
}

func renderLength(sqlCol string, lc *irpb.LengthCheck) string {
	parts := []string{}
	if lc.Min != nil {
		parts = append(parts, fmt.Sprintf("char_length(%s) >= %d", sqlCol, *lc.Min))
	}
	if lc.Max != nil {
		parts = append(parts, fmt.Sprintf("char_length(%s) <= %d", sqlCol, *lc.Max))
	}
	return strings.Join(parts, " AND ")
}

func renderRange(sqlCol string, rc *irpb.RangeCheck) string {
	// BETWEEN when we have a symmetric inclusive pair; otherwise join
	// individual bounds with AND. This matches the reference SQL in
	// iteration-1-models.md ("discount_rate CHECK (discount_rate BETWEEN 0 AND 1)").
	if rc.Gte != nil && rc.Lte != nil && rc.Gt == nil && rc.Lt == nil {
		return fmt.Sprintf("%s BETWEEN %s AND %s", sqlCol, fmtDouble(*rc.Gte), fmtDouble(*rc.Lte))
	}
	parts := []string{}
	if rc.Gt != nil {
		parts = append(parts, fmt.Sprintf("%s > %s", sqlCol, fmtDouble(*rc.Gt)))
	}
	if rc.Gte != nil {
		parts = append(parts, fmt.Sprintf("%s >= %s", sqlCol, fmtDouble(*rc.Gte)))
	}
	if rc.Lt != nil {
		parts = append(parts, fmt.Sprintf("%s < %s", sqlCol, fmtDouble(*rc.Lt)))
	}
	if rc.Lte != nil {
		parts = append(parts, fmt.Sprintf("%s <= %s", sqlCol, fmtDouble(*rc.Lte)))
	}
	return strings.Join(parts, " AND ")
}

func renderChoices(sqlCol string, cc *irpb.ChoicesCheck) string {
	// ChoicesCheck splits into two exclusive paths — names (string-carrier
	// `choices:` option, CHECK IN ('A','B')) and numbers (int-carrier
	// SEM_ENUM, CHECK IN (1,2)). The IR populates exactly one list per
	// instance; the emitter renders whichever is set.
	if len(cc.GetNumbers()) > 0 {
		parts := make([]string, 0, len(cc.GetNumbers()))
		for _, n := range cc.GetNumbers() {
			parts = append(parts, strconv.FormatInt(n, 10))
		}
		return fmt.Sprintf("%s IN (%s)", sqlCol, strings.Join(parts, ", "))
	}
	quoted := make([]string, 0, len(cc.GetValues()))
	for _, v := range cc.GetValues() {
		quoted = append(quoted, sqlStringLiteral(v))
	}
	return fmt.Sprintf("%s IN (%s)", sqlCol, strings.Join(quoted, ", "))
}

// fmtDouble renders a float64 the way PG accepts it. Integer-valued
// doubles use 'f' with zero precision (avoids "1e+06" for values like
// 1_000_000 in range CHECKs); fractional values keep 'g' with -1
// precision for the shortest round-trippable form.
func fmtDouble(v float64) string {
	if !math.IsNaN(v) && !math.IsInf(v, 0) && math.Trunc(v) == v && math.Abs(v) < 1e18 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
