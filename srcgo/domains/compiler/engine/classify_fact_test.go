package engine

// classifyFactChange dispatch coverage — each FactChange variant
// should route to the right (axis, case). Matrix runner exercises
// these indirectly but targeted tests catch regressions when new
// FactChange variants land.

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func loadCls(t *testing.T) *classifier.Classifier {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "classification")
	c, err := classifier.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestClassifyFactChange_EveryVariantDispatches(t *testing.T) {
	cls := loadCls(t)
	alter := &planpb.AlterColumn{
		Column: &irpb.Column{Carrier: irpb.Carrier_CARRIER_STRING},
	}
	cases := []struct {
		name     string
		fc       *planpb.FactChange
		wantAxis string
		wantOK   bool
	}{
		{"nullable relax",
			&planpb.FactChange{Variant: &planpb.FactChange_Nullable{Nullable: &planpb.NullableChange{From: false, To: true}}},
			"nullable", true},
		{"nullable tighten",
			&planpb.FactChange{Variant: &planpb.FactChange_Nullable{Nullable: &planpb.NullableChange{From: true, To: false}}},
			"nullable", true},
		{"max_len widen",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 16, To: 64}}},
			"max_len", true},
		{"max_len narrow",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 64, To: 16}}},
			"max_len", true},
		{"max_len add_bound",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 0, To: 32}}},
			"max_len", true},
		{"max_len remove_bound",
			&planpb.FactChange{Variant: &planpb.FactChange_MaxLen{MaxLen: &planpb.MaxLenChange{From: 32, To: 0}}},
			"max_len", true},
		{"numeric change",
			&planpb.FactChange{Variant: &planpb.FactChange_NumericPrecision{NumericPrecision: &planpb.NumericPrecisionChange{FromPrecision: 8, ToPrecision: 16}}},
			"numeric", true},
		{"dbtype change",
			&planpb.FactChange{Variant: &planpb.FactChange_DbType{DbType: &planpb.DbTypeChange{From: irpb.DbType_DBT_TEXT, To: irpb.DbType_DBT_CITEXT}}},
			"dbtype", true},
		{"generated_expr add",
			&planpb.FactChange{Variant: &planpb.FactChange_GeneratedExpr{GeneratedExpr: &planpb.GeneratedExprChange{From: "", To: "upper(name)"}}},
			"generated_expr", true},
		{"generated_expr drop",
			&planpb.FactChange{Variant: &planpb.FactChange_GeneratedExpr{GeneratedExpr: &planpb.GeneratedExprChange{From: "upper(name)", To: ""}}},
			"generated_expr", true},
		{"generated_expr change",
			&planpb.FactChange{Variant: &planpb.FactChange_GeneratedExpr{GeneratedExpr: &planpb.GeneratedExprChange{From: "a", To: "b"}}},
			"generated_expr", true},
		{"comment",
			&planpb.FactChange{Variant: &planpb.FactChange_Comment{Comment: &planpb.CommentChange{From: "", To: "doc"}}},
			"comment", true},
		{"allowed_extensions widen",
			&planpb.FactChange{Variant: &planpb.FactChange_AllowedExtensions{AllowedExtensions: &planpb.AllowedExtensionsChange{From: []string{"jpg"}, To: []string{"jpg", "png"}}}},
			"allowed_extensions", true},
		{"default add",
			&planpb.FactChange{Variant: &planpb.FactChange_DefaultValue{DefaultValue: &planpb.DefaultChange{To: &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "x"}}}}},
			"default", true},
		{"type_change (D33)",
			&planpb.FactChange{Variant: &planpb.FactChange_TypeChange{TypeChange: &planpb.TypeChange{
				FromColumn: &irpb.Column{Carrier: irpb.Carrier_CARRIER_BOOL},
				ToColumn:   &irpb.Column{Carrier: irpb.Carrier_CARRIER_STRING},
			}}},
			"carrier_change", true},
		{"pg_options (manifest-only)",
			&planpb.FactChange{Variant: &planpb.FactChange_PgOptions{PgOptions: &planpb.PgOptionsChange{}}},
			"", false},
		{"unique (manifest-only at this layer)",
			&planpb.FactChange{Variant: &planpb.FactChange_Unique{Unique: &planpb.UniqueChange{From: false, To: true}}},
			"", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, axis, ok := classifyFactChange(cls, alter, c.fc)
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
			}
			if axis != c.wantAxis {
				t.Errorf("axis = %q, want %q", axis, c.wantAxis)
			}
		})
	}
}

func TestTemplateData_PerVariantPopulation(t *testing.T) {
	alter := &planpb.AlterColumn{}
	{
		fc := &planpb.FactChange{Variant: &planpb.FactChange_MaxLen{
			MaxLen: &planpb.MaxLenChange{To: 64},
		}}
		data := templateData("t", "c", alter, fc)
		d := data.(struct {
			Table, Col                        string
			NewMaxLen, NewPrecision, NewScale int32
			IntMin, IntMax                    int64
		})
		if d.NewMaxLen != 64 {
			t.Errorf("MaxLen variant: NewMaxLen = %d, want 64", d.NewMaxLen)
		}
	}
	{
		s := int32(2)
		fc := &planpb.FactChange{Variant: &planpb.FactChange_NumericPrecision{
			NumericPrecision: &planpb.NumericPrecisionChange{ToPrecision: 10, ToScale: &s},
		}}
		data := templateData("t", "c", alter, fc)
		d := data.(struct {
			Table, Col                        string
			NewMaxLen, NewPrecision, NewScale int32
			IntMin, IntMax                    int64
		})
		if d.NewPrecision != 10 || d.NewScale != 2 {
			t.Errorf("Numeric variant: precision=%d scale=%d, want 10/2", d.NewPrecision, d.NewScale)
		}
	}
	// Unknown variant → returns bag with just Table+Col.
	{
		fc := &planpb.FactChange{Variant: &planpb.FactChange_Comment{Comment: &planpb.CommentChange{}}}
		data := templateData("t", "c", alter, fc)
		d := data.(struct {
			Table, Col                        string
			NewMaxLen, NewPrecision, NewScale int32
			IntMin, IntMax                    int64
		})
		if d.Table != "t" || d.Col != "c" {
			t.Errorf("base fields missing: %+v", d)
		}
	}
}
