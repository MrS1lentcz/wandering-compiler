package postgres

import (
	"math"
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestFmtDouble pins the renderer's branches for range/MONEY
// bounds. Integer-valued doubles must render in fixed-point (no
// 1e+06-style scientific output) so CHECK SQL stays readable;
// fractional values keep the shortest round-trippable form;
// special values (NaN, ±Inf) fall through to %g via
// strconv.FormatFloat 'g'.
func TestFmtDouble(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{1_000_000, "1000000"},
		{-42, "-42"},
		{0.5, "0.5"},
		{3.14159, "3.14159"},
		{1e20, "1e+20"},  // beyond the trunc threshold → scientific
		{math.NaN(), "NaN"},
		{math.Inf(1), "+Inf"},
		{math.Inf(-1), "-Inf"},
	}
	for _, c := range cases {
		got := fmtDouble(c.in)
		if got != c.want {
			t.Errorf("fmtDouble(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRenderRange covers the four bound combinations:
//   inclusive symmetric (gte+lte) → BETWEEN,
//   upper-only (lte),
//   lower-only (gte),
//   exclusive mix (gt+lt) → "col > x AND col < y".
func TestRenderRange(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		rc   *irpb.RangeCheck
		want string
	}{
		{
			name: "BETWEEN inclusive both",
			rc:   &irpb.RangeCheck{Gte: f64(0), Lte: f64(100)},
			want: "col BETWEEN 0 AND 100",
		},
		{
			name: "gt only",
			rc:   &irpb.RangeCheck{Gt: f64(5)},
			want: "col > 5",
		},
		{
			name: "lte only",
			rc:   &irpb.RangeCheck{Lte: f64(50)},
			want: "col <= 50",
		},
		{
			name: "gte only (no lte — no BETWEEN)",
			rc:   &irpb.RangeCheck{Gte: f64(10)},
			want: "col >= 10",
		},
		{
			name: "lt only",
			rc:   &irpb.RangeCheck{Lt: f64(99)},
			want: "col < 99",
		},
		{
			name: "exclusive mix (gt + lt — no BETWEEN, AND-joined)",
			rc:   &irpb.RangeCheck{Gt: f64(0), Lt: f64(1)},
			want: "col > 0 AND col < 1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderRange("col", c.rc); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderLength — min / max individually and combined.
func TestRenderLength(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	cases := []struct {
		name string
		lc   *irpb.LengthCheck
		want string
	}{
		{"min only", &irpb.LengthCheck{Min: i32(3)}, "char_length(col) >= 3"},
		{"max only", &irpb.LengthCheck{Max: i32(120)}, "char_length(col) <= 120"},
		{"both", &irpb.LengthCheck{Min: i32(8), Max: i32(64)}, "char_length(col) >= 8 AND char_length(col) <= 64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderLength("col", c.lc); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestRenderCheckBodyDispatch exercises every Check variant of the
// oneof including the "unknown variant" defensive branch. The
// fixture-driven goldens hit most variants organically, but this test
// pins each branch directly + covers the error path.
func TestRenderCheckBodyDispatch(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	f64 := func(v float64) *float64 { return &v }
	col := &irpb.Column{Name: "col", ProtoName: "col"}
	cases := []struct {
		name     string
		ck       *irpb.Check
		wantName string
		wantExpr string
		wantErr  bool
	}{
		{name: "length", ck: &irpb.Check{Variant: &irpb.Check_Length{Length: &irpb.LengthCheck{Min: i32(1)}}}, wantName: "len", wantExpr: "char_length(col) >= 1"},
		{name: "blank", ck: &irpb.Check{Variant: &irpb.Check_Blank{Blank: &irpb.BlankCheck{}}}, wantName: "blank", wantExpr: "col <> ''"},
		{name: "range", ck: &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{Gte: f64(0), Lte: f64(9)}}}, wantName: "range", wantExpr: "col BETWEEN 0 AND 9"},
		{name: "regex", ck: &irpb.Check{Variant: &irpb.Check_Regex{Regex: &irpb.RegexCheck{Pattern: "^a"}}}, wantName: "format", wantExpr: "col ~ '^a'"},
		{name: "choices", ck: &irpb.Check{Variant: &irpb.Check_Choices{Choices: &irpb.ChoicesCheck{Values: []string{"X"}}}}, wantName: "choices", wantExpr: "col IN ('X')"},
		{name: "unknown variant", ck: &irpb.Check{}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, expr, err := renderCheckBody(col, c.ck)
			if c.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if name != c.wantName || expr != c.wantExpr {
				t.Errorf("got (%q, %q), want (%q, %q)", name, expr, c.wantName, c.wantExpr)
			}
		})
	}
}

// TestRenderCheck covers the outer wrapper: it returns "" for an
// empty-expr body (range with no bounds) and surfaces renderCheckBody
// errors untouched.
func TestRenderCheck(t *testing.T) {
	col := &irpb.Column{Name: "x", ProtoName: "x"}

	// Empty range → no SQL surface, returns empty string.
	empty := &irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{}}}
	got, err := renderCheck("t", col, empty)
	if err != nil {
		t.Fatalf("unexpected err on empty range: %v", err)
	}
	if got != "" {
		t.Errorf("want empty string for empty range, got %q", got)
	}

	// Populated body renders the full CONSTRAINT line.
	blank := &irpb.Check{Variant: &irpb.Check_Blank{Blank: &irpb.BlankCheck{}}}
	got, err = renderCheck("t", col, blank)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "CONSTRAINT t_x_blank CHECK (x <> '')" {
		t.Errorf("unexpected render: %q", got)
	}

	// Unknown variant error propagates.
	if _, err := renderCheck("t", col, &irpb.Check{}); err == nil {
		t.Fatal("want error for unknown variant")
	}
}

// TestRenderCheckEmptyChoices covers the empty-choices defensive path —
// renderChoices receives neither Values nor Numbers (IR invariant
// violation) and emits `IN ()` which PG would reject.
func TestRenderCheckEmptyChoices(t *testing.T) {
	// Empty values + empty numbers renders `col IN ()`. Not a valid
	// CHECK but it's what the renderer produces — the IR builder
	// never populates an empty ChoicesCheck.
	if got := renderChoices("c", &irpb.ChoicesCheck{}); got != "c IN ()" {
		t.Errorf("empty choices: got %q, want %q", got, "c IN ()")
	}
}

// TestRenderRangeEmpty — no bounds set produces empty string (suffix
// stays "range" but outer renderCheck detects empty expr and returns "").
func TestRenderRangeEmpty(t *testing.T) {
	if got := renderRange("col", &irpb.RangeCheck{}); got != "" {
		t.Errorf("empty range: got %q, want empty", got)
	}
}

// TestRenderLengthEmpty — no min/max set produces empty string.
func TestRenderLengthEmpty(t *testing.T) {
	if got := renderLength("col", &irpb.LengthCheck{}); got != "" {
		t.Errorf("empty length: got %q, want empty", got)
	}
}

// TestRenderChoices — the two exclusive paths: string-name values
// (stringcarrier `choices:`) and numeric values (int SEM_ENUM).
func TestRenderChoices(t *testing.T) {
	// Numbers path wins when Numbers is populated.
	numCheck := &irpb.ChoicesCheck{Numbers: []int64{1, 2, 3}}
	if got, want := renderChoices("status", numCheck), "status IN (1, 2, 3)"; got != want {
		t.Errorf("numbers: got %q, want %q", got, want)
	}
	// Values path — quoted strings.
	strCheck := &irpb.ChoicesCheck{Values: []string{"DRAFT", "LIVE"}}
	if got, want := renderChoices("state", strCheck), "state IN ('DRAFT', 'LIVE')"; got != want {
		t.Errorf("strings: got %q, want %q", got, want)
	}
	// Escaping — apostrophe-containing value must double.
	esc := &irpb.ChoicesCheck{Values: []string{"don't"}}
	if got, want := renderChoices("x", esc), "x IN ('don''t')"; got != want {
		t.Errorf("escape: got %q, want %q", got, want)
	}
}
