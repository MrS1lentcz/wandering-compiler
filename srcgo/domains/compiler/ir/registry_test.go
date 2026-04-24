package ir

// D36 registry unit tests — pin the build-time invariants
// (unique aliases, no shadowing, no native-type collision,
// multi-file project-source rejection).
//
// Uses synthetic LoadedFile + proto messages directly; no .proto
// parsing dependency so the tests stay fast and deterministic.

import (
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	pgpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/w17/pg"
)

// mkLoadedFile builds a minimal LoadedFile carrying the given
// project / module options. File descriptor is nil — tests that
// don't go through diag.Atf rendering skip its use.
func mkLoadedFile(project *pgpb.PgProject, module *pgpb.PgModule) *loader.LoadedFile {
	return &loader.LoadedFile{
		PgProject: project,
		PgModule:  module,
	}
}

func TestBuildRegistry_ProjectOnly(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "hstore", SqlType: "HSTORE", RequiredExtensions: []string{"hstore"}},
				{Alias: "vec_1536", SqlType: "vector(1536)", RequiredExtensions: []string{"vector"}},
			},
		}, nil),
	}
	r, err := buildRegistry(files)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	if got, ok := r.Lookup("hstore"); !ok || got.GetSqlType() != "HSTORE" {
		t.Errorf("hstore lookup: got (%v, %v)", got, ok)
	}
	if got, ok := r.Lookup("vec_1536"); !ok || got.GetSqlType() != "vector(1536)" {
		t.Errorf("vec_1536 lookup: got (%v, %v)", got, ok)
	}
	if _, ok := r.Lookup("nonexistent"); ok {
		t.Error("nonexistent should miss")
	}
}

func TestBuildRegistry_ProjectPlusDomain_Merged(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "hstore", SqlType: "HSTORE"},
			},
		}, nil),
		mkLoadedFile(nil, &pgpb.PgModule{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "road_vector", SqlType: "vector(1536)"},
			},
		}),
	}
	r, err := buildRegistry(files)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	if _, ok := r.Lookup("hstore"); !ok {
		t.Error("project-level hstore missing")
	}
	if _, ok := r.Lookup("road_vector"); !ok {
		t.Error("domain-level road_vector missing")
	}
}

func TestBuildRegistry_DuplicateProjectAlias(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "x", SqlType: "TEXT"},
				{Alias: "x", SqlType: "VARCHAR"},
			},
		}, nil),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want duplicate-alias error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate alias") {
		t.Errorf("err should mention duplicate alias, got: %v", err)
	}
}

func TestBuildRegistry_DuplicateDomainAlias(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(nil, &pgpb.PgModule{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "y", SqlType: "TEXT"},
			},
		}),
		mkLoadedFile(nil, &pgpb.PgModule{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "y", SqlType: "VARCHAR"},
			},
		}),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want duplicate-alias error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate alias") {
		t.Errorf("err should mention duplicate alias, got: %v", err)
	}
}

func TestBuildRegistry_ProjectDomainCollision(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{{Alias: "z", SqlType: "TEXT"}},
		}, nil),
		mkLoadedFile(nil, &pgpb.PgModule{
			CustomTypes: []*pgpb.CustomType{{Alias: "z", SqlType: "VARCHAR"}},
		}),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want project/domain collision error, got nil")
	}
	if !strings.Contains(err.Error(), "already declared at project level") {
		t.Errorf("err should mention shadowing, got: %v", err)
	}
}

func TestBuildRegistry_MultipleProjectSources(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{CustomTypes: []*pgpb.CustomType{{Alias: "a", SqlType: "TEXT"}}}, nil),
		mkLoadedFile(&pgpb.PgProject{CustomTypes: []*pgpb.CustomType{{Alias: "b", SqlType: "TEXT"}}}, nil),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want multiple-project error, got nil")
	}
	if !strings.Contains(err.Error(), "only one project options file is allowed") {
		t.Errorf("err should reject multiple project sources, got: %v", err)
	}
}

func TestBuildRegistry_ReservedAlias(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{{Alias: "text", SqlType: "TEXT"}},
		}, nil),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want reserved-alias error, got nil")
	}
	if !strings.Contains(err.Error(), "native PG type keyword") {
		t.Errorf("err should mention reserved, got: %v", err)
	}
}

func TestBuildRegistry_EmptyAlias(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{{Alias: "", SqlType: "TEXT"}},
		}, nil),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want empty-alias error, got nil")
	}
}

func TestBuildRegistry_EmptySqlType(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{{Alias: "x", SqlType: ""}},
		}, nil),
	}
	_, err := buildRegistry(files)
	if err == nil {
		t.Fatal("want empty-sql_type error, got nil")
	}
	if !strings.Contains(err.Error(), "sql_type is empty") {
		t.Errorf("err should mention empty sql_type, got: %v", err)
	}
}

func TestRegistry_AllDeterministic(t *testing.T) {
	files := []*loader.LoadedFile{
		mkLoadedFile(&pgpb.PgProject{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "b", SqlType: "TEXT"},
				{Alias: "a", SqlType: "TEXT"},
			},
		}, &pgpb.PgModule{
			CustomTypes: []*pgpb.CustomType{
				{Alias: "d", SqlType: "TEXT"},
				{Alias: "c", SqlType: "TEXT"},
			},
		}),
	}
	r, err := buildRegistry(files)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	all := r.All()
	if len(all) != 4 {
		t.Fatalf("want 4 entries, got %d", len(all))
	}
	// Project entries first (sorted), then domain (sorted).
	wantOrder := []string{"a", "b", "c", "d"}
	for i, want := range wantOrder {
		if all[i].GetAlias() != want {
			t.Errorf("All()[%d] = %q, want %q (full: %v)", i, all[i].GetAlias(), want,
				func() []string {
					out := make([]string, len(all))
					for i, e := range all {
						out[i] = e.GetAlias()
					}
					return out
				}())
		}
	}
}

func TestRegistry_NilSafe(t *testing.T) {
	var r *CustomTypeRegistry
	if _, ok := r.Lookup("anything"); ok {
		t.Error("nil registry lookup should return false")
	}
	if got := r.All(); got != nil {
		t.Error("nil registry All() should return nil")
	}
}
