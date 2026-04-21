// Package application is the DI facade of the compiler domain — it owns
// process-wide, pre-configured state (resolved output directory today;
// platform gRPC clients, dialect plug-ins, build cache in later
// iterations) and exposes them through compiler.Application. Nothing
// per-request or derived from a single CLI invocation belongs here —
// see docs/conventions-global/go.md §application.
package application

import (
	"io"
	"log"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
)

// app is the concrete Application facade. Each getter contains a
// fail-fast guard: if a factory wasn't supplied (or returned nil), the
// process dies immediately rather than producing half-working output.
type app struct {
	cfg     compiler.Config
	output  compiler.OutputModule
	closers []io.Closer
}

func (a *app) Config() compiler.Config { return a.cfg }

func (a *app) OutputDir() string {
	if a.output == nil {
		log.Fatal("compiler: output module is not initialized")
	}
	return a.output.OutputDir()
}

// Close releases module resources in reverse registration order.
// Iteration-1 has no resources to release (the output module is a pure
// value); the plumbing is in place so future modules — platform gRPC
// client, proto parser cache, … — drop in without churn.
func (a *app) Close() error {
	var firstErr error
	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
