package postgres

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
	planpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/plan"
)

// emitAddIndex renders one CREATE [UNIQUE] INDEX statement; down
// drops the index. Reuses renderIndexes via a single-index synthetic
// table so the structured-vs-raw + method + per-field-sort + storage
// rendering stays in one place.
func (e Emitter) emitAddIndex(ai *planpb.AddIndex, usage *emit.Usage) (string, string, error) {
	tbl := indexTableShell(ai.GetCtx(), ai.GetColumns(), ai.GetIndex(), nil)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}
	colByProto := columnByProto(ai.GetColumns())
	stmts, names, err := renderIndexes(tbl, colByProto, usage)
	if err != nil {
		return "", "", err
	}
	if len(stmts) != 1 {
		return "", "", fmt.Errorf("postgres: AddIndex synthetic emit produced %d stmts (want 1)", len(stmts))
	}
	up := stmts[0]
	down := fmt.Sprintf("DROP INDEX IF EXISTS %s;", qualifiedIdentifier(tbl, names[0]))
	return up, down, nil
}

// emitDropIndex renders DROP INDEX [IF EXISTS] <qualified_name>;
// down recreates via the prev-side Index (carried by the op).
func (e Emitter) emitDropIndex(di *planpb.DropIndex) (string, string, error) {
	idx := di.GetIndex()
	if idx == nil || idx.GetName() == "" {
		return "", "", fmt.Errorf("postgres: DropIndex with no Index / empty name")
	}
	tbl := indexTableShell(di.GetCtx(), di.GetColumns(), idx, nil)
	up := fmt.Sprintf("DROP INDEX IF EXISTS %s;", qualifiedIdentifier(tbl, idx.GetName()))
	// Rebuild via emitAddIndex inverse to keep the down branch
	// aligned with what AddIndex would produce on its up.
	// DropIndex: no caps on up; down re-creates via emitAddIndex but
	// we don't credit caps to this op (drop is the net effect). Pass
	// nil so the rebuild doesn't leak caps into the manifest.
	addUp, _, err := e.emitAddIndex(&planpb.AddIndex{
		Ctx:     di.GetCtx(),
		Index:   idx,
		Columns: di.GetColumns(),
	}, nil)
	if err != nil {
		return "", "", err
	}
	return up, addUp, nil
}

// emitReplaceIndex emits DROP <from> + CREATE <to>; down inverts.
// PG has no ALTER INDEX for shape (fields, method, unique, include,
// storage) so any change collapses to drop + recreate.
func (e Emitter) emitReplaceIndex(ri *planpb.ReplaceIndex, usage *emit.Usage) (string, string, error) {
	from, to := ri.GetFrom(), ri.GetTo()
	if from == nil || to == nil {
		return "", "", fmt.Errorf("postgres: ReplaceIndex missing from/to")
	}
	tbl := indexTableShell(ri.GetCtx(), ri.GetColumns(), to, from)
	if tbl.GetNamespaceMode() == irpb.NamespaceMode_NAMESPACE_MODE_SCHEMA {
		usage.Use(emit.CapSchemaQualified)
	}

	addOps := []*irpb.Index{to}
	addStmts, addNames, err := renderIndexes(replaceIndexes(tbl, addOps), columnByProto(ri.GetColumns()), usage)
	if err != nil {
		return "", "", fmt.Errorf("postgres: ReplaceIndex render to: %w", err)
	}
	if len(addStmts) != 1 {
		return "", "", fmt.Errorf("postgres: ReplaceIndex synthetic emit produced %d add stmts", len(addStmts))
	}

	dropFrom := fmt.Sprintf("DROP INDEX IF EXISTS %s;", qualifiedIdentifier(tbl, from.GetName()))
	dropTo := fmt.Sprintf("DROP INDEX IF EXISTS %s;", qualifiedIdentifier(tbl, addNames[0]))

	// Build symmetric down via re-emitting the from side — share the
	// usage collector so any method/include cap the rollback re-
	// introduces shows on the manifest.
	addBackStmts, _, err := renderIndexes(replaceIndexes(tbl, []*irpb.Index{from}), columnByProto(ri.GetColumns()), usage)
	if err != nil {
		return "", "", fmt.Errorf("postgres: ReplaceIndex render from (down): %w", err)
	}
	if len(addBackStmts) != 1 {
		return "", "", fmt.Errorf("postgres: ReplaceIndex synthetic emit produced %d down stmts", len(addBackStmts))
	}

	up := dropFrom + "\n" + addStmts[0]
	down := dropTo + "\n" + addBackStmts[0]
	return up, down, nil
}

