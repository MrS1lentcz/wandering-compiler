// Package ir builds the dialect-agnostic intermediate representation from
// loader output. The IR is defined as proto messages at
// proto/domains/compiler/types/ir.proto (package w17.compiler.ir → Go irpb)
// so sibling tools — differ, SQL emitters, back-compat lint, changelog,
// visual editor — consume it wire-compat without speaking Go. See
// docs/iteration-1.md D4 (rev 2026-04-21) and tech-spec strategic decision
// #8 (proto, not Go structs).
//
// Build enforces every invariant from D2 / D7 / D8 / D9 and aggregates
// errors via errors.Join so one compile run surfaces all problems. Every
// user-facing diagnostic flows through *diag.Error (file:line:col + why/fix).
//
// Parse-stage descriptor handles live on the loader's LoadedFile (Go struct,
// non-serializable); the proto boundary starts here — Build populates
// SourceLocation messages from protoreflect.SourceLocations in place of
// live FieldDescriptor fields.
package ir

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	w17pb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17"
)

// Build converts a loaded .proto into a validated *irpb.Schema.
func Build(lf *loader.LoadedFile) (*irpb.Schema, error) {
	b := &builder{lf: lf, msgByTable: map[string]*loader.LoadedMessage{}}
	schema := &irpb.Schema{}
	for _, msg := range lf.Messages {
		if msg.Table == nil {
			// Messages without (w17.db.table) aren't compiler inputs in
			// iteration-1. (Enums and helper types live in the same file.)
			continue
		}
		tbl := b.buildTable(msg)
		if tbl != nil {
			schema.Tables = append(schema.Tables, tbl)
			b.msgByTable[tbl.GetName()] = msg
		}
	}
	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}
	b.resolveFKs(schema)
	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}
	return schema, nil
}

type builder struct {
	lf         *loader.LoadedFile
	errs       []error
	msgByTable map[string]*loader.LoadedMessage
}

func (b *builder) err(e *diag.Error) { b.errs = append(b.errs, e) }

func (b *builder) buildTable(msg *loader.LoadedMessage) *irpb.Table {
	if msg.Table.GetName() == "" {
		b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).name is empty", msg.Desc.Name()).
			WithWhy("the SQL table name is never auto-derived from the proto message name (D6 — explicit over implicit)").
			WithFix(`add option (w17.db.table) = { name: "snake_case_plural" };`))
		return nil
	}
	tbl := &irpb.Table{
		Name:       msg.Table.GetName(),
		MessageFqn: string(msg.Desc.FullName()),
		Location:   sourceLocation(msg.Desc),
	}

	for _, f := range msg.Fields {
		col := b.buildColumn(f)
		if col != nil {
			tbl.Columns = append(tbl.Columns, col)
			if col.GetPk() {
				tbl.PrimaryKey = append(tbl.PrimaryKey, col.GetProtoName())
			}
		}
	}

	// Parse FK references; target resolution runs later in resolveFKs.
	for _, col := range tbl.Columns {
		f := findLoadedField(msg, col.GetProtoName())
		if f == nil || f.Field == nil || f.Field.GetFk() == "" {
			continue
		}
		ref, ok := parseFKRef(f.Field.GetFk())
		if !ok {
			b.err(diag.Atf(f.Desc, `field %q: fk must be "<table>.<column>", got %q`, col.GetProtoName(), f.Field.GetFk()).
				WithWhy("iteration-1 supports only same-file references in the short form — cross-module package paths arrive later").
				WithFix(`set fk: "categories.id" (two segments, table and column, separated by a single dot)`))
			continue
		}
		action := irpb.FKAction_FK_ACTION_CASCADE
		if f.Field.Orphanable != nil {
			if *f.Field.Orphanable && !f.Field.GetNull() {
				b.err(diag.Atf(f.Desc, "field %q: orphanable=true requires null=true", col.GetProtoName()).
					WithWhy("SET NULL on a NOT NULL column would violate the column's own constraint during a parent delete").
					WithFix(`either set null: true on (w17.field), or drop orphanable and let the parent delete cascade`))
				continue
			}
			if *f.Field.Orphanable {
				action = irpb.FKAction_FK_ACTION_SET_NULL
			}
		} else if f.Field.GetNull() {
			// Inferred: nullable child rows survive parent deletes.
			action = irpb.FKAction_FK_ACTION_SET_NULL
		}
		tbl.ForeignKeys = append(tbl.ForeignKeys, &irpb.ForeignKey{
			Column:       col.GetProtoName(),
			TargetTable:  ref.table,
			TargetColumn: ref.column,
			OnDelete:     action,
		})
	}

	// Table-level indexes.
	for i, idx := range msg.Table.GetIndexes() {
		if len(idx.GetFields()) == 0 {
			b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] has no fields", msg.Desc.Name(), i).
				WithWhy("an index with zero columns has nothing to index on").
				WithFix("supply at least one field name in the `fields:` list"))
			continue
		}
		for _, fname := range idx.GetFields() {
			if findLoadedField(msg, fname) == nil {
				b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] references unknown field %q", msg.Desc.Name(), i, fname).
					WithWhy("every index column must refer to a declared proto field on the same message").
					WithFix(fmt.Sprintf("either declare field %q on the message, or remove it from this index's `fields:` list", fname)))
			}
		}
		for _, fname := range idx.GetInclude() {
			if findLoadedField(msg, fname) == nil {
				b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] INCLUDE references unknown field %q", msg.Desc.Name(), i, fname).
					WithWhy("every INCLUDE column must refer to a declared proto field on the same message").
					WithFix(fmt.Sprintf("either declare field %q on the message, or remove it from `include:`", fname)))
			}
		}
		tbl.Indexes = append(tbl.Indexes, &irpb.Index{
			Name:    idx.GetName(),
			Fields:  append([]string(nil), idx.GetFields()...),
			Unique:  idx.GetUnique(),
			Include: append([]string(nil), idx.GetInclude()...),
		})
	}

	// Synthesise UNIQUE INDEXes for (w17.field).unique columns. PK columns
	// are skipped — every SQL dialect auto-indexes the PRIMARY KEY, and a
	// duplicate unique index would clutter the migration and pg_indexes.
	for _, col := range tbl.Columns {
		if !col.GetUnique() || col.GetPk() {
			continue
		}
		if hasSingleColUniqueIndex(tbl.Indexes, col.GetProtoName()) {
			continue
		}
		tbl.Indexes = append(tbl.Indexes, &irpb.Index{
			Fields: []string{col.GetProtoName()},
			Unique: true,
		})
	}
	// Synthesise plain storage indexes for (w17.db.column).index columns.
	for _, col := range tbl.Columns {
		if !col.GetStorageIndex() {
			continue
		}
		if hasSingleColIndex(tbl.Indexes, col.GetProtoName()) {
			continue
		}
		tbl.Indexes = append(tbl.Indexes, &irpb.Index{
			Fields: []string{col.GetProtoName()},
		})
	}

	return tbl
}

