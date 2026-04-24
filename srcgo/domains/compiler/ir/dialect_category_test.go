package ir_test

// Coverage for the D34 DialectCategory lookup + String()
// rendering. BuildMany tests exercise the same lookup indirectly
// but only via the two dialects in their fixtures; this pins
// every enum value explicitly.

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

func TestDialectCategory_AllDialects(t *testing.T) {
	cases := []struct {
		in   irpb.Dialect
		want ir.Category
	}{
		{irpb.Dialect_POSTGRES, ir.CategoryRelational},
		{irpb.Dialect_MYSQL, ir.CategoryRelational},
		{irpb.Dialect_SQLITE, ir.CategoryRelational},
		{irpb.Dialect_REDIS, ir.CategoryKeyValue},
		{irpb.Dialect_DIALECT_UNSPECIFIED, ir.CategoryUnspecified},
		// Out-of-range Dialect values (defensive) fall to UNSPECIFIED.
		{irpb.Dialect(9999), ir.CategoryUnspecified},
	}
	for _, c := range cases {
		t.Run(c.in.String(), func(t *testing.T) {
			if got := ir.DialectCategory(c.in); got != c.want {
				t.Errorf("DialectCategory(%s) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestCategoryString(t *testing.T) {
	cases := []struct {
		in   ir.Category
		want string
	}{
		{ir.CategoryRelational, "RELATIONAL"},
		{ir.CategoryKeyValue, "KEY_VALUE"},
		{ir.CategoryMessageBroker, "MESSAGE_BROKER"},
		{ir.CategoryUnspecified, "UNSPECIFIED"},
		{ir.Category(99), "UNSPECIFIED"}, // out-of-range defensive
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.in.String(); got != c.want {
				t.Errorf("Category(%d).String() = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
