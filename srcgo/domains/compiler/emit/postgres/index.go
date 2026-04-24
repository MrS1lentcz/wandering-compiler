package postgres

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// renderIndexes emits separate CREATE [UNIQUE] INDEX statements for a table,
// returning (stmts, names, err). names is the ordered list for DROP INDEX
// generation in the down block (caller reverses it).
//
// Index names are resolved in ir.Build (see ir/names.go.derivedIndexName
// and the finalisation pass in ir/build.go.buildTable) — every Index
// arrives here with Name already set. Name collisions and identifier-
// length violations are rejected at IR time.
//
// D23 — SQL shape:
//
//	CREATE [UNIQUE] INDEX <name>
//	ON <qualified_table>
//	[USING <method>]                 -- omitted for BTREE / UNSPECIFIED
//	(<field>[ DESC][ NULLS FIRST|LAST][ <opclass>], ...)
//	[INCLUDE (<col>, ...)]
//	[WITH (<k>=<v>, ...)]            -- deterministic (sorted by key)
//
// Partial (WHERE) and expression indexes land on (w17.db.table).raw_indexes
// until the DQL iteration structures them.
func renderIndexes(t *irpb.Table, colByProto map[string]*irpb.Column, usage *emit.Usage) (stmts []string, names []string, err error) {
	for i, idx := range t.GetIndexes() {
		if idx.GetName() == "" {
			return nil, nil, fmt.Errorf("postgres: table %s: indexes[%d] has empty name (ir.Build was supposed to resolve it)", t.GetName(), i)
		}
		fieldClauses := make([]string, 0, len(idx.GetFields()))
		for _, f := range idx.GetFields() {
			c, ok := colByProto[f.GetName()]
			if !ok {
				return nil, nil, fmt.Errorf("postgres: table %s: indexes[%d] references unknown proto field %q (builder invariant violated)", t.GetName(), i, f.GetName())
			}
			fieldClauses = append(fieldClauses, renderIndexField(c.GetName(), f))
		}
		sqlInclude := make([]string, 0, len(idx.GetInclude()))
		for _, f := range idx.GetInclude() {
			c, ok := colByProto[f]
			if !ok {
				return nil, nil, fmt.Errorf("postgres: table %s: indexes[%d] INCLUDE references unknown proto field %q (builder invariant violated)", t.GetName(), i, f)
			}
			sqlInclude = append(sqlInclude, c.GetName())
		}

		var b strings.Builder
		if idx.GetUnique() {
			b.WriteString("CREATE UNIQUE INDEX ")
		} else {
			b.WriteString("CREATE INDEX ")
		}
		// CREATE INDEX name is bare per PG syntax — index lives in the
		// schema of its table automatically. The table in the ON clause
		// is schema-qualified under D19 SCHEMA mode (PREFIX mode already
		// baked the prefix into both identifier names at IR time).
		fmt.Fprintf(&b, "%s ON %s", idx.GetName(), qualifiedTable(t))
		if method := indexMethodKeyword(idx.GetMethod()); method != "" {
			recordIndexMethodCap(usage, idx.GetMethod())
			fmt.Fprintf(&b, " USING %s", method)
		}
		fmt.Fprintf(&b, " (%s)", strings.Join(fieldClauses, ", "))
		if len(sqlInclude) > 0 {
			usage.Use(emit.CapIncludeIndex)
			fmt.Fprintf(&b, " INCLUDE (%s)", strings.Join(sqlInclude, ", "))
		}
		if storage := renderStorageOptions(idx.GetStorage()); storage != "" {
			fmt.Fprintf(&b, " WITH (%s)", storage)
		}
		b.WriteString(";")

		stmts = append(stmts, b.String())
		names = append(names, idx.GetName())
	}
	return stmts, names, nil
}

// recordIndexMethodCap records the capability ID for a non-default
// index method. BTREE / UNSPECIFIED is the emitter's default and maps
// to no cap entry.
func recordIndexMethodCap(usage *emit.Usage, m irpb.IndexMethod) {
	switch m {
	case irpb.IndexMethod_IDX_GIN:
		usage.Use(emit.CapGinIndex)
	case irpb.IndexMethod_IDX_GIST:
		usage.Use(emit.CapGistIndex)
	case irpb.IndexMethod_IDX_BRIN:
		usage.Use(emit.CapBrinIndex)
	case irpb.IndexMethod_IDX_HASH:
		usage.Use(emit.CapHashIndex)
	case irpb.IndexMethod_IDX_SPGIST:
		usage.Use(emit.CapSpgistIndex)
	}
}

// renderIndexField renders a single entry in the index column list:
//
//	<col>[ DESC][ NULLS FIRST|LAST][ <opclass>]
//
// ASC is PG's default and never rendered explicitly — staying terse
// when the author hasn't opted out matches Django / SQLAlchemy output.
func renderIndexField(sqlCol string, f *irpb.IndexField) string {
	var b strings.Builder
	b.WriteString(sqlCol)
	// Opclass comes BEFORE ASC/DESC/NULLS per PG syntax:
	//   CREATE INDEX … (col text_pattern_ops DESC NULLS FIRST)
	if op := f.GetOpclass(); op != "" {
		b.WriteString(" ")
		b.WriteString(op)
	}
	if f.GetDesc() {
		b.WriteString(" DESC")
	}
	switch f.GetNulls() {
	case irpb.NullsOrder_NULLS_FIRST:
		b.WriteString(" NULLS FIRST")
	case irpb.NullsOrder_NULLS_LAST:
		b.WriteString(" NULLS LAST")
	}
	return b.String()
}

// indexMethodKeyword returns the `USING <method>` token for non-default
// methods or "" for BTREE / unspecified (PG treats both as btree; we
// omit the clause to keep CREATE INDEX output terse and matching the
// goldens of simple cases).
func indexMethodKeyword(m irpb.IndexMethod) string {
	switch m {
	case irpb.IndexMethod_IDX_GIN:
		return "gin"
	case irpb.IndexMethod_IDX_GIST:
		return "gist"
	case irpb.IndexMethod_IDX_BRIN:
		return "brin"
	case irpb.IndexMethod_IDX_HASH:
		return "hash"
	case irpb.IndexMethod_IDX_SPGIST:
		return "spgist"
	}
	return "" // BTREE / UNSPECIFIED
}

// renderStorageOptions serialises the free-form storage map into the
// `k1=v1, k2=v2` body of a WITH clause. Keys are sorted
// alphabetically so goldens stay deterministic across runs (Go map
// iteration order is randomised). Empty input returns ""; callers
// skip the WITH clause on empty.
//
// Value rendering: passes through verbatim. Authors providing strings
// with embedded commas / equals / quotes are responsible — this is
// the escape-hatch contract for free-form options (same as raw_checks
// / raw_indexes bodies). When typed per-method options graduate (see
// D9-style graduation path), this helper's surface narrows.
func renderStorageOptions(storage map[string]string) string {
	if len(storage) == 0 {
		return ""
	}
	keys := make([]string, 0, len(storage))
	for k := range storage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, storage[k]))
	}
	return strings.Join(parts, ", ")
}
