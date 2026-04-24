// Package sqlite is the iteration-1 stub implementation of
// emit.DialectEmitter. Every EmitOp returns a "not implemented" error.
//
// The stub exists to satisfy acceptance criterion #6: a second dialect
// emitter must compile against the same DialectEmitter contract as the
// postgres one. If the interface accidentally names a PG-only concept —
// or grows a method whose signature only makes sense for PG — this stub's
// compile surfaces it while iteration-1 is still small and cheap to
// refactor.
//
// Real SQLite output arrives when a pilot needs it; at that point this
// file's body gets replaced (the public surface stays). See
// docs/iteration-1.md D4 and docs/iteration-1-impl.md M5.
package sqlite

import (
	"errors"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Emitter is the SQLite DialectEmitter stub.
type Emitter struct{}

// Name returns the stable dialect identifier.
func (Emitter) Name() string { return "sqlite" }

// EmitOp is intentionally unimplemented in iteration-1. The error
// message is the same regardless of op variant — consumers that
// branch on dialect (CLI --dialect flag, later back-compat lint)
// surface it to the user verbatim.
func (Emitter) EmitOp(_ *planpb.Op, _ *emit.Usage) (up string, down string, err error) {
	return "", "", errors.New("sqlite emitter: not implemented in iteration-1")
}