func (b *builder) buildColumn(lf *loader.LoadedField) *irpb.Column {
	desc := lf.Desc
	protoName := string(desc.Name())

	carrier, carrierOK := protoKindToCarrier(desc)
	if !carrierOK {
		b.err(diag.Atf(desc, "field %q: carrier %s is not supported in iteration-1", protoName, describeKind(desc)).
			WithWhy("iteration-1 accepts string, int32, int64, bool, double, google.protobuf.Timestamp and google.protobuf.Duration as DB-column carriers; other kinds (bytes, repeated, oneof, nested messages) are parked for later iterations").
			WithFix("change the field's proto type to one of the supported carriers, or drop the (w17.field) annotation if the field isn't a DB column"))
		return nil
	}

	col := &irpb.Column{
		Name:      protoName,
		ProtoName: protoName,
		Location:  sourceLocation(desc),
		Carrier:   carrier,
	}

	// Pull data-level options from (w17.field), if present.
	fieldOpt := lf.Field
	if fieldOpt == nil {
		// bool carrier is allowed to lack (w17.field) (it has no SemType);
		// every other carrier requires at least a `type:`.
		if carrier != irpb.Carrier_CARRIER_BOOL {
			b.err(diag.Atf(desc, "field %q: missing (w17.field) option", protoName).
				WithWhy("every non-bool column needs a semantic type so the emitter can pick a concrete SQL column type — carrier alone is ambiguous (e.g. int64 could be ID, NUMBER, or COUNTER)").
				WithFix(fmt.Sprintf(`annotate the field, e.g. %s %s = %d [(w17.field) = { type: %s }];`,
					describeKind(desc), protoName, desc.Number(), suggestedTypeFor(carrier))))
			return nil
		}
	}

	// Storage-level options from (w17.db.column).
	if lf.Column != nil {
		if override := lf.Column.GetName(); override != "" {
			col.Name = override
		}
		col.StorageIndex = lf.Column.GetIndex()
	}

	// Carrier → SemType validation (D2 table).
	semType := irpb.SemType_SEM_UNSPECIFIED
	if fieldOpt != nil {
		semType = protoTypeToSem(fieldOpt.GetType())
	}
	if carrier == irpb.Carrier_CARRIER_DURATION && semType == irpb.SemType_SEM_UNSPECIFIED {
		semType = irpb.SemType_SEM_INTERVAL // D2: Duration defaults to INTERVAL when type unset.
	}
	if err := validateCarrierSemType(desc, carrier, semType); err != nil {
		b.err(err)
		return nil
	}
	col.Type = semType

	// Nullability, PK, uniqueness, immutability.
	if fieldOpt != nil {
		col.Nullable = fieldOpt.GetNull()
		col.Pk = fieldOpt.GetPk()
		col.Unique = fieldOpt.GetUnique() || col.Pk // PK implies UNIQUE (D2 note).
		col.Immutable = fieldOpt.GetImmutable()
	}

	// orphanable validity — must accompany fk.
	if fieldOpt != nil && fieldOpt.Orphanable != nil && fieldOpt.GetFk() == "" {
		b.err(diag.Atf(desc, "field %q: orphanable set without fk", protoName).
			WithWhy("orphanable declares what happens to this row when its *parent* row is deleted — meaningless without an fk pointing at a parent").
			WithFix(`either add fk: "<table>.<column>" on (w17.field), or remove orphanable`))
	}

	// max_len: required for CHAR/SLUG; string-only for all other types.
	if fieldOpt != nil {
		col.MaxLen = fieldOpt.GetMaxLen()
	}
	if carrier == irpb.Carrier_CARRIER_STRING {
		if (semType == irpb.SemType_SEM_CHAR || semType == irpb.SemType_SEM_SLUG) && col.MaxLen <= 0 {
			b.err(diag.Atf(desc, "field %q: type %s requires max_len", protoName, displaySemType(semType)).
				WithWhy("CHAR/SLUG render as VARCHAR(N) — without N the column type has no fixed size").
				WithFix("add max_len to (w17.field), e.g. max_len: 80 for short names, 255 for titles"))
		}
	} else if col.MaxLen != 0 {
		b.err(diag.Atf(desc, "field %q: max_len is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
			WithWhy("max_len controls char_length on string columns; numeric/temporal/bool columns have no length dimension").
			WithFix("drop max_len from (w17.field), or change the proto field to a string carrier"))
	}

	// DECIMAL precision/scale.
	if fieldOpt != nil {
		col.Precision = fieldOpt.GetPrecision()
		if fieldOpt.Scale != nil {
			scale := fieldOpt.GetScale()
			col.Scale = &scale
		}
	}
	if semType == irpb.SemType_SEM_DECIMAL {
		if col.GetPrecision() <= 0 {
			b.err(diag.Atf(desc, "field %q: type DECIMAL requires precision", protoName).
				WithWhy("DECIMAL renders as NUMERIC(precision, scale) — precision is the total number of significant digits and has no safe default").
				WithFix("add precision (and optionally scale) to (w17.field), e.g. { type: DECIMAL, precision: 12, scale: 4 }"))
		}
		if col.Scale != nil && *col.Scale < 0 {
			b.err(diag.Atf(desc, "field %q: DECIMAL scale must be >= 0", protoName).
				WithWhy("negative scale is meaningless for NUMERIC").
				WithFix("drop scale or set it to a non-negative integer"))
		}
		if col.Scale != nil && *col.Scale > col.GetPrecision() {
			b.err(diag.Atf(desc, "field %q: DECIMAL scale (%d) exceeds precision (%d)", protoName, *col.Scale, col.GetPrecision()).
				WithWhy("scale counts digits after the decimal point and cannot exceed total digits").
				WithFix(fmt.Sprintf("raise precision to at least %d, or lower scale to at most %d", *col.Scale, col.GetPrecision())))
		}
	} else {
		if col.GetPrecision() != 0 || col.Scale != nil {
			b.err(diag.Atf(desc, "field %q: precision/scale only apply to type DECIMAL (got %s)", protoName, displaySemType(semType)).
				WithWhy("MONEY/PERCENTAGE/RATIO are fixed-shape presets; other types have no precision/scale dimension").
				WithFix("drop precision/scale, or change the field to type: DECIMAL"))
		}
	}

	// Default value — resolve the oneof and validate against carrier/type.
	if fieldOpt != nil {
		def, err := b.resolveDefault(desc, fieldOpt, carrier, semType)
		if err != nil {
			b.err(err)
		}
		col.Default = def
	}

	// String-only / numeric-only option validation.
	if fieldOpt != nil {
		if carrier != irpb.Carrier_CARRIER_STRING {
			if fieldOpt.MinLen != nil {
				b.err(diag.Atf(desc, "field %q: min_len is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("min_len controls char_length on strings; other carriers have no length").
					WithFix("drop min_len, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetBlank() {
				b.err(diag.Atf(desc, "field %q: blank is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("blank relaxes the implicit `col <> ''` CHECK on strings; non-string columns have no such CHECK").
					WithFix("drop blank, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetPattern() != "" {
				b.err(diag.Atf(desc, "field %q: pattern is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("pattern emits a regex CHECK; regex only applies to strings").
					WithFix("drop pattern, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetChoices() != "" {
				b.err(diag.Atf(desc, "field %q: choices is only valid on string carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("choices emits `CHECK col IN ('A','B',…)` matched against enum *value names*, which are strings").
					WithFix("drop choices, or change the proto field to a string carrier"))
			}
		}
		numericOnly := carrier == irpb.Carrier_CARRIER_INT32 || carrier == irpb.Carrier_CARRIER_INT64 || carrier == irpb.Carrier_CARRIER_DOUBLE
		if !numericOnly {
			if fieldOpt.Gt != nil || fieldOpt.Gte != nil || fieldOpt.Lt != nil || fieldOpt.Lte != nil {
				b.err(diag.Atf(desc, "field %q: gt/gte/lt/lte are only valid on numeric carriers (got %s)", protoName, displayCarrier(carrier)).
					WithWhy("the range CHECK emits a numeric comparison; it's undefined for non-numeric types").
					WithFix("drop the bound, or change the proto field to int32/int64/double"))
			}
		}
	}

	// Build Checks from the surviving facts.
	if fieldOpt != nil {
		b.attachChecks(col, fieldOpt, carrier, semType, desc)
	}

	// Postgres dialect passthrough.
	if lf.PgField != nil {
		col.Pg = &irpb.PgOptions{
			Jsonb:              lf.PgField.GetJsonb(),
			Inet:               lf.PgField.GetInet(),
			Tsvector:           lf.PgField.GetTsvector(),
			Hstore:             lf.PgField.GetHstore(),
			CustomType:         lf.PgField.GetCustomType(),
			RequiredExtensions: append([]string(nil), lf.PgField.GetRequiredExtensions()...),
		}
	}

	return col
}

func (b *builder) attachChecks(col *irpb.Column, opt *w17pb.Field, carrier irpb.Carrier, semType irpb.SemType, origin protoreflect.FieldDescriptor) {
	// LengthCheck — omitted for CHAR/SLUG since VARCHAR(N) covers the upper
	// bound. MinLen always produces a CHECK when present.
	if carrier == irpb.Carrier_CARRIER_STRING {
		hasMin := opt.MinLen != nil
		hasMax := opt.GetMaxLen() > 0 && !(semType == irpb.SemType_SEM_CHAR || semType == irpb.SemType_SEM_SLUG)
		if hasMin || hasMax {
			lc := &irpb.LengthCheck{}
			if hasMin {
				min := opt.GetMinLen()
				lc.Min = &min
			}
			if hasMax {
				max := opt.GetMaxLen()
				lc.Max = &max
			}
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Length{Length: lc}})
		}
		// BlankCheck — added unless author opted into blank or column is nullable.
		// Skipped for string-carrier sem types whose SQL storage isn't string-typed
		// (UUID → UUID, DECIMAL → NUMERIC); `CHECK (col <> '')` on those would fail
		// at apply time because PG can't cast '' to UUID / NUMERIC.
		if !opt.GetBlank() && !col.GetNullable() && semTypeStoresAsString(semType) {
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Blank{Blank: &irpb.BlankCheck{}}})
		}
	}

	// RangeCheck.
	if opt.Gt != nil || opt.Gte != nil || opt.Lt != nil || opt.Lte != nil {
		rc := &irpb.RangeCheck{Gt: opt.Gt, Gte: opt.Gte, Lt: opt.Lt, Lte: opt.Lte}
		col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Range{Range: rc}})
	}

	// RegexCheck — pattern override takes precedence over type-implied.
	if opt.GetPattern() != "" {
		col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Regex{Regex: &irpb.RegexCheck{
			Pattern: opt.GetPattern(),
			Source:  irpb.RegexSource_REGEX_FROM_PATTERN,
		}}})
	} else if regex := defaultRegexFor(semType); regex != "" {
		col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Regex{Regex: &irpb.RegexCheck{
			Pattern: regex,
			Source:  irpb.RegexSource_REGEX_FROM_TYPE,
		}}})
	}

	// ChoicesCheck — resolve the enum FQN to its value names.
	if opt.GetChoices() != "" {
		values, resolveErr := b.resolveEnumValues(origin, opt.GetChoices())
		if resolveErr != nil {
			b.err(resolveErr)
		} else {
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Choices{Choices: &irpb.ChoicesCheck{
				EnumFqn: opt.GetChoices(),
				Values:  values,
			}}})
		}
	}

	// Percentage/Ratio: emit implicit domain constraints when no author bounds conflict.
	switch semType {
	case irpb.SemType_SEM_PERCENTAGE:
		if opt.Gte == nil && opt.Gt == nil && opt.Lt == nil && opt.Lte == nil {
			zero := 0.0
			hundred := 100.0
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{Gte: &zero, Lte: &hundred}}})
		}
	case irpb.SemType_SEM_RATIO:
		if opt.Gte == nil && opt.Gt == nil && opt.Lt == nil && opt.Lte == nil {
			zero := 0.0
			one := 1.0
			col.Checks = append(col.Checks, &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{Gte: &zero, Lte: &one}}})
		}
	}
}

