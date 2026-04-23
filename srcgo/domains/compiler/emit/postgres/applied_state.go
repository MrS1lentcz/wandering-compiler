// Postgres renderers for the wc_migrations applied-state table per D27.
// The table is per-DB-instance (D26 pins one domain = one DB so no
// scoping in the name or schema is needed); rows are timestamp-PK
// with content_sha256 integrity check. The CLI layer composes these
// renderers with plan.Emit output to produce the final migration files
// (hash computed AFTER the rest of up.sql is finalised so the INSERT
// statement carrying the hash can't include itself in the hash it
// stores).
package postgres

import (
	"encoding/hex"
	"fmt"

	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// EmitOp dispatch already handles the WcMigrations Op variants — these
// helpers expose the same SQL shapes for direct CLI orchestration when
// the wrapper isn't going through plan.Emit.

// RenderWcMigrationsCreate returns the `CREATE TABLE wc_migrations …`
// statement. Lives in the connection's default schema (D27).
func RenderWcMigrationsCreate() string {
	return `CREATE TABLE wc_migrations (
    timestamp      TIMESTAMPTZ PRIMARY KEY,
    applied_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    content_sha256 BYTEA NOT NULL
);`
}

// RenderWcMigrationsDrop returns the `DROP TABLE wc_migrations` for
// initial-migration rollback.
func RenderWcMigrationsDrop() string {
	return `DROP TABLE IF EXISTS wc_migrations;`
}

// RenderWcMigrationsInsert returns one INSERT INTO wc_migrations row
// for a given migration timestamp + content hash. Hash is rendered
// as PG bytea hex literal `\x<hex>`.
func RenderWcMigrationsInsert(ts string, hash []byte) string {
	return fmt.Sprintf("INSERT INTO wc_migrations (timestamp, applied_at, content_sha256) VALUES ('%s', now(), '\\x%s');",
		ts, hex.EncodeToString(hash))
}

// RenderWcMigrationsDelete returns the matching DELETE for the down
// migration. Symmetric inverse of RenderWcMigrationsInsert (no hash
// in WHERE — timestamp is the PK).
func RenderWcMigrationsDelete(ts string) string {
	return fmt.Sprintf("DELETE FROM wc_migrations WHERE timestamp = '%s';", ts)
}

// emitWcMigrationsCreate is the dispatch handler when a plan carries
// the Op variant directly. up = create, down = drop.
func (e Emitter) emitWcMigrationsCreate(_ *planpb.WcMigrationsCreate) (string, string, error) {
	return RenderWcMigrationsCreate(), RenderWcMigrationsDrop(), nil
}

// emitWcMigrationsInsert is the dispatch handler. The Op carries a
// timestamp + hash pre-computed by the CLI; emit just renders the
// statement.
func (e Emitter) emitWcMigrationsInsert(in *planpb.WcMigrationsInsert) (string, string, error) {
	if in.GetTimestamp() == "" {
		return "", "", fmt.Errorf("postgres: WcMigrationsInsert with empty timestamp")
	}
	return RenderWcMigrationsInsert(in.GetTimestamp(), in.GetContentSha256()),
		RenderWcMigrationsDelete(in.GetTimestamp()),
		nil
}
