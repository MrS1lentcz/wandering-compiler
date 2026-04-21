package sqlite

import "github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"

// sqliteCatalog is the stub companion to the stub Emitter. Shapes the
// SQLite capability surface when real emission lands; for now every
// lookup returns false and the emitter's own EmitOp still errors with
// "not implemented in iteration-1". Serves the same purpose as the
// Emitter stub (AC #6): prove the DialectCapabilities contract isn't
// PG-shaped by accident.
//
// When SQLite emission lands, the map fills in for every cap iter-1
// SQLite supports (TEXT, INTEGER, REAL, BLOB, JSON via json1, FTS5 via
// extension, partial / expression indexes, etc.) and the non-native
// caps (JSONB, UUID, HSTORE, INET, arrays, TSVECTOR) stay absent so
// Requirement returns ok=false — callers know to fall back or error.
var sqliteCatalog = map[string]emit.Requirement{}

// Requirement implements emit.DialectCapabilities. The stub always
// returns ok=false — iter-1 SQLite doesn't emit any SQL, so the
// catalog is intentionally empty. Real entries arrive with the real
// emitter.
func (Emitter) Requirement(cap string) (emit.Requirement, bool) {
	r, ok := sqliteCatalog[cap]
	return r, ok
}
