package engine

// D35 risk analyzer unit tests — pins every profile-table entry
// by driving analyzeRisks with a synthetic FactChange / Op and
// asserting the emitted RiskFinding shape.
//
// Complements matrix runner's end-to-end signal: the matrix
// ships real SQL that applies; these tests ship the deterministic
// risk profile each op kind gets annotated with.

import (
	"strings"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func TestAnalyzeRisks_CarrierChangeFinding(t *testing.T) {
	findings := []*planpb.ReviewFinding{{
		Id: "f1", Axis: "carrier_change",
		Column: &planpb.ColumnRef{TableName: "users", ColumnName: "flag"},
	}}
	got := analyzeRisks(&planpb.MigrationPlan{}, findings)
	if len(got) != 1 {
		t.Fatalf("want 1 risk, got %d", len(got))
	}
	r := got[0]
	if r.GetOpKind() != "carrier_change" {
		t.Errorf("OpKind = %q", r.GetOpKind())
	}
	if r.GetSeverity() != "HIGH" {
		t.Errorf("Severity = %q, want HIGH", r.GetSeverity())
	}
	if !r.GetRewrite() {
		t.Error("carrier_change must flag rewrite=true")
	}
	if r.GetScalesWith() != "row_count" {
		t.Errorf("ScalesWith = %q, want row_count", r.GetScalesWith())
	}
	if r.GetRecommendation() == "" {
		t.Error("HIGH-severity risks should carry a recommendation")
	}
	if c := r.GetColumn(); c == nil || c.GetTableName() != "users" {
		t.Errorf("column ref missing: %v", c)
	}
}

func TestAnalyzeRisks_PkFlipFinding(t *testing.T) {
	findings := []*planpb.ReviewFinding{{
		Id: "f2", Axis: "pk_flip",
		Column: &planpb.ColumnRef{TableName: "users", ColumnName: "id"},
	}}
	got := analyzeRisks(&planpb.MigrationPlan{}, findings)
	if len(got) != 1 || got[0].GetSeverity() != "HIGH" {
		t.Errorf("pk_flip should emit one HIGH risk, got %v", got)
	}
}

func TestAnalyzeRisks_FactChangeDispatch(t *testing.T) {
	// Table-driven: each FactChange → expected (opKind, severity).
	cases := []struct {
		name     string
		fc       *planpb.FactChange
		wantKind string
		wantSev  string
	}{
		{"nullable_relax",
			&planpb.FactChange{Variant: &planpb.FactChange_Nullable{Nullable: &planpb.NullableChange{From: false, To: true}}},
			"nullable_relax", "LOW"},
		{"nullable_tighten",
			&planpb.FactChange{Variant: &planpb.FactChange_Nullable{Nullable: &planpb.NullableChange{From: true, To: false}}},
			"nullable_tighten", "MEDIUM"},
		{"max_len_widen",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 16, To: 64}}},
			"max_len_widen", "LOW"},
		{"max_len_narrow",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 64, To: 16}}},
			"max_len_narrow", "MEDIUM"},
		{"max_len_remove_bound",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 32, To: 0}}},
			"max_len_remove_bound", "LOW"},
		{"numeric_widen_both",
			&planpb.FactChange{Variant: &planpb.FactChange_NumericPrecision{NumericPrecision: &planpb.NumericPrecisionChange{FromPrecision: 8, ToPrecision: 16}}},
			"numeric_widen_both", "LOW"},
		{"numeric_precision_narrow",
			&planpb.FactChange{Variant: &planpb.FactChange_NumericPrecision{NumericPrecision: &planpb.NumericPrecisionChange{FromPrecision: 16, ToPrecision: 8}}},
			"numeric_precision_narrow", "MEDIUM"},
		{"dbtype_change",
			&planpb.FactChange{Variant: &planpb.FactChange_DbType{DbType: &planpb.DbTypeChange{From: irpb.DbType_DBT_TEXT, To: irpb.DbType_DBT_CITEXT}}},
			"dbtype_change", "MEDIUM"},
		{"unique_enable",
			&planpb.FactChange{Variant: &planpb.FactChange_Unique{Unique: &planpb.UniqueChange{From: false, To: true}}},
			"unique_enable", "HIGH"},
		{"generated_expr_add",
			&planpb.FactChange{Variant: &planpb.FactChange_GeneratedExpr{GeneratedExpr: &planpb.GeneratedExprChange{From: "", To: "lower(name)"}}},
			"generated_expr_add", "HIGH"},
		{"generated_expr_drop",
			&planpb.FactChange{Variant: &planpb.FactChange_GeneratedExpr{GeneratedExpr: &planpb.GeneratedExprChange{From: "x", To: ""}}},
			"generated_expr_drop", "MEDIUM"},
		{"comment_change",
			&planpb.FactChange{Variant: &planpb.FactChange_Comment{Comment: &planpb.CommentChange{From: "", To: "doc"}}},
			"comment_change", "LOW"},
		{"allowed_extensions_widen",
			&planpb.FactChange{Variant: &planpb.FactChange_AllowedExtensions{AllowedExtensions: &planpb.AllowedExtensionsChange{From: []string{"jpg"}, To: []string{"jpg", "png"}}}},
			"allowed_extensions_widen", "LOW"},
		{"allowed_extensions_narrow",
			&planpb.FactChange{Variant: &planpb.FactChange_AllowedExtensions{AllowedExtensions: &planpb.AllowedExtensionsChange{From: []string{"jpg", "png"}, To: []string{"jpg"}}}},
			"allowed_extensions_narrow", "MEDIUM"},
		{"default_change",
			&planpb.FactChange{Variant: &planpb.FactChange_DefaultValue{DefaultValue: &planpb.DefaultChange{To: &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "x"}}}}},
			"default_change", "LOW"},
		{"carrier_change via TypeChange",
			&planpb.FactChange{Variant: &planpb.FactChange_TypeChange{TypeChange: &planpb.TypeChange{
				FromColumn: &irpb.Column{Carrier: irpb.Carrier_CARRIER_BOOL},
				ToColumn:   &irpb.Column{Carrier: irpb.Carrier_CARRIER_STRING},
			}}},
			"carrier_change", "HIGH"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := &planpb.MigrationPlan{Ops: []*planpb.Op{{
				Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
					Ctx:        &planpb.TableCtx{TableName: "t"},
					ColumnName: "col",
					Changes:    []*planpb.FactChange{c.fc},
				}},
			}}}
			got := analyzeRisks(plan, nil)
			if len(got) != 1 {
				t.Fatalf("want 1 risk, got %d: %v", len(got), got)
			}
			if got[0].GetOpKind() != c.wantKind {
				t.Errorf("OpKind = %q, want %q", got[0].GetOpKind(), c.wantKind)
			}
			if got[0].GetSeverity() != c.wantSev {
				t.Errorf("Severity = %q, want %q", got[0].GetSeverity(), c.wantSev)
			}
		})
	}
}

