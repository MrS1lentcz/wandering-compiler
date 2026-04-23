package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/application"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/redis"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/sqlite"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/naming"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/plan"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/writer"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// GenerateCmd implements `wc generate`. The --iteration-1 flag is
// mandatory: it pins the output surface to the iteration-1 shape so that
// future iterations — which will emit more kinds of artifacts — can be
// introduced behind their own flag without silently changing what
// existing invocations produce.
//
// Iteration-2 M1 adds --prev for alter-diff: when present, the differ
// computes prev → curr and emits an ALTER migration; when absent,
// behaviour falls back to the iter-1 initial-migration path.
type GenerateCmd struct {
	Iteration1 bool `name:"iteration-1" required:"" help:"Pin the output surface to the iteration-1 shape (Postgres migrations only). Required in this build; later iterations will add more output kinds behind their own flags."`

	Out     string   `short:"o" name:"out" placeholder:"DIR" help:"Output root. Migrations are written to <out>/migrations/. Overrides COMPILER_OUTPUT_DIR; defaults to the Config value (./out)."`
	Imports []string `short:"I" name:"import" placeholder:"DIR" help:"Additional proto import path — repeatable. The input file's directory is always included; this flag is how you point at the w17/*.proto vocabulary and any user-local shared proto trees."`

	Prev    string   `name:"prev" placeholder:"PROTO" help:"Path to the previous-revision .proto. When set, the differ computes prev → curr and emits an ALTER migration; absent → initial-migration path (iter-1 behaviour)."`
	PrevSet []string `name:"prev-set" placeholder:"PROTO" help:"Repeatable. Multi-file previous-revision set — one entry per prev-side .proto. Takes precedence over --prev when both are set."`

	NoAppliedState bool `name:"no-applied-state" hidden:"" help:"Skip emitting the wc_migrations bookkeeping (D27). For one-off scratch DBs only."`

	Protos []string `arg:"" name:"proto" required:"" help:"Path(s) to .proto schema file(s). Iteration-1 accepts exactly one."`
}

// Run wires the compiler pipeline end-to-end: loader → ir.Build →
// plan.Diff → emit (Postgres) → applied-state wrap → naming → writer.
// Every stage surfaces *diag.Error untouched so file:line:col + why/fix
// survive the round trip to the user's terminal.
func (c *GenerateCmd) Run() error {
	if !c.Iteration1 {
		return fmt.Errorf("wc generate: --iteration-1 is required in this build")
	}
	if len(c.Protos) == 0 {
		return fmt.Errorf("wc generate: at least one .proto path is required")
	}

	cfg := compiler.NewConfigFromEnv()
	app, closer, err := application.New(cfg)
	if err != nil {
		return fmt.Errorf("wc generate: init application: %w", err)
	}
	defer func() { _ = closer.Close() }()

	outRoot := c.Out
	if outRoot == "" {
		outRoot = app.OutputDir()
	}

	ctx := context.Background()
	currSchema, err := loadSchemaSet(ctx, c.Protos, c.Imports)
	if err != nil {
		return err
	}
	var prevSchema *irpb.Schema
	if len(c.PrevSet) > 0 {
		prevSchema, err = loadSchemaSet(ctx, c.PrevSet, c.Imports)
		if err != nil {
			return fmt.Errorf("wc generate: --prev load: %w", err)
		}
	} else if c.Prev != "" {
		prevSchema, err = loadSchemaSet(ctx, []string{c.Prev}, c.Imports)
		if err != nil {
			return fmt.Errorf("wc generate: --prev load: %w", err)
		}
	}

	// Bucket tables by effective connection (D26). Each bucket
	// produces one migration stream emitted by that dialect's emitter
	// to `<out>/migrations/<dialect>-<version>/`. Tables without an
	// explicit connection go to the default bucket (iter-1 layout:
	// bare `<out>/migrations/` + postgres emitter).
	buckets, order := bucketByConnection(prevSchema, currSchema)
	if len(buckets) == 0 {
		fmt.Println("wc generate: no tables to process")
		return nil
	}

	basename := naming.Name(time.Now().UTC())
	wroteAny := false
	for _, key := range order {
		bkt := buckets[key]
		// Classifier wired at step 6 (engine.Plan top-level); for now
		// pass nil — Diff falls back to plain-error behaviour on
		// REFUSE axes, preserving pre-D30 CLI UX until Resolution
		// source wiring lands.
		result, err := plan.Diff(bkt.prev, bkt.curr, nil)
		if err != nil {
			return fmt.Errorf("wc generate: bucket %q: %w", key, err)
		}
		emitter, err := pickEmitter(bkt.conn)
		if err != nil {
			return fmt.Errorf("wc generate: bucket %q: %w", key, err)
		}
		up, down, err := emit.Emit(emitter, result.Plan)
		if err != nil {
			return fmt.Errorf("wc generate: bucket %q: %w", key, err)
		}
		if up == "" && down == "" {
			fmt.Printf("wc generate [%s]: no changes — skipping\n", key)
			continue
		}
		if !c.NoAppliedState && bkt.conn != nil && bkt.conn.GetDialect() == irpb.Dialect_POSTGRES {
			// Applied-state tracking is PG-only for now; other
			// dialects (Redis, SQLite) have their own bookkeeping
			// shape or skip entirely (Redis does lazy ZADD).
			isInitial := bkt.prev == nil
			up, down = wrapAppliedState(up, down, basename, isInitial)
		}
		dir := filepath.Join(outRoot, "migrations")
		if bkt.conn != nil {
			dir = filepath.Join(dir, connectionDirKey(bkt.conn))
		}
		upPath, downPath, err := writer.Write(dir, basename, up, down)
		if err != nil {
			return err
		}
		fmt.Printf("wrote [%s]:\n  %s\n  %s\n", key, upPath, downPath)
		wroteAny = true
	}
	if !wroteAny {
		fmt.Println("wc generate: no changes — nothing written")
	}
	return nil
}

