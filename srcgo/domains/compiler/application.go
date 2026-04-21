// Package compiler is the domain root for the wandering-compiler — the CLI
// toolchain that turns annotated .proto schemas into dialect-aware SQL
// migrations (and, in later iterations, gRPC stubs, gateways, clients,
// admin, …).
//
// This file follows the project's Go domain convention (see
// docs/conventions-global/go.md — §Domain Structure): every domain exposes
// an Application interface and a Config struct at its root, even when —
// as in iteration-1 — there is essentially nothing wired. Keeping the
// shape in place from day one means later iterations that need real
// modules (platform gRPC client, dialect plug-ins, cache, …) have a
// canonical place to land without a layout churn.
package compiler

// OutputModule resolves the default directory where generated artifacts
// land. Per-run overrides (e.g. `wc generate --out ./elsewhere`) are
// runtime state and do not belong on Application — they are applied by
// the caller before reaching the module.
type OutputModule interface {
	OutputDir() string
}

// Application composes every startup-wired module of the compiler domain.
// Iteration-1 ships with only OutputModule; gRPC clients (to the hosted
// platform), dialect plug-ins, and a build cache will appear as new
// embedded interfaces as later iterations bring them online.
type Application interface {
	OutputModule

	// Config returns the process-wide, ENV-derived configuration snapshot.
	Config() Config
}