// resolveDefault parses the (w17.field).default oneof and validates
// carrier/type compatibility per D7.
func (b *builder) resolveDefault(desc protoreflect.FieldDescriptor, opt *w17pb.Field, carrier irpb.Carrier, semType irpb.SemType) (*irpb.Default, *diag.Error) {
	switch d := opt.GetDefault().(type) {
	case nil:
		return nil, nil
	case *w17pb.Field_DefaultString:
		if carrier != irpb.Carrier_CARRIER_STRING {
			return nil, diag.Atf(desc, "field %q: default_string requires a string carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("default_string emits a string literal — non-string columns can't accept it").
				WithFix("use default_int / default_double / default_auto for non-string carriers, or change the proto field to string")
		}
		return &irpb.Default{Variant: &irpb.Default_LiteralString{LiteralString: d.DefaultString}}, nil
	case *w17pb.Field_DefaultInt:
		if carrier != irpb.Carrier_CARRIER_INT32 && carrier != irpb.Carrier_CARRIER_INT64 {
			return nil, diag.Atf(desc, "field %q: default_int requires an integer carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("default_int emits an integer literal").
				WithFix("use default_double for double carriers, or default_string for strings")
		}
		return &irpb.Default{Variant: &irpb.Default_LiteralInt{LiteralInt: d.DefaultInt}}, nil
	case *w17pb.Field_DefaultDouble:
		if carrier != irpb.Carrier_CARRIER_DOUBLE {
			return nil, diag.Atf(desc, "field %q: default_double requires a double carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("default_double emits a floating-point literal").
				WithFix("use default_int for integer carriers, default_string for strings")
		}
		return &irpb.Default{Variant: &irpb.Default_LiteralDouble{LiteralDouble: d.DefaultDouble}}, nil
	case *w17pb.Field_DefaultAuto:
		kind := protoAutoToKind(d.DefaultAuto)
		if err := validateAutoDefault(desc, kind, carrier, semType); err != nil {
			return nil, err
		}
		return &irpb.Default{Variant: &irpb.Default_Auto{Auto: kind}}, nil
	default:
		return nil, diag.Atf(desc, "field %q: unknown default branch %T", desc.Name(), d).
			WithWhy("this is a compiler bug — the default oneof grew a branch the IR builder doesn't recognise").
			WithFix("please file an issue with the failing .proto attached")
	}
}

// resolveEnumValues walks a field's parent file + transitive imports for a
// fully-qualified enum name and returns the ordered value-name slice with the
// mandatory *_UNSPECIFIED zero sentinel stripped.
func (b *builder) resolveEnumValues(origin protoreflect.FieldDescriptor, fqn string) ([]string, *diag.Error) {
	enum := findEnum(origin.ParentFile(), protoreflect.FullName(fqn))
	if enum == nil {
		return nil, diag.Atf(origin, "field %q: choices enum %q not found", origin.Name(), fqn).
			WithWhy("choices takes a fully-qualified proto enum name; the IR builder walked the current file and its imports and could not locate it").
			WithFix(`verify the FQN (package + enum name, e.g. "catalog.v1.ProductStatus") and make sure the defining .proto is imported`)
	}
	values := enum.Values()
	out := make([]string, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if v.Number() == 0 {
			// Proto3 convention: 0-value is *_UNSPECIFIED / sentinel.
			continue
		}
		out = append(out, string(v.Name()))
	}
	if len(out) == 0 {
		return nil, diag.Atf(origin, "field %q: choices enum %q has no non-zero values", origin.Name(), fqn).
			WithWhy("every declared enum value had number 0 (the sentinel); a CHECK IN () would match nothing").
			WithFix("add at least one real value to the enum (e.g. DRAFT = 1)")
	}
	return out, nil
}

// resolveFKs verifies every FK target table/column exists among compiled tables.
func (b *builder) resolveFKs(schema *irpb.Schema) {
	byName := map[string]*irpb.Table{}
	for _, t := range schema.Tables {
		byName[t.GetName()] = t
	}
	for _, t := range schema.Tables {
		msg := b.msgByTable[t.GetName()]
		for _, fk := range t.ForeignKeys {
			// Descriptor of the FK-owning field, for file:line:col anchoring.
			var desc protoreflect.Descriptor
			if msg != nil {
				if f := findLoadedField(msg, fk.GetColumn()); f != nil {
					desc = f.Desc
				}
			}
			target, ok := byName[fk.GetTargetTable()]
			if !ok {
				b.err(diag.Atf(desc, "field %q: fk target table %q not defined in this file", fk.GetColumn(), fk.GetTargetTable()).
					WithWhy("iteration-1 resolves fk references within the single compiled proto — cross-file fk resolution lands in iter-2").
					WithFix(fmt.Sprintf("add a message annotated with (w17.db.table).name = %q to this file, or correct the fk reference", fk.GetTargetTable())))
				continue
			}
			if !hasColumn(target, fk.GetTargetColumn()) {
				b.err(diag.Atf(desc, "field %q: fk target column %q not found on table %q", fk.GetColumn(), fk.GetTargetColumn(), fk.GetTargetTable()).
					WithWhy("the fk references a column that doesn't exist on the target table — a broken FK would fail at apply time").
					WithFix(fmt.Sprintf("verify the column name (case-sensitive, proto-field name) on message for table %q, or correct the fk reference", fk.GetTargetTable())))
			}
		}
	}
}

// --- small helpers ---

func sourceLocation(d protoreflect.Descriptor) *irpb.SourceLocation {
	if d == nil {
		return nil
	}
	file := d.ParentFile()
	if file == nil {
		return nil
	}
	sl := &irpb.SourceLocation{File: file.Path()}
	loc := file.SourceLocations().ByDescriptor(d)
	if loc.StartLine > 0 || loc.StartColumn > 0 {
		sl.Line = int32(loc.StartLine + 1)
		sl.Col = int32(loc.StartColumn + 1)
	}
	return sl
}

func protoKindToCarrier(fd protoreflect.FieldDescriptor) (irpb.Carrier, bool) {
	if fd.IsList() || fd.IsMap() || fd.ContainingOneof() != nil {
		return irpb.Carrier_CARRIER_UNSPECIFIED, false
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return irpb.Carrier_CARRIER_BOOL, true
	case protoreflect.StringKind:
		return irpb.Carrier_CARRIER_STRING, true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return irpb.Carrier_CARRIER_INT32, true
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return irpb.Carrier_CARRIER_INT64, true
	case protoreflect.DoubleKind:
		return irpb.Carrier_CARRIER_DOUBLE, true
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Timestamp":
			return irpb.Carrier_CARRIER_TIMESTAMP, true
		case "google.protobuf.Duration":
			return irpb.Carrier_CARRIER_DURATION, true
		}
	}
	return irpb.Carrier_CARRIER_UNSPECIFIED, false
}

func describeKind(fd protoreflect.FieldDescriptor) string {
	if fd.IsList() {
		return "repeated " + fd.Kind().String()
	}
	if fd.IsMap() {
		return "map"
	}
	if fd.Kind() == protoreflect.MessageKind {
		return string(fd.Message().FullName())
	}
	return fd.Kind().String()
}

func suggestedTypeFor(c irpb.Carrier) string {
	switch c {
	case irpb.Carrier_CARRIER_STRING:
		return "CHAR, max_len: 255"
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		return "NUMBER"
	case irpb.Carrier_CARRIER_DOUBLE:
		return "NUMBER"
	case irpb.Carrier_CARRIER_TIMESTAMP:
		return "DATETIME"
	case irpb.Carrier_CARRIER_DURATION:
		return "INTERVAL"
	}
	return "CHAR"
}

func protoTypeToSem(t w17pb.Type) irpb.SemType {
	switch t {
	case w17pb.Type_CHAR:
		return irpb.SemType_SEM_CHAR
	case w17pb.Type_TEXT:
		return irpb.SemType_SEM_TEXT
	case w17pb.Type_UUID:
		return irpb.SemType_SEM_UUID
	case w17pb.Type_EMAIL:
		return irpb.SemType_SEM_EMAIL
	case w17pb.Type_URL:
		return irpb.SemType_SEM_URL
	case w17pb.Type_SLUG:
		return irpb.SemType_SEM_SLUG
	case w17pb.Type_NUMBER:
		return irpb.SemType_SEM_NUMBER
	case w17pb.Type_ID:
		return irpb.SemType_SEM_ID
	case w17pb.Type_COUNTER:
		return irpb.SemType_SEM_COUNTER
	case w17pb.Type_MONEY:
		return irpb.SemType_SEM_MONEY
	case w17pb.Type_PERCENTAGE:
		return irpb.SemType_SEM_PERCENTAGE
	case w17pb.Type_RATIO:
		return irpb.SemType_SEM_RATIO
	case w17pb.Type_DECIMAL:
		return irpb.SemType_SEM_DECIMAL
	case w17pb.Type_DATE:
		return irpb.SemType_SEM_DATE
	case w17pb.Type_TIME:
		return irpb.SemType_SEM_TIME
	case w17pb.Type_DATETIME:
		return irpb.SemType_SEM_DATETIME
	case w17pb.Type_INTERVAL:
		return irpb.SemType_SEM_INTERVAL
	}
	return irpb.SemType_SEM_UNSPECIFIED
}

func protoAutoToKind(a w17pb.AutoDefault) irpb.AutoKind {
	switch a {
	case w17pb.AutoDefault_NOW:
		return irpb.AutoKind_AUTO_NOW
	case w17pb.AutoDefault_UUID_V4:
		return irpb.AutoKind_AUTO_UUID_V4
	case w17pb.AutoDefault_UUID_V7:
		return irpb.AutoKind_AUTO_UUID_V7
	case w17pb.AutoDefault_EMPTY_JSON_ARRAY:
		return irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY
	case w17pb.AutoDefault_EMPTY_JSON_OBJECT:
		return irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT
	case w17pb.AutoDefault_TRUE:
		return irpb.AutoKind_AUTO_TRUE
	case w17pb.AutoDefault_FALSE:
		return irpb.AutoKind_AUTO_FALSE
	case w17pb.AutoDefault_IDENTITY:
		return irpb.AutoKind_AUTO_IDENTITY
	}
	return irpb.AutoKind_AUTO_UNSPECIFIED
}

// validateCarrierSemType enforces docs/iteration-1.md D2.
func validateCarrierSemType(desc protoreflect.FieldDescriptor, carrier irpb.Carrier, sem irpb.SemType) *diag.Error {
	name := desc.Name()
	switch carrier {
	case irpb.Carrier_CARRIER_BOOL:
		if sem != irpb.SemType_SEM_UNSPECIFIED {
			return diag.Atf(desc, "field %q: bool carrier must not set a semantic type (got %s)", name, displaySemType(sem)).
				WithWhy("bool has exactly one column shape (BOOLEAN) — there is no semantic refinement to pick").
				WithFix("drop type: from (w17.field); for a default value use default_auto: TRUE or FALSE")
		}
	case irpb.Carrier_CARRIER_STRING:
		switch sem {
		case irpb.SemType_SEM_CHAR, irpb.SemType_SEM_TEXT, irpb.SemType_SEM_UUID, irpb.SemType_SEM_EMAIL, irpb.SemType_SEM_URL, irpb.SemType_SEM_SLUG, irpb.SemType_SEM_DECIMAL:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: string carrier requires a semantic type", name).
				WithWhy("string maps to many SQL types (VARCHAR, TEXT, UUID) with different constraints; the compiler won't guess").
				WithFix("add one of: CHAR, TEXT, UUID, EMAIL, URL, SLUG, DECIMAL")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a string carrier", name, displaySemType(sem)).
				WithWhy("the D2 carrier×type table restricts string to CHAR, TEXT, UUID, EMAIL, URL, SLUG, DECIMAL").
				WithFix("pick one of the string-valid types, or change the carrier")
		}
	case irpb.Carrier_CARRIER_INT32, irpb.Carrier_CARRIER_INT64:
		switch sem {
		case irpb.SemType_SEM_NUMBER, irpb.SemType_SEM_ID, irpb.SemType_SEM_COUNTER:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: %s carrier requires a semantic type", name, displayCarrier(carrier)).
				WithWhy("int32/int64 can carry NUMBER / ID / COUNTER — each emits a different SQL shape (PK vs indexed FK vs bounded counter)").
				WithFix("add one of: NUMBER, ID, COUNTER")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on an integer carrier", name, displaySemType(sem)).
				WithWhy("the D2 carrier×type table restricts integer carriers to NUMBER, ID, COUNTER").
				WithFix("pick an integer-valid type, or change the carrier (e.g. double for MONEY)")
		}
	case irpb.Carrier_CARRIER_DOUBLE:
		switch sem {
		case irpb.SemType_SEM_NUMBER, irpb.SemType_SEM_MONEY, irpb.SemType_SEM_PERCENTAGE, irpb.SemType_SEM_RATIO:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: double carrier requires a semantic type", name).
				WithWhy("double can carry NUMBER / MONEY / PERCENTAGE / RATIO — each emits different constraints (bounds for PERCENTAGE/RATIO, scale for MONEY)").
				WithFix("add one of: NUMBER, MONEY, PERCENTAGE, RATIO; use DECIMAL on a string carrier for exact precision")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a double carrier", name, displaySemType(sem)).
				WithWhy("the D2 carrier×type table restricts double to NUMBER, MONEY, PERCENTAGE, RATIO").
				WithFix("pick a double-valid type, or change the carrier")
		}
	case irpb.Carrier_CARRIER_TIMESTAMP:
		switch sem {
		case irpb.SemType_SEM_DATE, irpb.SemType_SEM_TIME, irpb.SemType_SEM_DATETIME:
			// OK
		case irpb.SemType_SEM_UNSPECIFIED:
			return diag.Atf(desc, "field %q: google.protobuf.Timestamp carrier requires a semantic type", name).
				WithWhy("Timestamp can be a DATE, TIME, or DATETIME — each emits a different SQL column type").
				WithFix("add one of: DATE, TIME, DATETIME")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a Timestamp carrier", name, displaySemType(sem)).
				WithWhy("Timestamp is restricted to DATE / TIME / DATETIME per D2").
				WithFix("pick a Timestamp-valid type (DATE, TIME, DATETIME), or change the carrier")
		}
	case irpb.Carrier_CARRIER_DURATION:
		if sem != irpb.SemType_SEM_INTERVAL {
			return diag.Atf(desc, "field %q: Duration carrier must be INTERVAL (got %s)", name, displaySemType(sem)).
				WithWhy("google.protobuf.Duration maps 1:1 to the SQL INTERVAL type — no other refinement is defined in iter-1").
				WithFix("set type: INTERVAL or drop the type: key so it's inferred")
		}
	}
	return nil
}

// validateAutoDefault enforces the Type × AutoDefault table from D7.
func validateAutoDefault(desc protoreflect.FieldDescriptor, kind irpb.AutoKind, carrier irpb.Carrier, sem irpb.SemType) *diag.Error {
	switch kind {
	case irpb.AutoKind_AUTO_NOW:
		if carrier != irpb.Carrier_CARRIER_TIMESTAMP {
			return diag.Atf(desc, "field %q: default_auto: NOW requires a Timestamp carrier (got %s)", desc.Name(), displayCarrier(carrier)).
				WithWhy("NOW resolves to CURRENT_DATE / CURRENT_TIME / CURRENT_TIMESTAMP; only Timestamp columns accept any of those").
				WithFix("change the carrier to google.protobuf.Timestamp (type: DATETIME / DATE / TIME), or remove default_auto")
		}
	case irpb.AutoKind_AUTO_UUID_V4, irpb.AutoKind_AUTO_UUID_V7:
		if carrier != irpb.Carrier_CARRIER_STRING || sem != irpb.SemType_SEM_UUID {
			return diag.Atf(desc, "field %q: default_auto: %s requires string carrier with type UUID (got carrier=%s, type=%s)", desc.Name(), displayAutoKind(kind), displayCarrier(carrier), displaySemType(sem)).
				WithWhy("UUID_V4 / UUID_V7 generate a UUID literal — only columns declared as UUID accept it").
				WithFix("set the field to `string foo = N [(w17.field) = { type: UUID, default_auto: UUID_V4 }];`")
		}
	case irpb.AutoKind_AUTO_EMPTY_JSON_ARRAY, irpb.AutoKind_AUTO_EMPTY_JSON_OBJECT:
		if carrier != irpb.Carrier_CARRIER_STRING || (sem != irpb.SemType_SEM_TEXT && sem != irpb.SemType_SEM_CHAR) {
			return diag.Atf(desc, "field %q: default_auto: %s requires string carrier with type TEXT or CHAR (got carrier=%s, type=%s)", desc.Name(), displayAutoKind(kind), displayCarrier(carrier), displaySemType(sem)).
				WithWhy("empty-JSON defaults emit the literal '[]' or '{}' — stored today on a string column, reserved for JSONB when it lands").
				WithFix("use type: TEXT (or CHAR with max_len >= 2), or drop this default")
		}
	case irpb.AutoKind_AUTO_TRUE, irpb.AutoKind_AUTO_FALSE:
		if carrier != irpb.Carrier_CARRIER_BOOL {
			return diag.Atf(desc, "field %q: default_auto: %s requires a bool carrier (got %s)", desc.Name(), displayAutoKind(kind), displayCarrier(carrier)).
				WithWhy("TRUE/FALSE are the single channel for bool defaults (there is no default_bool literal branch)").
				WithFix("change the carrier to bool, or use a literal default for non-bool columns")
		}
	case irpb.AutoKind_AUTO_IDENTITY:
		integer := carrier == irpb.Carrier_CARRIER_INT32 || carrier == irpb.Carrier_CARRIER_INT64
		if !integer || sem != irpb.SemType_SEM_ID {
			return diag.Atf(desc, "field %q: default_auto: IDENTITY requires int32/int64 with type: ID (got carrier=%s, type=%s)", desc.Name(), displayCarrier(carrier), displaySemType(sem)).
				WithWhy("IDENTITY renders as `GENERATED BY DEFAULT AS IDENTITY` (PG) / AUTO_INCREMENT (MySQL) — both apply only to integer PK columns declared as ID").
				WithFix("set the field to `int64 id = 1 [(w17.field) = { type: ID, pk: true, default_auto: IDENTITY }];`")
		}
	}
	return nil
}

func findLoadedField(msg *loader.LoadedMessage, protoName string) *loader.LoadedField {
	for _, f := range msg.Fields {
		if string(f.Desc.Name()) == protoName {
			return f
		}
	}
	return nil
}

// fkRef is the parsed form of (w17.field).fk — package-private; the wire-
// shape lives in irpb.ForeignKey.
type fkRef struct {
	table  string
	column string
}

func parseFKRef(s string) (fkRef, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fkRef{}, false
	}
	return fkRef{table: parts[0], column: parts[1]}, true
}

func hasSingleColUniqueIndex(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if i.GetUnique() && len(i.GetFields()) == 1 && i.GetFields()[0] == field {
			return true
		}
	}
	return false
}

