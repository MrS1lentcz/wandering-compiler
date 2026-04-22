package diag_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
)

// TestErrorFormat exercises every branch of Error.Error():
//   bare Msg, File + line/col prefix, Why suffix, Fix suffix,
//   combined Why + Fix, empty descriptor fallback.
func TestErrorFormat(t *testing.T) {
	cases := []struct {
		name string
		e    *diag.Error
		want string
	}{
		{
			name: "msg only",
			e:    &diag.Error{Msg: "boom"},
			want: "boom",
		},
		{
			name: "file + line/col + msg",
			e:    &diag.Error{File: "x.proto", Line: 3, Col: 14, Msg: "bad"},
			want: "x.proto:3:14: bad",
		},
		{
			name: "with why",
			e:    (&diag.Error{Msg: "bad"}).WithWhy("rule"),
			want: "bad\n  why: rule",
		},
		{
			name: "with fix",
			e:    (&diag.Error{Msg: "bad"}).WithFix("do X"),
			want: "bad\n  fix: do X",
		},
		{
			name: "full",
			e:    (&diag.Error{File: "a.proto", Line: 1, Col: 2, Msg: "m"}).WithWhy("w").WithFix("f"),
			want: "a.proto:1:2: m\n  why: w\n  fix: f",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.e.Error(); got != c.want {
				t.Errorf("\ngot:  %q\nwant: %q", got, c.want)
			}
		})
	}
}

// TestAt_NilDescriptor — nil descriptor yields a bare Error with
// Msg only (no file path, no line/col). Guards against panic in
// code paths that call diag.Atf(nil, …).
func TestAt_NilDescriptor(t *testing.T) {
	e := diag.At(nil, "m")
	if e.File != "" || e.Line != 0 || e.Col != 0 {
		t.Errorf("expected zero-value anchoring, got %+v", e)
	}
	if e.Msg != "m" {
		t.Errorf("Msg = %q", e.Msg)
	}
}

// TestAsDiag — unwraps joined / wrapped diag.Error instances, and
// returns (nil, false) for non-diag errors.
func TestAsDiag(t *testing.T) {
	d := &diag.Error{Msg: "yes"}

	// Direct.
	got, ok := diag.AsDiag(d)
	if !ok || got != d {
		t.Errorf("direct: got %v ok=%v, want %v true", got, ok, d)
	}

	// Wrapped via fmt.Errorf %w.
	wrapped := fmt.Errorf("prefix: %w", d)
	got, ok = diag.AsDiag(wrapped)
	if !ok || got != d {
		t.Errorf("wrapped: got %v ok=%v, want %v true", got, ok, d)
	}

	// Joined.
	joined := errors.Join(errors.New("other"), d)
	got, ok = diag.AsDiag(joined)
	if !ok || got != d {
		t.Errorf("joined: got %v ok=%v, want %v true", got, ok, d)
	}

	// Non-diag returns false.
	plain := errors.New("plain")
	got, ok = diag.AsDiag(plain)
	if ok || got != nil {
		t.Errorf("plain: got %v ok=%v, want nil false", got, ok)
	}

	// Substring check on Error() for sanity — the internal test
	// helpers rely on .Error() containing the original Msg.
	if !strings.Contains(d.Error(), "yes") {
		t.Error("Error() missing Msg")
	}
}

// TestAtf_FormatsWithoutDescriptor — diag.Atf uses fmt.Sprintf
// internally; verify the variadic args land correctly for the
// nil-descriptor path (which we exercise elsewhere).
func TestAtf_FormatsWithoutDescriptor(t *testing.T) {
	e := diag.Atf(nil, "field %q: carrier %d invalid", "email", 42)
	want := `field "email": carrier 42 invalid`
	if e.Msg != want {
		t.Errorf("Msg = %q, want %q", e.Msg, want)
	}
}
