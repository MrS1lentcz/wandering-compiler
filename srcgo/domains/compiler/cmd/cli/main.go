// Command wc is the wandering-compiler CLI. It parses annotated .proto
// schemas and emits the artifacts a service actually needs — today that
// is one pair of plain-SQL migrations; later iterations add proto stubs,
// gRPC gateways, JS/TS clients, admin, Docker/k8s scaffolding. The CLI
// itself is a thin wiring layer over the compiler domain's packages
// (loader, ir, plan, emit, naming, writer): no business logic lives in
// this directory — see docs/conventions-global/go.md §CLI.
package main

import (
	"os"

	"github.com/alecthomas/kong"
	"github.com/willabides/kongplete"
)

// cli is the root command struct. Each subcommand lives in its own
// cmd_<name>.go file — this file is restricted to the root assembly plus
// the autocomplete installer, per convention.
var cli struct {
	Generate GenerateCmd `cmd:"" help:"Generate artifacts from a .proto schema (iteration-1: Postgres migrations)."`

	InstallCompletions kongplete.InstallCompletions `cmd:"" help:"Install shell completions for wc."`
}

func main() {
	parser := kong.Must(&cli,
		kong.Name("wc"),
		kong.Description("wandering-compiler — declarative schema → dialect-aware SQL migrations (iteration-1)."),
		kong.UsageOnError(),
	)

	// Handle the `install-completions` subcommand before normal dispatch.
	kongplete.Complete(parser)

	ctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

	err = ctx.Run()
	ctx.FatalIfErrorf(err)
}
