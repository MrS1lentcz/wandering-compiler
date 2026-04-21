// Package writer is the narrow bytes-to-disk layer of the compiler. It
// knows nothing about IR, plans, SQL dialects, or naming — its whole
// contract is "take two strings and a destination, write two files".
// Callers are expected to have already resolved everything upstream.
package writer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Write creates dir (and any missing parents) with 0755, then writes
// <dir>/<basename>.up.sql and <dir>/<basename>.down.sql with the given
// contents at mode 0644. Returns the two absolute paths on success so the
// caller (CLI / later platform client) can report exactly what landed on
// disk.
//
// Guards:
//   - basename must be non-empty and free of path separators or ".." (a
//     path-traversal slip would let a malicious input file hijack writes
//     outside dir). filepath.Base(basename) == basename is the check.
//   - up and down must be non-empty; the emitter always produces both for
//     AddTable ops, and a silently-empty migration is a bug worth surfacing.
func Write(dir, basename, up, down string) (upPath string, downPath string, err error) {
	if basename == "" {
		return "", "", errors.New("writer: basename is empty")
	}
	if filepath.Base(basename) != basename {
		return "", "", fmt.Errorf("writer: basename %q contains a path separator or traversal; expected a bare filename stem", basename)
	}
	if up == "" {
		return "", "", errors.New("writer: up SQL is empty")
	}
	if down == "" {
		return "", "", errors.New("writer: down SQL is empty")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("writer: mkdir %s: %w", dir, err)
	}

	upPath = filepath.Join(dir, basename+".up.sql")
	downPath = filepath.Join(dir, basename+".down.sql")

	if err := os.WriteFile(upPath, []byte(up), 0o644); err != nil {
		return "", "", fmt.Errorf("writer: write %s: %w", upPath, err)
	}
	if err := os.WriteFile(downPath, []byte(down), 0o644); err != nil {
		return "", "", fmt.Errorf("writer: write %s: %w", downPath, err)
	}
	return upPath, downPath, nil
}
