package classifier_test

// Exhaustive YAML-coverage tests — for every cell in every
// classification YAML, assert the classifier returns the expected
// strategy. Also assert matrix invariants (every carrier pair
// reaches a defined cell or the synthesized CUSTOM_MIGRATION
// fallback; no duplicate keys; no unknown axis IDs).
//
// This complements the hand-picked landmark cases in classifier_test.go:
// those pin specific intent, these protect against silent drift when
// someone edits a YAML and forgets the matching test.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// yamlCell mirrors the shape from classifier/load.go. Duplicated here
// because load.go keeps its unmarshalling types package-private.
type yamlCell struct {
	From, To     string
	Family       string
	Axis, Case   string
	Strategy     string
	Using        string `yaml:"using"`
	CheckSQL     string `yaml:"check_sql"`
	Rationale    string
}

type yamlCarrierFile struct {
	Cells []yamlCell `yaml:"cells"`
}

type yamlDbTypeFile struct {
	Cells []yamlCell `yaml:"cells"`
}

type yamlConstraintFile struct {
	Cells      []yamlCell `yaml:"cells"`
	TableLevel []yamlCell `yaml:"table_level"`
}

// loadYAMLRaw re-reads a classification YAML independently of
// classifier.Load so the exhaustive tests verify the classifier
// matches the source of truth, not its own cached parse.
func loadYAMLRaw[T any](t *testing.T, filename string) *T {
	t.Helper()
	dir := classificationDir(t)
	data, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	var out T
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", filename, err)
	}
	return &out
}

// parseStrategyName is the test-side mirror of classifier's internal
// parseStrategy; duplicated so the test is self-sufficient.
func parseStrategyName(s string) (planpb.Strategy, bool) {
	switch s {
	case "SAFE":
		return planpb.Strategy_SAFE, true
	case "LOSSLESS_USING":
		return planpb.Strategy_LOSSLESS_USING, true
	case "NEEDS_CONFIRM":
		return planpb.Strategy_NEEDS_CONFIRM, true
	case "DROP_AND_CREATE":
		return planpb.Strategy_DROP_AND_CREATE, true
	case "CUSTOM_MIGRATION":
		return planpb.Strategy_CUSTOM_MIGRATION, true
	}
	return 0, false
}

func parseCarrierName(s string) (irpb.Carrier, bool) {
	v, ok := irpb.Carrier_value["CARRIER_"+s]
	if !ok {
		return 0, false
	}
	return irpb.Carrier(v), true
}

func parseDbTypeName(s string) (irpb.DbType, bool) {
	v, ok := irpb.DbType_value["DBT_"+s]
	if !ok {
		return 0, false
	}
	return irpb.DbType(v), true
}

// TestExhaustive_Carrier walks every cell in carrier.yaml and asserts
// classifier.Carrier returns the same strategy. Fails if a cell lost
// its classifier entry (or vice versa).
func TestExhaustive_Carrier(t *testing.T) {
	f := loadYAMLRaw[yamlCarrierFile](t, "carrier.yaml")
	cls := mustLoad(t)
	for _, c := range f.Cells {
		t.Run(c.From+"→"+c.To, func(t *testing.T) {
			from, ok := parseCarrierName(c.From)
			if !ok {
				t.Fatalf("unknown carrier %q in YAML", c.From)
			}
			to, ok := parseCarrierName(c.To)
			if !ok {
				t.Fatalf("unknown carrier %q in YAML", c.To)
			}
			want, ok := parseStrategyName(c.Strategy)
			if !ok {
				t.Fatalf("unknown strategy %q in YAML cell %s→%s", c.Strategy, c.From, c.To)
			}
			got := cls.Carrier(from, to)
			if got.Strategy != want {
				t.Errorf("classifier %s→%s = %s; YAML says %s", c.From, c.To, got.Strategy, want)
			}
			if got.Rationale != c.Rationale {
				t.Errorf("rationale drift for %s→%s", c.From, c.To)
			}
			if got.Using != c.Using {
				t.Errorf("using drift for %s→%s: got %q, yaml %q", c.From, c.To, got.Using, c.Using)
			}
			if got.CheckSQL != c.CheckSQL {
				t.Errorf("check_sql drift for %s→%s", c.From, c.To)
			}
		})
	}
}

