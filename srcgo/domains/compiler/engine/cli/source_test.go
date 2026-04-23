package cli_test

import (
	"fmt"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine/cli"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// memoryLoader is a test double for the custom-SQL file reader.
type memoryLoader map[string]string

func (m memoryLoader) Load(path string) (string, error) {
	body, ok := m[path]
	if !ok {
		return "", fmt.Errorf("no file %q", path)
	}
	return body, nil
}

func TestParse_Strategy(t *testing.T) {
	cases := []struct {
		flag string
		want planpb.Strategy
	}{
		{"users.email=safe", planpb.Strategy_SAFE},
		{"users.email=lossless_using", planpb.Strategy_LOSSLESS_USING},
		{"users.email=using", planpb.Strategy_LOSSLESS_USING}, // alias
		{"users.email=needs_confirm", planpb.Strategy_NEEDS_CONFIRM},
		{"users.email=drop_and_create", planpb.Strategy_DROP_AND_CREATE},
		{"users.email=DROP_AND_CREATE", planpb.Strategy_DROP_AND_CREATE}, // case-insensitive
		{"users.email=drop", planpb.Strategy_DROP_AND_CREATE},            // alias
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			d, err := cli.Parse([]string{tc.flag}, nil)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			f := &planpb.ReviewFinding{
				Id: "abc",
				Column: &planpb.ColumnRef{TableName: "users", ColumnName: "email"},
			}
			rs := d.ResolveAll([]*planpb.ReviewFinding{f})
			if len(rs) != 1 {
				t.Fatalf("len(resolutions) = %d, want 1", len(rs))
			}
			if rs[0].GetStrategy() != tc.want {
				t.Errorf("strategy = %s, want %s", rs[0].GetStrategy(), tc.want)
			}
			if rs[0].GetFindingId() != "abc" {
				t.Errorf("finding_id = %q, want abc", rs[0].GetFindingId())
			}
			if rs[0].GetActor() != "cli" {
				t.Errorf("actor = %q, want cli", rs[0].GetActor())
			}
		})
	}
}

func TestParse_CustomSQL(t *testing.T) {
	loader := memoryLoader{
		"/tmp/migrate.sql": "UPDATE users SET email = lower(email) WHERE email IS NOT NULL;",
	}
	d, err := cli.Parse(
		[]string{"users.email=custom:/tmp/migrate.sql"},
		loader.Load,
	)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	f := &planpb.ReviewFinding{
		Id: "xyz",
		Column: &planpb.ColumnRef{TableName: "users", ColumnName: "email"},
	}
	rs := d.ResolveAll([]*planpb.ReviewFinding{f})
	if len(rs) != 1 || rs[0].GetStrategy() != planpb.Strategy_CUSTOM_MIGRATION {
		t.Fatalf("want 1 CUSTOM_MIGRATION, got %v", rs)
	}
	if rs[0].GetCustomSql() != "UPDATE users SET email = lower(email) WHERE email IS NOT NULL;" {
		t.Errorf("custom_sql mismatch: %q", rs[0].GetCustomSql())
	}
}

func TestParse_AxisSpecific(t *testing.T) {
	// axis:carrier_change precedes column-wide.
	d, err := cli.Parse([]string{
		"users.email=safe",
		"users.email:carrier_change=drop_and_create",
	}, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	carrier := &planpb.ReviewFinding{
		Id: "a",
		Column: &planpb.ColumnRef{TableName: "users", ColumnName: "email"},
		Axis:   "carrier_change",
	}
	generic := &planpb.ReviewFinding{
		Id: "b",
		Column: &planpb.ColumnRef{TableName: "users", ColumnName: "email"},
		Axis:   "nullable_tighten",
	}
	rs := d.ResolveAll([]*planpb.ReviewFinding{carrier, generic})
	if len(rs) != 2 {
		t.Fatalf("len = %d, want 2", len(rs))
	}
	if rs[0].GetStrategy() != planpb.Strategy_DROP_AND_CREATE {
		t.Errorf("carrier_change should hit axis-specific; got %s", rs[0].GetStrategy())
	}
	if rs[1].GetStrategy() != planpb.Strategy_SAFE {
		t.Errorf("generic should fall back to column-wide; got %s", rs[1].GetStrategy())
	}
}

func TestParse_Unresolved(t *testing.T) {
	d, err := cli.Parse([]string{"users.email=safe"}, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved := &planpb.ReviewFinding{
		Id: "a",
		Column: &planpb.ColumnRef{TableName: "users", ColumnName: "email"},
	}
	orphan := &planpb.ReviewFinding{
		Id: "b",
		Column: &planpb.ColumnRef{TableName: "orders", ColumnName: "total"},
	}
	un := d.Unresolved([]*planpb.ReviewFinding{resolved, orphan})
	if len(un) != 1 || un[0].GetId() != "b" {
		t.Errorf("Unresolved = %v, want [b]", un)
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		wantMsg string
	}{
		{"missing equals", "users.email", "missing '='"},
		{"missing dot", "users=safe", "missing '.'"},
		{"empty table", ".email=safe", "empty table or column"},
		{"empty column", "users.=safe", "empty table or column"},
		{"empty axis", "users.email:=safe", "empty axis"},
		{"unknown strategy", "users.email=wat", "unknown strategy"},
		{"empty custom path", "users.email=custom:", "empty path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cli.Parse([]string{tc.flag}, nil)
			if err == nil {
				t.Fatalf("Parse accepted invalid flag %q", tc.flag)
			}
			if !contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestParse_DuplicateKeyErrors(t *testing.T) {
	_, err := cli.Parse([]string{
		"users.email=safe",
		"users.email=drop_and_create",
	}, nil)
	if err == nil {
		t.Fatal("duplicate key should error")
	}
}

func TestParse_CustomLoaderFailure(t *testing.T) {
	loader := memoryLoader{} // empty
	_, err := cli.Parse(
		[]string{"users.email=custom:/nonexistent.sql"},
		loader.Load,
	)
	if err == nil {
		t.Fatal("loader failure should propagate")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
