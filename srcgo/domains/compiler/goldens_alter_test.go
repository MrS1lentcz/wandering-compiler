package compiler_test

// Golden-file suite for the alter-diff path — AC #2 of iter-2.md M1.
// Every subdirectory under testdata/alter/ is a case: prev.proto +
// curr.proto compile through loader → ir.Build (twice), then
// plan.Diff(prev, curr) → emit.Emit(postgres) and the resulting up/down
// SQL are byte-compared against expected.up.sql / expected.down.sql.
//
// REFUSE fixtures live under testdata/alter_refuse/<name>/ — same
// shape but with no expected SQL, asserting Diff returns a non-nil
// error.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/emit/postgres"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/ir"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/loader"
	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/plan"
	irpb "github.com/MrS1lentcz/wandering-compiler/srcgo/pb/domains/compiler/types/ir"
)

const alterTestRoot = "testdata/alter"
const refuseTestRoot = "testdata/alter_refuse"

func TestAlterGoldens(t *testing.T) {
	if _, err := os.Stat(alterTestRoot); os.IsNotExist(err) {
		t.Skip("no alter fixtures directory yet")
	}
	cases, err := discoverCases(alterTestRoot)
	if err != nil {
		t.Fatalf("discover cases: %v", err)
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			dir := filepath.Join(alterTestRoot, c)
			up, down := runAlterPipeline(t, dir)

			upPath := filepath.Join(dir, "expected.up.sql")
			downPath := filepath.Join(dir, "expected.down.sql")

			if *updateGoldens {
				writeFile(t, upPath, up)
				writeFile(t, downPath, down)
				t.Logf("updated alter goldens in %s", dir)
				return
			}

			if got, want := up, readFile(t, upPath); got != want {
				t.Errorf("up SQL mismatch in %s\n--- got ---\n%s--- want ---\n%s", dir, got, want)
			}
			if got, want := down, readFile(t, downPath); got != want {
				t.Errorf("down SQL mismatch in %s\n--- got ---\n%s--- want ---\n%s", dir, got, want)
			}

			up2, down2 := runAlterPipeline(t, dir)
			if up2 != up || down2 != down {
				t.Errorf("non-deterministic alter output in %s", dir)
			}
		})
	}
}

func TestAlterRefuseFixtures(t *testing.T) {
	if _, err := os.Stat(refuseTestRoot); os.IsNotExist(err) {
		t.Skip("no alter_refuse fixtures directory yet")
	}
	cases, err := discoverCases(refuseTestRoot)
	if err != nil {
		t.Fatalf("discover cases: %v", err)
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			dir := filepath.Join(refuseTestRoot, c)
			prev := mustBuild(t, dir, "prev.proto")
			curr := mustBuild(t, dir, "curr.proto")
			_, err := plan.Diff(prev, curr)
			if err == nil {
				t.Fatalf("Diff accepted REFUSE-strategy change in %s; want error", dir)
			}
		})
	}
}

func runAlterPipeline(t *testing.T, dir string) (up, down string) {
	t.Helper()
	prev := mustBuild(t, dir, "prev.proto")
	curr := mustBuild(t, dir, "curr.proto")
	p, err := plan.Diff(prev, curr)
	if err != nil {
		t.Fatalf("plan.Diff: %v", err)
	}
	up, down, err = emit.Emit(postgres.Emitter{}, p)
	if err != nil {
		t.Fatalf("emit.Emit: %v", err)
	}
	return up, down
}

func mustBuild(t *testing.T, dir, file string) *irpb.Schema {
	t.Helper()
	lf, err := loader.Load(context.Background(), file, []string{dir, protoImportRoot})
	if err != nil {
		t.Fatalf("loader.Load %s/%s: %v", dir, file, err)
	}
	s, err := ir.Build(lf)
	if err != nil {
		t.Fatalf("ir.Build %s/%s: %v", dir, file, err)
	}
	return s
}
