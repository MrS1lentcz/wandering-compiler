package engine

// D33 — engine-side synthesis of Ops from resolved cross-carrier
// ReviewFindings. Bridges the gap between "classifier says
// LOSSLESS_USING with this template" and "emitter produces an
// ALTER TABLE … TYPE … USING …" without requiring the author to
// supply CUSTOM_MIGRATION SQL manually.
//
// CUSTOM_MIGRATION resolutions are excluded here — they flow
// through spliceCustomMigrations at the string layer.

import (
	"bytes"
	"fmt"
	"sort"
	"text/template"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// injectStrategyOps walks resolved ReviewFindings (minus
// CUSTOM_MIGRATION — handled by spliceCustomMigrations) and
// synthesises the Ops the emitter needs to produce migration
// SQL. Returned Ops are Finding-ID-sorted for idempotence.
//
// Current scope:
//   - "carrier_change" → TypeChange FactChange or Drop+Add.
//   - "pg_custom_type" (D36) → registered-conversion TypeChange.
//   - "enum_values_remove" (D37) → EnumValuesChange with
//     RemovedNames; emitter renders CREATE TYPE new / ALTER /
//     DROP / RENAME rebuild. NEEDS_CONFIRM only.
//
// Remaining Finding axes land as B1 hard-errors (pk_flip,
// enum_fqn_change, element_carrier_reshape) — either the
// transition is genuinely non-deterministic (element reshape:
// string→int per element has no template) or the rebuild is
// not yet modelled (pk_flip structured DDL still pending).
func injectStrategyOps(
	pairs []resolvedPair,
	cls *classifier.Classifier,
	bkt *bucket,
) ([]*planpb.Op, error) {
	sorted := make([]resolvedPair, len(pairs))
	copy(sorted, pairs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Finding.GetId() < sorted[j].Finding.GetId()
	})

	var out []*planpb.Op
	for _, p := range sorted {
		if p.Resolution.GetStrategy() == planpb.Strategy_CUSTOM_MIGRATION {
			continue
		}
		switch p.Finding.GetAxis() {
		case "carrier_change":
			ops, err := injectCarrierChange(p, cls, bkt)
			if err != nil {
				return nil, fmt.Errorf("finding %s: %w", p.Finding.GetId(), err)
			}
			out = append(out, ops...)
		case "pg_custom_type":
			// D36 Commit B — typed registry path. Resolution looks up
			// conversion template in Schema.PgCustomTypes; falls back
			// to hard-error when no registered path exists.
			ops, err := injectCustomTypeChange(p, bkt)
			if err != nil {
				return nil, fmt.Errorf("finding %s: %w", p.Finding.GetId(), err)
			}
			out = append(out, ops...)
		case "enum_values_remove", "enum_fqn_change":
			// D37 enum_values/remove + D40 enum FQN change share the
			// rebuild template: CREATE TYPE new AS ENUM (curr.values),
			// ALTER COLUMN USING col::text::new, DROP TYPE old, RENAME.
			// Engine computes added + removed from prev vs curr enum
			// names; emit picks rebuild (removed non-empty), ADD VALUE
			// (added-only), or no-op.
			ops, err := injectEnumValuesChange(p, bkt)
			if err != nil {
				return nil, fmt.Errorf("finding %s: %w", p.Finding.GetId(), err)
			}
			out = append(out, ops...)
		case "default_identity_add", "default_identity_drop":
			// D38 — NEEDS_CONFIRM identity lifecycle. Engine emits an
			// AlterColumn with a DefaultChange carrying the full
			// prev/curr Default proto; emit/postgres detects AUTO_IDENTITY
			// on either side and renders the ADD GENERATED + setval or
			// DROP IDENTITY template.
			ops, err := injectDefaultIdentity(p, bkt)
			if err != nil {
				return nil, fmt.Errorf("finding %s: %w", p.Finding.GetId(), err)
			}
			out = append(out, ops...)
		case "pk_flip":
			// D39 — single-column PK enable/disable via NEEDS_CONFIRM.
			// Emits AlterColumn with PrimaryKeyChange FactChange; the
			// emitter renders ALTER TABLE ADD PRIMARY KEY / DROP
			// CONSTRAINT <t>_pkey. Multi-column PK swap on the same
			// table hard-errors pointing at CUSTOM_MIGRATION.
			ops, err := injectPkFlip(p, sorted, bkt)
			if err != nil {
				return nil, fmt.Errorf("finding %s: %w", p.Finding.GetId(), err)
			}
			out = append(out, ops...)
		case "element_carrier_reshape":
			// D36 B1 — these axes accept only CUSTOM_MIGRATION. User
			// resolving them to another strategy previously landed
			// silently (AppliedResolution recorded, no SQL emitted —
			// silent-empty-migration bug). Hard-error instead with
			// explicit guidance.
			return nil, fmt.Errorf("finding %s (%s): strategy %s is not supported for this axis — only CUSTOM_MIGRATION is accepted (supply --decide %s=custom:<sql-file>)",
				p.Finding.GetId(), p.Finding.GetAxis(), p.Resolution.GetStrategy(),
				findingKey(p.Finding))
		}
	}
	return out, nil
}

