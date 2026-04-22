package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateCmd_RunHappy wires the full CLI pipeline end-to-end
// (loader → ir.Build → plan.Diff → emit.Emit(postgres) → naming →
// writer) against the checked-in happy fixture. Exercises the
// default-Out branch (falls through to Config's OutputDir) and
// the Stat-then-Load sequencing that gives a clean "file not
// found" error on typos.
func TestGenerateCmd_RunHappy(t *testing.T) {
	outDir := t.TempDir()
	t.Setenv("COMPILER_OUTPUT_DIR", outDir)

	cmd := &GenerateCmd{
		Iteration1: true,
		Imports:    []string{"../../../../../proto"},
		Protos:     []string{"../../examples/iteration-1/happy.proto"},
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	migrations, err := filepath.Glob(filepath.Join(outDir, "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(migrations) != 2 {
		t.Errorf("got %d migration files, want 2 (up + down); %v", len(migrations), migrations)
	}
}

// TestGenerateCmd_RequireIterationFlag documents the defensive
// guard against the --iteration-1 flag silently disappearing
// (kong enforces `required:""` today, but if the surface reshapes
// into a mode enum the guard still catches unset invocations).
func TestGenerateCmd_RequireIterationFlag(t *testing.T) {
	cmd := &GenerateCmd{Iteration1: false, Protos: []string{"x.proto"}}
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected error when --iteration-1 is unset, got nil")
	}
	if !strings.Contains(err.Error(), "--iteration-1 is required") {
		t.Errorf("error %q missing --iteration-1 guard", err.Error())
	}
}

// TestGenerateCmd_MultiProtoRejected — iter-1 compiles exactly one
// proto per run. Multi-file lands in iter-2.
func TestGenerateCmd_MultiProtoRejected(t *testing.T) {
	cmd := &GenerateCmd{
		Iteration1: true,
		Protos:     []string{"a.proto", "b.proto"},
	}
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected error on multi-proto input, got nil")
	}
	if !strings.Contains(err.Error(), "exactly one .proto") {
		t.Errorf("error %q missing multi-file guard", err.Error())
	}
}

// TestGenerateCmd_MissingFile — stat-first sequencing: a typo
// surfaces as a clean "no such file" before protocompile's
// import-resolution cascade obscures the root cause.
func TestGenerateCmd_MissingFile(t *testing.T) {
	outDir := t.TempDir()
	t.Setenv("COMPILER_OUTPUT_DIR", outDir)

	cmd := &GenerateCmd{
		Iteration1: true,
		Protos:     []string{filepath.Join(outDir, "does_not_exist.proto")},
	}
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected error on missing proto, got nil")
	}
	// filepath.Join + os.Stat returns an error that wraps the OS
	// "no such file" — we just assert the file path is surfaced.
	if !strings.Contains(err.Error(), "does_not_exist.proto") {
		t.Errorf("error %q doesn't name the missing file", err.Error())
	}
}

// TestGenerateCmd_ExplicitOutOverridesDefault — the --out flag
// wins over Config.OutputDir when non-empty.
func TestGenerateCmd_ExplicitOutOverridesDefault(t *testing.T) {
	defaultDir := t.TempDir()
	explicitDir := t.TempDir()
	t.Setenv("COMPILER_OUTPUT_DIR", defaultDir)

	cmd := &GenerateCmd{
		Iteration1: true,
		Imports:    []string{"../../../../../proto"},
		Protos:     []string{"../../examples/iteration-1/happy.proto"},
		Out:        explicitDir,
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	explicit, _ := filepath.Glob(filepath.Join(explicitDir, "migrations", "*.sql"))
	if len(explicit) != 2 {
		t.Errorf("explicit --out got %d files, want 2", len(explicit))
	}
	defaults, _ := filepath.Glob(filepath.Join(defaultDir, "migrations", "*.sql"))
	if len(defaults) != 0 {
		t.Errorf("default OUTPUT_DIR unexpectedly used (%d files) — --out didn't win", len(defaults))
	}
}
