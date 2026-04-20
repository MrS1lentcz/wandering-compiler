// Package diag carries user-facing compiler diagnostics. Every error the
// compiler surfaces to a developer flows through *diag.Error so its format
// is uniform: a file:line:col header followed by an optional "why" (root
// cause — why the rule exists) and "fix" (concrete hint — what to change).
//
// Loader and IR-build errors build these directly from proto descriptors;
// emitters construct them from higher-level IR locations.
package diag

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Error is the shared user-facing diagnostic type.
type Error struct {
	File string
	Line int // 1-based
	Col  int // 1-based
	Msg  string
	Why  string // optional
	Fix  string // optional
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.File != "" {
		fmt.Fprintf(&b, "%s:%d:%d: %s", e.File, e.Line, e.Col, e.Msg)
	} else {
		b.WriteString(e.Msg)
	}
	if e.Why != "" {
		b.WriteString("\n  why: ")
		b.WriteString(e.Why)
	}
	if e.Fix != "" {
		b.WriteString("\n  fix: ")
		b.WriteString(e.Fix)
	}
	return b.String()
}

// Why attaches the root-cause explanation and returns the same error for
// chaining.
func (e *Error) WithWhy(why string) *Error {
	e.Why = why
	return e
}

// Fix attaches the concrete hint and returns the same error for chaining.
func (e *Error) WithFix(fix string) *Error {
	e.Fix = fix
	return e
}

// At builds an error anchored to a proto descriptor's source location.
// If the descriptor has no recorded source info (synthetic descriptor,
// missing source_code_info) the error falls back to the file path only.
func At(d protoreflect.Descriptor, msg string) *Error {
	e := &Error{Msg: msg}
	if d == nil {
		return e
	}
	file := d.ParentFile()
	if file == nil {
		return e
	}
	e.File = file.Path()
	loc := file.SourceLocations().ByDescriptor(d)
	// protoreflect returns zero-value SourceLocation when none is recorded;
	// StartLine == 0 then. Guard against emitting :0:0: which looks broken.
	if loc.StartLine > 0 || loc.StartColumn > 0 {
		e.Line = loc.StartLine + 1
		e.Col = loc.StartColumn + 1
	}
	return e
}

// Atf is At with a formatted message.
func Atf(d protoreflect.Descriptor, format string, args ...any) *Error {
	return At(d, fmt.Sprintf(format, args...))
}

// AsDiag reports whether err is a *diag.Error and returns it.
func AsDiag(err error) (*Error, bool) {
	var de *Error
	if errors.As(err, &de) {
		return de, true
	}
	return nil, false
}