// findingKey formats a human-readable `<table>.<column>:<axis>`
// identifier for --decide error messages. Omits the axis suffix
// when the Finding doesn't need axis disambiguation (column has
// only one pending axis).
func findingKey(f *planpb.ReviewFinding) string {
	c := f.GetColumn()
	return fmt.Sprintf("%s.%s:%s", c.GetTableName(), c.GetColumnName(), f.GetAxis())
}

// injectCarrierChange synthesises the Op(s) for a resolved
// carrier-change Finding. Strategy dispatch:
//
//   - SAFE / LOSSLESS_USING / NEEDS_CONFIRM → single AlterColumn
//     with a TypeChange FactChange. USING clause rendered from
//     the classifier's forward cell template; reverse rendered
//     from the symmetric cell or a default `col::<from_type>`.
//   - DROP_AND_CREATE → DropColumn + AddColumn pair in FK-safe
//     order (drop first, add second).
func injectCarrierChange(
	p resolvedPair,
	cls *classifier.Classifier,
	bkt *bucket,
) ([]*planpb.Op, error) {
	ref := p.Finding.GetColumn()
	prevTable, prevCol := findColumnByRef(bkt.prev, ref)
	_, currCol := findColumnByRef(bkt.curr, ref)
	if prevCol == nil || currCol == nil {
		return nil, fmt.Errorf("carrier_change: prev/curr column not found via %s/#%d",
			ref.GetTableFqn(), ref.GetFieldNumber())
	}

	ctx := &planpb.TableCtx{
		TableName:     prevTable.GetName(),
		MessageFqn:    prevTable.GetMessageFqn(),
		NamespaceMode: prevTable.GetNamespaceMode(),
		Namespace:     prevTable.GetNamespace(),
	}

	strategy := p.Resolution.GetStrategy()
	switch strategy {
	case planpb.Strategy_DROP_AND_CREATE:
		return []*planpb.Op{
			{Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{
				Ctx: ctx, Column: prevCol,
			}}},
			{Variant: &planpb.Op_AddColumn{AddColumn: &planpb.AddColumn{
				Ctx: ctx, Column: currCol,
			}}},
		}, nil

	case planpb.Strategy_SAFE, planpb.Strategy_LOSSLESS_USING, planpb.Strategy_NEEDS_CONFIRM:
		usingUp, usingDown := renderCarrierUsing(cls, prevCol, currCol, prevTable)
		return []*planpb.Op{{
			Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
				Ctx:        ctx,
				ColumnName: currCol.GetName(),
				Column:     currCol,
				PrevColumn: prevCol,
				Changes: []*planpb.FactChange{{
					Variant: &planpb.FactChange_TypeChange{TypeChange: &planpb.TypeChange{
						FromColumn: prevCol,
						ToColumn:   currCol,
						UsingUp:    usingUp,
						UsingDown:  usingDown,
						Strategy:   strategy,
					}},
				}},
			}},
		}}, nil
	}

	return nil, fmt.Errorf("carrier_change: unhandled strategy %s", strategy)
}

