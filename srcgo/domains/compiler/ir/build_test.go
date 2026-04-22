package ir_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

const irProtoImportPath = "../../../../proto"

func load(t *testing.T, protoPath string) *loader.LoadedFile {
	t.Helper()
	base, file := filepath.Split(protoPath)
	lf, err := loader.Load(
		context.Background(),
		file,
		[]string{base, irProtoImportPath},
	)
	if err != nil {
		t.Fatalf("Load(%q): %v", protoPath, err)
	}
	return lf
}

// TestBuildHappyPath is the golden check on the IR shape for a realistic
// multi-table fixture. Failures here mean the builder dropped facts.
func TestBuildHappyPath(t *testing.T) {
	lf := load(t, "testdata/happy.proto")
	schema, err := ir.Build(lf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got, want := len(schema.GetTables()), 2; got != want {
		t.Fatalf("len(tables) = %d, want %d", got, want)
	}

	tables := map[string]*irpb.Table{}
	for _, tbl := range schema.GetTables() {
		tables[tbl.GetName()] = tbl
	}

	customers := tables["customers"]
	if customers == nil {
		t.Fatal("customers table missing")
	}
	if got, want := len(customers.GetPrimaryKey()), 1; got != want {
		t.Errorf("customers.PrimaryKey len = %d, want %d", got, want)
	}
	// email: unique -> one synthetic unique index
	if !hasUniqueIdxOn(customers.GetIndexes(), "email") {
		t.Error("customers: synthesised UNIQUE INDEX on email missing")
	}

	orders := tables["orders"]
	if orders == nil {
		t.Fatal("orders table missing")
	}
	if got, want := len(orders.GetForeignKeys()), 1; got != want {
		t.Fatalf("orders.ForeignKeys len = %d, want %d", got, want)
	}
	fk := orders.GetForeignKeys()[0]
	if fk.GetTargetTable() != "customers" || fk.GetTargetColumn() != "id" {
		t.Errorf("fk target = %s.%s, want customers.id", fk.GetTargetTable(), fk.GetTargetColumn())
	}
	if fk.GetOnDelete() != irpb.FKAction_FK_ACTION_SET_NULL {
		t.Errorf("fk.OnDelete = %s, want FK_ACTION_SET_NULL (orphanable=true + null=true)", fk.GetOnDelete())
	}

	// status: choices -> ChoicesCheck with non-zero enum values.
	var status *irpb.Column
	for _, c := range orders.GetColumns() {
		if c.GetProtoName() == "status" {
			status = c
		}
	}
	if status == nil {
		t.Fatal("orders.status column missing")
	}
	var choices *irpb.ChoicesCheck
	for _, ck := range status.GetChecks() {
		if c := ck.GetChoices(); c != nil {
			choices = c
		}
	}
	if choices == nil {
		t.Fatal("orders.status: ChoicesCheck not attached")
	}
	if got, want := choices.GetValues(), []string{"PENDING", "PAID"}; !equalStrings(got, want) {
		t.Errorf("status.choices.Values = %v, want %v (UNSPECIFIED stripped)", got, want)
	}

	// total MONEY gte:0 -> one RangeCheck
	var total *irpb.Column
	for _, c := range orders.GetColumns() {
		if c.GetProtoName() == "total" {
			total = c
		}
	}
	if total == nil {
		t.Fatal("orders.total missing")
	}
	if !hasRangeCheckGte(total.GetChecks(), 0) {
		t.Error("orders.total: RangeCheck{Gte:0} not attached")
	}

	// created_at DATETIME + default_auto NOW
	var createdAt *irpb.Column
	for _, c := range orders.GetColumns() {
		if c.GetProtoName() == "created_at" {
			createdAt = c
		}
	}
	if createdAt == nil {
		t.Fatal("orders.created_at missing")
	}
	def := createdAt.GetDefault()
	if def == nil || def.GetAuto() != irpb.AutoKind_AUTO_NOW {
		t.Errorf("orders.created_at.Default = %v, want AutoKind=AUTO_NOW", def)
	}
	if !createdAt.GetImmutable() {
		t.Error("orders.created_at.Immutable = false, want true")
	}

	// customer_id has (w17.db.column).index -> synthesised plain index
	if !hasPlainIdxOn(orders.GetIndexes(), "customer_id") {
		t.Error("orders: synthesised storage index on customer_id missing")
	}

	// metadata: type: JSON core Type (post-D13; previously (w17.pg.field).jsonb).
	var metadata *irpb.Column
	for _, c := range orders.GetColumns() {
		if c.GetProtoName() == "metadata" {
			metadata = c
		}
	}
	if metadata == nil {
		t.Fatal("orders.metadata column missing")
	}
	if got, want := metadata.GetType(), irpb.SemType_SEM_JSON; got != want {
		t.Errorf("orders.metadata.Type = %s, want %s", got, want)
	}
}

// Error-class table: each entry names a fixture and the substrings we
// expect to find in the joined error text.
func TestBuildErrors(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		wants   []string
	}{
		{
			name:    "CHAR requires max_len",
			fixture: "testdata/errors/char_no_maxlen.proto",
			wants: []string{
				`char_no_maxlen.proto:`,
				`field "slug": type CHAR requires max_len`,
				`why:`,
				`VARCHAR(N)`,
				`fix:`,
				`max_len: 80`,
			},
		},
		{
			name:    "deletion_rule: ORPHAN requires null: true",
			fixture: "testdata/errors/orphan_requires_null.proto",
			wants: []string{
				`orphan_requires_null.proto:`,
				`deletion_rule: ORPHAN requires null: true`,
				`why:`,
				`SET NULL`,
				`NOT NULL`,
				`fix:`,
				`null: true`,
			},
		},
		{
			name:    "deletion_rule: RESET requires default_*",
			fixture: "testdata/errors/reset_requires_default.proto",
			wants: []string{
				`reset_requires_default.proto:`,
				`deletion_rule: RESET requires a (w17.field).default_* value`,
				`why:`,
				`ON DELETE SET DEFAULT`,
				`fix:`,
				`default_int`,
			},
		},
		{
			name:    "FK target table missing",
			fixture: "testdata/errors/fk_target_missing.proto",
			wants: []string{
				`fk_target_missing.proto:`,
				`fk target table "parents"`,
				`why:`,
				`iter-2`,
				`fix:`,
				`(w17.db.table).name = "parents"`,
			},
		},
		{
			name:    "DECIMAL requires precision",
			fixture: "testdata/errors/decimal_no_precision.proto",
			wants: []string{
				`decimal_no_precision.proto:`,
				`type DECIMAL requires precision`,
				`why:`,
				`NUMERIC(precision, scale)`,
				`fix:`,
				`precision: 12`,
			},
		},
		{
			name:    "default_auto NOW needs Timestamp carrier",
			fixture: "testdata/errors/bad_autodefault_now.proto",
			wants: []string{
				`bad_autodefault_now.proto:`,
				`default_auto: NOW requires a Timestamp carrier`,
				`why:`,
				`CURRENT_TIMESTAMP`,
				`fix:`,
				`google.protobuf.Timestamp`,
			},
		},
		{
			name:    "choices forbidden on integer carrier",
			fixture: "testdata/errors/choices_on_int.proto",
			wants: []string{
				`choices_on_int.proto:`,
				`choices is only valid on string carriers`,
				`why:`,
				`enum *value names*`,
				`fix:`,
				`change the proto field to a string carrier`,
			},
		},
		{
			name:    "bool carrier must not set type",
			fixture: "testdata/errors/bool_with_type.proto",
			wants: []string{
				`bool_with_type.proto:`,
				`bool carrier must not set a semantic type`,
				`why:`,
				`BOOLEAN`,
				`fix:`,
				`default_auto: TRUE`,
			},
		},
		{
			name:    "pg.field.custom_type rejected on non-string carrier",
			fixture: "testdata/errors/pg_override_non_string_carrier.proto",
			wants: []string{
				`pg_override_non_string_carrier.proto:`,
				`(w17.pg.field).custom_type is only allowed on string-carrier columns`,
				`why:`,
				`numeric / bool / temporal / bytes carriers`,
				`fix:`,
				`TEXT`,
			},
		},
		{
			name:    "pg.field.custom_type requires type: TEXT",
			fixture: "testdata/errors/pg_override_requires_text.proto",
			wants: []string{
				`pg_override_requires_text.proto:`,
				`(w17.pg.field).custom_type requires type: TEXT`,
				`why:`,
				`CHAR/SLUG`,
				`fix:`,
				`change type to TEXT`,
			},
		},
		{
			name:    "pg.field.custom_type incompatible with string-only CHECK options",
			fixture: "testdata/errors/pg_override_with_string_check.proto",
			wants: []string{
				`pg_override_with_string_check.proto:`,
				`min_len / max_len / pattern / choices / blank are incompatible with (w17.pg.field).custom_type`,
				`why:`,
				`char_length`,
				`fix:`,
				`pick one path`,
			},
		},
		{
			name:    "FK target must be single-col unique",
			fixture: "testdata/errors/fk_target_not_unique.proto",
			wants: []string{
				`fk_target_not_unique.proto:`,
				`has no uniqueness constraint`,
				`why:`,
				`composite PK`,
				`fix:`,
				`unique: true`,
			},
		},
		{
			name:    "reserved keyword rejected as table name",
			fixture: "testdata/errors/reserved_table_name.proto",
			wants: []string{
				`reserved_table_name.proto:`,
				`"user"`,
				`Postgres reserved keyword`,
				`why:`,
				`63 bytes or collide with reserved keywords`,
				`fix:`,
				`rename the table`,
			},
		},
		{
			name:    "identifier > 63 bytes rejected",
			fixture: "testdata/errors/identifier_too_long.proto",
			wants: []string{
				`identifier_too_long.proto:`,
				`NAMEDATALEN`,
				`why:`,
				`63 bytes`,
				`fix:`,
			},
		},
		{
			name:    "index name collision rejected",
			fixture: "testdata/errors/index_name_collision.proto",
			wants: []string{
				`index_name_collision.proto:`,
				`collides with`,
				`why:`,
				`per-schema unique index names`,
				`fix:`,
				`rename the explicit index`,
			},
		},
		{
			name:    "raw_checks[].name empty",
			fixture: "testdata/errors/raw_check_empty_name.proto",
			wants: []string{
				`raw_check_empty_name.proto:`,
				`raw_checks[0].name is empty`,
				`why:`,
				`opaque expression`,
				`fix:`,
				`descriptive identifier`,
			},
		},
		{
			name:    "raw check name collides with derived",
			fixture: "testdata/errors/raw_check_collides_with_derived.proto",
			wants: []string{
				`raw_check_collides_with_derived.proto:`,
				`"xs_email_blank"`,
				`collides with`,
				`why:`,
				`pg_constraint`,
				`fix:`,
				`rename the raw check`,
			},
		},
		{
			name:    "raw_indexes[].body empty",
			fixture: "testdata/errors/raw_index_empty_body.proto",
			wants: []string{
				`raw_index_empty_body.proto:`,
				`raw_indexes[0]`,
				`empty body`,
				`why:`,
				`rejects at apply`,
				`fix:`,
				`USING gin`,
			},
		},
		{
			name:    "raw index name collides with synth",
			fixture: "testdata/errors/raw_index_collides_with_synth.proto",
			wants: []string{
				`raw_index_collides_with_synth.proto:`,
				`raw_indexes[0]`,
				`"xs_email_uidx"`,
				`collides with`,
				`why:`,
				`share one namespace`,
				`fix:`,
				`rename the raw index`,
			},
		},
		{
			name:    "db_type carrier mismatch",
			fixture: "testdata/errors/dbtype_carrier_mismatch.proto",
			wants: []string{
				`dbtype_carrier_mismatch.proto:`,
				`db_type BIGINT is not valid on a string carrier`,
				`why:`,
				`class of compatible carriers`,
				`fix:`,
				`matches db_type: BIGINT`,
			},
		},
		{
			name:    "db_type conflicts with custom_type",
			fixture: "testdata/errors/dbtype_conflicts_with_custom_type.proto",
			wants: []string{
				`dbtype_conflicts_with_custom_type.proto:`,
				`db_type conflicts with (w17.pg.field).custom_type`,
				`why:`,
				`two different storage-override paths`,
				`fix:`,
				`pick one`,
			},
		},
		{
			name:    "db_type: VARCHAR needs max_len",
			fixture: "testdata/errors/dbtype_varchar_needs_max_len.proto",
			wants: []string{
				`dbtype_varchar_needs_max_len.proto:`,
				`db_type: VARCHAR requires (w17.field).max_len`,
				`why:`,
				`column-type-driven size`,
				`fix:`,
				`db_type: TEXT`,
			},
		},
		{
			name:    "map key must be string",
			fixture: "testdata/errors/map_key_must_be_string.proto",
			wants: []string{
				`map_key_must_be_string.proto:`,
				`map key must be string`,
				`why:`,
				`HSTORE`,
				`fix:`,
				`map<string, V>`,
			},
		},
		{
			name:    "list element sem forbidden on message element",
			fixture: "testdata/errors/list_type_on_message_element.proto",
			wants: []string{
				`list_type_on_message_element.proto:`,
				`repeated Message field cannot carry an element sem type`,
				`why:`,
				`scalar elements`,
				`fix:`,
				`type: AUTO`,
			},
		},
		{
			name:    "string-only CHECK options rejected on collection carriers",
			fixture: "testdata/errors/collection_string_synth_rejected.proto",
			wants: []string{
				`collection_string_synth_rejected.proto:`,
				`min_len / blank / pattern / choices are not supported on LIST carrier`,
				`why:`,
				`forall element`,
				`fix:`,
				`raw_checks`,
			},
		},
		{
			name:    "string+ENUM without choices rejected",
			fixture: "testdata/errors/enum_requires_choices.proto",
			wants: []string{
				`enum_requires_choices.proto:`,
				`type ENUM on string carrier requires choices`,
				`why:`,
				`resolve value names`,
				`fix:`,
				`choices: "<package>.<EnumName>"`,
			},
		},
		{
			name:    "ENUM on bool carrier rejected",
			fixture: "testdata/errors/enum_on_bool_carrier.proto",
			wants: []string{
				`enum_on_bool_carrier.proto:`,
				`bool carrier must not set a semantic type`,
				`why:`,
				`BOOLEAN`,
			},
		},
		{
			name:    "generated_expr rejects default_*",
			fixture: "testdata/errors/generated_with_default.proto",
			wants: []string{
				`generated_with_default.proto:`,
				`generated_expr is incompatible with default_*`,
				`why:`,
				`GENERATED ALWAYS AS`,
				`fix:`,
				`default_string`,
			},
		},
		{
			name:    "generated_expr rejects pk",
			fixture: "testdata/errors/generated_with_pk.proto",
			wants: []string{
				`generated_with_pk.proto:`,
				`generated_expr is incompatible with pk: true`,
				`why:`,
				`primary keys`,
				`fix:`,
				`non-generated column`,
			},
		},
		{
			name:    "generated_expr rejects fk",
			fixture: "testdata/errors/generated_with_fk.proto",
			wants: []string{
				`generated_with_fk.proto:`,
				`generated_expr is incompatible with fk`,
				`why:`,
				`referential integrity`,
				`fix:`,
				`plain column`,
			},
		},
		{
			name:    "module schema is a reserved PG system schema",
			fixture: "testdata/errors/module_schema_reserved.proto",
			wants: []string{
				`module_schema_reserved.proto:`,
				`"pg_catalog" is a reserved PostgreSQL system schema`,
				`why:`,
				`pg_*`,
				`fix:`,
				`reporting`,
			},
		},
		{
			name:    "module prefix is empty",
			fixture: "testdata/errors/module_prefix_empty.proto",
			wants: []string{
				`module_prefix_empty.proto:`,
				`(w17.db.module).prefix is empty`,
				`why:`,
				`ambiguous`,
				`fix:`,
				`{ prefix:`,
			},
		},
		{
			name:    "module prefix overflows NAMEDATALEN",
			fixture: "testdata/errors/module_prefix_overflow.proto",
			wants: []string{
				`module_prefix_overflow.proto:`,
				`NAMEDATALEN`,
				`why:`,
				`63 bytes`,
				`fix:`,
			},
		},
		{
			name:    "default-derived name hits a reserved keyword",
			fixture: "testdata/errors/default_name_reserved.proto",
			wants: []string{
				`default_name_reserved.proto:`,
				`"user"`,
				`Postgres reserved keyword`,
				`why:`,
				`reserved keywords`,
				`fix:`,
				`(w17.db.table) = { name:`,
				`derived name "user"`,
			},
		},
		{
			name:    "FILE_PATH without extensions",
			fixture: "testdata/errors/file_path_no_extensions.proto",
			wants: []string{
				`file_path_no_extensions.proto:`,
				`FILE_PATH requires extensions`,
				`why:`,
				`ambiguity`,
				`fix:`,
				`extensions: ["*"]`,
			},
		},
		{
			name:    "extensions on a non-path type",
			fixture: "testdata/errors/extensions_on_non_path.proto",
			wants: []string{
				`extensions_on_non_path.proto:`,
				`extensions is only valid on path presets`,
				`why:`,
				`FILE_PATH / IMAGE_PATH`,
				`fix:`,
				`FILE_PATH`,
			},
		},
		{
			name:    "wildcard extensions must stand alone",
			fixture: "testdata/errors/wildcard_mixed_with_extensions.proto",
			wants: []string{
				`wildcard_mixed_with_extensions.proto:`,
				`"*" must stand alone`,
				`why:`,
				`contradictory intent`,
				`fix:`,
				`without the wildcard`,
			},
		},
		{
			name:    "GIN index rejects UNIQUE",
			fixture: "testdata/errors/gin_index_with_unique.proto",
			wants: []string{
				`gin_index_with_unique.proto:`,
				`GIN does not support UNIQUE`,
				`why:`,
				`inverted / block-range`,
				`fix:`,
				`change ` + "`method:`" + ` to BTREE`,
			},
		},
		{
			name:    "HASH index rejects sort direction",
			fixture: "testdata/errors/hash_index_with_sort.proto",
			wants: []string{
				`hash_index_with_sort.proto:`,
				`HASH does not support sort direction`,
				`why:`,
				`no traversal order`,
				`fix:`,
				`BTREE (the default)`,
			},
		},
		{
			name:    "HASH index rejects per-field opclass",
			fixture: "testdata/errors/hash_index_with_opclass.proto",
			wants: []string{
				`hash_index_with_opclass.proto:`,
				`HASH does not accept a per-field opclass`,
				`why:`,
				`default hash function`,
				`fix:`,
				`drop ` + "`opclass:`",
			},
		},
		{
			name:    "HASH index rejects multi-field",
			fixture: "testdata/errors/hash_index_multi_field.proto",
			wants: []string{
				`hash_index_multi_field.proto:`,
				`HASH indexes cover exactly one field`,
				`why:`,
				`strictly single-column`,
				`fix:`,
				`BTREE`,
			},
		},
		{
			name:    "BRIN index rejects UNIQUE",
			fixture: "testdata/errors/brin_index_with_unique.proto",
			wants: []string{
				`brin_index_with_unique.proto:`,
				`BRIN does not support UNIQUE`,
				`why:`,
				`inverted / block-range`,
				`fix:`,
				`BTREE`,
			},
		},
		{
			name:    "unsupported carrier: float rejected",
			fixture: "testdata/errors/unsupported_carrier_float.proto",
			wants: []string{
				`unsupported_carrier_float.proto:`,
				`carrier float is not supported in iteration-1`,
				`why:`,
				`iteration-1 accepts string, int32, int64, bool, double`,
				`fix:`,
			},
		},
		{
			name:    "index with empty fields list",
			fixture: "testdata/errors/index_empty_fields.proto",
			wants: []string{
				`index_empty_fields.proto:`,
				`has no fields`,
				`why:`,
				`nothing to index on`,
				`fix:`,
			},
		},
		{
			name:    "index field entry with empty name",
			fixture: "testdata/errors/index_field_empty_name.proto",
			wants: []string{
				`index_field_empty_name.proto:`,
				`field entry with empty name`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "index references unknown field",
			fixture: "testdata/errors/index_field_unknown.proto",
			wants: []string{
				`index_field_unknown.proto:`,
				`references unknown field "ghost_field"`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "index INCLUDE references unknown field",
			fixture: "testdata/errors/index_include_unknown.proto",
			wants: []string{
				`index_include_unknown.proto:`,
				`INCLUDE references unknown field "ghost_field"`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "column name override to reserved keyword",
			fixture: "testdata/errors/column_name_reserved.proto",
			wants: []string{
				`column_name_reserved.proto:`,
				`field "note"`,
				`reserved`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "pk on list carrier rejected",
			fixture: "testdata/errors/pk_on_list_carrier.proto",
			wants: []string{
				`pk_on_list_carrier.proto:`,
				`pk not supported on LIST carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "unique on map carrier rejected",
			fixture: "testdata/errors/unique_on_map_carrier.proto",
			wants: []string{
				`unique_on_map_carrier.proto:`,
				`unique not supported on MAP carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "deletion_rule without fk rejected",
			fixture: "testdata/errors/deletion_rule_without_fk.proto",
			wants: []string{
				`deletion_rule_without_fk.proto:`,
				`deletion_rule set without fk`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "fk malformed reference",
			fixture: "testdata/errors/fk_malformed.proto",
			wants: []string{
				`fk_malformed.proto:`,
				`fk must be "<table>.<column>"`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "raw_check with empty expr",
			fixture: "testdata/errors/raw_check_empty_expr.proto",
			wants: []string{
				`raw_check_empty_expr.proto:`,
				`raw_checks[0]`,
				`empty expr`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "DECIMAL with negative scale",
			fixture: "testdata/errors/decimal_scale_negative.proto",
			wants: []string{
				`decimal_scale_negative.proto:`,
				`DECIMAL scale must be >= 0`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "DECIMAL with scale exceeding precision",
			fixture: "testdata/errors/decimal_scale_exceeds.proto",
			wants: []string{
				`decimal_scale_exceeds.proto:`,
				`DECIMAL scale (10) exceeds precision (4)`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "precision set on non-DECIMAL type",
			fixture: "testdata/errors/precision_on_non_decimal.proto",
			wants: []string{
				`precision_on_non_decimal.proto:`,
				`precision/scale only apply to type DECIMAL`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "max_len on non-string carrier",
			fixture: "testdata/errors/max_len_on_non_string.proto",
			wants: []string{
				`max_len_on_non_string.proto:`,
				`max_len is only valid on string carriers`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "max_len on wrong string sem type",
			fixture: "testdata/errors/max_len_on_wrong_sem.proto",
			wants: []string{
				`max_len_on_wrong_sem.proto:`,
				`max_len is not valid on type UUID`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "min_len on non-string carrier",
			fixture: "testdata/errors/min_len_on_non_string.proto",
			wants: []string{
				`min_len_on_non_string.proto:`,
				`min_len is only valid on string carriers`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "blank on non-string carrier",
			fixture: "testdata/errors/blank_on_non_string.proto",
			wants: []string{
				`blank_on_non_string.proto:`,
				`blank is only valid on string carriers`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "pattern on non-string carrier",
			fixture: "testdata/errors/pattern_on_non_string.proto",
			wants: []string{
				`pattern_on_non_string.proto:`,
				`pattern is only valid on string carriers`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "range bounds on non-numeric carrier",
			fixture: "testdata/errors/range_on_non_numeric.proto",
			wants: []string{
				`range_on_non_numeric.proto:`,
				`gt/gte/lt/lte require a numeric carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "string-only synth on map carrier",
			fixture: "testdata/errors/collection_string_synth_on_map.proto",
			wants: []string{
				`collection_string_synth_on_map.proto:`,
				`min_len / blank / pattern / choices are not supported on MAP carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "index name equals reserved keyword",
			fixture: "testdata/errors/index_name_reserved.proto",
			wants: []string{
				`index_name_reserved.proto:`,
				`index[0] name`,
				`reserved`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "raw_index name equals reserved keyword",
			fixture: "testdata/errors/raw_index_name_reserved.proto",
			wants: []string{
				`raw_index_name_reserved.proto:`,
				`raw_indexes[0] name`,
				`reserved`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "raw_check name equals reserved keyword",
			fixture: "testdata/errors/raw_check_name_reserved.proto",
			wants: []string{
				`raw_check_name_reserved.proto:`,
				`raw_checks[0] name`,
				`reserved`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "namespace schema empty string",
			fixture: "testdata/errors/namespace_schema_empty.proto",
			wants: []string{
				`namespace_schema_empty.proto:`,
				`(w17.db.module).schema is empty`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_auto UUID_V4 on non-UUID type",
			fixture: "testdata/errors/autodefault_uuidv4_wrong_type.proto",
			wants: []string{
				`autodefault_uuidv4_wrong_type.proto:`,
				`default_auto: UUID_V4 requires string carrier with type UUID`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_auto EMPTY_JSON_ARRAY on non-JSON",
			fixture: "testdata/errors/autodefault_empty_json_wrong_type.proto",
			wants: []string{
				`autodefault_empty_json_wrong_type.proto:`,
				`default_auto: EMPTY_JSON_ARRAY requires type: JSON`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_auto TRUE on int carrier",
			fixture: "testdata/errors/autodefault_true_on_int.proto",
			wants: []string{
				`autodefault_true_on_int.proto:`,
				`default_auto: TRUE requires a bool carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_auto IDENTITY on string carrier",
			fixture: "testdata/errors/autodefault_identity_on_string.proto",
			wants: []string{
				`autodefault_identity_on_string.proto:`,
				`default_auto: IDENTITY requires int32/int64 with type: ID`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_string on non-string carrier",
			fixture: "testdata/errors/default_string_on_int.proto",
			wants: []string{
				`default_string_on_int.proto:`,
				`default_string requires a string carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_int on non-integer carrier",
			fixture: "testdata/errors/default_int_on_string.proto",
			wants: []string{
				`default_int_on_string.proto:`,
				`default_int requires an integer carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "default_double on non-double carrier",
			fixture: "testdata/errors/default_double_on_string.proto",
			wants: []string{
				`default_double_on_string.proto:`,
				`default_double requires a double carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "raw_index empty name",
			fixture: "testdata/errors/raw_index_empty_name.proto",
			wants: []string{
				`raw_index_empty_name.proto:`,
				`raw_indexes[0].name is empty`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "bare Message carrier rejected",
			fixture: "testdata/errors/unsupported_carrier_bare_message.proto",
			wants: []string{
				`unsupported_carrier_bare_message.proto:`,
				`field "inner"`,
				`is not supported in iteration-1`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "uint32 carrier rejected",
			fixture: "testdata/errors/unsupported_carrier_uint32.proto",
			wants: []string{
				`unsupported_carrier_uint32.proto:`,
				`carrier uint32 is not supported in iteration-1`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "choices enum FQN not resolvable",
			fixture: "testdata/errors/enum_choices_unknown_fqn.proto",
			wants: []string{
				`enum_choices_unknown_fqn.proto:`,
				`does.not.exist.Nope`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "map value kind unsupported",
			fixture: "testdata/errors/map_value_unsupported.proto",
			wants: []string{
				`map_value_unsupported.proto:`,
				`unsupported map value kind`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "list element kind unsupported",
			fixture: "testdata/errors/list_element_unsupported.proto",
			wants: []string{
				`list_element_unsupported.proto:`,
				`unsupported repeated element kind`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "derived CHECK name overflow",
			fixture: "testdata/errors/check_name_overflow.proto",
			wants: []string{
				`check_name_overflow.proto:`,
				`check[`,
				`63 bytes`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "derived ENUM type name overflow",
			fixture: "testdata/errors/enum_type_name_overflow.proto",
			wants: []string{
				`enum_type_name_overflow.proto:`,
				`ENUM type name`,
				`63 bytes`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "schema name equals reserved keyword",
			fixture: "testdata/errors/namespace_schema_keyword.proto",
			wants: []string{
				`namespace_schema_keyword.proto:`,
				`(w17.db.module).schema:`,
				`reserved keyword`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "enum choices disagrees with proto-enum field",
			fixture: "testdata/errors/enum_choices_disagrees.proto",
			wants: []string{
				`enum_choices_disagrees.proto:`,
				`disagrees with proto-enum field's own enum`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "fk target column missing",
			fixture: "testdata/errors/fk_target_column_missing.proto",
			wants: []string{
				`fk_target_column_missing.proto:`,
				`fk target column "ghost_column" not found on table "customers"`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "oneof field rejected",
			fixture: "testdata/errors/unsupported_oneof.proto",
			wants: []string{
				`unsupported_oneof.proto:`,
				`is not supported in iteration-1`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "list element sem type mismatches element carrier",
			fixture: "testdata/errors/list_element_sem_mismatch.proto",
			wants: []string{
				`list_element_sem_mismatch.proto:`,
				`list carrier validates type against the element carrier`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "extensions contains empty entry",
			fixture: "testdata/errors/extensions_empty_entry.proto",
			wants: []string{
				`extensions_empty_entry.proto:`,
				`extensions contains an empty entry`,
				`why:`,
				`fix:`,
			},
		},
		{
			name:    "POSIX_PATH rejects extensions",
			fixture: "testdata/errors/posix_path_with_extensions.proto",
			wants: []string{
				`posix_path_with_extensions.proto:`,
				`extensions is not valid on POSIX_PATH`,
				`why:`,
				`fix:`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lf := load(t, tc.fixture)
			_, err := ir.Build(lf)
			if err == nil {
				t.Fatalf("Build(%s): expected error, got nil", tc.fixture)
			}
			got := err.Error()
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("error missing substring %q\nfull error:\n%s", want, got)
				}
			}

			// Each surfaced error must be a *diag.Error so downstream
			// tooling (LSP, kong output) can act on the structured form.
			if _, ok := diag.AsDiag(err); !ok {
				if u, uok := err.(interface{ Unwrap() []error }); uok {
					found := false
					for _, e := range u.Unwrap() {
						if _, d := diag.AsDiag(e); d {
							found = true
							break
						}
					}
					if !found {
						t.Error("no *diag.Error found in joined errors")
					}
				} else if !errors.As(err, new(*diag.Error)) {
					t.Error("error is not a *diag.Error")
				}
			}
		})
	}
}

func hasUniqueIdxOn(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if i.GetUnique() && len(i.GetFields()) == 1 && i.GetFields()[0].GetName() == field {
			return true
		}
	}
	return false
}

func hasPlainIdxOn(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if !i.GetUnique() && len(i.GetFields()) == 1 && i.GetFields()[0].GetName() == field {
			return true
		}
	}
	return false
}

func hasRangeCheckGte(checks []*irpb.Check, v float64) bool {
	for _, c := range checks {
		if rc := c.GetRange(); rc != nil && rc.Gte != nil && *rc.Gte == v {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
