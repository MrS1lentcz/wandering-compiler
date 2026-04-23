package compiler

import (
	"log"

	"github.com/caarlos0/env/v11"
)

// Config carries every ENV-derived setting of the compiler domain. ENV
// variable names follow the project-wide convention (see
// docs/conventions-global/tooling.md — §ENV Variables): domain prefix =
// domain name in uppercase + underscore → COMPILER_*.
//
// Iteration-1 has exactly one knob (the default output directory); knobs
// for dialect selection, concurrency, remote-platform credentials, …
// land as later iterations surface them.
type Config struct {
	// OutputDir is where the generator writes artifacts (migrations,
	// generated proto, …) when the caller does not override with a
	// per-run flag. Relative paths are resolved against the CLI's cwd.
	OutputDir string `env:"COMPILER_OUTPUT_DIR" envDefault:"./out"`

	// ClassificationDir is where the classifier loads D28 matrix YAMLs
	// from at startup. Defaults to the repo-root docs/classification
	// tree; override when running the binary outside the repo (copy
	// the YAMLs somewhere stable + point here).
	ClassificationDir string `env:"COMPILER_CLASSIFICATION_DIR" envDefault:"./docs/classification"`
}

// NewConfigFromEnv parses the process environment into a Config. Fatal on
// parse failure — a mis-typed ENV var should surface at startup, not at
// the first use of the offending field.
func NewConfigFromEnv() Config {
	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("compiler: failed to load configuration from environment: %v", err)
	}
	return cfg
}