// injectCustomTypeChange renders Ops for a resolved pg_custom_type
// finding. Strategy dispatch:
//
//   - DROP_AND_CREATE → DropColumn + AddColumn pair.
//   - SAFE / LOSSLESS_USING / NEEDS_CONFIRM → AlterColumn with
//     TypeChange; USING clauses rendered from the registered
//     convertible_to / convertible_from cast templates.
//
// Falls back to hard-error when no conversion template is
// registered between prev.alias and curr.alias — author must
// either extend the registry or resolve as CUSTOM_MIGRATION.
func injectCustomTypeChange(p resolvedPair, bkt *bucket) ([]*planpb.Op, error) {
	ref := p.Finding.GetColumn()
	prevTable, prevCol := findColumnByRef(bkt.prev, ref)
	_, currCol := findColumnByRef(bkt.curr, ref)
	if prevCol == nil || currCol == nil {
		return nil, fmt.Errorf("pg_custom_type: prev/curr column not found via %s/#%d",
			ref.GetTableFqn(), ref.GetFieldNumber())
	}

	ctx := &planpb.TableCtx{
		TableName:     prevTable.GetName(),
		MessageFqn:    prevTable.GetMessageFqn(),
		NamespaceMode: prevTable.GetNamespaceMode(),
		Namespace:     prevTable.GetNamespace(),
	}

	strategy := p.Resolution.GetStrategy()
	if strategy == planpb.Strategy_DROP_AND_CREATE {
		return []*planpb.Op{
			{Variant: &planpb.Op_DropColumn{DropColumn: &planpb.DropColumn{
				Ctx: ctx, Column: prevCol,
			}}},
			{Variant: &planpb.Op_AddColumn{AddColumn: &planpb.AddColumn{
				Ctx: ctx, Column: currCol,
			}}},
		}, nil
	}

	// SAFE / LOSSLESS_USING / NEEDS_CONFIRM — look up conversion path
	// + render cast template with {{.Col}} / {{.Table}} context.
	prevAlias := prevCol.GetPg().GetCustomTypeAlias()
	currAlias := currCol.GetPg().GetCustomTypeAlias()
	data := carrierUsingContext{
		Col:     currCol.GetName(),
		Table:   prevTable.GetName(),
		Project: projectContext{Encoding: "escape"},
	}
	usingUp := renderUsingTemplate(lookupCustomTypeCast(bkt.curr, prevAlias, currAlias), data)
	data.Col = prevCol.GetName()
	usingDown := renderUsingTemplate(lookupCustomTypeCast(bkt.curr, currAlias, prevAlias), data)
	if usingUp == "" && currAlias != "" && prevAlias != "" {
		return nil, fmt.Errorf("pg_custom_type %q → %q: no conversion path registered for strategy %s — either add a convertible_to entry on %q pointing at %q (or convertible_from on %q pointing at %q), or resolve with --decide %s=custom:<sql-file>",
			prevAlias, currAlias, strategy, prevAlias, currAlias, currAlias, prevAlias, findingKey(p.Finding))
	}

	return []*planpb.Op{{
		Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
			Ctx:        ctx,
			ColumnName: currCol.GetName(),
			Column:     currCol,
			PrevColumn: prevCol,
			Changes: []*planpb.FactChange{{
				Variant: &planpb.FactChange_TypeChange{TypeChange: &planpb.TypeChange{
					FromColumn: prevCol,
					ToColumn:   currCol,
					UsingUp:    usingUp,
					UsingDown:  usingDown,
					Strategy:   strategy,
				}},
			}},
		}},
	}}, nil
}

// lookupCustomTypeCast searches the registered custom_type entries
// for a conversion path from `from` to `to`. Checks first the
// `from` entry's convertible_to, then falls back to the `to`
// entry's convertible_from. Returns the rendered cast template
// (with {{.Col}} / {{.Table}} substituted) or "" if no path exists.
//
// The scan is O(entries-of-from + entries-of-to) — tiny in
// practice, each alias registration lists 1–5 conversion entries.
func lookupCustomTypeCast(schema *irpb.Schema, fromAlias, toAlias string) string {
	if fromAlias == "" || toAlias == "" || fromAlias == toAlias {
		return ""
	}
	if entry, ok := schema.GetPgCustomTypes()[fromAlias]; ok {
		for _, conv := range entry.GetConvertibleTo() {
			if conv.GetType() == toAlias {
				return conv.GetCast()
			}
		}
	}
	if entry, ok := schema.GetPgCustomTypes()[toAlias]; ok {
		for _, conv := range entry.GetConvertibleFrom() {
			if conv.GetType() == fromAlias {
				return conv.GetCast()
			}
		}
	}
	return ""
}

