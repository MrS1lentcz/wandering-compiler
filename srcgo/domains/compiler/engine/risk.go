package engine

// D35 — deterministic risk analyzer. Walks a MigrationPlan's Ops +
// the plan.Diff ReviewFindings, emits RiskFinding entries for every
// op matching a profile in the static riskProfiles table. No DB
// access; every assessment comes from the migration's shape alone.
//
// Emitted unconditionally (no opt-out flag). Platform review UI and
// senior engineers consume the structured list from
// Manifest.RiskFindings; the sink additionally prepends an
// `-- RISK:` comment block to up.sql / down.sql so the warnings
// are visible at apply time even without tooling that parses the
// manifest.
//
// What this file does NOT do:
//   - Connect to any database.
//   - Guess row counts, table sizes, or estimated apply time. All
//     three scale with data volume which is impossible to know at
//     compile time without connecting to a target DB. Instead the
//     profile surfaces `scales_with` so reviewers can apply their
//     own knowledge of table size.
//   - Rank severity across ops. Every profile entry is reported
//     independently; a migration with 10 MEDIUMs isn't aggregated
//     into a single HIGH.
//
// Adding a new profile: (1) append to riskProfiles below, (2) extend
// analyzeRisks dispatch if a new axis / FactChange variant appears,
// (3) add a unit test pinning the new profile's fields.

