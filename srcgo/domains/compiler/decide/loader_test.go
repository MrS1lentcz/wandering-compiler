package decide_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/decide"
)

// TestDefaultSQLLoader_ReadsFile — the production filesystem loader
// that resolves `--decide foo=custom:<path>` against the OS. The rest
// of the Parse flow uses test-injected loaders, so this was the one
// function at 0% coverage pre-sweep.
func TestDefaultSQLLoader_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cust.sql")
	body := "UPDATE users SET email = lower(email);\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := decide.DefaultSQLLoader(path)
	if err != nil {
		t.Fatalf("DefaultSQLLoader: %v", err)
	}
	if got != body {
		t.Errorf("loader got %q, want %q", got, body)
	}
}

// TestDefaultSQLLoader_MissingFileSurfacesError — the loader must
// propagate filesystem errors verbatim so `wc generate` exits
// non-zero with a useful message when a --decide path typo occurs.
func TestDefaultSQLLoader_MissingFileSurfacesError(t *testing.T) {
	_, err := decide.DefaultSQLLoader(filepath.Join(t.TempDir(), "nope.sql"))
	if err == nil {
		t.Fatal("missing file should error")
	}
}
