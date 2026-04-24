package engine

// Internal unit tests for the checks.go classifier dispatch helpers.
// These functions sit between the public engine.Plan API and the D28
// classifier — they decide which axis "case" string a given FactChange
// maps to, which then picks up a check.sql template from
// classifier.yaml. Today's fixture corpus doesn't drive NEEDS_CONFIRM
// cells for numeric / default / allowed_extensions axes (everything
// resolves SAFE/USING/REFUSE), so these helpers stayed at 0% coverage
// until this suite landed.

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func intp(v int32) *int32 { return &v }

// TestClassifyNumericCase pins every case-string classifyNumericCase
// can produce. Case coverage matches constraint.yaml A3 cells (numeric
// widen / narrow / add_bound / remove_bound).
func TestClassifyNumericCase(t *testing.T) {
	cases := []struct {
		name string
		ch   *planpb.NumericPrecisionChange
		want string
	}{
		{"0 → p = add_bound",
			&planpb.NumericPrecisionChange{FromPrecision: 0, ToPrecision: 10}, "add_bound"},
		{"p → 0 = remove_bound",
			&planpb.NumericPrecisionChange{FromPrecision: 10, ToPrecision: 0}, "remove_bound"},
		{"widen precision + scale",
			&planpb.NumericPrecisionChange{FromPrecision: 10, FromScale: intp(2), ToPrecision: 19, ToScale: intp(4)},
			"widen_both"},
		{"precision narrow",
			&planpb.NumericPrecisionChange{FromPrecision: 19, ToPrecision: 10}, "precision_narrow"},
		{"scale narrow (same precision)",
			&planpb.NumericPrecisionChange{FromPrecision: 10, FromScale: intp(4), ToPrecision: 10, ToScale: intp(2)},
			"scale_narrow"},
		{"no scale either side = widen_both (no op really)",
			&planpb.NumericPrecisionChange{FromPrecision: 10, ToPrecision: 10}, "widen_both"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyNumericCase(c.ch); got != c.want {
				t.Errorf("classifyNumericCase → %q, want %q", got, c.want)
			}
		})
	}
}

// TestScaleOf covers the nil + non-nil paths of the two-line helper.
func TestScaleOf(t *testing.T) {
	if _, ok := scaleOf(nil); ok {
		t.Error("scaleOf(nil) should return ok=false")
	}
	got, ok := scaleOf(intp(7))
	if !ok || got != 7 {
		t.Errorf("scaleOf(&7) = (%d, %v), want (7, true)", got, ok)
	}
}

// TestClassifyAllowedExtensionsCase covers the four outcomes per
// constraint.yaml A7 allowed_extensions cells (widen / narrow /
// disjoint / wildcards).
func TestClassifyAllowedExtensionsCase(t *testing.T) {
	cases := []struct {
		name       string
		from, to   []string
		want       string
	}{
		{"to wildcard dominates", []string{"jpg"}, []string{"*"}, "to_wildcard"},
		{"from wildcard → specific list = narrow (from_wildcard)",
			[]string{"*"}, []string{"jpg"}, "from_wildcard"},
		{"widen — add extension", []string{"jpg"}, []string{"jpg", "png"}, "widen"},
		{"narrow — remove extension", []string{"jpg", "png"}, []string{"jpg"}, "narrow"},
		{"disjoint — different sets",
			[]string{"jpg"}, []string{"png"}, "disjoint"},
		{"equal sets classify as widen (no-op)",
			[]string{"jpg", "png"}, []string{"jpg", "png"}, "widen"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyAllowedExtensionsCase(c.from, c.to); got != c.want {
				t.Errorf("classifyAllowedExtensionsCase(%v → %v) = %q, want %q",
					c.from, c.to, got, c.want)
			}
		})
	}
}

// TestDefaultCaseFor covers the three outcomes (add / drop /
// change_literal) of the default-axis classifier.
func TestDefaultCaseFor(t *testing.T) {
	litA := &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "a"}}
	litB := &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "b"}}
	empty := &irpb.Default{} // Variant unset → isEmptyDefault true

	cases := []struct {
		name     string
		from, to *irpb.Default
		want     string
	}{
		{"nil → literal = add", nil, litA, "add"},
		{"empty → literal = add", empty, litA, "add"},
		{"literal → nil = drop", litA, nil, "drop"},
		{"literal → empty = drop", litA, empty, "drop"},
		{"literal A → literal B = change", litA, litB, "change_literal"},
		{"nil → nil = change (coarse classifier)", nil, nil, "change_literal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := defaultCaseFor(c.from, c.to); got != c.want {
				t.Errorf("defaultCaseFor = %q, want %q", got, c.want)
			}
		})
	}
}

// TestIsEmptyDefault pins the two-line helper: nil + empty-variant.
func TestIsEmptyDefault(t *testing.T) {
	if !isEmptyDefault(nil) {
		t.Error("isEmptyDefault(nil) should be true")
	}
	if !isEmptyDefault(&irpb.Default{}) {
		t.Error("isEmptyDefault(&Default{}) should be true (variant unset)")
	}
	filled := &irpb.Default{Variant: &irpb.Default_LiteralInt{LiteralInt: 0}}
	if isEmptyDefault(filled) {
		t.Error("isEmptyDefault(filled) should be false")
	}
}

// TestContains_StringSet covers the two tiny slice helpers — they're
// trivial, but the 0% coverage was dragging the engine number down.
func TestContains_StringSet(t *testing.T) {
	if !contains([]string{"a", "b"}, "b") {
		t.Error(`contains(["a","b"], "b") should be true`)
	}
	if contains([]string{"a"}, "b") {
		t.Error(`contains(["a"], "b") should be false`)
	}
	set := stringSet([]string{"a", "b", "a"})
	if len(set) != 2 {
		t.Errorf("stringSet dedup failed: %v", set)
	}
	if _, ok := set["a"]; !ok {
		t.Error(`stringSet missing "a"`)
	}
}

// TestQualifiedTableName covers the SCHEMA / NONE split + the nil-ctx
// defensive branch.
func TestQualifiedTableName(t *testing.T) {
	if got := qualifiedTableName(nil); got != "" {
		t.Errorf("nil ctx should give empty, got %q", got)
	}
	bare := qualifiedTableName(&planpb.TableCtx{TableName: "users"})
	if bare != "users" {
		t.Errorf("bare ctx: got %q, want users", bare)
	}
	qual := qualifiedTableName(&planpb.TableCtx{
		TableName:     "users",
		NamespaceMode: irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA,
		Namespace:     "reporting",
	})
	if qual != "reporting.users" {
		t.Errorf("schema ctx: got %q, want reporting.users", qual)
	}
	// Schema mode + empty namespace falls through to bare name.
	schemaEmpty := qualifiedTableName(&planpb.TableCtx{
		TableName:     "users",
		NamespaceMode: irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA,
	})
	if schemaEmpty != "users" {
		t.Errorf("schema+empty ns: got %q, want users", schemaEmpty)
	}
}