func hasSingleColIndex(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if len(i.GetFields()) == 1 && i.GetFields()[0] == field {
			return true
		}
	}
	return false
}

func hasColumn(t *irpb.Table, protoName string) bool {
	for _, c := range t.GetColumns() {
		if c.GetProtoName() == protoName {
			return true
		}
	}
	return false
}

// findEnum walks a file + transitive imports for a fully-qualified enum name.
// Supports both top-level enums and enums nested inside messages.
func findEnum(root protoreflect.FileDescriptor, fqn protoreflect.FullName) protoreflect.EnumDescriptor {
	seen := map[string]bool{}
	var visit func(f protoreflect.FileDescriptor) protoreflect.EnumDescriptor
	visit = func(f protoreflect.FileDescriptor) protoreflect.EnumDescriptor {
		if seen[f.Path()] {
			return nil
		}
		seen[f.Path()] = true
		if e := findEnumInContainer(f, fqn); e != nil {
			return e
		}
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			if e := visit(imports.Get(i).FileDescriptor); e != nil {
				return e
			}
		}
		return nil
	}
	return visit(root)
}

// enumContainer is the common subset of FileDescriptor and MessageDescriptor
// that exposes both top-level Enums() and nested Messages().
type enumContainer interface {
	Enums() protoreflect.EnumDescriptors
	Messages() protoreflect.MessageDescriptors
}