// emitAddRawIndex / emitDropRawIndex / emitReplaceRawIndex handle the
// D11 escape-hatch indexes. Body is opaque; identity is the index name.
func (e Emitter) emitAddRawIndex(ari *planpb.AddRawIndex) (string, string, error) {
	tbl := tableShellFromCtx(ari.GetCtx(), nil)
	ri := ari.GetIndex()
	if ri == nil || ri.GetName() == "" {
		return "", "", fmt.Errorf("postgres: AddRawIndex with no body / empty name")
	}
	up := renderRawIndex(tbl, ri)
	down := fmt.Sprintf("DROP INDEX IF EXISTS %s;", qualifiedIdentifier(tbl, ri.GetName()))
	return up, down, nil
}

func (e Emitter) emitDropRawIndex(dri *planpb.DropRawIndex) (string, string, error) {
	tbl := tableShellFromCtx(dri.GetCtx(), nil)
	ri := dri.GetIndex()
	if ri == nil || ri.GetName() == "" {
		return "", "", fmt.Errorf("postgres: DropRawIndex with no body / empty name")
	}
	up := fmt.Sprintf("DROP INDEX IF EXISTS %s;", qualifiedIdentifier(tbl, ri.GetName()))
	down := renderRawIndex(tbl, ri)
	return up, down, nil
}

func (e Emitter) emitReplaceRawIndex(rri *planpb.ReplaceRawIndex) (string, string, error) {
	tbl := tableShellFromCtx(rri.GetCtx(), nil)
	from, to := rri.GetFrom(), rri.GetTo()
	if from == nil || to == nil {
		return "", "", fmt.Errorf("postgres: ReplaceRawIndex missing from/to")
	}
	up := fmt.Sprintf("DROP INDEX IF EXISTS %s;\n%s", qualifiedIdentifier(tbl, from.GetName()), renderRawIndex(tbl, to))
	down := fmt.Sprintf("DROP INDEX IF EXISTS %s;\n%s", qualifiedIdentifier(tbl, to.GetName()), renderRawIndex(tbl, from))
	return up, down, nil
}

// renderRawIndex builds `CREATE [UNIQUE] INDEX <name> ON <qualified> <body>;`
// — same shape as iter-1's writeIndexStatements raw branch.
func renderRawIndex(tbl *irpb.Table, ri *irpb.RawIndex) string {
	kw := "CREATE INDEX"
	if ri.GetUnique() {
		kw = "CREATE UNIQUE INDEX"
	}
	return fmt.Sprintf("%s %s ON %s %s;", kw, ri.GetName(), qualifiedTable(tbl), ri.GetBody())
}

// indexTableShell synthesises a *irpb.Table carrying the columns +
// the single Index (or the from-side index for ctx) so renderIndexes
// can emit the structured statement. The from arg is unused inside
// renderIndexes; carried for parity with future per-method needs.
func indexTableShell(ctx *planpb.TableCtx, cols []*irpb.Column, idx, _from *irpb.Index) *irpb.Table {
	t := &irpb.Table{
		Name:          ctx.GetTableName(),
		MessageFqn:    ctx.GetMessageFqn(),
		NamespaceMode: ctx.GetNamespaceMode(),
		Namespace:     ctx.GetNamespace(),
		Columns:       cols,
	}
	if idx != nil {
		t.Indexes = []*irpb.Index{idx}
	}
	return t
}

// replaceIndexes returns a clone of the table shell with its
// `Indexes` field swapped — used by the Replace path so we can
// re-render the from side for down without mutating the shared
// shell.
//
// Uses proto.Clone (not a Go-level value copy) because proto
// messages carry an internal MessageState with a sync.Mutex; a
// shallow copy trips `go vet` and risks concurrent-state confusion
// if the clone outlives the original in another goroutine. The
// cost is negligible for the small shell we're cloning.
func replaceIndexes(t *irpb.Table, idxs []*irpb.Index) *irpb.Table {
	clone := proto.Clone(t).(*irpb.Table)
	clone.Indexes = idxs
	return clone
}

// columnByProto rebuilds the proto-name → Column lookup the iter-1
// emit helpers expect. Empty input returns an empty map so callers
// don't have to nil-check.
func columnByProto(cols []*irpb.Column) map[string]*irpb.Column {
	out := make(map[string]*irpb.Column, len(cols))
	for _, c := range cols {
		out[c.GetProtoName()] = c
	}
	return out
}

// _ keeps the strings import live across refactors that may temporarily
// drop the only consumer.
var _ = strings.Builder{}
