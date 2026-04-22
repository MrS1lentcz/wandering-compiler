package writer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrS1lentcz/wandering-compiler/srcgo/domains/compiler/writer"
)

const (
	upSQL   = "CREATE TABLE t (id BIGINT PRIMARY KEY);\n"
	downSQL = "DROP TABLE IF EXISTS t;\n"
)

func TestWriteHappyPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "migrations")
	up, down, err := writer.Write(dir, "20260421T143015Z", upSQL, downSQL)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if up != filepath.Join(dir, "20260421T143015Z.up.sql") {
		t.Errorf("up path = %q", up)
	}
	if down != filepath.Join(dir, "20260421T143015Z.down.sql") {
		t.Errorf("down path = %q", down)
	}
	assertFile(t, up, upSQL)
	assertFile(t, down, downSQL)
}

// MkdirAll creates missing parents. The CLI runs against a fresh repo
// where `out/migrations/` doesn't exist yet — this catches a regression
// that would force users to create the dir by hand.
func TestWriteCreatesMissingParents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "migrations", "nested", "deep")
	_, _, err := writer.Write(dir, "20260421T143015Z", upSQL, downSQL)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

// Overwriting an existing pair is a no-ceremony replace. Useful for
// manual re-runs during development.
func TestWriteOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := writer.Write(dir, "X", "old up", "old down"); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if _, _, err := writer.Write(dir, "X", upSQL, downSQL); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	assertFile(t, filepath.Join(dir, "X.up.sql"), upSQL)
	assertFile(t, filepath.Join(dir, "X.down.sql"), downSQL)
}

// Path-traversal guards — a basename carrying "/" or ".." could otherwise
// write outside dir.
func TestWriteRejectsTraversal(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"leading slash": "/absolute",
		"inner slash":   "foo/bar",
		"double-dot":    "../escape",
	}
	for name, basename := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := writer.Write(t.TempDir(), basename, upSQL, downSQL)
			if err == nil {
				t.Errorf("basename %q: expected error, got nil", basename)
			}
		})
	}
}

// Empty SQL bodies are a bug in the emitter, not a valid migration —
// surface it at write time rather than producing a zero-byte .sql file.
func TestWriteRejectsEmptyBodies(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := writer.Write(dir, "X", "", downSQL); err == nil {
		t.Error("empty up: expected error")
	}
	if _, _, err := writer.Write(dir, "X", upSQL, ""); err == nil {
		t.Error("empty down: expected error")
	}
}

// Empty basename is a distinct error path from path-separator rejection
// — the two guards fire on different invariants.
func TestWriteRejectsEmptyBasename(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := writer.Write(dir, "", upSQL, downSQL); err == nil {
		t.Error("empty basename: expected error")
	}
}

// MkdirAll fails when a plain file already exists at the intended dir
// path. Exercises the mkdir error-return branch.
func TestWriteMkdirFails(t *testing.T) {
	parent := t.TempDir()
	// Create a regular file at the path we'll ask Write to MkdirAll.
	blocking := filepath.Join(parent, "blocking")
	if err := os.WriteFile(blocking, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}
	_, _, err := writer.Write(blocking, "X", upSQL, downSQL)
	if err == nil {
		t.Fatal("MkdirAll should have failed when a file blocks the path")
	}
	if !strings.Contains(err.Error(), "writer: mkdir") {
		t.Errorf("error missing writer prefix: %v", err)
	}
}

// WriteFile fails when a directory already exists at the intended file
// path. Pre-create the up file as a directory so os.WriteFile can't
// succeed. Covers the "writer: write <path>" error branch.
func TestWriteWriteFails(t *testing.T) {
	dir := t.TempDir()
	// Pre-create a directory named as the .up.sql file. os.WriteFile
	// then can't write to it (EISDIR).
	if err := os.MkdirAll(filepath.Join(dir, "X.up.sql"), 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	_, _, err := writer.Write(dir, "X", upSQL, downSQL)
	if err == nil {
		t.Fatal("expected write error when a directory blocks the up.sql path")
	}
	if !strings.Contains(err.Error(), "writer: write") {
		t.Errorf("error missing writer prefix: %v", err)
	}
}

// Two sequential writes into the same dir produce byte-identical files —
// AC #4 (byte-identical SQL content).
func TestWriteDeterministic(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if _, _, err := writer.Write(a, "X", upSQL, downSQL); err != nil {
		t.Fatalf("Write a: %v", err)
	}
	if _, _, err := writer.Write(b, "X", upSQL, downSQL); err != nil {
		t.Fatalf("Write b: %v", err)
	}
	aUp, _ := os.ReadFile(filepath.Join(a, "X.up.sql"))
	bUp, _ := os.ReadFile(filepath.Join(b, "X.up.sql"))
	if string(aUp) != string(bUp) {
		t.Error("up SQL differs across writes")
	}
}

// ---- helpers ----

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s:\n got:%q\nwant:%q", path, strings.TrimRight(string(got), "\n"), strings.TrimRight(want, "\n"))
	}
}
