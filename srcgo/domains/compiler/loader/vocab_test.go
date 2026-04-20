package loader_test

import (
	"context"
	"testing"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	w17pb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17"
	dbpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/db"
	pgpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/pg"
)

// TestW17VocabularyCompiles is M1's acceptance proof: a fixture proto that
// imports every (w17.*) option, loaded via protocompile, with every option
// value read back through the generated extensions. If the proto vocabulary
// is malformed or the Go stubs diverge from it, this test fails before any
// loader/IR code gets written on top.
func TestW17VocabularyCompiles(t *testing.T) {
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			// testdata/ holds the fixture; ../../../../proto holds the w17
			// vocabulary (proto/ lives at the repo root, outside srcgo/).
			ImportPaths: []string{"testdata", "../../../../proto"},
		}),
	}

	files, err := compiler.Compile(context.Background(), "vocab_fixture.proto")
	if err != nil {
		t.Fatalf("compile fixture: %v", err)
	}

	file := files.FindFileByPath("vocab_fixture.proto")
	if file == nil {
		t.Fatal("fixture not found in compiled file set")
	}

	product := file.Messages().ByName("Product")
	if product == nil {
		t.Fatal("message Product not found in fixture")
	}

	// --- (w17.db.table) on the Product message ---
	msgOpts := typedOptions[*descriptorpb.MessageOptions](t, product.Options())
	tableExt := proto.GetExtension(msgOpts, dbpb.E_Table).(*dbpb.Table)
	if got, want := tableExt.GetName(), "products"; got != want {
		t.Errorf("table.name = %q, want %q", got, want)
	}
	if got, want := len(tableExt.GetIndexes()), 3; got != want {
		t.Fatalf("len(table.indexes) = %d, want %d", got, want)
	}
	if got, want := tableExt.GetIndexes()[0].GetFields(), []string{"slug"}; !equalStrings(got, want) {
		t.Errorf("table.indexes[0].fields = %v, want %v", got, want)
	}
	if !tableExt.GetIndexes()[0].GetUnique() {
		t.Error("table.indexes[0].unique = false, want true")
	}
	if got, want := tableExt.GetIndexes()[1].GetFields(), []string{"category_id", "is_active"}; !equalStrings(got, want) {
		t.Errorf("table.indexes[1].fields = %v, want %v", got, want)
	}
	if tableExt.GetIndexes()[1].GetUnique() {
		t.Error("table.indexes[1].unique = true, want false")
	}
	if got, want := tableExt.GetIndexes()[2].GetName(), "products_name_covering_idx"; got != want {
		t.Errorf("table.indexes[2].name = %q, want %q", got, want)
	}
	if got, want := tableExt.GetIndexes()[2].GetInclude(), []string{"price"}; !equalStrings(got, want) {
		t.Errorf("table.indexes[2].include = %v, want %v", got, want)
	}

	// --- (w17.field) on id: int64 ID pk + default_auto IDENTITY ---
	idOpts := fieldOptions(t, product, "id")
	idExt := proto.GetExtension(idOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := idExt.GetType(), w17pb.Type_ID; got != want {
		t.Errorf("id.field.type = %v, want %v", got, want)
	}
	if !idExt.GetPk() {
		t.Error("id.field.pk = false, want true")
	}
	if got, want := idExt.GetDefaultAuto(), w17pb.AutoDefault_IDENTITY; got != want {
		t.Errorf("id.field.default_auto = %v, want %v", got, want)
	}

	// --- (w17.field) on slug: SLUG, max_len=120, unique=true ---
	slugOpts := fieldOptions(t, product, "slug")
	slugExt := proto.GetExtension(slugOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := slugExt.GetType(), w17pb.Type_SLUG; got != want {
		t.Errorf("slug.field.type = %v, want %v", got, want)
	}
	if got, want := slugExt.GetMaxLen(), int32(120); got != want {
		t.Errorf("slug.field.max_len = %d, want %d", got, want)
	}
	if !slugExt.GetUnique() {
		t.Error("slug.field.unique = false, want true")
	}

	// --- (w17.field) on name: CHAR, max_len=255, default_string="unnamed" ---
	nameOpts := fieldOptions(t, product, "name")
	nameExt := proto.GetExtension(nameOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := nameExt.GetType(), w17pb.Type_CHAR; got != want {
		t.Errorf("name.field.type = %v, want %v", got, want)
	}
	if got, want := nameExt.GetDefaultString(), "unnamed"; got != want {
		t.Errorf("name.field.default_string = %q, want %q", got, want)
	}

	// --- (w17.field) on price: MONEY, gte=0 (bounds merged into Field) ---
	priceOpts := fieldOptions(t, product, "price")
	priceExt := proto.GetExtension(priceOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := priceExt.GetType(), w17pb.Type_MONEY; got != want {
		t.Errorf("price.field.type = %v, want %v", got, want)
	}
	if priceExt.Gte == nil {
		t.Error("price.field.gte unset, want present")
	} else if got, want := *priceExt.Gte, float64(0); got != want {
		t.Errorf("price.field.gte = %v, want %v", got, want)
	}

	// --- (w17.field) on tax_rate: DECIMAL, precision=7, scale=4 ---
	taxOpts := fieldOptions(t, product, "tax_rate")
	taxExt := proto.GetExtension(taxOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := taxExt.GetType(), w17pb.Type_DECIMAL; got != want {
		t.Errorf("tax_rate.field.type = %v, want %v", got, want)
	}
	if got, want := taxExt.GetPrecision(), int32(7); got != want {
		t.Errorf("tax_rate.field.precision = %d, want %d", got, want)
	}
	if taxExt.Scale == nil {
		t.Error("tax_rate.field.scale unset, want present")
	} else if got, want := *taxExt.Scale, int32(4); got != want {
		t.Errorf("tax_rate.field.scale = %d, want %d", got, want)
	}

	// --- (w17.field) on discount_rate: RATIO, null=true ---
	drOpts := fieldOptions(t, product, "discount_rate")
	drExt := proto.GetExtension(drOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := drExt.GetType(), w17pb.Type_RATIO; got != want {
		t.Errorf("discount_rate.field.type = %v, want %v", got, want)
	}
	if !drExt.GetNull() {
		t.Error("discount_rate.field.null = false, want true")
	}

	// --- (w17.field) on status: CHAR + choices (cross-enum path) ---
	statusOpts := fieldOptions(t, product, "status")
	statusExt := proto.GetExtension(statusOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := statusExt.GetChoices(), "vocabtest.ProductStatus"; got != want {
		t.Errorf("status.field.choices = %q, want %q", got, want)
	}

	// --- category_id: int64 ID, fk, null:true, orphanable:true; (w17.db.column) index + name ---
	catOpts := fieldOptions(t, product, "category_id")
	catFieldExt := proto.GetExtension(catOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := catFieldExt.GetType(), w17pb.Type_ID; got != want {
		t.Errorf("category_id.field.type = %v, want %v", got, want)
	}
	if got, want := catFieldExt.GetFk(), "categories.id"; got != want {
		t.Errorf("category_id.field.fk = %q, want %q", got, want)
	}
	if !catFieldExt.GetNull() {
		t.Error("category_id.field.null = false, want true")
	}
	if catFieldExt.Orphanable == nil {
		t.Error("category_id.field.orphanable unset, want present (true)")
	} else if !*catFieldExt.Orphanable {
		t.Error("category_id.field.orphanable = false, want true")
	}
	catColExt := proto.GetExtension(catOpts, dbpb.E_Column).(*dbpb.Column)
	if !catColExt.GetIndex() {
		t.Error("category_id.db.column.index = false, want true")
	}
	if got, want := catColExt.GetName(), "cat_id"; got != want {
		t.Errorf("category_id.db.column.name = %q, want %q", got, want)
	}

	// --- stock_qty: COUNTER, default_int=0 ---
	stockOpts := fieldOptions(t, product, "stock_qty")
	stockExt := proto.GetExtension(stockOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := stockExt.GetType(), w17pb.Type_COUNTER; got != want {
		t.Errorf("stock_qty.field.type = %v, want %v", got, want)
	}
	if got, want := stockExt.GetDefaultInt(), int64(0); got != want {
		t.Errorf("stock_qty.field.default_int = %d, want %d", got, want)
	}
	if _, ok := stockExt.GetDefault().(*w17pb.Field_DefaultInt); !ok {
		t.Errorf("stock_qty.field.default branch = %T, want *Field_DefaultInt", stockExt.GetDefault())
	}

	// --- is_active: bool carrier, default_auto TRUE (the bool-default channel) ---
	activeOpts := fieldOptions(t, product, "is_active")
	activeExt := proto.GetExtension(activeOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := activeExt.GetDefaultAuto(), w17pb.AutoDefault_TRUE; got != want {
		t.Errorf("is_active.field.default_auto = %v, want %v", got, want)
	}
	// Carrier type is what the IR builder will read when the field has no
	// semantic type annotation.
	if got, want := product.Fields().ByName("is_active").Kind(), protoreflect.BoolKind; got != want {
		t.Errorf("is_active.Kind() = %v, want %v", got, want)
	}

	// --- created_at: Timestamp carrier, DATETIME, default_auto NOW, immutable ---
	createdOpts := fieldOptions(t, product, "created_at")
	createdExt := proto.GetExtension(createdOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := createdExt.GetType(), w17pb.Type_DATETIME; got != want {
		t.Errorf("created_at.field.type = %v, want %v", got, want)
	}
	if got, want := createdExt.GetDefaultAuto(), w17pb.AutoDefault_NOW; got != want {
		t.Errorf("created_at.field.default_auto = %v, want %v", got, want)
	}
	if !createdExt.GetImmutable() {
		t.Error("created_at.field.immutable = false, want true")
	}

	// --- metadata: (w17.pg.field) jsonb:true (dialect-specific) ---
	metaOpts := fieldOptions(t, product, "metadata")
	metaPgExt := proto.GetExtension(metaOpts, pgpb.E_Field).(*pgpb.PgField)
	if !metaPgExt.GetJsonb() {
		t.Error("metadata.pg.field.jsonb = false, want true")
	}

	// --- embedding: (w17.pg.field) escape hatch — custom_type + required_extensions ---
	embOpts := fieldOptions(t, product, "embedding")
	embPgExt := proto.GetExtension(embOpts, pgpb.E_Field).(*pgpb.PgField)
	if got, want := embPgExt.GetCustomType(), "vector(1536)"; got != want {
		t.Errorf("embedding.pg.field.custom_type = %q, want %q", got, want)
	}
	if got, want := embPgExt.GetRequiredExtensions(), []string{"vector"}; !equalStrings(got, want) {
		t.Errorf("embedding.pg.field.required_extensions = %v, want %v", got, want)
	}
}

// typedOptions re-marshals a dynamic options message through the global
// extension registry so generated w17 extensions are resolvable. protocompile
// returns options as *dynamicpb.Message by default; proto.GetExtension then
// refuses to decode them into the concrete *w17pb.Field etc.
func typedOptions[T proto.Message](t *testing.T, src protoreflect.ProtoMessage) T {
	t.Helper()
	raw, err := proto.Marshal(src)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	var zero T
	dst := zero.ProtoReflect().New().Interface().(T)
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal options with global resolver: %v", err)
	}
	return dst
}

func fieldOptions(t *testing.T, msg protoreflect.MessageDescriptor, name string) *descriptorpb.FieldOptions {
	t.Helper()
	fd := msg.Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("field %q not found on %s", name, msg.Name())
	}
	return typedOptions[*descriptorpb.FieldOptions](t, fd.Options())
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
