package postgres_test

import (
	"sort"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// TestEmit_UsageTracksCapsOnAddTable covers the AddTable path's
// instrumentation — one table with a mix of column types + a
// non-BTREE index + a structured comment must surface the right
// cap IDs on the returned Usage.
//
// This is the M4 Layer A integration test: it couples the emit
// dispatch with the tracker and pins the capability strings the
// manifest will carry.
func TestEmit_UsageTracksCapsOnAddTable(t *testing.T) {
	table := &irpb.Table{
		Name:       "documents",
		MessageFqn: "pkg.Document",
		Comment:    "notes table",
		Columns: []*irpb.Column{
			{Name: "id", ProtoName: "id", Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_UUID, Pk: true,
				Default: &irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_UUID_V7}}},
			{Name: "payload", ProtoName: "payload", Carrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_JSON, Nullable: true},
			{Name: "created_at", ProtoName: "created_at", Carrier: irpb.Carrier_CARRIER_TIMESTAMP, Type: irpb.SemType_SEM_DATETIME,
				Default: &irpb.Default{Variant: &irpb.Default_Auto{Auto: irpb.AutoKind_AUTO_NOW}}},
			{Name: "tags", ProtoName: "tags", Carrier: irpb.Carrier_CARRIER_LIST, ElementCarrier: irpb.Carrier_CARRIER_STRING, Type: irpb.SemType_SEM_TEXT, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		Indexes: []*irpb.Index{
			{Name: "documents_payload_gin", Method: irpb.IndexMethod_IDX_GIN, Fields: []*irpb.IndexField{{Name: "payload"}}},
		},
	}
	p := &planpb.MigrationPlan{Ops: []*planpb.Op{
		{Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{Table: table}}},
	}}

	_, _, usage, err := emit.Emit(postgres.Emitter{}, p)
	if err != nil {
		t.Fatalf("emit.Emit: %v", err)
	}

	got := usage.Sorted()
	want := map[string]bool{
		emit.CapUUID:              true,
		emit.CapFnUUIDv7:          true,
		emit.CapJSONB:             true,
		emit.CapTimestampTZ:       true,
		emit.CapArray:             true,
		emit.CapGinIndex:          true,
		emit.CapCommentOn:         true,
		emit.CapTransactionalDDL:  true, // wrap-level cap
	}
	for _, cap := range got {
		delete(want, cap)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for c := range want {
			missing = append(missing, c)
		}
		sort.Strings(missing)
		t.Errorf("missing expected caps %v; recorded %v", missing, got)
	}
}

// TestEmit_UsageEmptyPlanReturnsEmpty — an empty plan produces empty
// SQL + an empty usage collector. TRANSACTIONAL_DDL is not recorded
// because the wrapper is not emitted for empty plans (AC #1 — no
// files written for no-op diffs).
func TestEmit_UsageEmptyPlanReturnsEmpty(t *testing.T) {
	_, _, usage, err := emit.Emit(postgres.Emitter{}, &planpb.MigrationPlan{})
	if err != nil {
		t.Fatalf("emit.Emit: %v", err)
	}
	if got := usage.Sorted(); len(got) != 0 {
		t.Errorf("empty plan recorded caps: %v", got)
	}
}

// TestEmit_UsageFKOnDeleteActions covers the FK ON DELETE cap
// tagging at the AddForeignKey alter-op path. Only RESTRICT and
// SET DEFAULT get explicit caps; CASCADE / SET NULL are universal
// and don't.
func TestEmit_UsageFKOnDeleteActions(t *testing.T) {
	cases := []struct {
		name   string
		action irpb.FKAction
		cap    string
		record bool
	}{
		{"RESTRICT", irpb.FKAction_FK_ACTION_RESTRICT, emit.CapOnDeleteRestrict, true},
		{"SET_DEFAULT", irpb.FKAction_FK_ACTION_SET_DEFAULT, emit.CapOnDeleteSetDefault, true},
		{"CASCADE", irpb.FKAction_FK_ACTION_CASCADE, "", false},
		{"SET_NULL", irpb.FKAction_FK_ACTION_SET_NULL, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			op := &planpb.Op{Variant: &planpb.Op_AddForeignKey{AddForeignKey: &planpb.AddForeignKey{
				Ctx:            &planpb.TableCtx{TableName: "orders", MessageFqn: "pkg.Order"},
				ConstraintName: "orders_customer_fk",
				Fk: &irpb.ForeignKey{
					Column:       "customer_id",
					TargetTable:  "customers",
					TargetColumn: "id",
					OnDelete:     c.action,
				},
				Columns: []*irpb.Column{{Name: "customer_id", ProtoName: "customer_id"}},
			}}}
			usage := emit.NewUsage()
			if _, _, err := (postgres.Emitter{}).EmitOp(op, usage); err != nil {
				t.Fatalf("EmitOp: %v", err)
			}
			recorded := contains(usage.Sorted(), c.cap)
			if recorded != c.record {
				t.Errorf("ON DELETE %s: cap %q recorded=%v, want %v (caps=%v)",
					c.name, c.cap, recorded, c.record, usage.Sorted())
			}
		})
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
