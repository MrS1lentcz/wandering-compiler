package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/plan"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// EmitterFor resolves a Connection to the per-dialect emitter. Caller-
// supplied so the engine doesn't hard-import every dialect package.
//
// A nil Connection (iter-1 default-bucket path) must resolve to the
// caller's "default dialect" — typically Postgres.
type EmitterFor func(*irpb.Connection) (emit.DialectEmitter, error)

// Plan is the D30 top-level pure function: takes two IR snapshots
// plus the known Resolutions, returns a fully-rendered
// planpb.Plan (Migrations + unresolved Findings). No file I/O, no
// globals, no waiting — see docs/iteration-2.md D30 for the
// contract.
//
// Inputs:
//
//   - prev / curr — IR schemas; either nil (initial migration or
//     full teardown).
//   - cls — classifier for axis dispatch; must be non-nil. Pass a
//     cached instance (classifier.Load once, share).
//   - resolutions — known decisions from a ResolutionSource. Pass
//     nil/empty for the "no decisions yet" initial probe; re-run
//     with accumulated resolutions to close the loop.
//   - emitterFor — callback dispatching Connection → emitter.
//
// Output.Migrations carries one entry per (prev, curr) bucket grouped
// by Connection. Migrations whose diff is empty are omitted (AC #1).
// Output.Findings carries unresolved decision points — apply more
// Resolutions and re-run to drive the set empty.
//
// Idempotence: same (prev, curr, resolutions) input → byte-identical
// *planpb.Plan. Finding IDs are deterministic hashes so resolutions
// survive re-runs.
func Plan(
	prev, curr *irpb.Schema,
	cls *classifier.Classifier,
	resolutions []*planpb.Resolution,
	emitterFor EmitterFor,
) (*planpb.Plan, error) {
	if cls == nil {
		return nil, fmt.Errorf("engine.Plan: classifier is nil")
	}
	if emitterFor == nil {
		return nil, fmt.Errorf("engine.Plan: emitterFor is nil")
	}
	resolutionsByID := indexResolutions(resolutions)

	buckets, order := bucketByConnection(prev, curr)
	out := &planpb.Plan{}
	for _, key := range order {
		bkt := buckets[key]
		mig, findings, err := planBucket(bkt, cls, resolutionsByID, emitterFor)
		if err != nil {
			return nil, fmt.Errorf("engine.Plan: bucket %q: %w", key, err)
		}
		if mig != nil {
			out.Migrations = append(out.Migrations, mig)
		}
		out.Findings = append(out.Findings, findings...)
	}
	return out, nil
}

// planBucket runs Diff + emit for one connection bucket and returns
// (Migration, unresolved findings). Returns a nil Migration when the
// diff produced no SQL (both up and down empty).
func planBucket(
	bkt *bucket,
	cls *classifier.Classifier,
	resolutionsByID map[string]*planpb.Resolution,
	emitterFor EmitterFor,
) (*planpb.Migration, []*planpb.ReviewFinding, error) {
	result, err := plan.Diff(bkt.prev, bkt.curr, cls)
	if err != nil {
		return nil, nil, err
	}
	emitter, err := emitterFor(bkt.conn)
	if err != nil {
		return nil, nil, err
	}
	up, down, err := emit.Emit(emitter, result.Plan)
	if err != nil {
		return nil, nil, err
	}

	unresolved, applied, resolvedPairs := splitByResolution(result.Findings, resolutionsByID)

	// Splice author-provided CUSTOM_MIGRATION SQL into the Migration
	// body before AC #1's empty-diff check — CUSTOM_MIGRATION may be
	// the only change the migration carries (carrier-change-only
	// alter where the emitter omits its Op + user supplies the SQL
	// by hand).
	up, down = spliceCustomMigrations(up, down, resolvedPairs)

	// AC #1: no-op diff writes nothing + carries no findings → skip
	// the whole bucket. When findings exist but no SQL, keep the
	// finding stream but don't emit an empty Migration.
	if up == "" && down == "" {
		return nil, unresolved, nil
	}

	return &planpb.Migration{
		Connection: bkt.conn,
		Plan:       result.Plan,
		UpSql:      up,
		DownSql:    down,
		Checks:     collectChecks(result.Plan, cls),
		Manifest:   buildManifest(applied),
	}, unresolved, nil
}

// resolvedPair bundles a ReviewFinding with its matching Resolution.
// Used to splice CustomSQL into the Migration envelope — the Finding
// carries axis + column info the splice comment uses, the Resolution
// carries the SQL body.
type resolvedPair struct {
	Finding    *planpb.ReviewFinding
	Resolution *planpb.Resolution
}

