package postgres

import (
	"fmt"
	"strings"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// renderIndexes emits separate CREATE [UNIQUE] INDEX statements for a table,
// returning (stmts, names, err). names is the ordered list for DROP INDEX
// generation in the down block (caller reverses it).
//
// Index names are resolved in ir.Build (see ir/names.go.derivedIndexName
// and the finalisation pass in ir/build.go.buildTable) — every Index
// arrives here with Name already set. Name collisions and identifier-
// length violations are rejected at IR time.
func renderIndexes(t *irpb.Table, colByProto map[string]*irpb.Column) (stmts []string, names []string, err error) {
	for i, idx := range t.GetIndexes() {
		if idx.GetName() == "" {
			return nil, nil, fmt.Errorf("postgres: table %s: indexes[%d] has empty name (ir.Build was supposed to resolve it)", t.GetName(), i)
		}
		sqlCols := make([]string, 0, len(idx.GetFields()))
		for _, f := range idx.GetFields() {
			c, ok := colByProto[f]
			if !ok {
				return nil, nil, fmt.Errorf("postgres: table %s: indexes[%d] references unknown proto field %q (builder invariant violated)", t.GetName(), i, f)
			}
			sqlCols = append(sqlCols, c.GetName())
		}
		sqlInclude := make([]string, 0, len(idx.GetInclude()))
		for _, f := range idx.GetInclude() {
			c, ok := colByProto[f]
			if !ok {
				return nil, nil, fmt.Errorf("postgres: table %s: indexes[%d] INCLUDE references unknown proto field %q (builder invariant violated)", t.GetName(), i, f)
			}
			sqlInclude = append(sqlInclude, c.GetName())
		}

		var b strings.Builder
		if idx.GetUnique() {
			b.WriteString("CREATE UNIQUE INDEX ")
		} else {
			b.WriteString("CREATE INDEX ")
		}
		fmt.Fprintf(&b, "%s ON %s (%s)", idx.GetName(), t.GetName(), strings.Join(sqlCols, ", "))
		if len(sqlInclude) > 0 {
			fmt.Fprintf(&b, " INCLUDE (%s)", strings.Join(sqlInclude, ", "))
		}
		b.WriteString(";")

		stmts = append(stmts, b.String())
		names = append(names, idx.GetName())
	}
	return stmts, names, nil
}
