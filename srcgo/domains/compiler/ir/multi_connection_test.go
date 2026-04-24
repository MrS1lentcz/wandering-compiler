package ir_test

import (
	"context"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestBuildManyMultiConnection — two modules declaring distinct
// connections land as a single Schema with each Table stamped with
// its module's Connection. The CLI orchestrator (cmd_generate.go
// bucketByConnection) reads Table.Connection to route emission.
func TestBuildManyMultiConnection(t *testing.T) {
	main := mustLoad(t, "testdata/multi_connection/main.proto")
	sessions := mustLoad(t, "testdata/multi_connection/sessions.proto")

	schema, err := ir.BuildMany([]*loader.LoadedFile{main, sessions})
	if err != nil {
		t.Fatalf("BuildMany: %v", err)
	}

	if got := schema.GetConnection().GetName(); got != "main" {
		t.Errorf("Schema.Connection.Name = %q, want main", got)
	}
	if got := schema.GetConnection().GetDialect(); got != irpb.Dialect_POSTGRES {
		t.Errorf("Schema.Connection.Dialect = %v, want POSTGRES", got)
	}

	byName := map[string]*irpb.Table{}
	for _, tbl := range schema.GetTables() {
		byName[tbl.GetName()] = tbl
	}
	users := byName["users"]
	if users == nil {
		t.Fatalf("users table missing from schema")
	}
	if c := users.GetConnection(); c == nil || c.GetName() != "main" || c.GetDialect() != irpb.Dialect_POSTGRES {
		t.Errorf("users.Connection = %v, want {main, POSTGRES, 18}", c)
	}
	sess := byName["sessions"]
	if sess == nil {
		t.Fatalf("sessions table missing")
	}
	if c := sess.GetConnection(); c == nil || c.GetName() != "sessions" || c.GetDialect() != irpb.Dialect_REDIS {
		t.Errorf("sessions.Connection = %v, want {sessions, REDIS, 7}", c)
	}
}

// TestBuildManyPerTableConnectionOverride — (w17.db.table).connection
// override resolves against the domain-level registry.
func TestBuildManyPerTableConnectionOverride(t *testing.T) {
	main := mustLoad(t, "testdata/multi_connection_override/main.proto")
	side := mustLoad(t, "testdata/multi_connection_override/side.proto")

	schema, err := ir.BuildMany([]*loader.LoadedFile{main, side})
	if err != nil {
		t.Fatalf("BuildMany: %v", err)
	}

	var flag *irpb.Table
	for _, tbl := range schema.GetTables() {
		if tbl.GetName() == "feature_flags" {
			flag = tbl
		}
	}
	if flag == nil {
		t.Fatalf("feature_flags table missing")
	}
	c := flag.GetConnection()
	if c == nil || c.GetName() != "side" || c.GetDialect() != irpb.Dialect_REDIS {
		t.Errorf("feature_flags.Connection = %v, want {side, REDIS, 7}", c)
	}
}

// TestBuildManyUnknownConnectionOverride — override targets a name
// not registered by any module → diag.Error.
func TestBuildManyUnknownConnectionOverride(t *testing.T) {
	main := mustLoad(t, "testdata/multi_connection_unknown_override/main.proto")
	_, err := ir.BuildMany([]*loader.LoadedFile{main})
	if err == nil {
		t.Fatal("expected error on unknown connection override")
	}
}

// TestBuildManySameCategoryRejected — D34 enforcement: two
// RELATIONAL dialects (POSTGRES + SQLITE) in the same domain
// are rejected at IR build time with a diag.Error naming the
// category + prior connection.
func TestBuildManySameCategoryRejected(t *testing.T) {
	main := mustLoad(t, "testdata/multi_connection_same_category/main.proto")
	dev := mustLoad(t, "testdata/multi_connection_same_category/dev.proto")

	_, err := ir.BuildMany([]*loader.LoadedFile{main, dev})
	if err == nil {
		t.Fatal("expected D34 error on two RELATIONAL connections in one domain")
	}
	msg := err.Error()
	for _, want := range []string{
		"RELATIONAL",
		"D34",
		"split",
	} {
		if !containsSubstring(msg, want) {
			t.Errorf("error message missing %q, got:\n%s", want, msg)
		}
	}
}

// containsSubstring is a substring check that tolerates the
// multi-line diag.Error format.
func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func mustLoad(t *testing.T, path string) *loader.LoadedFile {
	t.Helper()
	lf, err := loader.Load(context.Background(), path, []string{".", "../../../../proto"})
	if err != nil {
		t.Fatalf("loader.Load %s: %v", path, err)
	}
	return lf
}
