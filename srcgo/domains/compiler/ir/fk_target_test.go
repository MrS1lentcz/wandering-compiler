package ir

import (
	"testing"

	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// TestFkTargetColumnIsUnique covers the three branches that
// qualify a column as a valid FK target:
//   (a) sole member of a single-column primary key — fast path;
//   (b) covered by a single-column UNIQUE index (explicit or
//       synthesised from (w17.field).unique);
//   (c) neither — rejected (composite-PK member, plain column,
//       column under a multi-col unique).
//
// Pre-test coverage was 44.4% — only the PK path fired from
// fixtures; the UNIQUE-index path and reject paths are exercised
// here.
func TestFkTargetColumnIsUnique(t *testing.T) {
	cases := []struct {
		name   string
		table  *irpb.Table
		col    string
		want   bool
	}{
		{
			name: "single-col PK",
			table: &irpb.Table{
				PrimaryKey: []string{"id"},
			},
			col:  "id",
			want: true,
		},
		{
			name: "composite PK member — not unique as FK target",
			table: &irpb.Table{
				PrimaryKey: []string{"tenant_id", "id"},
			},
			col:  "id",
			want: false,
		},
		{
			name: "single-col UNIQUE index covers column",
			table: &irpb.Table{
				Indexes: []*irpb.Index{
					{
						Unique: true,
						Fields: []*irpb.IndexField{{Name: "email"}},
					},
				},
			},
			col:  "email",
			want: true,
		},
		{
			name: "multi-col UNIQUE index does NOT qualify the member column",
			table: &irpb.Table{
				Indexes: []*irpb.Index{
					{
						Unique: true,
						Fields: []*irpb.IndexField{
							{Name: "tenant_id"},
							{Name: "handle"},
						},
					},
				},
			},
			col:  "handle",
			want: false,
		},
		{
			name: "non-unique single-col index does NOT qualify",
			table: &irpb.Table{
				Indexes: []*irpb.Index{
					{
						Fields: []*irpb.IndexField{{Name: "email"}},
					},
				},
			},
			col:  "email",
			want: false,
		},
		{
			name: "wrong column name — lookup misses",
			table: &irpb.Table{
				PrimaryKey: []string{"id"},
			},
			col:  "other",
			want: false,
		},
		{
			name:  "empty table rejects any lookup",
			table: &irpb.Table{},
			col:   "id",
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fkTargetColumnIsUnique(c.table, c.col)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
