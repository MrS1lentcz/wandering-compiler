package sqlite_test

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/sqlite"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Compile-time proof: sqlite.Emitter satisfies emit.DialectEmitter. If the
// shared interface ever grows a PG-shaped method, this line fails to build
// and M5's reason-for-existing (AC #6) triggers.
var _ emit.DialectEmitter = sqlite.Emitter{}

func TestNameIsStable(t *testing.T) {
	if got, want := (sqlite.Emitter{}).Name(), "sqlite"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

// EmitOp returns the parked error for every op variant. Exercise both a
// real AddTable and an empty Op to prove the stub doesn't accidentally
// handle the zero value.
func TestEmitOpReturnsNotImplemented(t *testing.T) {
	cases := []struct {
		name string
		op   *planpb.Op
	}{
		{"empty", &planpb.Op{}},
		{"add_table", &planpb.Op{Variant: &planpb.Op_AddTable{
			AddTable: &planpb.AddTable{Table: &irpb.Table{Name: "anything"}},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up, down, err := sqlite.Emitter{}.EmitOp(tc.op)
			if err == nil {
				t.Fatal("expected not-implemented error, got nil")
			}
			if up != "" || down != "" {
				t.Errorf("stub produced SQL: up=%q down=%q", up, down)
			}
			if !strings.Contains(err.Error(), "not implemented in iteration-1") {
				t.Errorf("error doesn't carry the iter-1 marker: %v", err)
			}
		})
	}
}

// TestStubRequirement — the sqlite emitter's capability stub always
// returns ok=false with a zero Requirement, because iter-1 SQLite emits
// no SQL and the catalog is intentionally empty. Exercises the
// emit.DialectCapabilities contract on the stub path.
func TestStubRequirement(t *testing.T) {
	e := sqlite.Emitter{}
	// Any cap string — the stub catalog is empty so lookup must miss.
	req, ok := e.Requirement("UUID_DEFAULT")
	if ok {
		t.Errorf("stub Requirement(UUID_DEFAULT) ok=true, want false — stub catalog must stay empty until real SQLite emission lands")
	}
	if req.MinVersion != "" || len(req.Extensions) > 0 {
		t.Errorf("stub Requirement returned non-zero value %+v, want zero Requirement", req)
	}
	// A second, unrelated cap — same behaviour.
	req, ok = e.Requirement("anything")
	if ok || req.MinVersion != "" {
		t.Errorf("stub Requirement(anything) = %+v ok=%v, want zero+false", req, ok)
	}
}

// The plan-level orchestrator surfaces the stub error wrapped with the
// dialect name and op index — checks that emit.Emit composes cleanly with
// a failing emitter (no silent swallowing, no partial output).
func TestPlanEmitSurfacesStubError(t *testing.T) {
	p := &planpb.MigrationPlan{Ops: []*planpb.Op{
		{Variant: &planpb.Op_AddTable{AddTable: &planpb.AddTable{Table: &irpb.Table{Name: "x"}}}},
	}}
	up, down, err := emit.Emit(sqlite.Emitter{}, p)
	if err == nil {
		t.Fatal("expected error from stub, got nil")
	}
	if up != "" || down != "" {
		t.Errorf("partial output leaked: up=%q down=%q", up, down)
	}
	if !strings.Contains(err.Error(), "sqlite") {
		t.Errorf("wrapped error lost the dialect name: %v", err)
	}
}
