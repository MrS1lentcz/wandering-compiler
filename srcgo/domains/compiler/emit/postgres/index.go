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
// Index name derivation (when Index.Name is empty):
//
//	<table>_<cols>_uidx      // unique
//	<table>_<cols>_idx       // non-unique
//
// Where <cols> is the SQL column names joined by "_". Matches the reference
// in iteration-1-models.md.
func renderIndexes(t *irpb.Table, colByProto map[string]*irpb.Column) (stmts []string, names []string, err error) {
	for i, idx := range t.GetIndexes() {
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

		name := idx.GetName()
		if name == "" {
			name = derivedIndexName(t.GetName(), sqlCols, idx.GetUnique())
		}

		var b strings.Builder
		if idx.GetUnique() {
			b.WriteString("CREATE UNIQUE INDEX ")
		} else {
			b.WriteString("CREATE INDEX ")
		}
		fmt.Fprintf(&b, "%s ON %s (%s)", name, t.GetName(), strings.Join(sqlCols, ", "))
		if len(sqlInclude) > 0 {
			fmt.Fprintf(&b, " INCLUDE (%s)", strings.Join(sqlInclude, ", "))
		}
		b.WriteString(";")

		stmts = append(stmts, b.String())
		names = append(names, name)
	}
	return stmts, names, nil
}

func derivedIndexName(table string, sqlCols []string, unique bool) string {
	suffix := "idx"
	if unique {
		suffix = "uidx"
	}
	return fmt.Sprintf("%s_%s_%s", table, strings.Join(sqlCols, "_"), suffix)
}