// bucket carries one per-connection slice of the prev/curr schema.
type bucket struct {
	conn *irpb.Connection // nil = default (iter-1 compat path)
	prev *irpb.Schema
	curr *irpb.Schema
}

// bucketByConnection groups tables from prev + curr by their
// effective connection. Returns (buckets, orderedKeys) — ordered keys
// are deterministic: default ("") first, then alpha on
// `<dialect>-<version>`.
func bucketByConnection(prev, curr *irpb.Schema) (map[string]*bucket, []string) {
	buckets := map[string]*bucket{}
	keyOf := func(t *irpb.Table) (string, *irpb.Connection) {
		c := t.GetConnection()
		if c == nil {
			return "", nil
		}
		return connectionDirKey(c), c
	}
	addTable := func(side string, t *irpb.Table) {
		key, conn := keyOf(t)
		b, ok := buckets[key]
		if !ok {
			b = &bucket{conn: conn, prev: &irpb.Schema{}, curr: &irpb.Schema{}}
			buckets[key] = b
		}
		switch side {
		case "prev":
			b.prev.Tables = append(b.prev.Tables, t)
		case "curr":
			b.curr.Tables = append(b.curr.Tables, t)
		}
	}
	for _, t := range prev.GetTables() {
		addTable("prev", t)
	}
	for _, t := range curr.GetTables() {
		addTable("curr", t)
	}
	// prev=nil means initial migration — every bucket's prev stays
	// zero-value *irpb.Schema{}, which plan.Diff compares as no prev.
	// But we need the nil sentinel to trigger the initial path.
	if prev == nil {
		for _, b := range buckets {
			b.prev = nil
		}
	}
	// Empty-curr buckets are also valid (full teardown of a dialect).
	// If a bucket exists only on prev side, curr stays zero-valued.
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return buckets, keys
}

// connectionDirKey derives the `<dialect>-<version>` directory name
// for the output path per D26. Lower-kebab on the dialect enum name.
func connectionDirKey(c *irpb.Connection) string {
	return strings.ToLower(c.GetDialect().String()) + "-" + c.GetVersion()
}

// pickEmitter selects the emit.DialectEmitter for the connection's
// dialect. Absent connection (iter-1 default-path case) → postgres
// (matches iter-1 behaviour). Unknown dialect surfaces a clear error
// rather than defaulting silently.
func pickEmitter(conn *irpb.Connection) (emit.DialectEmitter, error) {
	if conn == nil {
		return postgres.Emitter{}, nil
	}
	switch conn.GetDialect() {
	case irpb.Dialect_POSTGRES:
		return postgres.Emitter{}, nil
	case irpb.Dialect_MYSQL:
		return nil, fmt.Errorf("wc generate: dialect MYSQL is not yet implemented (tracked as iter-2 M4)")
	case irpb.Dialect_SQLITE:
		return sqlite.Emitter{}, nil
	case irpb.Dialect_REDIS:
		return redis.Emitter{}, nil
	}
	return nil, fmt.Errorf("wc generate: unknown connection dialect %v", conn.GetDialect())
}