// injectEnumValuesChange — D37 + D40. Resolves an enum_values_remove
// or enum_fqn_change ReviewFinding into an AlterColumn op whose
// EnumValuesChange FactChange carries (added, removed) sets computed
// from prev vs curr enum names. The PG emitter chooses between three
// templates based on the diff:
//
//   - removed non-empty → 4-statement rebuild (CREATE TYPE new /
//     ALTER COLUMN USING / DROP TYPE old / RENAME). Curr's full
//     value list seeds the new type, so any concurrently-added
//     values are folded in; PG-mechanically this is a single
//     atomic block.
//   - added-only → ALTER TYPE ADD VALUE per added name (SAFE pattern).
//   - both empty (FQN change with identical value sets) → emit a
//     no-op marker; PG type name is `<table>_<col>` independent of
//     proto FQN, so an identical-values FQN swap is a database-side
//     no-op.
//
// Only NEEDS_CONFIRM is accepted — the transition is deterministic;
// nothing for the author to decide semantically beyond confirming
// the destructive USING cast at apply time.
func injectEnumValuesChange(p resolvedPair, bkt *bucket) ([]*planpb.Op, error) {
	if p.Resolution.GetStrategy() != planpb.Strategy_NEEDS_CONFIRM {
		return nil, fmt.Errorf("%s on %s: strategy %s is not supported — only NEEDS_CONFIRM is accepted (supply --decide %s=needs_confirm)",
			p.Finding.GetAxis(), findingKey(p.Finding), p.Resolution.GetStrategy(), findingKey(p.Finding))
	}
	ref := p.Finding.GetColumn()
	prevTable, prevCol := findColumnByRef(bkt.prev, ref)
	_, currCol := findColumnByRef(bkt.curr, ref)
	if prevCol == nil || currCol == nil {
		return nil, fmt.Errorf("%s: prev/curr column not found via %s/#%d",
			p.Finding.GetAxis(), ref.GetTableFqn(), ref.GetFieldNumber())
	}
	prevSet := make(map[string]struct{}, len(prevCol.GetEnumNames()))
	for _, n := range prevCol.GetEnumNames() {
		prevSet[n] = struct{}{}
	}
	currSet := make(map[string]struct{}, len(currCol.GetEnumNames()))
	for _, n := range currCol.GetEnumNames() {
		currSet[n] = struct{}{}
	}
	var removedNames []string
	var removedNumbers []int64
	for i, n := range prevCol.GetEnumNames() {
		if _, ok := currSet[n]; ok {
			continue
		}
		removedNames = append(removedNames, n)
		if i < len(prevCol.GetEnumNumbers()) {
			removedNumbers = append(removedNumbers, prevCol.GetEnumNumbers()[i])
		}
	}
	var addedNames []string
	var addedNumbers []int64
	for i, n := range currCol.GetEnumNames() {
		if _, ok := prevSet[n]; ok {
			continue
		}
		addedNames = append(addedNames, n)
		if i < len(currCol.GetEnumNumbers()) {
			addedNumbers = append(addedNumbers, currCol.GetEnumNumbers()[i])
		}
	}
	ctx := &planpb.TableCtx{
		TableName:     prevTable.GetName(),
		MessageFqn:    prevTable.GetMessageFqn(),
		NamespaceMode: prevTable.GetNamespaceMode(),
		Namespace:     prevTable.GetNamespace(),
	}
	return []*planpb.Op{{
		Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
			Ctx:         ctx,
			FieldNumber: currCol.GetFieldNumber(),
			ColumnName:  currCol.GetName(),
			Column:      currCol,
			PrevColumn:  prevCol,
			Changes: []*planpb.FactChange{{
				Variant: &planpb.FactChange_EnumValues{EnumValues: &planpb.EnumValuesChange{
					AddedNames:     addedNames,
					AddedNumbers:   addedNumbers,
					RemovedNames:   removedNames,
					RemovedNumbers: removedNumbers,
				}},
			}},
		}},
	}}, nil
}