import (
	"fmt"
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// riskProfile captures the static attributes of one op kind.
// "static" = derived from PG semantics, not from any runtime input.
type riskProfile struct {
	opKind         string
	severity       string
	rewrite        bool
	lockType       string
	scalesWith     string
	rationale      string
	recommendation string
}

// riskProfiles — authoritative table of what's risky. Keyed by a
// compound "<source>_<case>" string so dispatch is exact-match
// rather than rule-chain (auditability: a reviewer can grep the
// table for the exact scenario they care about).
//
// Missing key = no risk annotation emitted. AddTable / initial
// DDL on empty tables has no entries here.
var riskProfiles = map[string]riskProfile{
	// ── HIGH: full rewrite or long ACCESS_EXCLUSIVE lock ────────────────
	"carrier_change": {
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ALTER COLUMN TYPE across carriers rewrites every row (~500K rows/sec on modern hardware). " +
			"ACCESS_EXCLUSIVE lock blocks all reads + writes on the table for the rewrite duration.",
		recommendation: "For tables > 1M rows consider expand-contract: add a new column with the target carrier, " +
			"backfill in batches, switch application reads, then drop the old column over multiple releases.",
	},
	"pk_flip_enable": {
		opKind:     "pk_flip_enable",
		severity:   "HIGH",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ADD PRIMARY KEY builds a unique index + validates NOT NULL across the table. " +
			"~1M rows/sec for the index build; holds ACCESS_EXCLUSIVE for the full duration.",
		recommendation: "For large tables add a UNIQUE NOT NULL constraint first with NOT VALID, VALIDATE CONSTRAINT " +
			"in a separate transaction, then ALTER TABLE ADD PRIMARY KEY USING INDEX.",
	},
	"pk_flip_disable": {
		opKind:     "pk_flip_disable",
		severity:   "HIGH",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "DROP CONSTRAINT <table>_pkey may cascade-fail if other tables hold FK references to this column. " +
			"Metadata-only but an FK referential graph surprise is common.",
		recommendation: "Enumerate inbound FKs via pg_constraint first; plan FK drops / rewrites before applying.",
	},
	// D36 — pg_custom_type change has two severity tiers depending on
	// whether the author registered a conversion path in the
	// (w17.pg.project) / (w17.pg.module) custom_types registry.
	"pg_custom_type_registered": {
		opKind:     "pg_custom_type_registered",
		severity:   "MEDIUM",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ALTER COLUMN TYPE via a registered conversion — template is deterministic but PG still rewrites every row under an ACCESS_EXCLUSIVE lock.",
		recommendation: "For large tables consider expand-contract — add a parallel column with the new custom_type, backfill in batches, switch reads, drop the old column.",
	},
	"pg_custom_type_unregistered": {
		opKind:     "pg_custom_type_unregistered",
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "custom_type change without a registered conversion in (w17.pg.project/module).custom_types. Author must supply the migration via --decide col=custom:<sql-file>; compiler can't template an arbitrary opaque-type transition.",
		recommendation: "Either register a convertible_to / convertible_from entry between the two custom_type aliases, or supply CUSTOM_MIGRATION SQL with an explicit USING cast.",
	},
	"element_carrier_reshape": {
		opKind:     "element_carrier_reshape",
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "MAP value / LIST element carrier change re-encodes every JSON document or array value.",
		recommendation: "Author-provided CUSTOM_MIGRATION SQL — wrapper around jsonb_build / array_remap per row.",
	},
	"enum_values_remove": {
		opKind:     "enum_values_remove",
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "PostgreSQL has no ALTER TYPE DROP VALUE. Engine emits the deterministic rebuild: " +
			"CREATE TYPE new, ALTER COLUMN ... USING col::text::new, DROP TYPE old, RENAME new → old. " +
			"Cast fails at apply if any row still carries the removed value.",
		recommendation: "Pre-audit rows carrying the removed value; either surface via a SELECT before deploy or accept apply-time failure and rollback.",
	},
	"enum_fqn_change": {
		opKind:     "enum_fqn_change",
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "Proto enum FQN change means the target enum type changes; column needs to be re-typed.",
		recommendation: "CUSTOM_MIGRATION with USING <col>::new_enum_type; pre-check for unrepresentable values.",
	},
	"default_identity_add": {
		opKind:     "default_identity_add",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "ALTER COLUMN ADD GENERATED BY DEFAULT AS IDENTITY takes an ACCESS EXCLUSIVE lock briefly — no full row rewrite. The follow-up setval(pg_get_serial_sequence, MAX) reads the current MAX under that lock; no race window.",
		recommendation: "Schedule during a low-write window; downstream callers must stop passing explicit id values that conflict with the new sequence seed.",
	},
	"default_identity_drop": {
		opKind:         "default_identity_drop",
		severity:       "LOW",
		rewrite:        false,
		lockType:       "ACCESS_EXCLUSIVE",
		scalesWith:     "constant",
		rationale:      "ALTER COLUMN DROP IDENTITY is metadata-only — the sequence is dropped, existing column values preserved. Next INSERT requires explicit value.",
		recommendation: "Confirm downstream inserters supply the column explicitly before applying.",
	},
	"generated_expr_add": {
		opKind:     "generated_expr_add",
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "Converting a plain column to GENERATED ALWAYS AS STORED requires DROP + ADD (PG has no in-place convert). " +
			"Full row rewrite — every row recomputes the expression.",
		recommendation: "For large tables mirror the value into a new column with the generated_expr, switch reads, drop old.",
	},
	"generated_expr_change": {
		opKind:     "generated_expr_change",
		severity:   "HIGH",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "GENERATED expression rewrite has no in-place syntax; DROP + ADD recomputes every row.",
		recommendation: "Expand-contract: add a parallel generated column with the new expr, switch reads, drop old.",
	},
	"unique_enable": {
		opKind:     "unique_enable",
		severity:   "HIGH",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "CREATE UNIQUE INDEX (implicit on ADD UNIQUE) builds a new btree + validates uniqueness. " +
			"Aborts if duplicates exist — author must dedupe beforehand.",
		recommendation: "CREATE UNIQUE INDEX CONCURRENTLY (non-blocking) outside a transaction, then ALTER TABLE " +
			"ADD CONSTRAINT USING INDEX to promote it.",
	},

	// ── MEDIUM: full scan / validate existing rows ─────────────────────
	"nullable_tighten": {
		opKind:     "nullable_tighten",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "SET NOT NULL scans every row to validate no NULL values exist. Aborts on first NULL found. " +
			"Blocks writes for the scan duration.",
		recommendation: "Add CHECK (col IS NOT NULL) NOT VALID, VALIDATE CONSTRAINT in a separate transaction, then SET NOT NULL (skips rescan in PG 12+).",
	},
	"max_len_narrow": {
		opKind:     "max_len_narrow",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ALTER COLUMN TYPE VARCHAR(smaller) validates every row (error if any > new limit). " +
			"No rewrite on narrow-within-VARCHAR but holds ACCESS_EXCLUSIVE for full scan.",
		recommendation: "Validate data first: SELECT count(*) FROM <table> WHERE char_length(<col>) > <new_limit>.",
	},
	"numeric_precision_narrow": {
		opKind:     "numeric_precision_narrow",
		severity:   "MEDIUM",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "NUMERIC precision decrease requires row rewrite; aborts if any value exceeds new precision.",
		recommendation: "Query out-of-range rows first; consider expand-contract for large tables.",
	},
	"numeric_scale_narrow": {
		opKind:     "numeric_scale_narrow",
		severity:   "MEDIUM",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "NUMERIC scale decrease truncates stored decimals; full rewrite.",
		recommendation: "Author must decide truncation vs rounding; typically CUSTOM_MIGRATION.",
	},
	"dbtype_change": {
		opKind:     "dbtype_change",
		severity:   "MEDIUM",
		rewrite:    true,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ALTER COLUMN TYPE within carrier (TEXT↔CITEXT, JSON↔JSONB, etc.) typically rewrites rows via USING cast.",
		recommendation: "Verify cast compatibility (`SELECT col::<new> FROM <table> LIMIT 1`); consider expand-contract for large tables.",
	},
	"fk_add": {
		opKind:     "fk_add",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "SHARE_ROW_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ADD FOREIGN KEY validates every row against the referenced table. Blocks DML (writes) on both " +
			"sides during validation.",
		recommendation: "ADD CONSTRAINT ... NOT VALID (instant, no scan), then VALIDATE CONSTRAINT in a separate transaction — holds SHARE_UPDATE_EXCLUSIVE, allows concurrent reads + writes.",
	},
	"check_add_structured": {
		opKind:     "check_add_structured",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "ADD CHECK constraint validates every row. Aborts on first violation.",
		recommendation: "NOT VALID + VALIDATE CONSTRAINT pattern (same as fk_add).",
	},
	"allowed_extensions_narrow": {
		opKind:     "allowed_extensions_narrow",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "row_count",
		rationale: "Narrowing allowed path extensions regenerates the regex CHECK constraint + validates every row.",
		recommendation: "Query out-of-range paths first: SELECT count(*) FROM <table> WHERE <col> !~ '<new regex>'.",
	},
	"generated_expr_drop": {
		opKind:     "generated_expr_drop",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "PG 13+ DROP EXPRESSION materialises current values in place — no rewrite, but requires " +
			"ACCESS_EXCLUSIVE briefly.",
		recommendation: "Generally safe; verify target PG >= 13.",
	},

	// ── LOW: metadata-only or small index builds ────────────────────────
	"nullable_relax": {
		opKind:     "nullable_relax",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "DROP NOT NULL is metadata-only; instant regardless of table size.",
	},
	"max_len_widen": {
		opKind:     "max_len_widen",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "Widening VARCHAR(N) is metadata-only in PG (no row validation needed).",
	},
	"max_len_remove_bound": {
		opKind:     "max_len_remove_bound",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "VARCHAR(N) → TEXT/VARCHAR unbounded is metadata-only.",
	},
	"numeric_widen_both": {
		opKind:     "numeric_widen_both",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "NUMERIC precision + scale widen — metadata-only, no row rewrite.",
	},
	"numeric_remove_bound": {
		opKind:     "numeric_remove_bound",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "NUMERIC(P,S) → unbounded NUMERIC — widen, metadata-only.",
	},
	"allowed_extensions_widen": {
		opKind:     "allowed_extensions_widen",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "Widening allowed path extensions — regex CHECK regenerated, no row validation needed.",
	},
	"default_change": {
		opKind:     "default_change",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "SET DEFAULT / DROP DEFAULT is metadata-only; existing rows unaffected.",
	},
	"comment_change": {
		opKind:     "comment_change",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "METADATA",
		scalesWith: "constant",
		rationale: "COMMENT ON COLUMN is system-catalog metadata only.",
	},
	"table_comment_change": {
		opKind:     "table_comment_change",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "METADATA",
		scalesWith: "constant",
		rationale: "COMMENT ON TABLE is system-catalog metadata only.",
	},
	"table_rename": {
		opKind:     "table_rename",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "ALTER TABLE ... RENAME is metadata-only. Application code referencing the old name breaks; coordinate with code deploy.",
		recommendation: "Coordinate with application deploy — two-phase: add alias view, deploy code, remove alias.",
	},
	"table_namespace_move": {
		opKind:     "table_namespace_move",
		severity:   "LOW",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "SET SCHEMA is metadata-only. Application search_path must be consistent.",
	},
	"drop_column": {
		opKind:     "drop_column",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "DROP COLUMN is metadata-only in PG (disk space reclaimed on next VACUUM FULL). But data is irrecoverable; verify no app reads the column.",
		recommendation: "Verify application compatibility before apply. Reclaim disk via VACUUM FULL off-hours if space-sensitive.",
	},
	"drop_table": {
		opKind:     "drop_table",
		severity:   "HIGH",
		rewrite:    false,
		lockType:   "ACCESS_EXCLUSIVE",
		scalesWith: "constant",
		rationale: "DROP TABLE removes data + all indexes + cascades to dependent FKs. Irreversible (unless down rollback with data backup).",
		recommendation: "Verify zero application readers; consider renaming to <table>_dropped first for a grace period.",
	},

	// ── Raw / opaque ────────────────────────────────────────────────────
	"raw_unknown": {
		opKind:     "raw_unknown",
		severity:   "MEDIUM",
		rewrite:    false,
		lockType:   "UNKNOWN",
		scalesWith: "unknown",
		rationale: "Raw index or raw CHECK body is opaque to the compiler. Impact depends on author SQL.",
		recommendation: "Review the raw body manually; CREATE INDEX CONCURRENTLY outside transactions if large table.",
	},
}

// analyzeRisks walks a migration Plan + its Findings and produces
// the RiskFinding list. Deterministic + pure function of input.
func analyzeRisks(plan *planpb.MigrationPlan, findings []*planpb.ReviewFinding) []*planpb.RiskFinding {
	var out []*planpb.RiskFinding

	// Cross-axis findings → one RiskFinding per axis.
	for _, f := range findings {
		kind := mapFindingAxisToProfileKey(f)
		if kind == "" {
			continue
		}
		profile, ok := riskProfiles[kind]
		if !ok {
			continue
		}
		out = append(out, profileToFinding(profile, kind, f.GetColumn()))
	}

	// In-axis FactChanges + table-level ops → one RiskFinding per op.
	for _, op := range plan.GetOps() {
		switch v := op.GetVariant().(type) {
		case *planpb.Op_AlterColumn:
			out = append(out, risksFromAlterColumn(v.AlterColumn)...)
		case *planpb.Op_DropColumn:
			out = append(out, profileToFinding(riskProfiles["drop_column"], "drop_column",
				columnRefFromCtx(v.DropColumn.GetCtx(), v.DropColumn.GetColumn().GetName())))
		case *planpb.Op_DropTable:
			out = append(out, profileToFinding(riskProfiles["drop_table"], "drop_table",
				tableColumnRef(v.DropTable.GetTable())))
		case *planpb.Op_RenameTable:
			out = append(out, profileToFinding(riskProfiles["table_rename"], "table_rename",
				&planpb.ColumnRef{TableName: v.RenameTable.GetFromName()}))
		case *planpb.Op_SetTableNamespace:
			out = append(out, profileToFinding(riskProfiles["table_namespace_move"], "table_namespace_move",
				&planpb.ColumnRef{TableName: v.SetTableNamespace.GetTableNameFrom()}))
		case *planpb.Op_SetTableComment:
			out = append(out, profileToFinding(riskProfiles["table_comment_change"], "table_comment_change",
				&planpb.ColumnRef{TableName: v.SetTableComment.GetCtx().GetTableName()}))
		case *planpb.Op_AddForeignKey:
			out = append(out, profileToFinding(riskProfiles["fk_add"], "fk_add",
				&planpb.ColumnRef{TableName: v.AddForeignKey.GetCtx().GetTableName(),
					ColumnName: v.AddForeignKey.GetFk().GetColumn()}))
		case *planpb.Op_AddCheck:
			out = append(out, profileToFinding(riskProfiles["check_add_structured"], "check_add_structured",
				&planpb.ColumnRef{TableName: v.AddCheck.GetCtx().GetTableName(),
					ColumnName: v.AddCheck.GetColumn().GetName()}))
		case *planpb.Op_AddRawIndex, *planpb.Op_ReplaceRawIndex, *planpb.Op_AddRawCheck, *planpb.Op_ReplaceRawCheck:
			out = append(out, profileToFinding(riskProfiles["raw_unknown"], "raw_unknown", nil))
		}
	}
	return out
}

// risksFromAlterColumn examines each FactChange on an AlterColumn op
// and emits one RiskFinding per risky axis. Axes not in riskProfiles
// (e.g. PgOptions metadata changes) produce nothing.
func risksFromAlterColumn(alter *planpb.AlterColumn) []*planpb.RiskFinding {
	var out []*planpb.RiskFinding
	colRef := columnRefFromCtx(alter.GetCtx(), alter.GetColumnName())
	for _, fc := range alter.GetChanges() {
		kind := factChangeKind(fc)
		if kind == "" {
			continue
		}
		if profile, ok := riskProfiles[kind]; ok {
			out = append(out, profileToFinding(profile, kind, colRef))
		}
	}
	return out
}

// factChangeKind maps a FactChange variant + its case-dependent
// sub-kind (e.g. max_len widen vs narrow) to a riskProfiles key.
func factChangeKind(fc *planpb.FactChange) string {
	switch v := fc.GetVariant().(type) {
	case *planpb.FactChange_Nullable:
		if v.Nullable.GetTo() {
			return "nullable_relax"
		}
		return "nullable_tighten"
	case *planpb.FactChange_MaxLen:
		from, to := v.MaxLen.GetFrom(), v.MaxLen.GetTo()
		switch {
		case from != 0 && to == 0:
			return "max_len_remove_bound"
		case to > from:
			return "max_len_widen"
		case to < from:
			return "max_len_narrow"
		}
		return ""
	case *planpb.FactChange_NumericPrecision:
		from, to := v.NumericPrecision.GetFromPrecision(), v.NumericPrecision.GetToPrecision()
		switch {
		case from != 0 && to == 0:
			return "numeric_remove_bound"
		case to > from:
			return "numeric_widen_both"
		case to < from:
			return "numeric_precision_narrow"
		}
		if fs, ts := v.NumericPrecision.GetFromScale(), v.NumericPrecision.GetToScale(); ts < fs {
			return "numeric_scale_narrow"
		}
		return ""
	case *planpb.FactChange_DbType:
		return "dbtype_change"
	case *planpb.FactChange_Unique:
		if v.Unique.GetTo() {
			return "unique_enable"
		}
		return ""
	case *planpb.FactChange_GeneratedExpr:
		from, to := v.GeneratedExpr.GetFrom(), v.GeneratedExpr.GetTo()
		switch {
		case from == "" && to != "":
			return "generated_expr_add"
		case from != "" && to == "":
			return "generated_expr_drop"
		case from != to:
			return "generated_expr_change"
		}
		return ""
	case *planpb.FactChange_Comment:
		return "comment_change"
	case *planpb.FactChange_AllowedExtensions:
		from, to := len(v.AllowedExtensions.GetFrom()), len(v.AllowedExtensions.GetTo())
		if to > from {
			return "allowed_extensions_widen"
		}
		return "allowed_extensions_narrow"
	case *planpb.FactChange_DefaultValue:
		// D38 — identity lifecycle rides the DefaultChange FactChange
		// shape but carries distinct risk tiers. Plain literal /
		// auto-kind transitions fall through to the LOW default_change
		// profile (metadata-only).
		fromID := isIdentityAutoKind(v.DefaultValue.GetFrom())
		toID := isIdentityAutoKind(v.DefaultValue.GetTo())
		switch {
		case !fromID && toID:
			return "default_identity_add"
		case fromID && !toID:
			return "default_identity_drop"
		}
		return "default_change"
	case *planpb.FactChange_PrimaryKey:
		// D39 — single-column PK flip. Enable tier is HIGH (unique index
		// build + ACCESS EXCLUSIVE for full duration); disable tier is
		// HIGH too (FK referential surprise risk).
		if v.PrimaryKey.GetTo() {
			return "pk_flip_enable"
		}
		return "pk_flip_disable"
	case *planpb.FactChange_EnumValues:
		// D37 — RemovedNames non-empty = NEEDS_CONFIRM rebuild path
		// that rewrites every row in the enum-typed column under an
		// ACCESS_EXCLUSIVE lock. AddedNames (SAFE) is unannotated;
		// ADD VALUE is cheap and doesn't rewrite rows.
		if len(v.EnumValues.GetRemovedNames()) > 0 {
			return "enum_values_remove"
		}
		return ""
	case *planpb.FactChange_TypeChange:
		// TypeChange originates from either carrier_change or
		// pg_custom_type finding resolution. Distinguish by whether
		// the from / to columns declare a custom_type alias — if
		// either side does, this is the pg_custom_type path;
		// otherwise plain carrier.
		fromAlias := v.TypeChange.GetFromColumn().GetPg().GetCustomTypeAlias()
		toAlias := v.TypeChange.GetToColumn().GetPg().GetCustomTypeAlias()
		if fromAlias != "" || toAlias != "" {
			// UsingUp non-empty → conversion template was registered.
			// Empty → unregistered path (shouldn't normally reach risk
			// analyser since injectCustomTypeChange hard-errors on
			// unregistered LOSSLESS_USING, but DROP_AND_CREATE path
			// produces TypeChange-less Ops anyway).
			if v.TypeChange.GetUsingUp() != "" {
				return "pg_custom_type_registered"
			}
			return "pg_custom_type_unregistered"
		}
		return "carrier_change"
	}
	return ""
}

// isIdentityAutoKind — true when d is an AUTO_IDENTITY default. Used by
// factChangeKind to tier DefaultChange FactChanges into the D38 identity-
// lifecycle risk profiles.
func isIdentityAutoKind(d *irpb.Default) bool {
	if d == nil {
		return false
	}
	auto, ok := d.GetVariant().(*irpb.Default_Auto)
	return ok && auto.Auto == irpb.AutoKind_AUTO_IDENTITY
}

// mapFindingAxisToProfileKey converts a ReviewFinding.Axis string
// into the canonical riskProfiles key. Finding axes already carry
// strategy context (carrier_change, pk_flip); here we just add the
// enable/disable suffix for pk, and map pg_custom_type to the
// unregistered profile (author still deciding path).
func mapFindingAxisToProfileKey(f *planpb.ReviewFinding) string {
	axis := f.GetAxis()
	switch axis {
	case "pk_flip":
		// D39 — FindingContext carries prev/curr Column snapshots;
		// inspect the Pk flag flip to tier risk correctly.
		if col := f.GetContext().GetColumn(); col != nil && !col.GetPrev().GetPk() && col.GetCurr().GetPk() {
			return "pk_flip_enable"
		}
		return "pk_flip_disable"
	case "pg_custom_type":
		// Unresolved pg_custom_type finding — author hasn't decided
		// path yet. Flag as unregistered (worst-case HIGH); if author
		// later supplies --decide with a registered conversion, the
		// Op-level walk emits pg_custom_type_registered instead.
		return "pg_custom_type_unregistered"
	}
	return axis
}

// profileToFinding builds a RiskFinding proto from a profile +
// context. Handles the optional ColumnRef (nil for table-level).
func profileToFinding(p riskProfile, kind string, col *planpb.ColumnRef) *planpb.RiskFinding {
	rf := &planpb.RiskFinding{
		OpKind:         kind,
		Severity:       p.severity,
		Rewrite:        p.rewrite,
		LockType:       p.lockType,
		ScalesWith:     p.scalesWith,
		Rationale:      p.rationale,
		Recommendation: p.recommendation,
	}
	if col != nil {
		rf.Column = col
	}
	return rf
}

// columnRefFromCtx builds a minimal ColumnRef the renderer can
// print. Used for ops that carry TableCtx + a column name.
func columnRefFromCtx(ctx *planpb.TableCtx, colName string) *planpb.ColumnRef {
	if ctx == nil {
		return nil
	}
	return &planpb.ColumnRef{
		TableFqn:   ctx.GetMessageFqn(),
		TableName:  ctx.GetTableName(),
		ColumnName: colName,
	}
}

// tableColumnRef produces a table-level ColumnRef (no column set).
func tableColumnRef(t *irpb.Table) *planpb.ColumnRef {
	return &planpb.ColumnRef{
		TableFqn:  t.GetMessageFqn(),
		TableName: t.GetName(),
	}
}

// renderRiskComments formats a RiskFinding list as a multi-line
// `-- RISK:` comment block, suitable for prepending to up.sql /
// down.sql. Empty list → empty string (no comment block emitted).
// Ordered by severity (HIGH first, then MEDIUM, then LOW) so the
// most urgent entries are top-of-file.
func renderRiskComments(risks []*planpb.RiskFinding) string {
	if len(risks) == 0 {
		return ""
	}
	ordered := sortedByImpact(risks)
	var b strings.Builder
	b.WriteString("-- ======================================================================\n")
	b.WriteString("-- Migration risk analysis (compile-time, deterministic; no DB inspection).\n")
	b.WriteString("-- ======================================================================\n")
	for _, r := range ordered {
		fmt.Fprintf(&b, "-- RISK %s [%s]", r.GetSeverity(), r.GetOpKind())
		if c := r.GetColumn(); c != nil && c.GetTableName() != "" {
			if c.GetColumnName() != "" {
				fmt.Fprintf(&b, " %s.%s", c.GetTableName(), c.GetColumnName())
			} else {
				fmt.Fprintf(&b, " %s", c.GetTableName())
			}
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "--   lock: %s; rewrite: %v; scales with: %s\n",
			r.GetLockType(), r.GetRewrite(), r.GetScalesWith())
		for _, line := range wrapWords(r.GetRationale(), 70) {
			fmt.Fprintf(&b, "--   why: %s\n", line)
		}
		if r.GetRecommendation() != "" {
			for _, line := range wrapWords(r.GetRecommendation(), 70) {
				fmt.Fprintf(&b, "--   recommend: %s\n", line)
			}
		}
		b.WriteString("--\n")
	}
	b.WriteString("-- ======================================================================\n\n")
	return b.String()
}

// sortedByImpact returns risks ordered HIGH → MEDIUM → LOW → other,
// ties broken by op_kind lexicographically for determinism.
func sortedByImpact(in []*planpb.RiskFinding) []*planpb.RiskFinding {
	buckets := map[string][]*planpb.RiskFinding{"HIGH": nil, "MEDIUM": nil, "LOW": nil, "": nil}
	for _, r := range in {
		key := r.GetSeverity()
		if _, ok := buckets[key]; !ok {
			key = ""
		}
		buckets[key] = append(buckets[key], r)
	}
	out := make([]*planpb.RiskFinding, 0, len(in))
	for _, sev := range []string{"HIGH", "MEDIUM", "LOW", ""} {
		bucket := buckets[sev]
		sortRisksByOpKind(bucket)
		out = append(out, bucket...)
	}
	return out
}

func sortRisksByOpKind(in []*planpb.RiskFinding) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0; j-- {
			if in[j].GetOpKind() < in[j-1].GetOpKind() {
				in[j], in[j-1] = in[j-1], in[j]
				continue
			}
			break
		}
	}
}

// wrapWords breaks a sentence at word boundaries so rationale /
// recommendation comments stay under ~80 cols when rendered as
// `--   why: ...`. Returns the original string as a single-element
// slice when already short enough.
func wrapWords(s string, width int) []string {
	if len(s) <= width {
		return []string{s}
	}
	words := strings.Fields(s)
	var out []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() == 0 {
			cur.WriteString(w)
			continue
		}
		if cur.Len()+1+len(w) > width {
			out = append(out, cur.String())
			cur.Reset()
			cur.WriteString(w)
			continue
		}
		cur.WriteString(" ")
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
