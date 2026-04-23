package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/application"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
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

	Prev string `name:"prev" placeholder:"PROTO" help:"Path to the previous-revision .proto. When set, the differ computes prev → curr and emits an ALTER migration; absent → initial-migration path (iter-1 behaviour)."`

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
	if len(c.Protos) != 1 {
		return fmt.Errorf("wc generate: iteration-1 compiles exactly one .proto per run (got %d); multi-file support lands in iteration-2 M2", len(c.Protos))
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
	migrationsDir := filepath.Join(outRoot, "migrations")

	ctx := context.Background()
	currSchema, err := loadSchema(ctx, c.Protos[0], c.Imports)
	if err != nil {
		return err
	}
	var prevSchema *irpb.Schema
	if c.Prev != "" {
		prevSchema, err = loadSchema(ctx, c.Prev, c.Imports)
		if err != nil {
			return fmt.Errorf("wc generate: --prev load: %w", err)
		}
	}

	p, err := plan.Diff(prevSchema, currSchema)
	if err != nil {
		return err
	}
	up, down, err := emit.Emit(postgres.Emitter{}, p)
	if err != nil {
		return err
	}

	// Empty plan = skip emit entirely (Open Question #1, resolved SKIP).
	if up == "" && down == "" {
		fmt.Println("wc generate: no changes — nothing to write")
		return nil
	}

	basename := naming.Name(time.Now().UTC())
	if !c.NoAppliedState {
		isInitial := prevSchema == nil
		up, down = wrapAppliedState(up, down, basename, isInitial)
	}
	upPath, downPath, err := writer.Write(migrationsDir, basename, up, down)
	if err != nil {
		return err
	}
	fmt.Printf("wrote:\n  %s\n  %s\n", upPath, downPath)
	return nil
}

// loadSchema runs the loader → ir.Build pipeline for one proto file.
func loadSchema(ctx context.Context, path string, imports []string) (*irpb.Schema, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir, base := filepath.Split(abs)
	importPaths := append([]string{dir}, imports...)
	lf, err := loader.Load(ctx, base, importPaths)
	if err != nil {
		return nil, err
	}
	return ir.Build(lf)
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
