// Package e2e is the classifier-matrix end-to-end test harness.
// Each cell in docs/classification/{carrier,dbtype,constraint}.yaml
// gets exercised against a real Postgres container:
//
//  1. Synthesize (prev, curr) IR schemas that differ only by the
//     axis the cell describes.
//  2. Run engine.Plan with the appropriate Resolution for the
//     cell's Strategy (--decide equivalent).
//  3. Apply prev.up → diff.up → diff.down → prev.down against a
//     fresh database on the target PG version.
//  4. Verify every phase succeeds.
//
// The package is build-tagged `e2e` so `go test ./...` never
// spins up Docker. Run it explicitly:
//
//	go test -tags=e2e ./domains/compiler/e2e/...
//
// Defaults to PG 18 only. Set PG_VERSIONS env var to override,
// matching the Makefile convention:
//
//	PG_VERSIONS="14 15 16 17 18" go test -tags=e2e ./...
//
// See iteration-2.md D33 for the engine machinery this harness
// exercises (cross-carrier strategy rendering).
package e2e
