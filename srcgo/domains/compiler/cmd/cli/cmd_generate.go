package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
)

// GenerateCmd implements `wc generate`. The --iteration-1 flag is
// mandatory: it pins the output surface to the iteration-1 shape so that
// future iterations — which will emit more kinds of artifacts — can be
// introduced behind their own flag without silently changing what
// existing invocations produce.
//
// Protos is declared as a variadic arg for forward-compatibility with
// iteration-2's multi-file schemas; iteration-1 rejects anything other
// than exactly one file (see docs/iteration-1-impl.md §Out of scope).
type GenerateCmd struct {
	Iteration1 bool `name:"iteration-1" required:"" help:"Pin the output surface to the iteration-1 shape (Postgres migrations only). Required in this build; later iterations will add more output kinds behind their own flags."`

	Out     string   `short:"o" name:"out" placeholder:"DIR" help:"Output root. Migrations are written to <out>/migrations/. Overrides COMPILER_OUTPUT_DIR; defaults to the Config value (./out)."`
	Imports []string `short:"I" name:"import" placeholder:"DIR" help:"Additional proto import path — repeatable. The input file's directory is always included; this flag is how you point at the w17/*.proto vocabulary and any user-local shared proto trees."`

	Protos []string `arg:"" name:"proto" required:"" help:"Path(s) to .proto schema file(s). Iteration-1 accepts exactly one."`
}

// Run wires the compiler pipeline end-to-end: loader → ir.Build →
// plan.Diff → emit (Postgres) → naming → writer. Every stage surfaces
// *diag.Error untouched so file:line:col + why/fix survive the round
// trip to the user's terminal.
func (c *GenerateCmd) Run() error {
	if !c.Iteration1 {
		// kong's required tag already enforces this, but keep the guard
		// in case the surface shifts (e.g. the flag becomes a mode enum).
		return fmt.Errorf("wc generate: --iteration-1 is required in this build")
	}
	if len(c.Protos) != 1 {
		return fmt.Errorf("wc generate: iteration-1 compiles exactly one .proto per run (got %d); multi-file support lands in iteration-2", len(c.Protos))
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

	absProto, err := filepath.Abs(c.Protos[0])
	if err != nil {
		return fmt.Errorf("wc generate: resolve %s: %w", c.Protos[0], err)
	}
	// Stat upfront so a typo surfaces a clean "not found" instead of
	// protocompile's import-resolution cascade (which reports the last
	// failed import-path lookup and obscures the actual problem).
	if _, err := os.Stat(absProto); err != nil {
		return fmt.Errorf("wc generate: %s: %w", c.Protos[0], err)
	}
	protoDir, protoBase := filepath.Split(absProto)
	importPaths := append([]string{protoDir}, c.Imports...)

	ctx := context.Background()
	lf, err := loader.Load(ctx, protoBase, importPaths)
	if err != nil {
		return err
	}
	schema, err := ir.Build(lf)
	if err != nil {
		return err
	}
	p, err := plan.Diff(nil, schema)
	if err != nil {
		return err
	}
	up, down, err := emit.Emit(postgres.Emitter{}, p)
	if err != nil {
		return err
	}
	basename := naming.Name(time.Now().UTC())
	upPath, downPath, err := writer.Write(migrationsDir, basename, up, down)
	if err != nil {
		return err
	}
	fmt.Printf("wrote:\n  %s\n  %s\n", upPath, downPath)
	return nil
}
