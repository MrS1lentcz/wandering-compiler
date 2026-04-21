package naming_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/naming"
)

// Name produces a fixed-width UTC basename. This test pins the exact format
// so D5 rev2 (filename = YYYYMMDDTHHMMSSZ) can't drift silently.
func TestNameFormat(t *testing.T) {
	at := time.Date(2026, time.April, 21, 14, 30, 15, 0, time.UTC)
	got := naming.Name(at)
	want := "20260421T143015Z"
	if got != want {
		t.Errorf("Name(%v) = %q, want %q", at, got, want)
	}
}

// Inputs in a non-UTC zone must still render as UTC. The CLI always passes
// time.Now().UTC(), but a future caller that forgets shouldn't silently
// produce local-time filenames.
func TestNameNormalisesToUTC(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// 16:30:15 Europe/Prague (CEST, UTC+2 in April) == 14:30:15 UTC.
	at := time.Date(2026, time.April, 21, 16, 30, 15, 0, loc)
	if got, want := naming.Name(at), "20260421T143015Z"; got != want {
		t.Errorf("Name(%v) = %q, want %q", at, got, want)
	}
}

// Sanity: the output is always the exact layout shape. Anything emitting a
// different length would break platform lex-sort assumptions.
func TestNameShape(t *testing.T) {
	re := regexp.MustCompile(`^\d{8}T\d{6}Z$`)
	samples := []time.Time{
		time.Unix(0, 0).UTC(),
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	}
	for _, at := range samples {
		got := naming.Name(at)
		if !re.MatchString(got) {
			t.Errorf("Name(%v) = %q, want match %s", at, got, re)
		}
		if len(got) != len("20060102T150405Z") {
			t.Errorf("Name(%v) length = %d, want 16", at, len(got))
		}
	}
}

// Determinism: same input → same output. Cheap but keeps AC #4 honest even
// for the trivial pieces.
func TestNameDeterministic(t *testing.T) {
	at := time.Date(2026, 4, 21, 14, 30, 15, 0, time.UTC)
	if naming.Name(at) != naming.Name(at) {
		t.Fatal("Name is not deterministic")
	}
}
