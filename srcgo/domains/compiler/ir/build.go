package ir

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	w17pb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17"
)

// Build converts a loaded .proto into a validated *Schema. All invariants
// from docs/iteration-1.md D2/D7/D8 are enforced here; every user-facing
// error is a *diag.Error with a `why:` and (where actionable) a `fix:`.
//
// Build accumulates errors rather than failing on the first — a single
// compile run should tell the user about all the problems in their file.
// Errors are joined with errors.Join so each prints on its own chunk of
// output and callers can still errors.As them back to *diag.Error.
func Build(lf *loader.LoadedFile) (*Schema, error) {
	b := &builder{lf: lf}
	schema := &Schema{}
	for _, msg := range lf.Messages {
		if msg.Table == nil {
			// Messages without (w17.db.table) aren't compiler inputs in
			// iteration-1. (Enums and helper types live in the same file.)
			continue
		}
		tbl := b.buildTable(msg)
		if tbl != nil {
			schema.Tables = append(schema.Tables, tbl)
		}
	}
	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}
	// Cross-table: resolve FK target tables once every table is built.
	b.resolveFKs(schema)
	if len(b.errs) > 0 {
		return nil, errors.Join(b.errs...)
	}
	return schema, nil
}

type builder struct {
	lf   *loader.LoadedFile
	errs []error
}

func (b *builder) err(e *diag.Error) { b.errs = append(b.errs, e) }

// buildTable builds one *Table. Returns nil only if the message is so
// malformed (e.g. missing table.name) that downstream work would be
// meaningless; otherwise returns a partially-populated table so later
// errors still point at specific fields.
func (b *builder) buildTable(msg *loader.LoadedMessage) *Table {
	if msg.Table.GetName() == "" {
		b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).name is empty", msg.Desc.Name()).
			WithWhy("the SQL table name is never auto-derived from the proto message name (D6 — explicit over implicit)").
			WithFix(`add option (w17.db.table) = { name: "snake_case_plural" };`))
		return nil
	}
	tbl := &Table{
		Name:    msg.Table.GetName(),
		Message: msg.Desc,
	}

	// Build columns first — other derivations depend on them.
	for _, f := range msg.Fields {
		col := b.buildColumn(f, msg)
		if col != nil {
			tbl.Columns = append(tbl.Columns, col)
			if col.PK {
				tbl.PrimaryKey = append(tbl.PrimaryKey, col)
			}
		}
	}

	// Parse FK references on columns that carry (w17.field).fk. Target
	// table resolution happens later in resolveFKs.
	for _, col := range tbl.Columns {
		if f := findLoadedField(msg, col.ProtoName); f != nil && f.Field.GetFk() != "" {
			ref, ok := parseFKRef(f.Field.GetFk())
			if !ok {
				b.err(diag.Atf(f.Desc, `field %q: fk must be "<table>.<column>", got %q`, col.ProtoName, f.Field.GetFk()).
					WithWhy("iteration-1 supports only same-file references in the short form — cross-module package paths arrive later").
					WithFix(`set fk: "categories.id" (two segments, table and column, separated by a single dot)`))
				continue
			}
			action := FKActionCascade
			// orphanable inference + validation — see D8.
			if f.Field.Orphanable != nil {
				if *f.Field.Orphanable && !f.Field.GetNull() {
					b.err(diag.Atf(f.Desc, "field %q: orphanable=true requires null=true", col.ProtoName).
						WithWhy("SET NULL on a NOT NULL column would violate the column's own constraint during a parent delete").
						WithFix(`either set null: true on (w17.field), or drop orphanable and let the parent delete cascade`))
					continue
				}
				if *f.Field.Orphanable {
					action = FKActionSetNull
				}
			} else if f.Field.GetNull() {
				// Inferred: nullable child rows survive parent deletes.
				action = FKActionSetNull
			}
			tbl.ForeignKeys = append(tbl.ForeignKeys, &ForeignKey{
				Column:   col,
				Target:   ref,
				OnDelete: action,
			})
		}
	}

	// Indexes: table-level first, then synthesised from unique/storage.
	for i, idx := range msg.Table.GetIndexes() {
		if len(idx.GetFields()) == 0 {
			b.err(diag.Atf(msg.Desc, "message %q: (w17.db.table).indexes[%d] has no fields", msg.Desc.Name(), i).
				WithWhy("an index with zero columns has nothing to index on").
				WithFix("supply at least one field name in the `fields:` list"))
			continue
		}
		// Verify every referenced field exists on the message.
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
		tbl.Indexes = append(tbl.Indexes, &Index{
			Name:    idx.GetName(),
			Fields:  append([]string(nil), idx.GetFields()...),
			Unique:  idx.GetUnique(),
			Include: append([]string(nil), idx.GetInclude()...),
		})
	}
	// Synthesise a UNIQUE INDEX for each (w17.field).unique column that
	// isn't already covered by a declared index with exactly that single
	// column and unique=true.
	for _, col := range tbl.Columns {
		if !col.Unique {
			continue
		}
		if hasSingleColUniqueIndex(tbl.Indexes, col.ProtoName) {
			continue
		}
		tbl.Indexes = append(tbl.Indexes, &Index{
			Fields: []string{col.ProtoName},
			Unique: true,
		})
	}
	// Synthesise a plain storage index for each StorageIndex column that
	// isn't already covered.
	for _, col := range tbl.Columns {
		if !col.StorageIndex {
			continue
		}
		if hasSingleColIndex(tbl.Indexes, col.ProtoName) {
			continue
		}
		tbl.Indexes = append(tbl.Indexes, &Index{
			Fields: []string{col.ProtoName},
		})
	}

	return tbl
}

