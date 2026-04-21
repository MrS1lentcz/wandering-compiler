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
			name:    "missing table name",
			fixture: "testdata/errors/missing_table_name.proto",
			wants: []string{
				`missing_table_name.proto:`,
				`(w17.db.table).name is empty`,
				`why:`,
				`never auto-derived`,
				`fix:`,
				`snake_case_plural`,
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
		if i.GetUnique() && len(i.GetFields()) == 1 && i.GetFields()[0] == field {
			return true
		}
	}
	return false
}

func hasPlainIdxOn(idx []*irpb.Index, field string) bool {
	for _, i := range idx {
		if !i.GetUnique() && len(i.GetFields()) == 1 && i.GetFields()[0] == field {
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
