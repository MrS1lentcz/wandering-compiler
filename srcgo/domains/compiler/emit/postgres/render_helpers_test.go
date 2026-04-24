package postgres

// Branch coverage for the small rendering helpers —
// varcharTypeSQL (unbounded vs. bounded) and
// renderAlterColumnType (with/without USING). The matrix runner
// exercises both happy paths; this pins the edge cases.

import "testing"

func TestVarcharTypeSQL(t *testing.T) {
	if got := varcharTypeSQL(0); got != "VARCHAR" {
		t.Errorf("zero length → %q, want VARCHAR", got)
	}
	if got := varcharTypeSQL(-1); got != "VARCHAR" {
		t.Errorf("negative length → %q, want VARCHAR", got)
	}
	if got := varcharTypeSQL(64); got != "VARCHAR(64)" {
		t.Errorf("64 → %q, want VARCHAR(64)", got)
	}
}

func TestNumericTypeSQL_UnboundedAndBounded(t *testing.T) {
	if got := numericTypeSQL(0, nil); got != "NUMERIC" {
		t.Errorf("precision=0 → %q, want NUMERIC", got)
	}
	if got := numericTypeSQL(-1, nil); got != "NUMERIC" {
		t.Errorf("precision=-1 → %q, want NUMERIC", got)
	}
	if got := numericTypeSQL(10, nil); got != "NUMERIC(10)" {
		t.Errorf("precision=10 → %q, want NUMERIC(10)", got)
	}
	s := int32(3)
	if got := numericTypeSQL(10, &s); got != "NUMERIC(10, 3)" {
		t.Errorf("precision=10, scale=3 → %q", got)
	}
}

func TestRenderAlterColumnType_WithAndWithoutUsing(t *testing.T) {
	bare := renderAlterColumnType("t", "c", "TEXT", "")
	want := "ALTER TABLE t ALTER COLUMN c TYPE TEXT;"
	if bare != want {
		t.Errorf("bare: got %q, want %q", bare, want)
	}
	withUsing := renderAlterColumnType("t", "c", "INTEGER", "c::int")
	want2 := "ALTER TABLE t ALTER COLUMN c TYPE INTEGER USING c::int;"
	if withUsing != want2 {
		t.Errorf("with USING: got %q, want %q", withUsing, want2)
	}
}
