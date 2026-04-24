package emit_test

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
)

// TestUsage_NilSafe asserts every method tolerates a nil receiver so
// tests that don't care about tracking can pass a bare nil into
// EmitOp without boilerplate.
func TestUsage_NilSafe(t *testing.T) {
	var u *emit.Usage
	u.Use("JSONB") // must not panic
	if got := u.Sorted(); got != nil {
		t.Errorf("nil Usage.Sorted() = %v, want nil", got)
	}
}

// TestUsage_EmptyCapNoOp asserts the empty-string cap is dropped
// silently — callers doing `usage.Use(someFn())` don't want an empty
// string (from an unset default branch) polluting the collector.
func TestUsage_EmptyCapNoOp(t *testing.T) {
	u := emit.NewUsage()
	u.Use("")
	if got := u.Sorted(); got != nil {
		t.Errorf("empty-cap recorded: %v", got)
	}
}

// TestUsage_SortedDeterministic pins the sort + dedupe contract.
// Idempotence (D30 / iter-1 AC #4) requires byte-identical output
// on repeat runs; Sorted() is where that determinism originates.
func TestUsage_SortedDeterministic(t *testing.T) {
	u := emit.NewUsage()
	u.Use("JSONB")
	u.Use("UUID")
	u.Use("JSONB") // duplicate
	u.Use("GIN_INDEX")
	u.Use("UUID") // duplicate

	got := u.Sorted()
	want := []string{"GIN_INDEX", "JSONB", "UUID"}
	if len(got) != len(want) {
		t.Fatalf("Sorted() len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Sorted()[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestUsage_ZeroValueUsable confirms `&emit.Usage{}` works as a
// drop-in collector without NewUsage(). Matches the nil-safe
// contract on pointer methods.
func TestUsage_ZeroValueUsable(t *testing.T) {
	u := &emit.Usage{}
	u.Use("JSON")
	got := u.Sorted()
	if len(got) != 1 || got[0] != "JSON" {
		t.Errorf("zero-value Usage collected %v, want [JSON]", got)
	}
}
