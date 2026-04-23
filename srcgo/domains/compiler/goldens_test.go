package compiler_test

// Golden-file suite for the end-to-end compile pipeline — AC #5 of
// docs/iteration-1.md. Every subdirectory under testdata/ is a case:
// input.proto is compiled in-memory all the way through loader →
// ir.Build → plan.Diff → emit.Emit(postgres) and the resulting up/down
// SQL are byte-compared against expected.up.sql / expected.down.sql.
//
// Use `go test -update` to rewrite the expected files after an
// intentional generator change. The updater never touches input.proto
// and never creates new case directories.
//
// The test file lives at the compiler-domain root because Go ignores any
// package inside testdata/; it uses the external _test package so it
// depends only on the public surface of the child packages.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/plan"
)

// -update regenerates expected.{up,down}.sql for every case. Kept
// package-local — `go test -update ./srcgo/domains/compiler` is the
// intended invocation.
var updateGoldens = flag.Bool("update", false, "rewrite expected.{up,down}.sql files from current pipeline output")

// Resolves to the repo's proto/ root from this file's location:
// srcgo/domains/compiler/ → ../../../proto.
const protoImportRoot = "../../../proto"

func TestGoldens(t *testing.T) {
	cases, err := discoverCases("testdata")
	if err != nil {
		t.Fatalf("discover cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no golden cases found under testdata/ — expected at least product, no_indexes, multi_unique")
	}

	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			dir := filepath.Join("testdata", c)
			up, down := runPipeline(t, dir)

			upPath := filepath.Join(dir, "expected.up.sql")
			downPath := filepath.Join(dir, "expected.down.sql")

			if *updateGoldens {
				writeFile(t, upPath, up)
				writeFile(t, downPath, down)
				t.Logf("updated goldens in %s", dir)
				return
			}

			if got, want := up, readFile(t, upPath); got != want {
				t.Errorf("up SQL mismatch in %s\n--- got ---\n%s--- want ---\n%s", dir, got, want)
			}
			if got, want := down, readFile(t, downPath); got != want {
				t.Errorf("down SQL mismatch in %s\n--- got ---\n%s--- want ---\n%s", dir, got, want)
			}

			// AC #4: determinism — the same pipeline run twice must be
			// byte-identical. Catches any accidental map iteration or
			// random ordering introduced between milestones.
			up2, down2 := runPipeline(t, dir)
			if up2 != up || down2 != down {
				t.Errorf("non-deterministic output across two runs in %s", dir)
			}
		})
	}
}

func discoverCases(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip alter-diff fixture roots — they have a different
		// shape (prev.proto + curr.proto, not input.proto) and
		// are owned by goldens_alter_test's discovery pass.
		if e.Name() == "alter" || e.Name() == "alter_refuse" {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

func runPipeline(t *testing.T, caseDir string) (up, down string) {
	t.Helper()
	lf, err := loader.Load(context.Background(), "input.proto", []string{caseDir, protoImportRoot})
	if err != nil {
		t.Fatalf("loader.Load: %v", err)
	}
	schema, err := ir.Build(lf)
	if err != nil {
		t.Fatalf("ir.Build: %v", err)
	}
	p, err := plan.Diff(nil, schema)
	if err != nil {
		t.Fatalf("plan.Diff: %v", err)
	}
	up, down, err = emit.Emit(postgres.Emitter{}, p)
	if err != nil {
		t.Fatalf("emit.Emit: %v", err)
	}
	return up, down
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run `go test -update` to bootstrap missing goldens)", path, err)
	}
	// Goldens are stored with trailing newline for readability; the
	// emitter already appends one, so the comparison is direct. Guard
	// against a stray CRLF sneaking in via editor settings.
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
