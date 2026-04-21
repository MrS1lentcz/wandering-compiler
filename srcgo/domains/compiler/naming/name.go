// Package naming derives the migration basename from the compile moment.
// Per docs/iteration-1.md D5 rev2 (2026-04-21) the basename is a compact
// UTC ISO-8601 timestamp — no sequence counter, no op-derived slug — so
// cross-machine generate runs collide at the filename level only when the
// clock agrees to the second, and review of migrations happens entirely in
// the hosted migration platform's UI (D6).
package naming

import "time"

// layout is the compact UTC ISO-8601 basic format — fixed-width, lex-sorts
// chronologically, no separators beyond T and the trailing Z.
const layout = "20060102T150405Z"

// Name returns the migration basename for a given generation moment. The
// CLI passes time.Now().UTC(); tests inject a frozen clock. No error path —
// time.Format always produces a fixed-width string for the layout above.
func Name(at time.Time) string {
	return at.UTC().Format(layout)
}
