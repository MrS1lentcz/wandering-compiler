package memory_test

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine/memory"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Compile-time interface assertions: the Memory types implement the
// engine contract. If either signature drifts, this fails to build.
var (
	_ engine.ResolutionSource = (*memory.Source)(nil)
	_ engine.Sink             = (*memory.Sink)(nil)
)

func TestSource_LookupAndAll(t *testing.T) {
	r1 := &planpb.Resolution{FindingId: "abc", Strategy: planpb.Strategy_DROP_AND_CREATE, Actor: "test"}
	r2 := &planpb.Resolution{FindingId: "xyz", Strategy: planpb.Strategy_CUSTOM_MIGRATION, CustomSql: "UPDATE …"}
	src := memory.NewSource(r1, r2)

	got, ok := src.Lookup("abc")
	if !ok {
		t.Fatal("Lookup(abc) missing")
	}
	if got.GetStrategy() != planpb.Strategy_DROP_AND_CREATE {
		t.Errorf("Lookup(abc) strategy = %s, want DROP_AND_CREATE", got.GetStrategy())
	}

	got, ok = src.Lookup("xyz")
	if !ok || got.GetCustomSql() != "UPDATE …" {
		t.Errorf("Lookup(xyz) mismatch: ok=%v got=%v", ok, got)
	}

	if _, ok := src.Lookup("nope"); ok {
		t.Error("Lookup(nope) returned true; want false")
	}

	all := src.All()
	if len(all) != 2 {
		t.Fatalf("All len = %d, want 2", len(all))
	}
	if all[0].GetFindingId() != "abc" || all[1].GetFindingId() != "xyz" {
		t.Errorf("All order not stable: %v", all)
	}
}

func TestSource_AddReplace(t *testing.T) {
	src := &memory.Source{}
	src.Add(&planpb.Resolution{FindingId: "abc", Strategy: planpb.Strategy_DROP_AND_CREATE})
	src.Add(&planpb.Resolution{FindingId: "abc", Strategy: planpb.Strategy_CUSTOM_MIGRATION})

	got, _ := src.Lookup("abc")
	if got.GetStrategy() != planpb.Strategy_CUSTOM_MIGRATION {
		t.Errorf("after replace, strategy = %s, want CUSTOM_MIGRATION", got.GetStrategy())
	}
	if len(src.All()) != 1 {
		t.Errorf("len(All()) = %d after replace, want 1", len(src.All()))
	}
}

func TestSource_AddNilSafe(t *testing.T) {
	src := &memory.Source{}
	src.Add(nil)
	src.Add(&planpb.Resolution{FindingId: ""}) // empty ID also skipped
	if len(src.All()) != 0 {
		t.Errorf("nil / empty-id adds should be skipped")
	}
}

func TestSink_Capture(t *testing.T) {
	sink := &memory.Sink{}
	if sink.Count() != 0 || sink.Last() != nil {
		t.Error("empty sink should have Count 0 and Last nil")
	}

	p1 := &planpb.Plan{Migrations: []*planpb.Migration{{UpSql: "UP 1"}}}
	p2 := &planpb.Plan{Migrations: []*planpb.Migration{{UpSql: "UP 2"}}}
	if err := sink.Write(p1); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Write(p2); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if sink.Count() != 2 {
		t.Errorf("Count = %d, want 2", sink.Count())
	}
	if sink.Last().GetMigrations()[0].GetUpSql() != "UP 2" {
		t.Errorf("Last plan should be p2")
	}
	if len(sink.Plans) != 2 {
		t.Errorf("Plans len = %d, want 2", len(sink.Plans))
	}
}

func TestSink_NilSafe(t *testing.T) {
	sink := &memory.Sink{}
	if err := sink.Write(nil); err != nil {
		t.Errorf("Write(nil) error = %v, want nil", err)
	}
	if sink.Count() != 0 {
		t.Errorf("nil-plan write should be no-op")
	}
}
