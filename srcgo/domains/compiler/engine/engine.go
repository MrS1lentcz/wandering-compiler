// Package engine is the D30 boundary — the public API of the wc
// compiler engine.
//
// Engine contract (per docs/iteration-2.md D30):
//
//   - Plan(prev, curr, resolutions) → (*Plan, error) is the pure
//     single-entry-point function. No file I/O, no globals, no
//     waiting on user input. (Plan() itself lands in step 6; this
//     file ships the adapter interfaces it relies on.)
//
//   - ResolutionSource supplies decisions to the engine. Impls:
//     Memory (tests), CLI (--decide flags; step 5), Platform (D29
//     hosted tool API, iter-3+).
//
//   - Sink serialises a returned Plan's artifacts. Impls: Memory
//     (tests), Filesystem (today's wc generate --out <dir>; step 5),
//     Platform (registry push; iter-3+).
//
// The engine never imports adapter packages; adapters never reach
// into the engine. That keeps Plan() pure and the adapter story an
// architectural plug-in, not a fork.
package engine

import (
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// ResolutionSource supplies Resolutions to the engine. Implementations
// represent the various places decisions can live — in a test map, in
// CLI flags, in a yaml file, in a platform API.
//
// The engine calls Lookup(id) for each ReviewFinding it encounters;
// when a match exists, the Resolution turns the finding into applied
// SQL (structural Op synthesis + optional CustomSQL). When no match,
// the finding surfaces in the returned Plan.Findings list and the
// caller policy decides what to do.
type ResolutionSource interface {
	// Lookup returns the Resolution matching the given finding ID,
	// or (_, false) if none is known.
	Lookup(findingID string) (*planpb.Resolution, bool)

	// All returns every resolution the source holds. Used by Plan()
	// to embed applied resolutions in the Migration's Manifest (audit
	// trail) and by tools that want to replay a full decision set.
	All() []*planpb.Resolution
}

// Sink serialises Plan artifacts. Implementations decide the physical
// storage — filesystem directory, platform registry, in-memory buffer.
//
// Convention: Write(nil) is a no-op and succeeds. Write of a plan
// that has Findings but no Migrations (nothing to persist) also
// succeeds — the caller's findings-handling path is orthogonal.
type Sink interface {
	// Write persists every Migration in plan. Implementations should
	// be atomic per-migration (a partial write failure on migration N
	// doesn't leave migrations 1..N-1 half-written).
	Write(plan *planpb.Plan) error
}