// injectDefaultIdentity — D38. Emits an AlterColumn with a single
// DefaultChange FactChange carrying prev/curr Default protos. The PG
// emitter inspects the from/to Default and branches to the identity
// rebuild template (ADD GENERATED + setval for add, DROP IDENTITY for
// drop). Symmetric enough that one injector covers both finding axes.
//
// Only NEEDS_CONFIRM is accepted — the template is deterministic; any
// other strategy is a misuse signal.
func injectDefaultIdentity(p resolvedPair, bkt *bucket) ([]*planpb.Op, error) {
	if p.Resolution.GetStrategy() != planpb.Strategy_NEEDS_CONFIRM {
		return nil, fmt.Errorf("%s on %s: strategy %s is not supported — only NEEDS_CONFIRM is accepted (supply --decide %s=needs_confirm)",
			p.Finding.GetAxis(), findingKey(p.Finding), p.Resolution.GetStrategy(), findingKey(p.Finding))
	}
	ref := p.Finding.GetColumn()
	prevTable, prevCol := findColumnByRef(bkt.prev, ref)
	_, currCol := findColumnByRef(bkt.curr, ref)
	if prevCol == nil || currCol == nil {
		return nil, fmt.Errorf("%s: prev/curr column not found via %s/#%d",
			p.Finding.GetAxis(), ref.GetTableFqn(), ref.GetFieldNumber())
	}
	ctx := &planpb.TableCtx{
		TableName:     prevTable.GetName(),
		MessageFqn:    prevTable.GetMessageFqn(),
		NamespaceMode: prevTable.GetNamespaceMode(),
		Namespace:     prevTable.GetNamespace(),
	}
	return []*planpb.Op{{
		Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
			Ctx:         ctx,
			FieldNumber: currCol.GetFieldNumber(),
			ColumnName:  currCol.GetName(),
			Column:      currCol,
			PrevColumn:  prevCol,
			Changes: []*planpb.FactChange{{
				Variant: &planpb.FactChange_DefaultValue{DefaultValue: &planpb.DefaultChange{
					From: prevCol.GetDefault(),
					To:   currCol.GetDefault(),
				}},
			}},
		}},
	}}, nil
}

// injectPkFlip — D39. Resolves a pk_flip ReviewFinding into an
// AlterColumn op carrying a PrimaryKeyChange FactChange. The emit
// path renders ALTER TABLE ADD PRIMARY KEY (col) for enable and
// ALTER TABLE DROP CONSTRAINT <table>_pkey for disable.
//
// Only NEEDS_CONFIRM is accepted. Multi-column PK swap on the same
// table (two pk_flip findings on one carrier) is flagged as a
// composite transition and hard-errors with a pointer to CUSTOM.
// Reason: a swap requires coordinated DROP + ADD within one ALTER
// TABLE, plus FK-referential preconditions the engine doesn't
// currently reason about; author owns those via --decide custom.
func injectPkFlip(p resolvedPair, allPairs []resolvedPair, bkt *bucket) ([]*planpb.Op, error) {
	if p.Resolution.GetStrategy() != planpb.Strategy_NEEDS_CONFIRM {
		return nil, fmt.Errorf("pk_flip on %s: strategy %s is not supported — only NEEDS_CONFIRM is accepted (supply --decide %s=needs_confirm, or resolve as CUSTOM_MIGRATION for multi-column PK swaps)",
			findingKey(p.Finding), p.Resolution.GetStrategy(), findingKey(p.Finding))
	}
	ref := p.Finding.GetColumn()
	// Multi-column PK swap guard — iterate allPairs (the sorted
	// resolved-findings list this dispatcher is walking) and count
	// pk_flip findings whose table matches.
	table := ref.GetTableFqn()
	var siblings int
	for _, q := range allPairs {
		if q.Finding.GetAxis() != "pk_flip" {
			continue
		}
		if q.Finding.GetColumn().GetTableFqn() != table {
			continue
		}
		siblings++
	}
	if siblings > 1 {
		return nil, fmt.Errorf("pk_flip on %s: table %s has %d concurrent pk_flip findings (composite PK swap) — single-column enable/disable templates don't cover this; resolve all pk findings as CUSTOM_MIGRATION with --decide <col>=custom:<sql-file>",
			findingKey(p.Finding), table, siblings)
	}
	prevTable, prevCol := findColumnByRef(bkt.prev, ref)
	_, currCol := findColumnByRef(bkt.curr, ref)
	if prevCol == nil || currCol == nil {
		return nil, fmt.Errorf("pk_flip: prev/curr column not found via %s/#%d",
			ref.GetTableFqn(), ref.GetFieldNumber())
	}
	ctx := &planpb.TableCtx{
		TableName:     prevTable.GetName(),
		MessageFqn:    prevTable.GetMessageFqn(),
		NamespaceMode: prevTable.GetNamespaceMode(),
		Namespace:     prevTable.GetNamespace(),
	}
	return []*planpb.Op{{
		Variant: &planpb.Op_AlterColumn{AlterColumn: &planpb.AlterColumn{
			Ctx:         ctx,
			FieldNumber: currCol.GetFieldNumber(),
			ColumnName:  currCol.GetName(),
			Column:      currCol,
			PrevColumn:  prevCol,
			Changes: []*planpb.FactChange{{
				Variant: &planpb.FactChange_PrimaryKey{PrimaryKey: &planpb.PrimaryKeyChange{
					From: prevCol.GetPk(),
					To:   currCol.GetPk(),
				}},
			}},
		}},
	}}, nil
}

