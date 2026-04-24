package filesystem_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine/filesystem"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// TestWrite_ManifestJSONEmittedWhenPopulated — a migration carrying a
// non-empty Manifest produces `<Basename>.manifest.json` alongside the
// SQL pair. Content is canonical protojson (compact, deterministic).
func TestWrite_ManifestJSONEmittedWhenPopulated(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	plan := &planpb.Plan{
		Migrations: []*planpb.Migration{{
			UpSql:   "CREATE TABLE x (id INT);",
			DownSql: "DROP TABLE x;",
			Manifest: &planpb.Manifest{
				Capabilities:       []string{"JSONB", "UUID"},
				RequiredExtensions: []string{"pg_jsonschema"},
			},
		}},
	}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := filepath.Join(dir, "migrations", "ts.manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("manifest.json missing: %v", err)
	}
	// Canonical protojson: compact, trailing newline, proto field names.
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("manifest.json missing trailing newline")
	}
	// protojson wraps array elements + map entries with optional
	// whitespace as an anti-fragile-parser measure. Strip all
	// whitespace before substring-matching so the test pins content
	// without wedging on formatting jitter.
	body := stripWhitespace(string(data))
	for _, want := range []string{
		`"capabilities":["JSONB","UUID"]`,
		`"required_extensions":["pg_jsonschema"]`,
	} {
		if !strings.Contains(body, stripWhitespace(want)) {
			t.Errorf("manifest.json missing %q in body %q", want, body)
		}
	}

	// Determinism: a second Write produces byte-identical content.
	sink2 := &filesystem.Sink{OutRoot: t.TempDir(), Basename: "ts"}
	if err := sink2.Write(plan); err != nil {
		t.Fatalf("Write (retry): %v", err)
	}
	data2, err := os.ReadFile(filepath.Join(sink2.OutRoot, "migrations", "ts.manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json retry missing: %v", err)
	}
	if !bytes.Equal(data, data2) {
		t.Errorf("non-deterministic manifest output\n1: %s\n2: %s", data, data2)
	}
}

// TestWrite_ManifestSkippedWhenEmpty — empty Manifest (no caps, no
// extensions, no applied resolutions) produces no file. AC #1
// preservation: only the SQL files land.
func TestWrite_ManifestSkippedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	sink := &filesystem.Sink{OutRoot: dir, Basename: "ts"}

	// Nil manifest case.
	plan := &planpb.Plan{
		Migrations: []*planpb.Migration{{
			UpSql:   "CREATE TABLE x (id INT);",
			DownSql: "DROP TABLE x;",
		}},
	}
	if err := sink.Write(plan); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "migrations", "ts.manifest.json")); !os.IsNotExist(err) {
		t.Errorf("manifest.json exists despite nil manifest (err=%v)", err)
	}

	// Empty-but-non-nil manifest case.
	dir2 := t.TempDir()
	sink2 := &filesystem.Sink{OutRoot: dir2, Basename: "ts"}
	plan2 := &planpb.Plan{
		Migrations: []*planpb.Migration{{
			UpSql:    "CREATE TABLE x (id INT);",
			DownSql:  "DROP TABLE x;",
			Manifest: &planpb.Manifest{},
		}},
	}
	if err := sink2.Write(plan2); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "migrations", "ts.manifest.json")); !os.IsNotExist(err) {
		t.Errorf("manifest.json exists despite empty manifest (err=%v)", err)
	}
}

func stripWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
