// Package filesystem is the engine.Sink implementation that writes
// Plan artifacts to a local filesystem tree — today's `wc generate
// --out <dir>` behaviour, lifted into an adapter per D30.
//
// Layout (mirrors the iter-1 / iter-2 M3 convention):
//
//   <OutRoot>/
//     migrations/
//       <conn-dir>/             // e.g. postgres-18; omitted for default-conn migration
//         <Basename>.up.sql
//         <Basename>.down.sql
//         <Basename>.check.sql  // optional; present when the migration carries NEEDS_CONFIRM checks
//
// `<conn-dir>` = `<lower(dialect)>-<version>` (D26). Migrations
// without an explicit Connection (iter-1 single-default case) land
// directly under `migrations/`.
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/writer"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Sink writes Plan migrations to a filesystem tree. Caller pre-
// configures OutRoot + Basename; each Write call materialises every
// Migration in the plan using those settings.
type Sink struct {
	// OutRoot — directory under which `migrations/<conn-dir>/` trees
	// are created. Required.
	OutRoot string

	// Basename — filename stem shared by every migration written in
	// one Plan. Typically a naming.Name(time.Now()) timestamp from
	// the caller. Required.
	Basename string
}

// Write implements engine.Sink. Walks plan.Migrations and writes
// each to its own `<conn-dir>` under OutRoot/migrations/. Migrations
// with empty up + down SQL are skipped (no-op diffs don't create
// stub files — AC #1 of iter-2 M1).
//
// Nil-safe: Write(nil) returns nil.
func (s *Sink) Write(plan *planpb.Plan) error {
	if plan == nil {
		return nil
	}
	if s.OutRoot == "" {
		return fmt.Errorf("filesystem.Sink: OutRoot is empty")
	}
	if s.Basename == "" {
		return fmt.Errorf("filesystem.Sink: Basename is empty")
	}
	for i, m := range plan.GetMigrations() {
		if err := s.writeMigration(m); err != nil {
			return fmt.Errorf("filesystem.Sink: migration #%d: %w", i, err)
		}
	}
	return nil
}

func (s *Sink) writeMigration(m *planpb.Migration) error {
	up := m.GetUpSql()
	down := m.GetDownSql()
	if up == "" && down == "" {
		return nil // no-op diff; nothing to write
	}
	if up == "" || down == "" {
		return fmt.Errorf("migration has asymmetric SQL: up=%d bytes, down=%d bytes (emit should produce both or neither)",
			len(up), len(down))
	}
	dir := s.dirFor(m.GetConnection())
	if _, _, err := writer.Write(dir, s.Basename, up, down); err != nil {
		return err
	}
	if err := s.writeChecks(dir, m.GetChecks()); err != nil {
		return err
	}
	return nil
}

// writeChecks materialises each NamedSQL check as a separate file
// next to the up/down pair. Filename: `<Basename>.check.<name>.sql`.
// Empty check list is a no-op.
func (s *Sink) writeChecks(dir string, checks []*planpb.NamedSQL) error {
	for _, c := range checks {
		if c.GetSql() == "" {
			continue
		}
		filename := fmt.Sprintf("%s.check.%s.sql", s.Basename, sanitizeName(c.GetName()))
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, []byte(c.GetSql()), 0o644); err != nil {
			return fmt.Errorf("check file %s: %w", path, err)
		}
	}
	return nil
}

// dirFor derives the migration subdirectory per-connection. Absent
// connection = iter-1 single-default layout (`migrations/` bare).
func (s *Sink) dirFor(conn *irpb.Connection) string {
	dir := filepath.Join(s.OutRoot, "migrations")
	if conn == nil || conn.GetName() == "" {
		return dir
	}
	return filepath.Join(dir, connectionDirKey(conn))
}

// connectionDirKey mirrors cmd/cli's derivation: lower(dialect) + "-"
// + version. Duplicated here (2 lines) so the sink package doesn't
// import cmd/cli.
func connectionDirKey(c *irpb.Connection) string {
	return strings.ToLower(c.GetDialect().String()) + "-" + c.GetVersion()
}

// sanitizeName keeps check filenames filesystem-safe. Check names
// originate in classifier.Cell; expected shape is already
// identifier-like, but we guard against future drift.
func sanitizeName(name string) string {
	if name == "" {
		return "unnamed"
	}
	// Replace path separators + whitespace; leave anything else
	// (alphanumeric + underscore-ish) untouched.
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
