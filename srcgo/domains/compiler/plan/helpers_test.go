package plan

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// Package-internal tests for small helpers. External
// TestDiffAlter* exercise these through the public API, but a
// couple of branches (length-mismatch short-circuit, nil scale
// both-sides, enum-kind dispatch) stay uncovered without direct
// calls.

func TestEqualStringSlicesMismatch(t *testing.T) {
	if equalStringSlices([]string{"a"}, []string{"a", "b"}) {
		t.Error("different-length slices reported equal")
	}
	if equalStringSlices([]string{"a", "b"}, []string{"a", "c"}) {
		t.Error("different-content slices reported equal")
	}
	if !equalStringSlices(nil, []string{}) {
		t.Error("nil vs empty slice should compare equal")
	}
	if !equalStringSlices([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("identical slices reported unequal")
	}
}

func TestScaleOfSet(t *testing.T) {
	c := &irpb.Column{Scale: nil}
	if _, ok := scaleOf(c); ok {
		t.Error("nil scale reported present")
	}
	v := int32(4)
	c2 := &irpb.Column{Scale: &v}
	got, ok := scaleOf(c2)
	if !ok || got != 4 {
		t.Errorf("set scale returned (%d, %v), want (4, true)", got, ok)
	}
}

func TestScalePtr(t *testing.T) {
	c := &irpb.Column{Scale: nil}
	if scalePtr(c) != nil {
		t.Error("nil scale should give nil ptr")
	}
	v := int32(2)
	c2 := &irpb.Column{Scale: &v}
	got := scalePtr(c2)
	if got == nil || *got != 2 {
		t.Errorf("set scale should give ptr to 2, got %v", got)
	}
}

// TestCheckVariantKindAll — every Check variant maps to its stable
// suffix string.
func TestCheckVariantKindAll(t *testing.T) {
	cases := []struct {
		check *irpb.Check
		want  string
	}{
		{&irpb.Check{Variant: &irpb.Check_Length{Length: &irpb.LengthCheck{}}}, "len"},
		{&irpb.Check{Variant: &irpb.Check_Blank{Blank: &irpb.BlankCheck{}}}, "blank"},
		{&irpb.Check{Variant: &irpb.Check_Range{Range: &irpb.RangeCheck{}}}, "range"},
		{&irpb.Check{Variant: &irpb.Check_Regex{Regex: &irpb.RegexCheck{}}}, "format"},
		{&irpb.Check{Variant: &irpb.Check_Choices{Choices: &irpb.ChoicesCheck{}}}, "choices"},
		{&irpb.Check{}, ""}, // unset variant
	}
	for i, c := range cases {
		if got := checkVariantKind(c.check); got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}

// TestNumericChangedScaleEdges — scale nil vs set vs different values.
func TestNumericChangedScaleEdges(t *testing.T) {
	s2 := int32(2)
	s3 := int32(3)
	cases := []struct {
		name       string
		prev, curr *irpb.Column
		want       bool
	}{
		{"both nil same precision", &irpb.Column{Precision: 10}, &irpb.Column{Precision: 10}, false},
		{"precision differs", &irpb.Column{Precision: 10}, &irpb.Column{Precision: 20}, true},
		{"nil vs set scale", &irpb.Column{Precision: 10}, &irpb.Column{Precision: 10, Scale: &s2}, true},
		{"both set equal", &irpb.Column{Precision: 10, Scale: &s2}, &irpb.Column{Precision: 10, Scale: &s2}, false},
		{"both set different", &irpb.Column{Precision: 10, Scale: &s2}, &irpb.Column{Precision: 10, Scale: &s3}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := numericChanged(c.prev, c.curr); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