// loadSchemaSet runs loader.LoadMany → ir.BuildMany over a proto set.
// The first file's directory seeds the import paths so single-file
// callers keep working unchanged; each additional file contributes its
// own directory to the import set as well.
func loadSchemaSet(ctx context.Context, paths, imports []string) (*irpb.Schema, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no proto paths given")
	}
	absPaths := make([]string, 0, len(paths))
	seenDirs := map[string]bool{}
	importPaths := append([]string{}, imports...)
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", p, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		dir, _ := filepath.Split(abs)
		if !seenDirs[dir] {
			importPaths = append([]string{dir}, importPaths...)
			seenDirs[dir] = true
		}
		absPaths = append(absPaths, abs)
	}
	bases := make([]string, 0, len(absPaths))
	for _, abs := range absPaths {
		_, base := filepath.Split(abs)
		bases = append(bases, base)
	}
	files, err := loader.LoadMany(ctx, bases, importPaths)
	if err != nil {
		return nil, err
	}
	return ir.BuildMany(files)
}

// wrapAppliedState injects the wc_migrations CREATE/INSERT (up) and
// matching DELETE/DROP (down) into the BEGIN…COMMIT-wrapped SQL emit
// produced. Hash is sha256 of the up body BEFORE the INSERT statement
// itself is appended (D27 invariant: the hash-carrying statement
// can't include itself in the hash it stores).
//
// Layout for the initial migration:
//
//	BEGIN;
//	  CREATE TABLE wc_migrations (...);
//	  <plan ops...>
//	  INSERT INTO wc_migrations VALUES (ts, NOW(), '\xHASH');
//	COMMIT;
//
// For follow-up migrations: omit the CREATE TABLE / DROP TABLE pair.
func wrapAppliedState(up, down, ts string, isInitial bool) (string, string) {
	if up == "" && down == "" {
		return up, down
	}
	upBody, hadUp := splitBeginCommit(up)
	downBody, hadDown := splitBeginCommit(down)

	if isInitial {
		upBody = postgres.RenderWcMigrationsCreate() + "\n\n" + upBody
		// Down: the existing emit already drops everything; append the
		// wc_migrations DROP at the very end (after all reverse drops).
		if downBody != "" {
			downBody = downBody + "\n\n" + postgres.RenderWcMigrationsDrop()
		} else {
			downBody = postgres.RenderWcMigrationsDrop()
		}
	}

	// Compute hash over the up body (excluding the INSERT statement
	// itself). Then append the INSERT.
	hash := sha256.Sum256([]byte(upBody))
	upBody = upBody + "\n\n" + postgres.RenderWcMigrationsInsert(ts, hash[:])
	// Down: prepend the DELETE so a partial-failure rollback gets the
	// state row out before any other reverse ops attempt to run.
	downBody = postgres.RenderWcMigrationsDelete(ts) + "\n\n" + downBody

	wrappedUp := upBody
	if hadUp {
		wrappedUp = "BEGIN;\n\n" + upBody + "\n\nCOMMIT;\n"
	}
	wrappedDown := downBody
	if hadDown {
		wrappedDown = "BEGIN;\n\n" + downBody + "\n\nCOMMIT;\n"
	}
	return wrappedUp, wrappedDown
}

// splitBeginCommit strips the BEGIN/COMMIT wrapper that emit.Emit
// adds around plan ops. Returns (body, true) when both lines are
// found; returns (input, false) otherwise so callers can fall back
// to no-wrap formatting.
func splitBeginCommit(sql string) (string, bool) {
	const begin = "BEGIN;\n\n"
	const commit = "\n\nCOMMIT;\n"
	if !strings.HasPrefix(sql, begin) {
		return sql, false
	}
	if !strings.HasSuffix(sql, commit) {
		return sql, false
	}
	return sql[len(begin) : len(sql)-len(commit)], true
}
