package classifier_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// classificationDir returns the repo-root path to docs/classification
// regardless of where the test binary is invoked from. runtime.Caller
// gives us the test file's path; we walk up to the project root and
// append docs/classification.
func classificationDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file = srcgo/domains/compiler/classifier/classifier_test.go
	// repo root = up 4 dirs.
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "classification")
}

func TestLoadRealYAMLs(t *testing.T) {
	c, err := classifier.Load(classificationDir(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c == nil {
		t.Fatal("Load returned nil classifier")
	}
}

func TestLoad_MissingDir(t *testing.T) {
	_, err := classifier.Load("/nonexistent/classification/dir")
	if err == nil {
		t.Fatal("Load on missing dir should error")
	}
}

// TestCarrier_CoreCells pins a handful of carrier transitions to the
// expected strategy. Not exhaustive — Phase 5 generates the full
// per-YAML-cell test matrix. This sanity-checks the loader wiring and
// a few hand-picked landmark cells.
func TestCarrier_CoreCells(t *testing.T) {
	c, err := classifier.Load(classificationDir(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name     string
		from, to irpb.Carrier
		want     planpb.Strategy
		wantCheck bool // whether check_sql is non-empty
	}{
		{"INT32→INT64 widen", irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64, planpb.Strategy_SAFE, false},
		{"BOOL→STRING canonical", irpb.Carrier_CARRIER_BOOL, irpb.Carrier_CARRIER_STRING, planpb.Strategy_LOSSLESS_USING, false},
		{"STRING→BOOL strict", irpb.Carrier_CARRIER_STRING, irpb.Carrier_CARRIER_BOOL, planpb.Strategy_NEEDS_CONFIRM, true},
		{"STRING→INT32 parse-risk", irpb.Carrier_CARRIER_STRING, irpb.Carrier_CARRIER_INT32, planpb.Strategy_NEEDS_CONFIRM, true},
		{"INT32→TIMESTAMP unit-ambiguous", irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_TIMESTAMP, planpb.Strategy_CUSTOM_MIGRATION, false},
		{"MAP→BOOL projection-author-owned", irpb.Carrier_CARRIER_MAP, irpb.Carrier_CARRIER_BOOL, planpb.Strategy_CUSTOM_MIGRATION, false},
		{"STRING→MAP wrap-author-owned", irpb.Carrier_CARRIER_STRING, irpb.Carrier_CARRIER_MAP, planpb.Strategy_CUSTOM_MIGRATION, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cell := c.Carrier(tc.from, tc.to)
			if cell.Strategy != tc.want {
				t.Errorf("Carrier(%s, %s) strategy = %s, want %s", tc.from, tc.to, cell.Strategy, tc.want)
			}
			if (cell.CheckSQL != "") != tc.wantCheck {
				t.Errorf("Carrier(%s, %s) check_sql non-empty = %v, want %v (got %q)",
					tc.from, tc.to, cell.CheckSQL != "", tc.wantCheck, cell.CheckSQL)
			}
			if cell.Rationale == "" {
				t.Errorf("Carrier(%s, %s) rationale is empty", tc.from, tc.to)
			}
		})
	}
}

func TestCarrier_NoOp(t *testing.T) {
	c := mustLoad(t)
	cell := c.Carrier(irpb.Carrier_CARRIER_STRING, irpb.Carrier_CARRIER_STRING)
	if cell.Strategy != planpb.Strategy_SAFE {
		t.Errorf("same-carrier no-op strategy = %s, want SAFE", cell.Strategy)
	}
}

// TestDbType_CoreCells — landmark cells across families.
func TestDbType_CoreCells(t *testing.T) {
	c := mustLoad(t)

	cases := []struct {
		name        string
		carrier     irpb.Carrier
		from, to    irpb.DbType
		want        planpb.Strategy
		wantCheck   bool
	}{
		{"TEXT→CITEXT same-data", irpb.Carrier_CARRIER_STRING, irpb.DbType_DBT_TEXT, irpb.DbType_DBT_CITEXT, planpb.Strategy_LOSSLESS_USING, false},
		{"INTEGER→BIGINT widen", irpb.Carrier_CARRIER_INT64, irpb.DbType_DBT_INTEGER, irpb.DbType_DBT_BIGINT, planpb.Strategy_SAFE, false},
		{"BIGINT→INTEGER overflow", irpb.Carrier_CARRIER_INT64, irpb.DbType_DBT_BIGINT, irpb.DbType_DBT_INTEGER, planpb.Strategy_NEEDS_CONFIRM, true},
		{"DOUBLE→NUMERIC NaN-guard", irpb.Carrier_CARRIER_DOUBLE, irpb.DbType_DBT_DOUBLE_PRECISION, irpb.DbType_DBT_NUMERIC, planpb.Strategy_NEEDS_CONFIRM, true},
		{"JSON→JSONB normalise", irpb.Carrier_CARRIER_MAP, irpb.DbType_DBT_JSON, irpb.DbType_DBT_JSONB, planpb.Strategy_LOSSLESS_USING, false},
		{"TEXT→UUID format-check", irpb.Carrier_CARRIER_STRING, irpb.DbType_DBT_TEXT, irpb.DbType_DBT_UUID, planpb.Strategy_NEEDS_CONFIRM, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cell := c.DbType(tc.carrier, tc.from, tc.to)
			if cell.Strategy != tc.want {
				t.Errorf("DbType(%s, %s, %s) strategy = %s, want %s",
					tc.carrier, tc.from, tc.to, cell.Strategy, tc.want)
			}
			if (cell.CheckSQL != "") != tc.wantCheck {
				t.Errorf("DbType(%s, %s, %s) check_sql non-empty = %v, want %v",
					tc.carrier, tc.from, tc.to, cell.CheckSQL != "", tc.wantCheck)
			}
		})
	}
}

func TestDbType_MissingFallback(t *testing.T) {
	c := mustLoad(t)
	// UUID → MACADDR inside STRING family has no explicit cell —
	// classifier synthesises CUSTOM_MIGRATION.
	cell := c.DbType(irpb.Carrier_CARRIER_STRING, irpb.DbType_DBT_UUID, irpb.DbType_DBT_MACADDR)
	if cell.Strategy != planpb.Strategy_CUSTOM_MIGRATION {
		t.Errorf("missing cell fallback = %s, want CUSTOM_MIGRATION", cell.Strategy)
	}
	if !strings.Contains(cell.Rationale, "No explicit classification rule") {
		t.Errorf("missing-cell rationale = %q, want to mention explicit-rule absence", cell.Rationale)
	}
}

// TestConstraint_CoreCells pins a sample of axis transitions.
func TestConstraint_CoreCells(t *testing.T) {
	c := mustLoad(t)

	cases := []struct {
		axis, caseID string
		want         planpb.Strategy
	}{
		{"nullable", "relax", planpb.Strategy_SAFE},
		{"nullable", "tighten", planpb.Strategy_NEEDS_CONFIRM},
		{"default", "add", planpb.Strategy_SAFE},
		{"default", "identity_add", planpb.Strategy_NEEDS_CONFIRM},
		{"max_len", "widen", planpb.Strategy_SAFE},
		{"max_len", "narrow", planpb.Strategy_NEEDS_CONFIRM},
		{"pk", "enable", planpb.Strategy_NEEDS_CONFIRM},
		{"pk", "disable", planpb.Strategy_NEEDS_CONFIRM},
		{"generated_expr", "add", planpb.Strategy_DROP_AND_CREATE},
		{"generated_expr", "drop", planpb.Strategy_NEEDS_CONFIRM},
		{"comment", "any", planpb.Strategy_SAFE},
		{"enum_values", "remove", planpb.Strategy_NEEDS_CONFIRM},
		{"enum_values", "fqn_change", planpb.Strategy_NEEDS_CONFIRM},
		{"pg_custom_type", "any", planpb.Strategy_CUSTOM_MIGRATION},
		{"fk_add", "any", planpb.Strategy_NEEDS_CONFIRM},
		{"check_add", "structured", planpb.Strategy_NEEDS_CONFIRM},
		{"check_add", "raw", planpb.Strategy_NEEDS_CONFIRM},
	}

	for _, tc := range cases {
		t.Run(tc.axis+"."+tc.caseID, func(t *testing.T) {
			cell := c.Constraint(tc.axis, tc.caseID)
			if cell.Strategy != tc.want {
				t.Errorf("Constraint(%s, %s) strategy = %s, want %s", tc.axis, tc.caseID, cell.Strategy, tc.want)
			}
		})
	}
}

func TestConstraint_MissingFallback(t *testing.T) {
	c := mustLoad(t)
	cell := c.Constraint("nonexistent_axis", "nonexistent_case")
	if cell.Strategy != planpb.Strategy_CUSTOM_MIGRATION {
		t.Errorf("missing constraint cell = %s, want CUSTOM_MIGRATION", cell.Strategy)
	}
}

// TestFold pins the strictness-fold rule. SAFE < USING < NEEDS_CONFIRM
// < DROP_AND_CREATE < CUSTOM_MIGRATION. Highest rank wins.
func TestFold(t *testing.T) {
	c := mustLoad(t)

	cases := []struct {
		name  string
		cells []classifier.Cell
		want  planpb.Strategy
	}{
		{"empty = SAFE", nil, planpb.Strategy_SAFE},
		{"single SAFE", []classifier.Cell{{Strategy: planpb.Strategy_SAFE}}, planpb.Strategy_SAFE},
		{"SAFE + NEEDS_CONFIRM = NEEDS_CONFIRM",
			[]classifier.Cell{{Strategy: planpb.Strategy_SAFE}, {Strategy: planpb.Strategy_NEEDS_CONFIRM}},
			planpb.Strategy_NEEDS_CONFIRM},
		{"NEEDS_CONFIRM + CUSTOM_MIGRATION = CUSTOM_MIGRATION",
			[]classifier.Cell{{Strategy: planpb.Strategy_NEEDS_CONFIRM}, {Strategy: planpb.Strategy_CUSTOM_MIGRATION}},
			planpb.Strategy_CUSTOM_MIGRATION},
		{"all five = CUSTOM_MIGRATION",
			[]classifier.Cell{
				{Strategy: planpb.Strategy_SAFE},
				{Strategy: planpb.Strategy_LOSSLESS_USING},
				{Strategy: planpb.Strategy_NEEDS_CONFIRM},
				{Strategy: planpb.Strategy_DROP_AND_CREATE},
				{Strategy: planpb.Strategy_CUSTOM_MIGRATION},
			},
			planpb.Strategy_CUSTOM_MIGRATION},
		{"DROP_AND_CREATE beats NEEDS_CONFIRM",
			[]classifier.Cell{{Strategy: planpb.Strategy_NEEDS_CONFIRM}, {Strategy: planpb.Strategy_DROP_AND_CREATE}},
			planpb.Strategy_DROP_AND_CREATE},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Fold(tc.cells)
			if got.Strategy != tc.want {
				t.Errorf("Fold strategy = %s, want %s", got.Strategy, tc.want)
			}
		})
	}
}

// TestRank verifies strategies.yaml rank ordering (strictest = highest
// rank; the Fold rule depends on this).
func TestRank(t *testing.T) {
	c := mustLoad(t)
	want := []planpb.Strategy{
		planpb.Strategy_SAFE,
		planpb.Strategy_LOSSLESS_USING,
		planpb.Strategy_NEEDS_CONFIRM,
		planpb.Strategy_DROP_AND_CREATE,
		planpb.Strategy_CUSTOM_MIGRATION,
	}
	for i := 1; i < len(want); i++ {
		if c.Rank(want[i]) <= c.Rank(want[i-1]) {
			t.Errorf("rank(%s) = %d, must be > rank(%s) = %d",
				want[i], c.Rank(want[i]), want[i-1], c.Rank(want[i-1]))
		}
	}
}

func mustLoad(t *testing.T) *classifier.Classifier {
	t.Helper()
	c, err := classifier.Load(classificationDir(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}
