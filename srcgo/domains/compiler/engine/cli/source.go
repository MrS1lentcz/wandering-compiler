// Package cli parses `--decide` CLI flags into engine Resolutions.
//
// Flag syntax:
//
//	--decide <table>.<column>=<strategy>
//	--decide <table>.<column>=custom:<sql-file-path>
//
// Strategy values: safe, lossless_using, needs_confirm, drop_and_create,
// custom_migration (case-insensitive).
//
// Match semantics: a decision with column key "users.email" matches any
// ReviewFinding whose ColumnRef.TableName == "users" and ColumnName ==
// "email", regardless of axis. Users who need axis-specific decisions
// can add axis suffix:
//
//	--decide <table>.<column>:<axis>=<strategy>
//
// Per D30, CLISource is a pre-processing helper that produces a slice
// of Resolutions. Callers typically wrap the slice in memory.Source to
// satisfy engine.ResolutionSource. CLISource does not implement
// ResolutionSource directly because the Resolution → ReviewFinding
// binding requires access to the findings list (finding IDs are hashes
// the CLI can't predict up-front).
package cli

import (
	"fmt"
	"os"
	"strings"

	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// Decision is one --decide flag's parsed content. CustomSQL is empty
// unless Strategy == CUSTOM_MIGRATION; CustomSQLPath is the source
// path when CustomSQL was loaded from disk.
type Decision struct {
	Strategy      planpb.Strategy
	CustomSQL     string
	CustomSQLPath string
}

// decisionKey matches a Decision against a ReviewFinding.ColumnRef.
// When axis == "", the key matches findings on (table, column)
// regardless of axis.
type decisionKey struct {
	table, column, axis string
}

// Decisions is a bag of --decide flags, keyed for fast Finding
// matching. Build via Parse; match via ResolveAll.
type Decisions struct {
	byKey map[decisionKey]Decision
}

// Parse converts a slice of --decide flag values into a Decisions bag.
// Each flag is validated independently; the first parse error halts.
//
// customSQLLoader resolves `custom:<path>` into the SQL body. Pass
// DefaultSQLLoader for filesystem reads; pass a test double for in-
// memory test fixtures.
func Parse(flags []string, customSQLLoader func(path string) (string, error)) (*Decisions, error) {
	d := &Decisions{byKey: make(map[decisionKey]Decision)}
	for _, raw := range flags {
		key, decision, err := parseOne(raw, customSQLLoader)
		if err != nil {
			return nil, fmt.Errorf("parse --decide %q: %w", raw, err)
		}
		if _, dup := d.byKey[key]; dup {
			return nil, fmt.Errorf("parse --decide %q: duplicate key for (%s.%s:%s)", raw, key.table, key.column, key.axis)
		}
		d.byKey[key] = decision
	}
	return d, nil
}

// DefaultSQLLoader reads a CustomSQL file from the filesystem.
func DefaultSQLLoader(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ResolveAll walks the findings list and builds Resolutions for every
// finding that matches one of the parsed decisions. Match precedence:
// axis-specific key first, then column-wide fallback.
//
// The returned slice is in findings order. Findings without a matching
// Decision are skipped — use Unresolved() to get those.
func (d *Decisions) ResolveAll(findings []*planpb.ReviewFinding) []*planpb.Resolution {
	var out []*planpb.Resolution
	for _, f := range findings {
		dec, ok := d.lookupFor(f)
		if !ok {
			continue
		}
		out = append(out, &planpb.Resolution{
			FindingId: f.GetId(),
			Strategy:  dec.Strategy,
			CustomSql: dec.CustomSQL,
			Actor:     "cli",
		})
	}
	return out
}

// Unresolved returns findings that don't have a matching Decision.
// Callers typically print these and exit non-zero so users know which
// --decide flags are still missing.
func (d *Decisions) Unresolved(findings []*planpb.ReviewFinding) []*planpb.ReviewFinding {
	var out []*planpb.ReviewFinding
	for _, f := range findings {
		if _, ok := d.lookupFor(f); !ok {
			out = append(out, f)
		}
	}
	return out
}

func (d *Decisions) lookupFor(f *planpb.ReviewFinding) (Decision, bool) {
	if f.GetColumn() == nil {
		return Decision{}, false
	}
	table := f.GetColumn().GetTableName()
	column := f.GetColumn().GetColumnName()
	axis := f.GetAxis()
	if dec, ok := d.byKey[decisionKey{table: table, column: column, axis: axis}]; ok {
		return dec, true
	}
	if dec, ok := d.byKey[decisionKey{table: table, column: column}]; ok {
		return dec, true
	}
	return Decision{}, false
}

// parseOne validates a single "<table>.<column>[:<axis>]=<strategy>"
// flag and returns (key, Decision, err).
func parseOne(raw string, loader func(string) (string, error)) (decisionKey, Decision, error) {
	eqIdx := strings.Index(raw, "=")
	if eqIdx < 0 {
		return decisionKey{}, Decision{}, fmt.Errorf("missing '='; expected <table>.<column>[:<axis>]=<strategy>")
	}
	lhs := strings.TrimSpace(raw[:eqIdx])
	rhs := strings.TrimSpace(raw[eqIdx+1:])

	key, err := parseKey(lhs)
	if err != nil {
		return decisionKey{}, Decision{}, err
	}
	decision, err := parseValue(rhs, loader)
	if err != nil {
		return decisionKey{}, Decision{}, err
	}
	return key, decision, nil
}

func parseKey(lhs string) (decisionKey, error) {
	// Optional axis suffix after ':'.
	axis := ""
	if colonIdx := strings.Index(lhs, ":"); colonIdx >= 0 {
		axis = strings.TrimSpace(lhs[colonIdx+1:])
		lhs = strings.TrimSpace(lhs[:colonIdx])
		if axis == "" {
			return decisionKey{}, fmt.Errorf("empty axis after ':'")
		}
	}
	dot := strings.Index(lhs, ".")
	if dot < 0 {
		return decisionKey{}, fmt.Errorf("key %q missing '.'; expected <table>.<column>", lhs)
	}
	table := strings.TrimSpace(lhs[:dot])
	column := strings.TrimSpace(lhs[dot+1:])
	if table == "" || column == "" {
		return decisionKey{}, fmt.Errorf("key %q has empty table or column", lhs)
	}
	return decisionKey{table: table, column: column, axis: axis}, nil
}

func parseValue(rhs string, loader func(string) (string, error)) (Decision, error) {
	if strings.HasPrefix(strings.ToLower(rhs), "custom:") {
		path := strings.TrimSpace(rhs[len("custom:"):])
		if path == "" {
			return Decision{}, fmt.Errorf("custom: prefix with empty path")
		}
		if loader == nil {
			return Decision{}, fmt.Errorf("custom: requires a loader (nil given)")
		}
		body, err := loader(path)
		if err != nil {
			return Decision{}, fmt.Errorf("load custom SQL from %s: %w", path, err)
		}
		return Decision{
			Strategy:      planpb.Strategy_CUSTOM_MIGRATION,
			CustomSQL:     body,
			CustomSQLPath: path,
		}, nil
	}
	strat, ok := parseStrategyName(rhs)
	if !ok {
		return Decision{}, fmt.Errorf("unknown strategy %q; valid: safe | lossless_using | needs_confirm | drop_and_create | custom:<path>", rhs)
	}
	return Decision{Strategy: strat}, nil
}

// parseStrategyName maps a case-insensitive flag value to a Strategy.
// custom_migration is intentionally excluded — users must specify a
// custom SQL path via `custom:<path>` so the CLI always has the SQL.
func parseStrategyName(s string) (planpb.Strategy, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "safe":
		return planpb.Strategy_SAFE, true
	case "lossless_using", "using":
		return planpb.Strategy_LOSSLESS_USING, true
	case "needs_confirm", "confirm":
		return planpb.Strategy_NEEDS_CONFIRM, true
	case "drop_and_create", "drop":
		return planpb.Strategy_DROP_AND_CREATE, true
	}
	return planpb.Strategy_STRATEGY_UNSPECIFIED, false
}
