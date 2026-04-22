package compiler_test

import (
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler"
)

// TestNewConfigFromEnv_Defaults — without any COMPILER_* ENV set,
// Config falls back to the documented defaults (./out for the
// output directory). t.Setenv scopes the change to this test so
// the process environment stays clean.
func TestNewConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("COMPILER_OUTPUT_DIR", "")
	cfg := compiler.NewConfigFromEnv()
	if cfg.OutputDir != "./out" {
		t.Errorf("OutputDir default = %q, want ./out", cfg.OutputDir)
	}
}

// TestNewConfigFromEnv_Override — COMPILER_OUTPUT_DIR from the
// environment wins over the default.
func TestNewConfigFromEnv_Override(t *testing.T) {
	t.Setenv("COMPILER_OUTPUT_DIR", "/tmp/wc-out")
	cfg := compiler.NewConfigFromEnv()
	if cfg.OutputDir != "/tmp/wc-out" {
		t.Errorf("OutputDir = %q, want /tmp/wc-out", cfg.OutputDir)
	}
}
