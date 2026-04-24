package engine

// Internal unit tests for inject.go helpers — exercise the
// template rendering + lookup + dispatch branches without going
// through engine.Plan. Complements the e2e matrix runner which
// covers the happy path end-to-end.

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

func TestRenderUsingTemplate_HappyPath(t *testing.T) {
	got := renderUsingTemplate("{{.Col}}::text", carrierUsingContext{Col: "target"})
	if got != "target::text" {
		t.Errorf("got %q, want target::text", got)
	}
}

func TestRenderUsingTemplate_EmptyTemplateReturnsEmpty(t *testing.T) {
	if got := renderUsingTemplate("", carrierUsingContext{}); got != "" {
		t.Errorf("empty template should return empty, got %q", got)
	}
}

func TestRenderUsingTemplate_MissingKeyReturnsEmpty(t *testing.T) {
	// {{.Unknown}} references a field not in the context struct;
	// missingkey=error mode returns an error, which the helper
	// swallows → empty string signals "don't emit USING".
	got := renderUsingTemplate("{{.Unknown}}", carrierUsingContext{Col: "c"})
	if got != "" {
		t.Errorf("missing key should return empty, got %q", got)
	}
}

func TestRenderUsingTemplate_ProjectEncodingAccessible(t *testing.T) {
	// Project.Encoding is a nested field — the decode/encode
	// templates reference it via {{.Project.Encoding}}.
	got := renderUsingTemplate("encode({{.Col}}, '{{.Project.Encoding}}')",
		carrierUsingContext{Col: "payload", Project: projectContext{Encoding: "escape"}})
	if got != "encode(payload, 'escape')" {
		t.Errorf("got %q", got)
	}
}

func TestFindColumnByRef_Matches(t *testing.T) {
	schema := &irpb.Schema{Tables: []*irpb.Table{{
		Name: "users", MessageFqn: "shop.User",
		Columns: []*irpb.Column{
			{Name: "id", FieldNumber: 1},
			{Name: "flag", FieldNumber: 7},
		},
	}}}
	ref := &planpb.ColumnRef{TableFqn: "shop.User", FieldNumber: 7}
	table, col := findColumnByRef(schema, ref)
	if table == nil || col == nil {
		t.Fatal("expected match")
	}
	if col.GetName() != "flag" {
		t.Errorf("got col %q, want flag", col.GetName())
	}
}

func TestFindColumnByRef_NilInputs(t *testing.T) {
	if table, col := findColumnByRef(nil, &planpb.ColumnRef{}); table != nil || col != nil {
		t.Error("nil schema should return (nil, nil)")
	}
	if table, col := findColumnByRef(&irpb.Schema{}, nil); table != nil || col != nil {
		t.Error("nil ref should return (nil, nil)")
	}
}

func TestFindColumnByRef_WrongFQN(t *testing.T) {
	schema := &irpb.Schema{Tables: []*irpb.Table{{
		Name: "users", MessageFqn: "shop.User",
		Columns: []*irpb.Column{{Name: "id", FieldNumber: 1}},
	}}}
	table, col := findColumnByRef(schema, &planpb.ColumnRef{
		TableFqn: "shop.Other", FieldNumber: 1,
	})
	if table != nil || col != nil {
		t.Errorf("wrong FQN should miss, got table=%v col=%v", table, col)
	}
}

func TestFindColumnByRef_WrongFieldNumber(t *testing.T) {
	schema := &irpb.Schema{Tables: []*irpb.Table{{
		Name: "users", MessageFqn: "shop.User",
		Columns: []*irpb.Column{{Name: "id", FieldNumber: 1}},
	}}}
	table, col := findColumnByRef(schema, &planpb.ColumnRef{
		TableFqn: "shop.User", FieldNumber: 99,
	})
	if table != nil || col != nil {
		t.Errorf("wrong field_number should miss, got table=%v col=%v", table, col)
	}
}