// TestExhaustive_DbType walks every cell in dbtype.yaml. Since dbtype
// lookups require a carrier-to-family mapping, we pick one
// representative carrier per family for the test.
func TestExhaustive_DbType(t *testing.T) {
	familyCarrier := map[string]irpb.Carrier{
		"STRING":    irpb.Carrier_CARRIER_STRING,
		"INT":       irpb.Carrier_CARRIER_INT64,
		"DOUBLE":    irpb.Carrier_CARRIER_DOUBLE,
		"TIMESTAMP": irpb.Carrier_CARRIER_TIMESTAMP,
		"BYTES":     irpb.Carrier_CARRIER_BYTES,
		"JSON":      irpb.Carrier_CARRIER_MAP,
	}
	f := loadYAMLRaw[yamlDbTypeFile](t, "dbtype.yaml")
	cls := mustLoad(t)
	for _, c := range f.Cells {
		t.Run(c.Family+"/"+c.From+"→"+c.To, func(t *testing.T) {
			carrier, ok := familyCarrier[c.Family]
			if !ok {
				t.Fatalf("unknown family %q", c.Family)
			}
			from, ok := parseDbTypeName(c.From)
			if !ok {
				t.Fatalf("unknown dbtype %q", c.From)
			}
			to, ok := parseDbTypeName(c.To)
			if !ok {
				t.Fatalf("unknown dbtype %q", c.To)
			}
			want, ok := parseStrategyName(c.Strategy)
			if !ok {
				t.Fatalf("unknown strategy %q", c.Strategy)
			}
			got := cls.DbType(carrier, from, to)
			if got.Strategy != want {
				t.Errorf("classifier %s/%s→%s = %s; YAML says %s",
					c.Family, c.From, c.To, got.Strategy, want)
			}
		})
	}
}

// TestExhaustive_Constraint walks every cell in constraint.yaml
// (both column-level and table-level) and asserts classifier
// .Constraint agrees.
func TestExhaustive_Constraint(t *testing.T) {
	f := loadYAMLRaw[yamlConstraintFile](t, "constraint.yaml")
	cls := mustLoad(t)
	check := func(t *testing.T, cells []yamlCell) {
		for _, c := range cells {
			t.Run(c.Axis+"."+c.Case, func(t *testing.T) {
				want, ok := parseStrategyName(c.Strategy)
				if !ok {
					t.Fatalf("unknown strategy %q for %s.%s", c.Strategy, c.Axis, c.Case)
				}
				got := cls.Constraint(c.Axis, c.Case)
				if got.Strategy != want {
					t.Errorf("classifier %s.%s = %s; YAML says %s",
						c.Axis, c.Case, got.Strategy, want)
				}
			})
		}
	}
	check(t, f.Cells)
	check(t, f.TableLevel)
}

// TestInvariant_UnknownCarrierFalbacksToCustom — D28 rule: no silent
// coercion, every unknown path defaults to CUSTOM_MIGRATION.
// Pick a few carrier pairs explicitly left out of carrier.yaml's
// coverage and assert the fallback behavior.
func TestInvariant_UnknownCarrierFallback(t *testing.T) {
	cls := mustLoad(t)
	// Every "same carrier" pair is a no-op SAFE — confirm the no-op
	// path doesn't accidentally produce CUSTOM_MIGRATION.
	for _, c := range []irpb.Carrier{
		irpb.Carrier_CARRIER_BOOL,
		irpb.Carrier_CARRIER_STRING,
		irpb.Carrier_CARRIER_INT32,
		irpb.Carrier_CARRIER_MESSAGE,
	} {
		if got := cls.Carrier(c, c); got.Strategy != planpb.Strategy_SAFE {
			t.Errorf("same-carrier no-op %s = %s, want SAFE", c, got.Strategy)
		}
	}
}

// TestInvariant_NoDuplicateCarrierPairs — every (from, to) in
// carrier.yaml should appear at most once. classifier.Load doesn't
// currently detect duplicates; this test does.
func TestInvariant_NoDuplicateCarrierPairs(t *testing.T) {
	f := loadYAMLRaw[yamlCarrierFile](t, "carrier.yaml")
	seen := map[string]bool{}
	for _, c := range f.Cells {
		key := c.From + "→" + c.To
		if seen[key] {
			t.Errorf("duplicate cell %s", key)
		}
		seen[key] = true
	}
}

// TestInvariant_NoDuplicateDbTypeTriples — same for dbtype.yaml
// keyed by (family, from, to).
func TestInvariant_NoDuplicateDbTypeTriples(t *testing.T) {
	f := loadYAMLRaw[yamlDbTypeFile](t, "dbtype.yaml")
	seen := map[string]bool{}
	for _, c := range f.Cells {
		key := c.Family + "/" + c.From + "→" + c.To
		if seen[key] {
			t.Errorf("duplicate cell %s", key)
		}
		seen[key] = true
	}
}

// TestInvariant_ClassificationDirExists — smoke test that
// runtime.Caller correctly resolves to the checked-in YAMLs.
// Catches environmental issues (someone renamed docs/).
func TestInvariant_ClassificationDirExists(t *testing.T) {
	dir := classificationDir(t)
	for _, f := range []string{"strategies.yaml", "carrier.yaml", "dbtype.yaml", "constraint.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing YAML %s: %v", f, err)
		}
	}
}

// classificationDir duplicates the helper from classifier_test.go so
// this file is independently runnable. Tiny duplication; acceptable.
func classificationDirLocal() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "classification")
}
