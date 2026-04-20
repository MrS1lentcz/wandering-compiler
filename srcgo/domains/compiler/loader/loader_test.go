package loader_test

import (
	"context"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
)

// TestLoadVocabFixture exercises the public Load API on the same fixture
// the M1 vocab test uses. It asserts the LoadedFile shape without
// re-validating every decoded option (the vocab test already covers the
// extension round-trip surface).
func TestLoadVocabFixture(t *testing.T) {
	lf, err := loader.Load(
		context.Background(),
		"vocab_fixture.proto",
		[]string{"testdata", "../../../../proto"},
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(lf.Messages), 1; got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
	product := lf.Messages[0]
	if got, want := string(product.Desc.Name()), "Product"; got != want {
		t.Errorf("message name = %q, want %q", got, want)
	}
	if product.Table == nil {
		t.Fatal("product.Table nil, want populated (w17.db.table)")
	}
	if got, want := product.Table.GetName(), "products"; got != want {
		t.Errorf("table.name = %q, want %q", got, want)
	}

	// Spot-check a couple of fields — full per-option coverage lives in
	// vocab_test.go.
	var slug, metadata *loader.LoadedField
	for _, f := range product.Fields {
		switch string(f.Desc.Name()) {
		case "slug":
			slug = f
		case "metadata":
			metadata = f
		}
	}
	if slug == nil || slug.Field == nil {
		t.Fatal("slug field or its (w17.field) option missing")
	}
	if !slug.Field.GetUnique() {
		t.Error("slug.Field.unique = false, want true")
	}
	if metadata == nil || metadata.PgField == nil {
		t.Fatal("metadata field or its (w17.pg.field) option missing")
	}
	if !metadata.PgField.GetJsonb() {
		t.Error("metadata.PgField.jsonb = false, want true")
	}
}
