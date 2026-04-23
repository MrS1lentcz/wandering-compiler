package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine/filesystem"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Compile-time interface conformance.
var _ engine.Sink = (*filesystem.Sink)(nil)

func TestWrite_SingleDefaultMigration(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "20260424T100000Z"}

	plan := &planpb.Plan{
		Migrations: []*planpb.Migration{{
			UpSql:   "CREATE TABLE users (id INT);",
			DownSql: "DROP TABLE users;",
		}},
	}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Default (no Connection) → `migrations/` bare.
	upPath := filepath.Join(dir, "migrations", "20260424T100000Z.up.sql")
	assertFileContent(t, upPath, "CREATE TABLE users (id INT);")
	downPath := filepath.Join(dir, "migrations", "20260424T100000Z.down.sql")
	assertFileContent(t, downPath, "DROP TABLE users;")
}

func TestWrite_MultiConnection(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	plan := &planpb.Plan{Migrations: []*planpb.Migration{
		{
			Connection: &irpb.Connection{Name: "main", Dialect: irpb.Dialect_POSTGRES, Version: "18"},
			UpSql:      "CREATE TABLE a (id INT);",
			DownSql:    "DROP TABLE a;",
		},
		{
			Connection: &irpb.Connection{Name: "cache", Dialect: irpb.Dialect_REDIS, Version: "7"},
			UpSql:      "-- redis DDL",
			DownSql:    "-- redis cleanup",
		},
	}}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertFileContent(t, filepath.Join(dir, "migrations", "postgres-18", "ts.up.sql"), "CREATE TABLE a (id INT);")
	assertFileContent(t, filepath.Join(dir, "migrations", "redis-7", "ts.up.sql"), "-- redis DDL")
}

func TestWrite_SkipEmptyMigration(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	plan := &planpb.Plan{Migrations: []*planpb.Migration{{
		// Both empty → AC #1 no-op: sink writes nothing.
	}}}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// No migrations/ dir created.
	_, err := os.Stat(filepath.Join(dir, "migrations"))
	if !os.IsNotExist(err) {
		t.Errorf("empty migration created files; Stat err = %v", err)
	}
}

func TestWrite_AsymmetricSQL(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	plan := &planpb.Plan{Migrations: []*planpb.Migration{{
		UpSql:   "CREATE TABLE a (id INT);",
		DownSql: "", // missing down — bug-level state
	}}}
	if err := sink.Write(plan); err == nil {
		t.Fatal("Write accepted asymmetric SQL; want error")
	}
}

func TestWrite_WithChecks(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	plan := &planpb.Plan{Migrations: []*planpb.Migration{{
		UpSql:   "ALTER TABLE users ALTER COLUMN email TYPE citext USING email::citext;",
		DownSql: "ALTER TABLE users ALTER COLUMN email TYPE text USING email::text;",
		Checks: []*planpb.NamedSQL{
			{Name: "email_format", Sql: "SELECT count(*) FROM users WHERE email !~ '@';"},
			{Name: "nullable_tighten", Sql: "SELECT count(*) FROM users WHERE email IS NULL;"},
		},
	}}}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}
	assertFileContent(t,
		filepath.Join(dir, "migrations", "ts.check.email_format.sql"),
		"SELECT count(*) FROM users WHERE email !~ '@';")
	assertFileContent(t,
		filepath.Join(dir, "migrations", "ts.check.nullable_tighten.sql"),
		"SELECT count(*) FROM users WHERE email IS NULL;")
}

func TestWrite_NilPlan(t *testing.T) {
	sink := &filesystem.Sink{OutRoot: "/dev/null/whatever", Basename: "ts"}
	if err := sink.Write(nil); err != nil {
		t.Errorf("Write(nil) error = %v, want nil", err)
	}
}

func TestWrite_MissingConfig(t *testing.T) {
	plan := &planpb.Plan{Migrations: []*planpb.Migration{{
		UpSql: "x", DownSql: "x",
	}}}
	if err := (&filesystem.Sink{Basename: "ts"}).Write(plan); err == nil {
		t.Error("empty OutRoot should error")
	}
	if err := (&filesystem.Sink{OutRoot: "/tmp/x"}).Write(plan); err == nil {
		t.Error("empty Basename should error")
	}
}

func TestWrite_SanitizeCheckName(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	plan := &planpb.Plan{Migrations: []*planpb.Migration{{
		UpSql:   "x",
		DownSql: "y",
		Checks:  []*planpb.NamedSQL{{Name: "weird/name with spaces", Sql: "SELECT 1;"}},
	}}}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// `/` and spaces become `_`.
	assertFileContent(t,
		filepath.Join(dir, "migrations", "ts.check.weird_name_with_spaces.sql"),
		"SELECT 1;")
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Errorf("%s content = %q, want %q", path, string(data), want)
	}
}