func findEnumInContainer(c enumContainer, fqn protoreflect.FullName) protoreflect.EnumDescriptor {
	enums := c.Enums()
	for i := 0; i < enums.Len(); i++ {
		e := enums.Get(i)
		if e.FullName() == fqn {
			return e
		}
	}
	msgs := c.Messages()
	for i := 0; i < msgs.Len(); i++ {
		if e := findEnumInContainer(msgs.Get(i), fqn); e != nil {
			return e
		}
	}
	return nil
}

// defaultRegexFor returns the type-implied regex pattern for string semantic
// types that carry one, or "" for types without a default pattern.
//
// UUID is intentionally absent: PG's native UUID column rejects non-UUID
// strings by construction, so a regex CHECK would be pure redundancy. If a
// future dialect lacks a native UUID type and stores it as CHAR(36), the
// emitter for that dialect re-introduces the pattern locally — IR stays
// dialect-neutral.
func defaultRegexFor(sem irpb.SemType) string {
	switch sem {
	case irpb.SemType_SEM_SLUG:
		return `^[a-z0-9]+(?:-[a-z0-9]+)*$`
	case irpb.SemType_SEM_EMAIL:
		// Not RFC 5322 — the "good enough" check every ORM ships.
		return `^[^@\s]+@[^@\s]+\.[^@\s]+$`
	case irpb.SemType_SEM_URL:
		return `^https?://.+$`
	}
	return ""
}

// semTypeStoresAsString returns true when the sem type maps to a string-shaped
// SQL column (VARCHAR / TEXT) across all iter-1 dialects. Returns false for
// UUID (→ UUID) and DECIMAL (→ NUMERIC) where string-only CHECKs such as the
// blank-check synth would fail at apply time.
func semTypeStoresAsString(sem irpb.SemType) bool {
	switch sem {
	case irpb.SemType_SEM_UUID, irpb.SemType_SEM_DECIMAL:
		return false
	}
	return true
}
