//go:build e2e

package e2e

// Constraint-axis synth. Each classifier.constraint cell (axis,
// case) describes a within-column transition; this file maps
// authored cells to runnable Cell instances. Column-level axes
// (nullable / max_len / numeric / default / comment / unique)
// exercise the in-axis FactChange emit path — no Findings, no
// --decide. Cross-axis findings (pk / pg_custom_type /
// element_reshape / pg_required_extensions / enum_values remove)
// route through the Finding+Resolution path like carrier cells.
// Table-level + index/fk/check axes land as follow-up waves —
// flagged Skip here with a reason.

import (
	"fmt"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
	pgpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/pg"
)

// allConstraintCells iterates classifier.AllConstraintCells() and
// dispatches each (axis, case) to its synth helper.
func allConstraintCells(cls *classifier.Classifier) []Cell {
	entries := cls.AllConstraintCells()
	out := make([]Cell, 0, len(entries))
	for _, e := range entries {
		out = append(out, constraintCell(e))
	}
	return out
}

func constraintCell(e classifier.ConstraintEntry) Cell {
	c := Cell{
		Axis:     "constraint",
		Name:     e.Axis + "_" + e.CaseID,
		Strategy: e.Cell.Strategy,
	}
	switch e.Axis {
	case "nullable":
		nullableSynth(&c, e.CaseID)
	case "max_len":
		maxLenSynth(&c, e.CaseID)
	case "numeric":
		numericSynth(&c, e.CaseID)
	case "default":
		defaultSynth(&c, e.CaseID)
	case "comment":
		commentSynth(&c, e.CaseID)
	case "unique":
		uniqueSynth(&c, e.CaseID)
	case "pg_custom_type":
		pgCustomTypeSynth(&c, e.CaseID)
	case "enum_values":
		enumValuesSynth(&c, e.CaseID)
	default:
		c.SkipReason = fmt.Sprintf("axis %s not yet synthesizable (future wave)", e.Axis)
	}
	return c
}

// column-axis synths build minimal prev + curr schemas that
// differ only on the targeted axis/case. The in-axis FactChange
// path picks up the delta and emits SAFE/NEEDS_CONFIRM SQL
// automatically.

func nullableSynth(c *Cell, caseID string) {
	makeCol := func(nullable bool) *irpb.Column {
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier:  irpb.Carrier_CARRIER_STRING,
			Type:     irpb.SemType_SEM_TEXT,
			Nullable: nullable,
		}
	}
	switch caseID {
	case "relax":
		c.Prev = wrapOneColumn(makeCol(false))
		c.Curr = wrapOneColumn(makeCol(true))
	case "tighten":
		c.Prev = wrapOneColumn(makeCol(true))
		c.Curr = wrapOneColumn(makeCol(false))
	default:
		c.SkipReason = fmt.Sprintf("nullable case %q not synthesised", caseID)
	}
}

func maxLenSynth(c *Cell, caseID string) {
	makeCol := func(n int32) *irpb.Column {
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_STRING,
			Type:    irpb.SemType_SEM_CHAR,
			MaxLen:  n,
		}
	}
	switch caseID {
	case "widen":
		c.Prev = wrapOneColumn(makeCol(16))
		c.Curr = wrapOneColumn(makeCol(64))
	case "narrow":
		c.Prev = wrapOneColumn(makeCol(64))
		c.Curr = wrapOneColumn(makeCol(16))
	case "remove_bound":
		// VARCHAR(N) → unbounded requires sem=TEXT (no MaxLen) on curr.
		prev := makeCol(32)
		curr := &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_STRING,
			Type:    irpb.SemType_SEM_TEXT,
		}
		c.Prev = wrapOneColumn(prev)
		c.Curr = wrapOneColumn(curr)
	case "add_bound":
		prev := &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_STRING,
			Type:    irpb.SemType_SEM_TEXT,
		}
		c.Prev = wrapOneColumn(prev)
		c.Curr = wrapOneColumn(makeCol(64))
	default:
		c.SkipReason = fmt.Sprintf("max_len case %q not synthesised", caseID)
	}
}

