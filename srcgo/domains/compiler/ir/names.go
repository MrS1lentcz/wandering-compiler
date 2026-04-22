package ir

import (
	"fmt"
	"strings"
	"unicode"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// camelToSnake converts a proto CamelCase identifier to snake_case. Used
// by ir.Build to derive the default SQL table name from a message's
// local name when the author did not set (w17.db.table).name (D21).
// No pluralisation — that would need English-only rules (y→ies,
// Person→people, Child→children, Datum→data, …) which don't survive
// contact with real vocabulary. Matches what SQLAlchemy does by default
// (author writes snake-case in Python, we do the conversion from proto
// CamelCase).
//
// Insert `_`:
//   - between a lowercase/digit and the next uppercase rune
//     (`ProductCategory` → `product_category`, `URL1Parser` →
//     `url1_parser`)
//   - between two uppercase runes when the second is followed by a
//     lowercase rune (acronym-then-word boundary,
//     `URLParser` → `url_parser`, `DashboardURLField` →
//     `dashboard_url_field`)
//
// ASCII-only input in practice (proto message names are identifiers in
// the proto grammar, which restricts to [A-Za-z_][0-9A-Za-z_]*); the
// unicode package calls cover the corner case without adding a separate
// ASCII path.
func camelToSnake(name string) string {
	if name == "" {
		return ""
	}
	runes := []rune(name)
	var b strings.Builder
	b.Grow(len(name) + 4)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			needsSeparator := unicode.IsLower(prev) || unicode.IsDigit(prev) ||
				(unicode.IsUpper(prev) && unicode.IsLower(next))
			if needsSeparator {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// validateIdentifier checks a single SQL identifier — table name, column
// name, index name, or CHECK constraint name — against two failure modes
// both Postgres inflicts silently:
//
//  1. Length > 63 bytes. Postgres's NAMEDATALEN is 64 (63 bytes + NUL);
//     longer names are truncated without warning, which lets two distinct
//     source identifiers collide in pg_class / pg_constraint and break
//     subsequent DDL in unpredictable ways. We reject at IR time with a
//     clear diag instead.
//  2. Clashes with a Postgres reserved keyword. Unquoted reserved words
//     produce `syntax error at or near "…"` at apply time, which points
//     at the SQL line and leaves users hunting for the cause. Emitting
//     always-quoted identifiers is the proper long-term fix; until then
//     (parked for iter-2), reject reserved names up front so authors
//     rename before apply.
//
// Returns nil on valid identifier; otherwise returns a brief phrase
// describing the problem for inclusion in a diag.Error. The caller
// attaches file:line:col + fix: via diag.Atf.
func validateIdentifier(name string) string {
	if name == "" {
		return "identifier is empty"
	}
	if len(name) > 63 {
		return fmt.Sprintf("identifier %q is %d bytes long — Postgres NAMEDATALEN caps identifiers at 63 bytes (silent truncation risks collision)", name, len(name))
	}
	if _, reserved := pgReservedKeywords[strings.ToLower(name)]; reserved {
		return fmt.Sprintf("identifier %q is a Postgres reserved keyword (category R) — unquoted use fails at apply with a syntax error", name)
	}
	return ""
}

// derivedIndexName mirrors the algorithm emit/postgres/index.go used to
// apply before names moved to IR. Exposed here so ir.Build can resolve
// every index's name up front (required for collision detection and for
// identifier-length validation).
func derivedIndexName(table string, sqlCols []string, unique bool) string {
	suffix := "idx"
	if unique {
		suffix = "uidx"
	}
	return fmt.Sprintf("%s_%s_%s", table, strings.Join(sqlCols, "_"), suffix)
}

// derivedCheckName mirrors emit/postgres/check.go's naming scheme. The
// variant suffix ("blank" / "len" / "range" / "format" / "choices") is
// fixed per CheckVariant so two CHECKs of the same variant on the same
// column never collide (ir.attachChecks only synthesises one per
// variant per column). Exposed here for length-validation only — the
// emitter still builds the final string; this helper's output must
// match exactly.
func derivedCheckName(table, sqlCol string, ck *irpb.Check) string {
	return fmt.Sprintf("%s_%s_%s", table, sqlCol, checkSuffix(ck))
}

// applyPrefix returns the post-prefix effective SQL identifier for a
// module-level PREFIX namespace. Empty prefix = no prefix. Used to
// derive the final CREATE TABLE / CREATE INDEX name that actually
// lands in PG so NAMEDATALEN validation can run on the form the DB
// will see. Called at IR time (identifier length validation) and at
// emit time (identifier rendering) — single source of truth for the
// prefix-concatenation shape.
//
// Schema mode (namespace lives in a separate slot, not concatenated
// into the name) uses the bare identifier — callers route the
// qualification through the emitter's schema helper instead.
func applyPrefix(prefix, bare string) string {
	// Preserve empty bare: author-supplied names are validated
	// downstream against `"" → diag error`; prefixing would mask the
	// empty state (catalog_ would pass both the emptiness check and
	// identifier validation) and land a malformed name in SQL.
	if prefix == "" || bare == "" {
		return bare
	}
	return prefix + "_" + bare
}

func checkSuffix(ck *irpb.Check) string {
	switch ck.GetVariant().(type) {
	case *irpb.Check_Length:
		return "len"
	case *irpb.Check_Blank:
		return "blank"
	case *irpb.Check_Range:
		return "range"
	case *irpb.Check_Regex:
		return "format"
	case *irpb.Check_Choices:
		return "choices"
	}
	return "check"
}

// pgReservedSchemas names PostgreSQL system schemas that author code must
// not settle into. `pg_catalog` + `pg_toast` + `information_schema` are
// explicitly reserved; anything starting with `pg_` is reserved by
// convention for PostgreSQL system use (catalogs, toast, temp). Rejecting
// these at IR time keeps authors from accidentally landing tables in
// system schemas (silently creates user tables in pg_catalog-adjacent
// space, which is legal but strongly discouraged).
//
// This list is PG-specific and lives here because iter-1 is PG-only.
// When MySQL / SQLite emitters land, the reserved-namespace check should
// graduate to a dialect-indexed map (MySQL reserves `mysql`,
// `performance_schema`, etc.; SQLite has no schema concept).
func isReservedPgSchema(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "pg_") {
		return true
	}
	switch lower {
	case "information_schema":
		return true
	}
	return false
}

// pgReservedKeywords is the Postgres "category R" reserved-word list —
// words that produce a syntax error when used unquoted as a column or
// table name. Sourced from PostgreSQL's SQL Key Words appendix (column
// "PostgreSQL" = R). Non-reserved categories (U / T / C) are omitted
// because they're safe unquoted in identifier position.
//
// The list is intentionally frozen to PG 14+'s reserved set; newer
// reserved words arrive rarely and the test-apply harness would catch
// them as apply-time regressions.
var pgReservedKeywords = map[string]struct{}{
	"all": {}, "analyse": {}, "analyze": {}, "and": {}, "any": {}, "array": {},
	"as": {}, "asc": {}, "asymmetric": {}, "authorization": {},
	"binary": {}, "both": {},
	"case": {}, "cast": {}, "check": {}, "collate": {}, "collation": {},
	"column": {}, "concurrently": {}, "constraint": {}, "create": {},
	"cross": {}, "current_catalog": {}, "current_date": {},
	"current_role": {}, "current_schema": {}, "current_time": {},
	"current_timestamp": {}, "current_user": {},
	"default": {}, "deferrable": {}, "desc": {}, "distinct": {}, "do": {},
	"else": {}, "end": {}, "except": {},
	"false": {}, "fetch": {}, "for": {}, "foreign": {}, "freeze": {},
	"from": {}, "full": {},
	"grant": {}, "group": {},
	"having": {},
	"ilike": {}, "in": {}, "initially": {}, "inner": {}, "intersect": {},
	"into": {}, "is": {}, "isnull": {},
	"join": {},
	"lateral": {}, "leading": {}, "left": {}, "like": {}, "limit": {},
	"localtime": {}, "localtimestamp": {},
	"natural": {}, "not": {}, "notnull": {}, "null": {},
	"offset": {}, "on": {}, "only": {}, "or": {}, "order": {}, "outer": {},
	"overlaps": {},
	"placing": {}, "primary": {},
	"references": {}, "returning": {}, "right": {},
	"select": {}, "session_user": {}, "similar": {}, "some": {},
	"symmetric": {}, "system_user": {},
	"table": {}, "tablesample": {}, "then": {}, "to": {}, "trailing": {},
	"true": {},
	"union": {}, "unique": {}, "user": {}, "using": {},
	"variadic": {}, "verbose": {},
	"when": {}, "where": {}, "window": {}, "with": {},
}