// renderCarrierUsing renders the USING expressions for forward +
// reverse carrier transitions. Forward uses the classifier's
// from→to cell template; reverse uses the to→from cell template
// if its strategy is non-CUSTOM, else falls back to a plain
// "<col>::<type>" cast.
//
// Template context keys:
//   {{.Col}}       — post-rename column name
//   {{.Table}}     — bare table name (no namespace qualifier)
//   {{.Project.Encoding}} — project-default bytes encoding
//                           (hex / utf8 / escape). Defaults to
//                           "escape" — PG's universally-safe
//                           encoding that round-trips for every
//                           byte string. Projects override via
//                           future w17.yaml Project.Encoding.
//
// Template errors fall back to an empty USING — caller's emit
// omits the clause rather than serialising broken SQL.
func renderCarrierUsing(
	cls *classifier.Classifier,
	prev, curr *irpb.Column,
	table *irpb.Table,
) (string, string) {
	forward := cls.Carrier(prev.GetCarrier(), curr.GetCarrier())
	reverse := cls.Carrier(curr.GetCarrier(), prev.GetCarrier())

	data := carrierUsingContext{
		Col:     curr.GetName(),
		Table:   table.GetName(),
		Project: projectContext{Encoding: "escape"},
	}

	usingUp := renderUsingTemplate(forward.Using, data)
	// Reverse direction: if the symmetric cell is author-SQL-
	// required (CUSTOM_MIGRATION / no template), we can't
	// auto-synthesise a reliable USING for the down. Leave empty
	// and the emitter drops the USING clause — PG may accept the
	// implicit cast or refuse at apply time (which is the correct
	// signal to the operator that rollback needs a custom path).
	usingDown := ""
	if reverse.Strategy != planpb.Strategy_CUSTOM_MIGRATION && reverse.Using != "" {
		data.Col = prev.GetName()
		usingDown = renderUsingTemplate(reverse.Using, data)
	}
	return usingUp, usingDown
}

// carrierUsingContext is the template-data bag for carrier.yaml
// `using:` entries. Exported-looking field names (capitalised) so
// text/template can reach them.
type carrierUsingContext struct {
	Col     string
	Table   string
	Project projectContext
}

// projectContext surfaces project-level settings to classifier
// templates. Today only Encoding (bytes↔text conversion). Future:
// Locale, TZ, etc. Lives here rather than classifier pkg because
// it's rendering-side — classifier is the catalog, engine is the
// renderer that knows the project.
type projectContext struct {
	Encoding string
}

func renderUsingTemplate(tmpl string, data any) string {
	if tmpl == "" {
		return ""
	}
	t, err := template.New("using").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return ""
	}
	return buf.String()
}

// findColumnByRef locates the (table, column) pair a ColumnRef
// points at within a Schema. Returns (nil, nil) when the ref
// doesn't resolve — callers treat that as a hard error because
// engine produced the ref from the same Schema it's now re-
// reading.
func findColumnByRef(schema *irpb.Schema, ref *planpb.ColumnRef) (*irpb.Table, *irpb.Column) {
	if schema == nil || ref == nil {
		return nil, nil
	}
	for _, t := range schema.GetTables() {
		if t.GetMessageFqn() != ref.GetTableFqn() {
			continue
		}
		for _, c := range t.GetColumns() {
			if c.GetFieldNumber() == ref.GetFieldNumber() {
				return t, c
			}
		}
	}
	return nil, nil
}