// buildColumn validates one field and returns its *Column. Returns nil
// when the field is so malformed further checks would be nonsense; most
// other failures still return a populated column so the caller can keep
// processing.
func (b *builder) buildColumn(lf *loader.LoadedField, msg *loader.LoadedMessage) *Column {
	desc := lf.Desc
	protoName := string(desc.Name())

	carrier, carrierOK := protoKindToCarrier(desc)
	if !carrierOK {
		b.err(diag.Atf(desc, "field %q: carrier %s is not supported in iteration-1", protoName, describeKind(desc)).
			WithWhy("iteration-1 accepts string, int32, int64, bool, double, google.protobuf.Timestamp and google.protobuf.Duration as DB-column carriers; other kinds (bytes, repeated, oneof, nested messages) are parked for later iterations").
			WithFix("change the field's proto type to one of the supported carriers, or drop the (w17.field) annotation if the field isn't a DB column"))
		return nil
	}

	col := &Column{
		Name:      protoName,
		ProtoName: protoName,
		Field:     desc,
		Carrier:   carrier,
	}

	// Pull data-level options from (w17.field), if present.
	fieldOpt := lf.Field
	if fieldOpt == nil {
		// bool carrier is allowed to lack (w17.field) (it has no SemType);
		// every other carrier requires at least a `type:`.
		if carrier != CarrierBool {
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
	semType := SemUnspecified
	if fieldOpt != nil {
		semType = protoTypeToSem(fieldOpt.GetType())
	}
	if carrier == CarrierDuration && semType == SemUnspecified {
		semType = SemInterval // D2: Duration defaults to INTERVAL when type unset.
	}
	if err := validateCarrierSemType(desc, carrier, semType); err != nil {
		b.err(err)
		return nil
	}
	col.Type = semType

	// Nullability, PK, uniqueness, immutability.
	if fieldOpt != nil {
		col.Nullable = fieldOpt.GetNull()
		col.PK = fieldOpt.GetPk()
		col.Unique = fieldOpt.GetUnique() || col.PK // PK implies UNIQUE (D2 note).
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
	if carrier == CarrierString {
		if (semType == SemChar || semType == SemSlug) && col.MaxLen <= 0 {
			b.err(diag.Atf(desc, "field %q: type %s requires max_len", protoName, semType).
				WithWhy("CHAR/SLUG render as VARCHAR(N) — without N the column type has no fixed size").
				WithFix("add max_len to (w17.field), e.g. max_len: 80 for short names, 255 for titles"))
		}
	} else if col.MaxLen != 0 {
		b.err(diag.Atf(desc, "field %q: max_len is only valid on string carriers (got %s)", protoName, carrier).
			WithWhy("max_len controls char_length on string columns; numeric/temporal/bool columns have no length dimension").
			WithFix("drop max_len from (w17.field), or change the proto field to a string carrier"))
	}

	// DECIMAL precision/scale.
	if fieldOpt != nil {
		col.Precision = fieldOpt.GetPrecision()
		col.Scale = fieldOpt.GetScale()
		col.HasScale = fieldOpt.Scale != nil
	}
	if semType == SemDecimal {
		if col.Precision <= 0 {
			b.err(diag.Atf(desc, "field %q: type DECIMAL requires precision", protoName).
				WithWhy("DECIMAL renders as NUMERIC(precision, scale) — precision is the total number of significant digits and has no safe default").
				WithFix("add precision (and optionally scale) to (w17.field), e.g. { type: DECIMAL, precision: 12, scale: 4 }"))
		}
		if col.HasScale && col.Scale < 0 {
			b.err(diag.Atf(desc, "field %q: DECIMAL scale must be >= 0", protoName).
				WithWhy("negative scale is meaningless for NUMERIC").
				WithFix("drop scale or set it to a non-negative integer"))
		}
		if col.HasScale && col.Scale > col.Precision {
			b.err(diag.Atf(desc, "field %q: DECIMAL scale (%d) exceeds precision (%d)", protoName, col.Scale, col.Precision).
				WithWhy("scale counts digits after the decimal point and cannot exceed total digits").
				WithFix(fmt.Sprintf("raise precision to at least %d, or lower scale to at most %d", col.Scale, col.Precision)))
		}
	} else {
		if col.Precision != 0 || col.HasScale {
			b.err(diag.Atf(desc, "field %q: precision/scale only apply to type DECIMAL (got %s)", protoName, semType).
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
		if carrier != CarrierString {
			if fieldOpt.MinLen != nil {
				b.err(diag.Atf(desc, "field %q: min_len is only valid on string carriers (got %s)", protoName, carrier).
					WithWhy("min_len controls char_length on strings; other carriers have no length").
					WithFix("drop min_len, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetBlank() {
				b.err(diag.Atf(desc, "field %q: blank is only valid on string carriers (got %s)", protoName, carrier).
					WithWhy("blank relaxes the implicit `col <> ''` CHECK on strings; non-string columns have no such CHECK").
					WithFix("drop blank, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetPattern() != "" {
				b.err(diag.Atf(desc, "field %q: pattern is only valid on string carriers (got %s)", protoName, carrier).
					WithWhy("pattern emits a regex CHECK; regex only applies to strings").
					WithFix("drop pattern, or change the proto field to a string carrier"))
			}
			if fieldOpt.GetChoices() != "" {
				b.err(diag.Atf(desc, "field %q: choices is only valid on string carriers (got %s)", protoName, carrier).
					WithWhy("choices emits `CHECK col IN ('A','B',…)` matched against enum *value names*, which are strings").
					WithFix("drop choices, or change the proto field to a string carrier"))
			}
		}
		numericOnly := carrier == CarrierInt32 || carrier == CarrierInt64 || carrier == CarrierDouble
		if !numericOnly {
			if fieldOpt.Gt != nil || fieldOpt.Gte != nil || fieldOpt.Lt != nil || fieldOpt.Lte != nil {
				b.err(diag.Atf(desc, "field %q: gt/gte/lt/lte are only valid on numeric carriers (got %s)", protoName, carrier).
					WithWhy("the range CHECK emits a numeric comparison; it's undefined for non-numeric types").
					WithFix("drop the bound, or change the proto field to int32/int64/double"))
			}
		}
	}

	// Build Checks from the surviving facts.
	if fieldOpt != nil {
		b.attachChecks(col, fieldOpt, carrier, semType, msg)
	}

	// Postgres dialect passthrough.
	if lf.PgField != nil {
		col.Pg = &PgOptions{
			JSONB:              lf.PgField.GetJsonb(),
			Inet:               lf.PgField.GetInet(),
			TSVector:           lf.PgField.GetTsvector(),
			HStore:             lf.PgField.GetHstore(),
			CustomType:         lf.PgField.GetCustomType(),
			RequiredExtensions: append([]string(nil), lf.PgField.GetRequiredExtensions()...),
		}
	}

	return col
}

func (b *builder) attachChecks(col *Column, opt *w17pb.Field, carrier Carrier, semType SemType, _ *loader.LoadedMessage) {
	// LengthCheck — omitted for CHAR/SLUG since VARCHAR(N) covers the upper
	// bound. MinLen always produces a CHECK when present.
	if carrier == CarrierString {
		hasMin := opt.MinLen != nil
		hasMax := opt.GetMaxLen() > 0 && !(semType == SemChar || semType == SemSlug)
		if hasMin || hasMax {
			lc := LengthCheck{HasMin: hasMin, HasMax: hasMax}
			if hasMin {
				lc.Min = opt.GetMinLen()
			}
			if hasMax {
				lc.Max = opt.GetMaxLen()
			}
			col.Checks = append(col.Checks, lc)
		}
		// BlankCheck — added unless author opted into blank or the field is nullable.
		// (A NULL string can't be '' by definition; CHECK would still pass, but
		// we skip the redundant clause for readability.)
		if !opt.GetBlank() && !col.Nullable {
			col.Checks = append(col.Checks, BlankCheck{})
		}
	}

	// RangeCheck.
	if opt.Gt != nil || opt.Gte != nil || opt.Lt != nil || opt.Lte != nil {
		col.Checks = append(col.Checks, RangeCheck{
			Gt:  opt.Gt,
			Gte: opt.Gte,
			Lt:  opt.Lt,
			Lte: opt.Lte,
		})
	}

	// RegexCheck — pattern override takes precedence over type-implied.
	if opt.GetPattern() != "" {
		col.Checks = append(col.Checks, RegexCheck{Pattern: opt.GetPattern(), Source: RegexFromPattern})
	} else if regex := defaultRegexFor(semType); regex != "" {
		col.Checks = append(col.Checks, RegexCheck{Pattern: regex, Source: RegexFromType})
	}

	// ChoicesCheck — resolve the enum FQN to its value names.
	if opt.GetChoices() != "" {
		values, resolveErr := b.resolveEnumValues(col.Field, opt.GetChoices())
		if resolveErr != nil {
			b.err(resolveErr)
		} else {
			col.Checks = append(col.Checks, ChoicesCheck{EnumFQN: opt.GetChoices(), Values: values})
		}
	}

	// Percentage/Ratio: emit the implicit domain constraints when no author
	// bounds conflict. These are part of the TYPE semantics, not opt-in.
	switch semType {
	case SemPercentage:
		if opt.Gte == nil && opt.Gt == nil && opt.Lt == nil && opt.Lte == nil {
			zero := 0.0
			hundred := 100.0
			col.Checks = append(col.Checks, RangeCheck{Gte: &zero, Lte: &hundred})
		}
	case SemRatio:
		if opt.Gte == nil && opt.Gt == nil && opt.Lt == nil && opt.Lte == nil {
			zero := 0.0
			one := 1.0
			col.Checks = append(col.Checks, RangeCheck{Gte: &zero, Lte: &one})
		}
	}
}

// resolveDefault parses the oneof and validates carrier/type compatibility
// per D7.
func (b *builder) resolveDefault(desc protoreflect.FieldDescriptor, opt *w17pb.Field, carrier Carrier, semType SemType) (Default, *diag.Error) {
	switch d := opt.GetDefault().(type) {
	case nil:
		return nil, nil
	case *w17pb.Field_DefaultString:
		if carrier != CarrierString {
			return nil, diag.Atf(desc, "field %q: default_string requires a string carrier (got %s)", desc.Name(), carrier).
				WithWhy("default_string emits a string literal — non-string columns can't accept it").
				WithFix("use default_int / default_double / default_auto for non-string carriers, or change the proto field to string")
		}
		return LiteralString{Value: d.DefaultString}, nil
	case *w17pb.Field_DefaultInt:
		if carrier != CarrierInt32 && carrier != CarrierInt64 {
			return nil, diag.Atf(desc, "field %q: default_int requires an integer carrier (got %s)", desc.Name(), carrier).
				WithWhy("default_int emits an integer literal").
				WithFix("use default_double for double carriers, or default_string for strings")
		}
		return LiteralInt{Value: d.DefaultInt}, nil
	case *w17pb.Field_DefaultDouble:
		if carrier != CarrierDouble {
			return nil, diag.Atf(desc, "field %q: default_double requires a double carrier (got %s)", desc.Name(), carrier).
				WithWhy("default_double emits a floating-point literal").
				WithFix("use default_int for integer carriers, default_string for strings")
		}
		return LiteralDouble{Value: d.DefaultDouble}, nil
	case *w17pb.Field_DefaultAuto:
		kind := protoAutoToKind(d.DefaultAuto)
		if err := validateAutoDefault(desc, kind, carrier, semType); err != nil {
			return nil, err
		}
		return AutoDefault{Kind: kind}, nil
	default:
		return nil, diag.Atf(desc, "field %q: unknown default branch %T", desc.Name(), d).
			WithWhy("this is a compiler bug — the default oneof grew a branch the IR builder doesn't recognise").
			WithFix("please file an issue with the failing .proto attached")
	}
}

// resolveEnumValues looks up a proto enum by fully-qualified name, walking
// the field's parent file and its transitive imports. Returns an ordered
// slice of value names (excluding the mandatory *_UNSPECIFIED zero value,
// which is not a valid data-plane choice).
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
			// Excluding it from the CHECK keeps the data plane strict.
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

// resolveFKs verifies every FK target (<table>) exists among compiled
// tables and that the target column is present on it.
func (b *builder) resolveFKs(schema *Schema) {
	byName := map[string]*Table{}
	for _, t := range schema.Tables {
		byName[t.Name] = t
	}
	for _, t := range schema.Tables {
		for _, fk := range t.ForeignKeys {
			target, ok := byName[fk.Target.Table]
			if !ok {
				b.err(diag.Atf(fk.Column.Field, "field %q: fk target table %q not defined in this file", fk.Column.ProtoName, fk.Target.Table).
					WithWhy("iteration-1 resolves fk references within the single compiled proto — cross-file fk resolution lands in iter-2").
					WithFix(fmt.Sprintf("add a message annotated with (w17.db.table).name = %q to this file, or correct the fk reference", fk.Target.Table)))
				continue
			}
			if !hasColumn(target, fk.Target.Column) {
				b.err(diag.Atf(fk.Column.Field, "field %q: fk target column %q not found on table %q", fk.Column.ProtoName, fk.Target.Column, fk.Target.Table).
					WithWhy("the fk references a column that doesn't exist on the target table — a broken FK would fail at apply time").
					WithFix(fmt.Sprintf("verify the column name (case-sensitive, proto-field name) on message for table %q, or correct the fk reference", fk.Target.Table)))
			}
		}
	}
}

// --- small helpers ---

func protoKindToCarrier(fd protoreflect.FieldDescriptor) (Carrier, bool) {
	if fd.IsList() || fd.IsMap() || fd.ContainingOneof() != nil {
		return CarrierUnspecified, false
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return CarrierBool, true
	case protoreflect.StringKind:
		return CarrierString, true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return CarrierInt32, true
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return CarrierInt64, true
	case protoreflect.DoubleKind:
		return CarrierDouble, true
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Timestamp":
			return CarrierTimestamp, true
		case "google.protobuf.Duration":
			return CarrierDuration, true
		}
	}
	return CarrierUnspecified, false
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

func suggestedTypeFor(c Carrier) string {
	switch c {
	case CarrierString:
		return "CHAR, max_len: 255"
	case CarrierInt32, CarrierInt64:
		return "NUMBER"
	case CarrierDouble:
		return "NUMBER"
	case CarrierTimestamp:
		return "DATETIME"
	case CarrierDuration:
		return "INTERVAL"
	}
	return "CHAR"
}

func protoTypeToSem(t w17pb.Type) SemType {
	switch t {
	case w17pb.Type_CHAR:
		return SemChar
	case w17pb.Type_TEXT:
		return SemText
	case w17pb.Type_UUID:
		return SemUUID
	case w17pb.Type_EMAIL:
		return SemEmail
	case w17pb.Type_URL:
		return SemURL
	case w17pb.Type_SLUG:
		return SemSlug
	case w17pb.Type_NUMBER:
		return SemNumber
	case w17pb.Type_ID:
		return SemID
	case w17pb.Type_COUNTER:
		return SemCounter
	case w17pb.Type_MONEY:
		return SemMoney
	case w17pb.Type_PERCENTAGE:
		return SemPercentage
	case w17pb.Type_RATIO:
		return SemRatio
	case w17pb.Type_DECIMAL:
		return SemDecimal
	case w17pb.Type_DATE:
		return SemDate
	case w17pb.Type_TIME:
		return SemTime
	case w17pb.Type_DATETIME:
		return SemDateTime
	case w17pb.Type_INTERVAL:
		return SemInterval
	}
	return SemUnspecified
}

func protoAutoToKind(a w17pb.AutoDefault) AutoKind {
	switch a {
	case w17pb.AutoDefault_NOW:
		return AutoNow
	case w17pb.AutoDefault_UUID_V4:
		return AutoUUIDv4
	case w17pb.AutoDefault_UUID_V7:
		return AutoUUIDv7
	case w17pb.AutoDefault_EMPTY_JSON_ARRAY:
		return AutoEmptyJSONArray
	case w17pb.AutoDefault_EMPTY_JSON_OBJECT:
		return AutoEmptyJSONObject
	case w17pb.AutoDefault_TRUE:
		return AutoTrue
	case w17pb.AutoDefault_FALSE:
		return AutoFalse
	case w17pb.AutoDefault_IDENTITY:
		return AutoIdentity
	}
	return AutoUnspecified
}

// validateCarrierSemType enforces docs/iteration-1.md D2.
func validateCarrierSemType(desc protoreflect.FieldDescriptor, carrier Carrier, sem SemType) *diag.Error {
	name := desc.Name()
	switch carrier {
	case CarrierBool:
		if sem != SemUnspecified {
			return diag.Atf(desc, "field %q: bool carrier must not set a semantic type (got %s)", name, sem).
				WithWhy("bool has exactly one column shape (BOOLEAN) — there is no semantic refinement to pick").
				WithFix("drop type: from (w17.field); for a default value use default_auto: TRUE or FALSE")
		}
	case CarrierString:
		switch sem {
		case SemChar, SemText, SemUUID, SemEmail, SemURL, SemSlug, SemDecimal:
			// OK
		case SemUnspecified:
			return diag.Atf(desc, "field %q: string carrier requires a semantic type", name).
				WithWhy("string maps to many SQL types (VARCHAR, TEXT, UUID) with different constraints; the compiler won't guess").
				WithFix("add one of: CHAR, TEXT, UUID, EMAIL, URL, SLUG, DECIMAL")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a string carrier", name, sem).
				WithWhy("the D2 carrier×type table restricts string to CHAR, TEXT, UUID, EMAIL, URL, SLUG, DECIMAL").
				WithFix("pick one of the string-valid types, or change the carrier")
		}
	case CarrierInt32, CarrierInt64:
		switch sem {
		case SemNumber, SemID, SemCounter:
			// OK
		case SemUnspecified:
			return diag.Atf(desc, "field %q: %s carrier requires a semantic type", name, carrier).
				WithWhy("int32/int64 can carry NUMBER / ID / COUNTER — each emits a different SQL shape (PK vs indexed FK vs bounded counter)").
				WithFix("add one of: NUMBER, ID, COUNTER")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on an integer carrier", name, sem).
				WithWhy("the D2 carrier×type table restricts integer carriers to NUMBER, ID, COUNTER").
				WithFix("pick an integer-valid type, or change the carrier (e.g. double for MONEY)")
		}
	case CarrierDouble:
		switch sem {
		case SemNumber, SemMoney, SemPercentage, SemRatio:
			// OK
		case SemUnspecified:
			return diag.Atf(desc, "field %q: double carrier requires a semantic type", name).
				WithWhy("double can carry NUMBER / MONEY / PERCENTAGE / RATIO — each emits different constraints (bounds for PERCENTAGE/RATIO, scale for MONEY)").
				WithFix("add one of: NUMBER, MONEY, PERCENTAGE, RATIO; use DECIMAL on a string carrier for exact precision")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a double carrier", name, sem).
				WithWhy("the D2 carrier×type table restricts double to NUMBER, MONEY, PERCENTAGE, RATIO").
				WithFix("pick a double-valid type, or change the carrier")
		}
	case CarrierTimestamp:
		switch sem {
		case SemDate, SemTime, SemDateTime:
			// OK
		case SemUnspecified:
			return diag.Atf(desc, "field %q: google.protobuf.Timestamp carrier requires a semantic type", name).
				WithWhy("Timestamp can be a DATE, TIME, or DATETIME — each emits a different SQL column type").
				WithFix("add one of: DATE, TIME, DATETIME")
		default:
			return diag.Atf(desc, "field %q: type %s is not valid on a Timestamp carrier", name, sem).
				WithWhy("Timestamp is restricted to DATE / TIME / DATETIME per D2").
				WithFix("pick a Timestamp-valid type (DATE, TIME, DATETIME), or change the carrier")
		}
	case CarrierDuration:
		if sem != SemInterval {
			return diag.Atf(desc, "field %q: Duration carrier must be INTERVAL (got %s)", name, sem).
				WithWhy("google.protobuf.Duration maps 1:1 to the SQL INTERVAL type — no other refinement is defined in iter-1").
				WithFix("set type: INTERVAL or drop the type: key so it's inferred")
		}
	}
	return nil
}

// validateAutoDefault enforces the Type × AutoDefault table from D7.
func validateAutoDefault(desc protoreflect.FieldDescriptor, kind AutoKind, carrier Carrier, sem SemType) *diag.Error {
	switch kind {
	case AutoNow:
		if carrier != CarrierTimestamp {
			return diag.Atf(desc, "field %q: default_auto: NOW requires a Timestamp carrier (got %s)", desc.Name(), carrier).
				WithWhy("NOW resolves to CURRENT_DATE / CURRENT_TIME / CURRENT_TIMESTAMP; only Timestamp columns accept any of those").
				WithFix("change the carrier to google.protobuf.Timestamp (type: DATETIME / DATE / TIME), or remove default_auto")
		}
	case AutoUUIDv4, AutoUUIDv7:
		if carrier != CarrierString || sem != SemUUID {
			return diag.Atf(desc, "field %q: default_auto: %s requires string carrier with type UUID (got carrier=%s, type=%s)", desc.Name(), kind, carrier, sem).
				WithWhy("UUID_V4 / UUID_V7 generate a UUID literal — only columns declared as UUID accept it").
				WithFix("set the field to `string foo = N [(w17.field) = { type: UUID, default_auto: UUID_V4 }];`")
		}
	case AutoEmptyJSONArray, AutoEmptyJSONObject:
		if carrier != CarrierString || (sem != SemText && sem != SemChar) {
			return diag.Atf(desc, "field %q: default_auto: %s requires string carrier with type TEXT or CHAR (got carrier=%s, type=%s)", desc.Name(), kind, carrier, sem).
				WithWhy("empty-JSON defaults emit the literal '[]' or '{}' — stored today on a string column, reserved for JSONB when it lands").
				WithFix("use type: TEXT (or CHAR with max_len >= 2), or drop this default")
		}
	case AutoTrue, AutoFalse:
		if carrier != CarrierBool {
			return diag.Atf(desc, "field %q: default_auto: %s requires a bool carrier (got %s)", desc.Name(), kind, carrier).
				WithWhy("TRUE/FALSE are the single channel for bool defaults (there is no default_bool literal branch)").
				WithFix("change the carrier to bool, or use a literal default for non-bool columns")
		}
	case AutoIdentity:
		integer := carrier == CarrierInt32 || carrier == CarrierInt64
		if !integer || sem != SemID {
			return diag.Atf(desc, "field %q: default_auto: IDENTITY requires int32/int64 with type: ID (got carrier=%s, type=%s)", desc.Name(), carrier, sem).
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

func parseFKRef(s string) (FKRef, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return FKRef{}, false
	}
	return FKRef{Table: parts[0], Column: parts[1]}, true
}

func hasSingleColUniqueIndex(idx []*Index, field string) bool {
	for _, i := range idx {
		if i.Unique && len(i.Fields) == 1 && i.Fields[0] == field {
			return true
		}
	}
	return false
}

func hasSingleColIndex(idx []*Index, field string) bool {
	for _, i := range idx {
		if len(i.Fields) == 1 && i.Fields[0] == field {
			return true
		}
	}
	return false
}

func hasColumn(t *Table, protoName string) bool {
	for _, c := range t.Columns {
		if c.ProtoName == protoName {
			return true
		}
	}
	return false
}

// findEnum walks the file + transitive imports for a fully-qualified enum
// name. Supports both top-level enums and enums nested inside messages.
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

// defaultRegexFor returns the type-implied regex pattern for string
// semantic types that carry one, or "" for types without a default pattern.
func defaultRegexFor(sem SemType) string {
	switch sem {
	case SemUUID:
		return `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	case SemSlug:
		return `^[a-z0-9]+(?:-[a-z0-9]+)*$`
	case SemEmail:
		// Not RFC 5322 — the "good enough" check every ORM ships. Authors
		// who care pass their own via (w17.field).pattern.
		return `^[^@\s]+@[^@\s]+\.[^@\s]+$`
	case SemURL:
		return `^https?://.+$`
	}
	return ""
}
