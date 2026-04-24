package compiler_test

// Manifest golden — engine.Plan is run against each fixture carrying
// an `expected.manifest.json` file, and the Manifest that
// FilesystemSink would write is compared byte-for-byte against that
// golden. Opt-in per fixture: fixtures without the file are skipped.
//
// Why opt-in instead of adding a manifest golden to every fixture:
// most fixtures don't carry interesting capabilities (plain INTEGER
// columns produce only TRANSACTIONAL_DDL). The manifest is only a
// useful signal on grand-tour-shaped fixtures that exercise multiple
// dialect features + IR-level required_extensions. pg_dialect is the
// exemplar; MySQL's arrival (Layer C) will add a parallel fixture
// under testdata/<mysql-grand-tour>/.
//
// Run `go test -update` to rewrite the expected file after an
// intentional change to cap instrumentation.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/classifier"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/engine"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

// manifestMarshaler mirrors the one in engine/filesystem/sink.go —
// kept in sync so the golden compares the exact byte output a Sink
// would produce. If these drift, the test is lying.
var manifestMarshaler = protojson.MarshalOptions{
	UseProtoNames:   true,
	EmitUnpopulated: false,
	Multiline:       false,
	Indent:          "",
}

func TestManifestGoldens(t *testing.T) {
	cases, err := discoverManifestCases("testdata")
	if err != nil {
		t.Fatalf("discover manifest cases: %v", err)
	}
	if len(cases) == 0 {
		t.Skip("no fixtures carry expected.manifest.json — add one under a grand-tour fixture to lock cap instrumentation")
	}

	cls := testManifestClassifier(t)
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			dir := filepath.Join("testdata", c)
			schema := loadManifestSchema(t, dir)

			plan, err := engine.Plan(nil, schema, cls, nil,
				func(*irpb.Connection) (emit.DialectEmitter, error) {
					return postgres.Emitter{}, nil
				})
			if err != nil {
				t.Fatalf("engine.Plan: %v", err)
			}
			if len(plan.Migrations) != 1 {
				t.Fatalf("expected 1 migration, got %d", len(plan.Migrations))
			}

			got, err := manifestMarshaler.Marshal(plan.Migrations[0].GetManifest())
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Trailing newline parity with FilesystemSink output so
			// goldens match the on-disk artifact byte-for-byte.
			got = append(got, '\n')

			goldenPath := filepath.Join(dir, "expected.manifest.json")
			if *updateGoldens {
				writeFile(t, goldenPath, string(got))
				t.Logf("updated manifest golden %s", goldenPath)
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read %s: %v (run `go test -update` to bootstrap)", goldenPath, err)
			}
			if string(got) != string(want) {
				t.Errorf("manifest mismatch in %s\n--- got ---\n%s--- want ---\n%s", dir, got, want)
			}
		})
	}
}

// discoverManifestCases returns every fixture dir carrying an
// `expected.manifest.json` file. Skips alter / alter_refuse roots
// (different shape — they'd need per-pair manifests and the
// grand-tour already covers alter surfaces via iter-1-style
// initial migrations).
func discoverManifestCases(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "alter" || e.Name() == "alter_refuse" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "expected.manifest.json")); err == nil {
			out = append(out, e.Name())
		} else if *updateGoldens && e.Name() == "pg_dialect" {
			// Special case on -update: bootstrap the canonical golden
			// so a fresh checkout can run `go test -update` once and
			// have the file materialise.
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func loadManifestSchema(t *testing.T, dir string) *irpb.Schema {
	t.Helper()
	single := filepath.Join(dir, "input.proto")
	if _, err := os.Stat(single); err == nil {
		lf, err := loader.Load(context.Background(), "input.proto",
			[]string{dir, protoImportRoot})
		if err != nil {
			t.Fatalf("loader.Load: %v", err)
		}
		schema, err := ir.Build(lf)
		if err != nil {
			t.Fatalf("ir.Build: %v", err)
		}
		return schema
	}
	// Multi-file fixture — gather all .proto files under the dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".proto") {
			paths = append(paths, e.Name())
		}
	}
	files, err := loader.LoadMany(context.Background(), paths,
		[]string{dir, protoImportRoot})
	if err != nil {
		t.Fatalf("loader.LoadMany: %v", err)
	}
	schema, err := ir.BuildMany(files)
	if err != nil {
		t.Fatalf("ir.BuildMany: %v", err)
	}
	return schema
}

// testManifestClassifier loads the production D28 YAMLs from the repo
// root. Duplicated from engine/plan_test.go's helper rather than
// exported — the engine test package is separate.
func testManifestClassifier(t *testing.T) *classifier.Classifier {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "classification")
	c, err := classifier.Load(dir)
	if err != nil {
		t.Fatalf("classifier.Load: %v", err)
	}
	return c
}