func TestAnalyzeRisks_TableLevelOps(t *testing.T) {
	cases := []struct {
		name    string
		op      *planpb.Op
		wantKind string
	}{
		{"drop_table",
			&planpb.Op{Variant: &planpb.Op_DropTable{DropTable: &planpb.DropTable{
				Table: &irpb.Table{Name: "legacy", MessageFqn: "pkg.Legacy"},
			}}},
			"drop_table"},
		{"drop_column",
			&planpb.Op{Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{
				Ctx:    &planpb.TableCtx{TableName: "users", MessageFqn: "pkg.User"},
				Column: &irpb.Column{Name: "legacy_flag"},
			}}},
			"drop_column"},
		{"table_rename",
			&planpb.Op{Variant: &planpb.Op_RenameTable{RenameTable: &planpb.RenameTable{
				Ctx:      &planpb.TableCtx{MessageFqn: "pkg.User"},
				FromName: "users", ToName: "accounts",
			}}},
			"table_rename"},
		{"fk_add",
			&planpb.Op{Variant: &planpb.Op_AddForeignKey{AddForeignKey: &planpb.AddForeignKey{
				Ctx: &planpb.TableCtx{TableName: "orders"},
				Fk:  &irpb.ForeignKey{Column: "customer_id"},
			}}},
			"fk_add"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := analyzeRisks(&planpb.MigrationPlan{Ops: []*planpb.Op{c.op}}, nil)
			if len(got) != 1 {
				t.Fatalf("want 1 risk, got %d: %v", len(got), got)
			}
			if got[0].GetOpKind() != c.wantKind {
				t.Errorf("OpKind = %q, want %q", got[0].GetOpKind(), c.wantKind)
			}
		})
	}
}

func TestAnalyzeRisks_NoRisksForAddTable(t *testing.T) {
	plan := &planpb.MigrationPlan{Ops: []*planpb.Op{{
		Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{
			Table: &irpb.Table{Name: "new_table"},
		}},
	}}}
	got := analyzeRisks(plan, nil)
	if len(got) != 0 {
		t.Errorf("AddTable should produce no risks (new table has no data to migrate), got %v", got)
	}
}

func TestRenderRiskComments_Empty(t *testing.T) {
	if got := renderRiskComments(nil); got != "" {
		t.Errorf("empty risks should render empty, got %q", got)
	}
}

func TestRenderRiskComments_ShapeAndSort(t *testing.T) {
	risks := []*planpb.RiskFinding{
		{OpKind: "comment_change", Severity: "LOW", Rationale: "metadata"},
		{OpKind: "carrier_change", Severity: "HIGH", Rationale: "rewrite",
			Column: &planpb.ColumnRef{TableName: "users", ColumnName: "flag"}},
		{OpKind: "nullable_tighten", Severity: "MEDIUM", Rationale: "scan"},
	}
	out := renderRiskComments(risks)
	// Expected order: HIGH → MEDIUM → LOW.
	idxHigh := strings.Index(out, "HIGH")
	idxMed := strings.Index(out, "MEDIUM")
	idxLow := strings.Index(out, "LOW")
	if !(idxHigh < idxMed && idxMed < idxLow) {
		t.Errorf("order not HIGH→MEDIUM→LOW: high=%d med=%d low=%d\n%s", idxHigh, idxMed, idxLow, out)
	}
	// Shape checks.
	if !strings.Contains(out, "-- RISK HIGH [carrier_change] users.flag") {
		t.Errorf("HIGH line shape wrong:\n%s", out)
	}
	if !strings.Contains(out, "Migration risk analysis") {
		t.Errorf("header missing:\n%s", out)
	}
}

func TestRenderRiskComments_LineWrapping(t *testing.T) {
	// Very long rationale should wrap at ~70 chars.
	long := strings.Repeat("word ", 30) // ~150 chars
	risks := []*planpb.RiskFinding{{OpKind: "x", Severity: "LOW", Rationale: long}}
	out := renderRiskComments(risks)
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 100 {
			t.Errorf("line too long (%d chars): %q", len(line), line)
		}
	}
}
