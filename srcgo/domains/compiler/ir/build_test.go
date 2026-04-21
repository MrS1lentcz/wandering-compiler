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

	// metadata: PgOptions.Jsonb
	var metadata *irpb.Column
	for _, c := range orders.GetColumns() {
		if c.GetProtoName() == "metadata" {
			metadata = c
		}
	}
	if metadata == nil || metadata.GetPg() == nil || !metadata.GetPg().GetJsonb() {
		t.Errorf("orders.metadata.Pg.Jsonb missing: %+v", metadata)
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
			name:    "orphanable=true requires null=true",
			fixture: "testdata/errors/orphanable_requires_null.proto",
			wants: []string{
				`orphanable_requires_null.proto:`,
				`orphanable=true requires null=true`,
				`why:`,
				`SET NULL on a NOT NULL column`,
				`fix:`,
				`null: true`,
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
			name:    "pg.field override rejected on non-string carrier",
			fixture: "testdata/errors/pg_override_non_string_carrier.proto",
			wants: []string{
				`pg_override_non_string_carrier.proto:`,
				`(w17.pg.field) storage override is only allowed on string-carrier columns`,
				`why:`,
				`numeric / bool / temporal carriers`,
				`fix:`,
				`TEXT`,
			},
		},
		{
			name:    "pg.field override requires type: TEXT",
			fixture: "testdata/errors/pg_override_requires_text.proto",
			wants: []string{
				`pg_override_requires_text.proto:`,
				`(w17.pg.field) storage override requires type: TEXT`,
				`why:`,
				`CHAR/SLUG`,
				`fix:`,
				`change type to TEXT`,
			},
		},
		{
			name:    "pg.field override incompatible with string-only CHECK options",
			fixture: "testdata/errors/pg_override_with_string_check.proto",
			wants: []string{
				`pg_override_with_string_check.proto:`,
				`min_len / max_len / pattern / choices / blank are incompatible with (w17.pg.field) storage override`,
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
