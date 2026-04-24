package classifier_test

// Iterator coverage — classifier's AllCarrierCells /
// AllDbTypeCells / AllConstraintCells are used exclusively by
// the build-tagged e2e harness, so default `go test` never
// exercises them. These tests pin the iteration contract +
// deterministic ordering the harness relies on.

import (
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
)

func loadCls(t *testing.T) *classifier.Classifier {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "classification")
	c, err := classifier.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestAllCarrierCells_ReturnsAuthoredMatrixSize(t *testing.T) {
	cls := loadCls(t)
	got := cls.AllCarrierCells()
	// carrier.yaml has 110 authored entries. Number may grow over
	// time; assert a floor.
	if len(got) < 100 {
		t.Errorf("want >= 100 carrier cells, got %d", len(got))
	}
}

func TestAllCarrierCells_Deterministic(t *testing.T) {
	cls := loadCls(t)
	a := cls.AllCarrierCells()
	b := cls.AllCarrierCells()
	if len(a) != len(b) {
		t.Fatalf("len differs across calls: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].From != b[i].From || a[i].To != b[i].To {
			t.Errorf("order differs at [%d]: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestAllCarrierCells_SortedByFromThenTo(t *testing.T) {
	cls := loadCls(t)
	cells := cls.AllCarrierCells()
	if !sort.SliceIsSorted(cells, func(i, j int) bool {
		if cells[i].From != cells[j].From {
			return cells[i].From < cells[j].From
		}
		return cells[i].To < cells[j].To
	}) {
		t.Error("AllCarrierCells not sorted by (From, To) enum values")
	}
}

func TestAllDbTypeCells_ReturnsAuthoredMatrixSize(t *testing.T) {
	cls := loadCls(t)
	got := cls.AllDbTypeCells()
	if len(got) < 40 {
		t.Errorf("want >= 40 dbtype cells, got %d", len(got))
	}
}

func TestAllDbTypeCells_SortedByFamilyFromTo(t *testing.T) {
	cls := loadCls(t)
	cells := cls.AllDbTypeCells()
	if !sort.SliceIsSorted(cells, func(i, j int) bool {
		if cells[i].Family != cells[j].Family {
			return cells[i].Family < cells[j].Family
		}
		if cells[i].From != cells[j].From {
			return cells[i].From < cells[j].From
		}
		return cells[i].To < cells[j].To
	}) {
		t.Error("AllDbTypeCells not sorted by (Family, From, To)")
	}
}

func TestAllConstraintCells_ReturnsAuthoredMatrixSize(t *testing.T) {
	cls := loadCls(t)
	got := cls.AllConstraintCells()
	if len(got) < 50 {
		t.Errorf("want >= 50 constraint cells, got %d", len(got))
	}
}

func TestAllConstraintCells_SortedByAxisCase(t *testing.T) {
	cls := loadCls(t)
	cells := cls.AllConstraintCells()
	if !sort.SliceIsSorted(cells, func(i, j int) bool {
		if cells[i].Axis != cells[j].Axis {
			return cells[i].Axis < cells[j].Axis
		}
		return cells[i].CaseID < cells[j].CaseID
	}) {
		t.Error("AllConstraintCells not sorted by (Axis, Case)")
	}
}
