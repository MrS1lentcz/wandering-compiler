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
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/redis"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/sqlite"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/decide"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine/filesystem"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/naming"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
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

	Decide []string `name:"decide" placeholder:"TABLE.COL=STRATEGY" help:"Repeatable. Resolve a decision-required axis on a column. Syntax: <table>.<column>[:<axis>]=<strategy> or <table>.<column>[:<axis>]=custom:<sql-file>. Strategies: safe, lossless_using (alias using), needs_confirm (alias confirm), drop_and_create (alias drop)."`

	Protos []string `arg:"" name:"proto" required:"" help:"Path(s) to .proto schema file(s). Iteration-1 accepts exactly one."`
}

// Run wires the compiler pipeline end-to-end: loader → ir.Build →
// engine.Plan (classifier + differ + emit) → applied-state wrap →
// FilesystemSink. Every stage surfaces *diag.Error untouched so
// file:line:col + why/fix survive the round trip to the user's
// terminal. Decision-required axes (carrier change / pk flip / enum
// remove / …) now produce ReviewFindings the user resolves via
// --decide (step 7 work; for now they error with a helpful list).
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

	cls, err := classifier.Load(cfg.ClassificationDir)
	if err != nil {
		return fmt.Errorf("wc generate: load classifier from %s: %w", cfg.ClassificationDir, err)
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

	// Parse --decide flags (may be empty). Deferred loading of custom
	// SQL files happens here — if a flag references a missing file,
	// we surface the error before the expensive pipeline runs.
	decisions, err := decide.Parse(c.Decide, decide.DefaultSQLLoader)
	if err != nil {
		return fmt.Errorf("wc generate: --decide: %w", err)
	}

	// Probe Plan to discover decision-required findings. Always runs
	// even when --decide is empty so the error path can show findings.
	probe, err := engine.Plan(prevSchema, currSchema, cls, nil, pickEmitter)
	if err != nil {
		return fmt.Errorf("wc generate: %w", err)
	}
	resolutions := decisions.ResolveAll(probe.Findings)

	// Final Plan with applied resolutions. If decisions covered every
	// finding, Plan.Findings will be empty.
	result, err := engine.Plan(prevSchema, currSchema, cls, resolutions, pickEmitter)
	if err != nil {
		return fmt.Errorf("wc generate: %w", err)
	}
	if len(result.Findings) > 0 {
		printFindings(result.Findings)
		return fmt.Errorf("wc generate: %d unresolved decision(s); see above", len(result.Findings))
	}
	if len(result.Migrations) == 0 {
		fmt.Println("wc generate: no changes — nothing to write")
		return nil
	}

	// Post-process: wrap wc_migrations applied-state (D27) on every
	// Postgres migration. Iter-1 default-bucket (nil Connection)
	// historically skipped this; preserved here for byte-compat with
	// existing goldens.
	basename := naming.Name(time.Now().UTC())
	isInitial := prevSchema == nil
	for _, m := range result.Migrations {
		if c.NoAppliedState {
			continue
		}
		if m.GetConnection() == nil || m.GetConnection().GetDialect() != irpb.Dialect_POSTGRES {
			continue
		}
		up, down := wrapAppliedState(m.GetUpSql(), m.GetDownSql(), basename, isInitial)
		m.UpSql = up
		m.DownSql = down
	}

	sink := &filesystem.Sink{OutRoot: outRoot, Basename: basename}
	if err := sink.Write(result); err != nil {
		return fmt.Errorf("wc generate: write: %w", err)
	}
	for _, m := range result.Migrations {
		key := ""
		if m.GetConnection() != nil {
			key = strings.ToLower(m.GetConnection().GetDialect().String()) + "-" + m.GetConnection().GetVersion()
		}
		fmt.Printf("wrote [%s]\n", key)
	}
	return nil
}

// printFindings writes a helpful decision-needed message for every
// unresolved finding. Format: one stanza per finding with axis, column
// reference, proposed strategy, rationale, and the --decide snippet
// that would unblock it.
func printFindings(findings []*planpb.ReviewFinding) {
	fmt.Fprintln(os.Stderr, "wc generate: migration blocked on pending decisions:")
	for _, f := range findings {
		col := f.GetColumn()
		fmt.Fprintf(os.Stderr, "\n  %s.%s (#%d) — %s\n",
			col.GetTableName(), col.GetColumnName(), col.GetFieldNumber(), f.GetAxis())
		fmt.Fprintf(os.Stderr, "    proposed: %s\n", f.GetProposed())
		if f.GetRationale() != "" {
			fmt.Fprintf(os.Stderr, "    why: %s\n", f.GetRationale())
		}
		fmt.Fprintf(os.Stderr, "    resolve: --decide %s.%s=<strategy>  (see `wc generate --help`)\n",
			col.GetTableName(), col.GetColumnName())
	}
	fmt.Fprintln(os.Stderr)
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