func numericSynth(c *Cell, caseID string) {
	make := func(p int32, s *int32) *irpb.Column {
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier:   irpb.Carrier_CARRIER_STRING,
			Type:      irpb.SemType_SEM_DECIMAL,
			Precision: p,
			Scale:     s,
		}
	}
	s2 := int32(2)
	s4 := int32(4)
	switch caseID {
	case "widen_both":
		c.Prev = wrapOneColumn(make(8, &s2))
		c.Curr = wrapOneColumn(make(16, &s4))
	case "precision_narrow":
		c.Prev = wrapOneColumn(make(16, &s2))
		c.Curr = wrapOneColumn(make(8, &s2))
	case "scale_narrow":
		c.Prev = wrapOneColumn(make(12, &s4))
		c.Curr = wrapOneColumn(make(12, &s2))
	case "add_bound":
		// Unbounded NUMERIC requires a different IR shape (sem=NUMBER
		// on DOUBLE carrier with db_type=NUMERIC override) — the
		// sem=DECIMAL path IR-rejects precision=0. Future wave.
		c.SkipReason = "numeric add_bound needs unbounded-NUMERIC synth (sem=NUMBER + db_type=NUMERIC override); follow-up wave"
	case "remove_bound":
		c.Prev = wrapOneColumn(make(12, &s2))
		c.Curr = wrapOneColumn(make(0, nil))
	default:
		c.SkipReason = fmt.Sprintf("numeric case %q not synthesised", caseID)
	}
}

func defaultSynth(c *Cell, caseID string) {
	plain := func() *irpb.Column {
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_STRING,
			Type:    irpb.SemType_SEM_TEXT,
		}
	}
	withDefault := func(literal string) *irpb.Column {
		col := plain()
		col.Default = &irpb.Default{
			Variant: &irpb.Default_LiteralString{LiteralString: literal},
		}
		return col
	}
	// D38 — identity_add / identity_drop synth flips AUTO_IDENTITY on
	// a dedicated non-PK INT64 counter column (AUTO_IDENTITY is valid
	// on INT32/INT64 semantically; we avoid the PK since wrapOneColumn
	// already hard-codes the PK id with DbType=BIGINT + no default).
	mkIdentityCol := func(identity bool) *irpb.Column {
		col := &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_INT64,
			Type:    irpb.SemType_SEM_COUNTER,
			DbType:  irpb.DbType_DBT_BIGINT,
		}
		if identity {
			col.Default = &irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_IDENTITY}}
		}
		return col
	}
	// auto_kind_change uses SEM_DATETIME on a TIMESTAMP carrier so PG
	// renders TIMESTAMPTZ + AUTO_NOW → NOW().
	mkAutoKindCol := func(def *irpb.Default) *irpb.Column {
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_TIMESTAMP,
			Type:    irpb.SemType_SEM_DATETIME,
			Default: def,
		}
	}
	switch caseID {
	case "add":
		c.Prev = wrapOneColumn(plain())
		c.Curr = wrapOneColumn(withDefault("alpha"))
	case "drop":
		c.Prev = wrapOneColumn(withDefault("alpha"))
		c.Curr = wrapOneColumn(plain())
	case "change_literal":
		c.Prev = wrapOneColumn(withDefault("alpha"))
		c.Curr = wrapOneColumn(withDefault("beta"))
	case "auto_kind_change":
		c.Prev = wrapOneColumn(mkAutoKindCol(&irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_NOW}}))
		c.Curr = wrapOneColumn(mkAutoKindCol(&irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: "1970-01-01 00:00:00"}}))
	case "identity_add":
		c.Prev = wrapOneColumn(mkIdentityCol(false))
		c.Curr = wrapOneColumn(mkIdentityCol(true))
	case "identity_drop":
		c.Prev = wrapOneColumn(mkIdentityCol(true))
		c.Curr = wrapOneColumn(mkIdentityCol(false))
	default:
		c.SkipReason = fmt.Sprintf("default case %q not synthesised", caseID)
	}
}

func commentSynth(c *Cell, caseID string) {
	make := func(comment string) *irpb.Column {
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_STRING,
			Type:    irpb.SemType_SEM_TEXT,
			Comment: comment,
		}
	}
	switch caseID {
	case "any":
		c.Prev = wrapOneColumn(make(""))
		c.Curr = wrapOneColumn(make("the target column"))
	default:
		c.SkipReason = fmt.Sprintf("comment case %q not synthesised", caseID)
	}
}

