package postgres_test

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
)

// Compile-time DialectCapabilities conformance lives in
// production-side capabilities.go (alongside the Requirement impl)
// — kept there so build failure surfaces before tests even compile.

// Every cap the PG emitter may reference during generation must be
// in the catalog. This list is the authoritative enumeration —
// adding a cap constant in emit/capabilities.go without a catalog
// entry here surfaces as a test failure. The list is sorted by the
// constant's declaration order in emit/capabilities.go for easy
// audit.
var expectedPgCaps = []string{
	// Core SQL types
	emit.CapJSON,
	emit.CapJSONB,
	emit.CapDate,
	emit.CapTime,
	emit.CapTimestamp,
	emit.CapTimestampTZ,
	emit.CapInterval,
	emit.CapNumeric,
	emit.CapDoublePrecision,
	emit.CapUUID,
	emit.CapBYTEA,
	emit.CapBoolean,
	emit.CapArray,
	emit.CapEnumType,
	emit.CapSchemaQualified,
	emit.CapCommentOn,

	// Index + constraint features
	emit.CapIncludeIndex,
	emit.CapPartialIndex,
	emit.CapExpressionIndex,
	emit.CapIdentityColumn,
	emit.CapGeneratedColumn,
	emit.CapOnDeleteRestrict,
	emit.CapOnDeleteSetDefault,
	emit.CapGinIndex,
	emit.CapGistIndex,
	emit.CapBrinIndex,
	emit.CapSpgistIndex,
	emit.CapHashIndex,

	// Transactional DDL
	emit.CapTransactionalDDL,

	// Postgres-specific types
	emit.CapINET,
	emit.CapCIDR,
	emit.CapMACADDR,
	emit.CapTSVECTOR,
	emit.CapTSQUERY,

	// Postgres-specific functions + opclasses
	emit.CapFnGenRandomUUID,
	emit.CapFnUUIDv7,
	emit.CapOpJsonbPathOps,
	emit.CapOpGinTrgmOps,

	// Extensions
	emit.CapExtHstore,
	emit.CapExtCitext,
	emit.CapExtPgTrgm,
	emit.CapExtPgJsonschema,
	emit.CapExtPgUUIDv7,
}

func TestPgCatalogCovers(t *testing.T) {
	e := postgres.Emitter{}
	for _, cap := range expectedPgCaps {
		if _, ok := e.Requirement(cap); !ok {
			t.Errorf("PG catalog missing capability %q — add an entry in emit/postgres/capabilities.go", cap)
		}
	}
}

// Catalog entries must be internally coherent — MinVersion must parse
// as dotted-decimal, Extensions must be non-empty if declared.
func TestPgCatalogEntriesWellFormed(t *testing.T) {
	e := postgres.Emitter{}
	for _, cap := range expectedPgCaps {
		req, _ := e.Requirement(cap)
		if req.MinVersion != "" {
			// Dotted-decimal: all segments must be digits; 1-3 segments.
			segs := strings.Split(req.MinVersion, ".")
			if len(segs) < 1 || len(segs) > 3 {
				t.Errorf("cap %q: MinVersion %q must be 1-3 dotted-decimal segments", cap, req.MinVersion)
			}
			for _, s := range segs {
				if s == "" {
					t.Errorf("cap %q: MinVersion %q has empty segment", cap, req.MinVersion)
				}
				for _, r := range s {
					if r < '0' || r > '9' {
						t.Errorf("cap %q: MinVersion %q contains non-digit", cap, req.MinVersion)
					}
				}
			}
		}
		for _, ext := range req.Extensions {
			if ext == "" {
				t.Errorf("cap %q: empty extension name", cap)
			}
		}
	}
}

// Unknown caps must return ok=false. Contract for callers that need
// to surface "the emitter references a cap the catalog doesn't know
// about" as a compiler bug rather than silently ignoring it.
func TestPgCatalogRejectsUnknown(t *testing.T) {
	e := postgres.Emitter{}
	if _, ok := e.Requirement("DEFINITELY_NOT_A_CAPABILITY"); ok {
		t.Error("expected unknown cap to return ok=false")
	}
	if _, ok := e.Requirement(""); ok {
		t.Error("expected empty cap to return ok=false")
	}
}

// Dialect Name matches between DialectEmitter and DialectCapabilities
// contracts — iter-2 tooling keys both by the same string.
func TestNameMatches(t *testing.T) {
	e := postgres.Emitter{}
	if got, want := e.Name(), "postgres"; got != want {
		t.Errorf("Emitter.Name() = %q, want %q", got, want)
	}
}
