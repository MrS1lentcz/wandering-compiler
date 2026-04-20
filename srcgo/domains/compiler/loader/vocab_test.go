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
	if got, want := len(tableExt.GetIndexes()), 2; got != want {
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

	// --- (w17.field) on slug: type=SLUG, max_len=120 ---
	slugOpts := fieldOptions(t, product, "slug")
	slugExt := proto.GetExtension(slugOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := slugExt.GetType(), w17pb.Type_SLUG; got != want {
		t.Errorf("slug.field.type = %v, want %v", got, want)
	}
	if got, want := slugExt.GetMaxLen(), int32(120); got != want {
		t.Errorf("slug.field.max_len = %d, want %d", got, want)
	}

	// --- (w17.field) + (w17.validate) on price: MONEY, gte=0 ---
	priceOpts := fieldOptions(t, product, "price")
	priceFieldExt := proto.GetExtension(priceOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := priceFieldExt.GetType(), w17pb.Type_MONEY; got != want {
		t.Errorf("price.field.type = %v, want %v", got, want)
	}
	priceValExt := proto.GetExtension(priceOpts, w17pb.E_Validate).(*w17pb.Validate)
	if priceValExt.Gte == nil {
		t.Error("price.validate.gte unset, want present")
	} else if got, want := *priceValExt.Gte, float64(0); got != want {
		t.Errorf("price.validate.gte = %v, want %v", got, want)
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

	// --- (w17.field) on category_id: UUID + fk="categories.id" ---
	catOpts := fieldOptions(t, product, "category_id")
	catExt := proto.GetExtension(catOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := catExt.GetType(), w17pb.Type_UUID; got != want {
		t.Errorf("category_id.field.type = %v, want %v", got, want)
	}
	if got, want := catExt.GetFk(), "categories.id"; got != want {
		t.Errorf("category_id.field.fk = %q, want %q", got, want)
	}

	// --- (w17.field) on stock_qty: COUNTER ---
	stockOpts := fieldOptions(t, product, "stock_qty")
	stockExt := proto.GetExtension(stockOpts, w17pb.E_Field).(*w17pb.Field)
	if got, want := stockExt.GetType(), w17pb.Type_COUNTER; got != want {
		t.Errorf("stock_qty.field.type = %v, want %v", got, want)
	}

	// --- bool carrier with no (w17.field): presence check is explicit ---
	activeField := product.Fields().ByName("is_active")
	activeOpts := typedOptions[*descriptorpb.FieldOptions](t, activeField.Options())
	if proto.HasExtension(activeOpts, w17pb.E_Field) {
		t.Error("is_active unexpectedly has (w17.field) set")
	}
	// Carrier type is what the IR builder will read when the field has no
	// semantic type annotation.
	if got, want := activeField.Kind(), protoreflect.BoolKind; got != want {
		t.Errorf("is_active.Kind() = %v, want %v", got, want)
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