// pgCustomTypeSynth covers the D36 registered-conversion path.
// Builds prev / curr schemas where the `payload` column flips
// between two alias-registered custom_types whose registry has
// convertible_to set, so engine renders ALTER COLUMN TYPE ...
// USING <rendered cast>. The CUSTOM_MIGRATION default strategy is
// honoured by the harness via stub SQL (since the YAML default is
// CUSTOM_MIGRATION, user opt-in to registered conversion flows
// via the test's Resolution override).
// pgCustomTypeSynth uses real PG types (JSONB, JSON) wrapped behind
// non-reserved aliases so the PG container recognises the sql_type
// without requiring CREATE DOMAIN / CREATE TYPE extension setup.
// `payload_jsonb` → JSONB, `payload_json` → JSON, with registered
// `col::json` cast — round-trippable on stock postgres.
func pgCustomTypeSynth(c *Cell, caseID string) {
	registry := map[string]*pgpb.CustomType{
		"payload_jsonb": {
			Alias: "payload_jsonb", SqlType: "JSONB",
			ConvertibleTo: []*pgpb.Conversion{
				{Type: "payload_json", Cast: "{{.Col}}::json",
					Rationale: "JSONB → JSON: PG implicit cast preserves structure, drops ordering + uniqueness guarantees."},
			},
		},
		"payload_json": {
			Alias: "payload_json", SqlType: "JSON",
			ConvertibleFrom: []*pgpb.Conversion{
				{Type: "payload_jsonb", Cast: "{{.Col}}::jsonb"},
			},
			ConvertibleTo: []*pgpb.Conversion{
				{Type: "payload_jsonb", Cast: "{{.Col}}::jsonb",
					Rationale: "JSON → JSONB: PG parses and normalises."},
			},
		},
	}
	mkSchema := func(alias, sqlType string) *irpb.Schema {
		s := wrapOneColumn(&irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier: irpb.Carrier_CARRIER_STRING,
			Type:    irpb.SemType_SEM_TEXT,
			Pg: &irpb.PgOptions{
				CustomType:      sqlType,
				CustomTypeAlias: alias,
			},
		})
		s.PgCustomTypes = registry
		return s
	}
	switch caseID {
	case "any":
		c.Prev = mkSchema("payload_jsonb", "JSONB")
		c.Curr = mkSchema("payload_json", "JSON")
		// Override default strategy: classifier.yaml says
		// CUSTOM_MIGRATION, but the registry provides a registered
		// conversion — opt into LOSSLESS_USING to exercise the D36
		// Commit B engine path end-to-end (template render → ALTER
		// TABLE ... TYPE ... USING <cast>).
		c.Strategy = planpb.Strategy_LOSSLESS_USING
	default:
		c.SkipReason = fmt.Sprintf("pg_custom_type case %q not synthesised", caseID)
	}
}

// enumValuesSynth covers two paths:
//   - add: in-axis FactChange, ALTER TYPE ADD VALUE (SAFE, no Finding).
//   - remove: D37 NEEDS_CONFIRM Finding resolution, engine emits the
//     4-statement rebuild (CREATE TYPE new / ALTER USING / DROP / RENAME).
func enumValuesSynth(c *Cell, caseID string) {
	mkCol := func(values ...string) *irpb.Column {
		numbers := make([]int64, len(values))
		for i := range values {
			numbers[i] = int64(i + 1)
		}
		return &irpb.Column{
			Name: "target", ProtoName: "target", FieldNumber: 2,
			Carrier:     irpb.Carrier_CARRIER_STRING,
			Type:        irpb.SemType_SEM_ENUM,
			EnumFqn:     "e2e.Target",
			EnumNames:   values,
			EnumNumbers: numbers,
		}
	}
	switch caseID {
	case "add":
		c.Prev = wrapOneColumn(mkCol("alpha", "beta"))
		c.Curr = wrapOneColumn(mkCol("alpha", "beta", "gamma"))
	case "remove":
		c.Prev = wrapOneColumn(mkCol("alpha", "beta", "gamma"))
		c.Curr = wrapOneColumn(mkCol("alpha", "beta"))
	default:
		c.SkipReason = fmt.Sprintf("enum_values case %q not synthesised (fqn_change / rename_in_place land as follow-ups)", caseID)
	}
}

func uniqueSynth(c *Cell, caseID string) {
	// Per constraint.yaml A5: unique flips route through the Index
	// bucket (CREATE UNIQUE INDEX / DROP INDEX), not through
	// FactChange on the Column. The column-level Unique field is
	// a convenience flag; plan.Diff restructures it into Index
	// ops which this stub synthesizer doesn't exercise yet.
	// Follow-up wave: full Index synth (constraint_index_add,
	// etc.) will naturally cover this path.
	c.SkipReason = fmt.Sprintf("unique case %q routes through Index bucket; needs index-level synth (follow-up wave)", caseID)
}

// wrapOneColumn lifts a single Column into the canonical
// (id PK + target) schema shape the harness expects.
func wrapOneColumn(col *irpb.Column) *irpb.Schema {
	return &irpb.Schema{Tables: []*irpb.Table{{
		Name:       "t",
		MessageFqn: "e2e.T",
		Columns: []*irpb.Column{
			{
				Name: "id", ProtoName: "id", FieldNumber: 1,
				Carrier: irpb.Carrier_CARRIER_INT64,
				Type:    irpb.SemType_SEM_ID,
				DbType:  irpb.DbType_DBT_BIGINT,
				Pk:      true,
			},
			col,
		},
		PrimaryKey: []string{"id"},
	}}}
}

// _ = planpb. // keeps import alive across scope cuts
var _ = planpb.Strategy_SAFE
