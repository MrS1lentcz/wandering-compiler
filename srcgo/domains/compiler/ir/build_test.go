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

	if got, want := len(schema.Tables), 2; got != want {
		t.Fatalf("len(tables) = %d, want %d", got, want)
	}

	tables := map[string]*ir.Table{}
	for _, tbl := range schema.Tables {
		tables[tbl.Name] = tbl
	}

	customers := tables["customers"]
	if customers == nil {
		t.Fatal("customers table missing")
	}
	if got, want := len(customers.PrimaryKey), 1; got != want {
		t.Errorf("customers.PrimaryKey len = %d, want %d", got, want)
	}
	// email: unique -> one synthetic unique index
	if !hasUniqueIdxOn(customers.Indexes, "email") {
		t.Error("customers: synthesised UNIQUE INDEX on email missing")
	}

	orders := tables["orders"]
	if orders == nil {
		t.Fatal("orders table missing")
	}
	if got, want := len(orders.ForeignKeys), 1; got != want {
		t.Fatalf("orders.ForeignKeys len = %d, want %d", got, want)
	}
	fk := orders.ForeignKeys[0]
	if fk.Target.Table != "customers" || fk.Target.Column != "id" {
		t.Errorf("fk target = %+v, want customers.id", fk.Target)
	}
	if fk.OnDelete != ir.FKActionSetNull {
		t.Errorf("fk.OnDelete = %s, want SET NULL (orphanable=true + null=true)", fk.OnDelete)
	}

	// status: choices -> ChoicesCheck with non-zero enum values.
	var status *ir.Column
	for _, c := range orders.Columns {
		if c.ProtoName == "status" {
			status = c
		}
	}
	if status == nil {
		t.Fatal("orders.status column missing")
	}
	var choices *ir.ChoicesCheck
	for _, ck := range status.Checks {
		if cc, ok := ck.(ir.ChoicesCheck); ok {
			choices = &cc
		}
	}
	if choices == nil {
		t.Fatal("orders.status: ChoicesCheck not attached")
	}
	if got, want := choices.Values, []string{"PENDING", "PAID"}; !equalStrings(got, want) {
		t.Errorf("status.choices.Values = %v, want %v (UNSPECIFIED stripped)", got, want)
	}

	// total MONEY gte:0 -> one RangeCheck
	var total *ir.Column
	for _, c := range orders.Columns {
		if c.ProtoName == "total" {
			total = c
		}
	}
	if total == nil {
		t.Fatal("orders.total missing")
	}
	if !hasRangeCheckGte(total.Checks, 0) {
		t.Error("orders.total: RangeCheck{Gte:0} not attached")
	}

	// created_at DATETIME + default_auto NOW
	var createdAt *ir.Column
	for _, c := range orders.Columns {
		if c.ProtoName == "created_at" {
			createdAt = c
		}
	}
	if createdAt == nil {
		t.Fatal("orders.created_at missing")
	}
	auto, ok := createdAt.Default.(ir.AutoDefault)
	if !ok || auto.Kind != ir.AutoNow {
		t.Errorf("orders.created_at.Default = %+v, want AutoDefault{NOW}", createdAt.Default)
	}
	if !createdAt.Immutable {
		t.Error("orders.created_at.Immutable = false, want true")
	}

	// customer_id has (w17.db.column).index -> synthesised plain index
	if !hasPlainIdxOn(orders.Indexes, "customer_id") {
		t.Error("orders: synthesised storage index on customer_id missing")
	}

	// metadata: PgOptions.JSONB
	var metadata *ir.Column
	for _, c := range orders.Columns {
		if c.ProtoName == "metadata" {
			metadata = c
		}
	}
	if metadata == nil || metadata.Pg == nil || !metadata.Pg.JSONB {
		t.Errorf("orders.metadata.Pg.JSONB missing: %+v", metadata)
	}
}

// Error-class table: each entry names a fixture and the substrings we
// expect to find in the joined error text. Checking substrings (not full
// equality) because error order is stable per fixture but unicode hyphens
// etc. can slip.
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
			// errors.Join wraps multiple; verify at least one unwraps.
			if _, ok := diag.AsDiag(err); !ok {
				// errors.Join produces a []error via Unwrap; walk once.
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

func hasUniqueIdxOn(idx []*ir.Index, field string) bool {
	for _, i := range idx {
		if i.Unique && len(i.Fields) == 1 && i.Fields[0] == field {
			return true
		}
	}
	return false
}

func hasPlainIdxOn(idx []*ir.Index, field string) bool {
	for _, i := range idx {
		if !i.Unique && len(i.Fields) == 1 && i.Fields[0] == field {
			return true
		}
	}
	return false
}

func hasRangeCheckGte(checks []ir.Check, v float64) bool {
	for _, c := range checks {
		if rc, ok := c.(ir.RangeCheck); ok && rc.Gte != nil && *rc.Gte == v {
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
