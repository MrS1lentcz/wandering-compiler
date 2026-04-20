// Package loader parses a single .proto file via bufbuild/protocompile,
// decodes every (w17.*) option into its concrete Go type, and returns the
// bundle as *LoadedFile. It performs no semantic validation: the IR
// builder (ir.Build) is the gatekeeper.
//
// Why separate from ir.Build: the loader answers "is this a well-formed
// proto that uses our option vocabulary?" with protocompile's parser.
// ir.Build answers "does the authored schema satisfy the invariants in
// docs/iteration-1.md D2/D7/D8?" with a different error vocabulary. Keeping
// them apart lets each emit narrowly-scoped diagnostics.
package loader

import (
	"context"
	"fmt"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/diag"
	w17pb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17"
	dbpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/db"
	pgpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/pg"
)

// LoadedFile is one parsed .proto plus every decoded option relevant to
// the compiler. The raw FileDescriptor is kept alongside for source-
// location lookups by diag.At.
type LoadedFile struct {
	File     protoreflect.FileDescriptor
	Messages []*LoadedMessage
}

// LoadedMessage is one top-level proto message with its decoded
// (w17.db.table) option (nil if the message lacks the annotation).
type LoadedMessage struct {
	Desc   protoreflect.MessageDescriptor
	Table  *dbpb.Table
	Fields []*LoadedField
}

// LoadedField carries each of the four supported field-level option
// payloads. Any may be nil when the corresponding annotation was not
// applied.
type LoadedField struct {
	Desc    protoreflect.FieldDescriptor
	Field   *w17pb.Field
	Column  *dbpb.Column
	PgField *pgpb.PgField
}

// Load parses one .proto file. importPaths are passed verbatim to
// protocompile; callers are expected to include at least a path that
// resolves the w17/*.proto vocabulary (typically the repo's ./proto
// directory).
//
// Errors from Load surface to the CLI user — keep them readable.
func Load(ctx context.Context, path string, importPaths []string) (*LoadedFile, error) {
	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: importPaths,
		}),
		// Enable source info so diag.At can map descriptors back to
		// file:line:col for user-facing errors.
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := compiler.Compile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	file := files.FindFileByPath(path)
	if file == nil {
		return nil, &diag.Error{
			File: path,
			Msg:  "file not found in compiled set after parse",
			Why:  "protocompile accepted the input but did not surface it by path — usually a symlink/relative-path mismatch",
			Fix:  "pass an absolute path to wc, or a path relative to the current working directory without symlinks",
		}
	}

	loaded := &LoadedFile{File: file}
	msgs := file.Messages()
	for i := 0; i < msgs.Len(); i++ {
		md := msgs.Get(i)
		lm := &LoadedMessage{Desc: md}

		msgOpts, err := reparse[*descriptorpb.MessageOptions](md.Options())
		if err != nil {
			return nil, diag.Atf(md, "internal: re-marshal message options: %v", err).
				WithWhy("protocompile returned options as dynamicpb; we re-marshal through the global registry to decode concrete extensions").
				WithFix("this is a compiler bug — please file an issue with the failing .proto attached")
		}
		if proto.HasExtension(msgOpts, dbpb.E_Table) {
			lm.Table = proto.GetExtension(msgOpts, dbpb.E_Table).(*dbpb.Table)
		}

		fields := md.Fields()
		for j := 0; j < fields.Len(); j++ {
			fd := fields.Get(j)
			lf := &LoadedField{Desc: fd}

			fOpts, err := reparse[*descriptorpb.FieldOptions](fd.Options())
			if err != nil {
				return nil, diag.Atf(fd, "internal: re-marshal field options: %v", err).
					WithWhy("protocompile returned options as dynamicpb; we re-marshal through the global registry to decode concrete extensions").
					WithFix("this is a compiler bug — please file an issue with the failing .proto attached")
			}
			if proto.HasExtension(fOpts, w17pb.E_Field) {
				lf.Field = proto.GetExtension(fOpts, w17pb.E_Field).(*w17pb.Field)
			}
			if proto.HasExtension(fOpts, dbpb.E_Column) {
				lf.Column = proto.GetExtension(fOpts, dbpb.E_Column).(*dbpb.Column)
			}
			if proto.HasExtension(fOpts, pgpb.E_Field) {
				lf.PgField = proto.GetExtension(fOpts, pgpb.E_Field).(*pgpb.PgField)
			}

			lm.Fields = append(lm.Fields, lf)
		}

		loaded.Messages = append(loaded.Messages, lm)
	}

	return loaded, nil
}

// reparse re-marshals a dynamicpb options message through the global
// extension registry so generated extensions (w17.*) can be decoded with
// proto.GetExtension. Without this step GetExtension returns the raw
// unknown-fields bytes and panics on type-assert.
func reparse[T proto.Message](src protoreflect.ProtoMessage) (T, error) {
	var zero T
	raw, err := proto.Marshal(src)
	if err != nil {
		return zero, err
	}
	dst := zero.ProtoReflect().New().Interface().(T)
	if err := (proto.UnmarshalOptions{Resolver: protoregistry.GlobalTypes}).Unmarshal(raw, dst); err != nil {
		return zero, err
	}
	return dst, nil
}