// spliceCustomMigrations walks resolved CUSTOM_MIGRATION pairs and
// prepends their CustomSQL to UpSql (with an attribution comment),
// appends a rollback marker to DownSql (author's responsibility,
// since CUSTOM_MIGRATION has no automatic reverse).
//
// Multiple CUSTOM_MIGRATION resolutions splice in Finding ID-sorted
// order for determinism. DROP_AND_CREATE + other strategies pass
// through untouched — they flow via Op emission (future work) or
// stay as annotations.
func spliceCustomMigrations(up, down string, pairs []resolvedPair) (string, string) {
	for _, p := range pairs {
		if p.Resolution.GetStrategy() != planpb.Strategy_CUSTOM_MIGRATION {
			continue
		}
		if p.Resolution.GetCustomSql() == "" {
			continue
		}
		header := fmt.Sprintf("-- CUSTOM_MIGRATION: %s.%s (%s)\n",
			p.Finding.GetColumn().GetTableName(),
			p.Finding.GetColumn().GetColumnName(),
			p.Finding.GetAxis())
		footer := "-- END CUSTOM_MIGRATION\n"
		up = header + p.Resolution.GetCustomSql() + "\n" + footer + up
		down = down + fmt.Sprintf("-- NOTE: CUSTOM_MIGRATION applied for %s.%s (%s); author owns rollback SQL for this change.\n",
			p.Finding.GetColumn().GetTableName(),
			p.Finding.GetColumn().GetColumnName(),
			p.Finding.GetAxis())
	}
	return up, down
}

// splitByResolution partitions findings into (unresolved, applied,
// resolvedPairs) based on which have a matching Resolution by ID.
// Applied drives the Manifest audit trail; resolvedPairs drives
// downstream CustomSQL splicing (the pair retains the original
// finding reference for context).
func splitByResolution(
	findings []*planpb.ReviewFinding,
	byID map[string]*planpb.Resolution,
) (unresolved []*planpb.ReviewFinding, applied []*planpb.AppliedResolution, pairs []resolvedPair) {
	for _, f := range findings {
		r, ok := byID[f.GetId()]
		if !ok {
			unresolved = append(unresolved, f)
			continue
		}
		applied = append(applied, &planpb.AppliedResolution{
			FindingId:     r.GetFindingId(),
			Strategy:      r.GetStrategy(),
			Actor:         r.GetActor(),
			DecidedAtUnix: r.GetDecidedAtUnix(),
			CustomSql:     r.GetCustomSql(),
		})
		pairs = append(pairs, resolvedPair{Finding: f, Resolution: r})
	}
	return unresolved, applied, pairs
}

// buildManifest assembles the Manifest for a Migration. Today only
// carries the applied-resolution audit trail; required_extensions +
// capabilities land in M4 (emitter-driven usage tracking).
func buildManifest(applied []*planpb.AppliedResolution) *planpb.Manifest {
	if len(applied) == 0 {
		return nil
	}
	return &planpb.Manifest{AppliedResolutions: applied}
}

// indexResolutions builds an O(1) finding_id → Resolution map. Nil
// input returns an empty map (not nil) so callers can Lookup without
// a nil-check.
func indexResolutions(rs []*planpb.Resolution) map[string]*planpb.Resolution {
	out := make(map[string]*planpb.Resolution, len(rs))
	for _, r := range rs {
		if r.GetFindingId() == "" {
			continue
		}
		out[r.GetFindingId()] = r
	}
	return out
}

// bucket groups prev + curr tables by Connection. Duplicated from
// cmd/cli/cmd_generate.go (soon replaceable once that file uses
// engine.Plan; this copy makes engine self-contained for tests).
type bucket struct {
	conn *irpb.Connection
	prev *irpb.Schema
	curr *irpb.Schema
}

// bucketByConnection — same semantics as cmd/cli's copy (D26 multi-
// connection orchestration). Ordered keys: default ("") first, then
// lexical on <lower(dialect)>-<version>.
func bucketByConnection(prev, curr *irpb.Schema) (map[string]*bucket, []string) {
	buckets := map[string]*bucket{}
	keyOf := func(t *irpb.Table) (string, *irpb.Connection) {
		c := t.GetConnection()
		if c == nil {
			return "", nil
		}
		return connectionDirKey(c), c
	}
	addTable := func(side string, t *irpb.Table) {
		key, conn := keyOf(t)
		b, ok := buckets[key]
		if !ok {
			b = &bucket{conn: conn, prev: &irpb.Schema{}, curr: &irpb.Schema{}}
			buckets[key] = b
		}
		switch side {
		case "prev":
			b.prev.Tables = append(b.prev.Tables, t)
		case "curr":
			b.curr.Tables = append(b.curr.Tables, t)
		}
	}
	for _, t := range prev.GetTables() {
		addTable("prev", t)
	}
	for _, t := range curr.GetTables() {
		addTable("curr", t)
	}
	// prev == nil means initial migration — the buckets' prev side
	// must surface as nil so plan.Diff takes the "no prev" path.
	if prev == nil {
		for _, b := range buckets {
			b.prev = nil
		}
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return buckets, keys
}

func connectionDirKey(c *irpb.Connection) string {
	return strings.ToLower(c.GetDialect().String()) + "-" + c.GetVersion()
}
