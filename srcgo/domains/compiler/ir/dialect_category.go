package ir

// D34 — every dialect belongs to exactly one category. Within a
// domain (= one BuildMany input batch) at most one connection per
// category is allowed. Combining different categories is fine
// (RELATIONAL + KEY_VALUE = typical "relational records +
// ephemeral cache"); combining the same category (PG + MySQL, or
// Redis + Memcached) is rejected here at IR build time.
//
// Classification is by primary focus. DBs that span paradigms
// (PG LISTEN/NOTIFY broker-ish, Redis streams broker-ish) keep
// their primary category — this map is the single source of
// truth and must not grow a dialect into two categories.

import (
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// Category is the coarse-grained bucket each dialect belongs to.
// Used only by BuildMany's domain-level validation; not persisted
// on the Connection message.
type Category int

const (
	CategoryUnspecified Category = iota
	CategoryRelational
	CategoryKeyValue
	CategoryMessageBroker
)

// String renders a human-readable label for diagnostics.
func (c Category) String() string {
	switch c {
	case CategoryRelational:
		return "RELATIONAL"
	case CategoryKeyValue:
		return "KEY_VALUE"
	case CategoryMessageBroker:
		return "MESSAGE_BROKER"
	}
	return "UNSPECIFIED"
}

// DialectCategory returns the fixed category for a dialect.
// Unknown / UNSPECIFIED dialects return CategoryUnspecified; the
// D26 / D34 checks filter those out separately (they fail per-
// file validation before the domain-level check runs).
func DialectCategory(d irpb.Dialect) Category {
	switch d {
	case irpb.Dialect_POSTGRES, irpb.Dialect_MYSQL, irpb.Dialect_SQLITE:
		return CategoryRelational
	case irpb.Dialect_REDIS:
		return CategoryKeyValue
	}
	return CategoryUnspecified
}
