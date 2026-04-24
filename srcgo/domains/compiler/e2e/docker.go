//go:build e2e

package e2e

// Docker container lifecycle helpers. One container per PG major
// version, re-used across cells via `CREATE DATABASE ...` for
// per-cell isolation. Tearing down is trap-based so Ctrl-C still
// cleans up.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PGContainer wraps a running postgres:<version>-alpine container.
type PGContainer struct {
	ID      string
	Version string
}

// StartPG launches postgres:<version>-alpine and waits for it to
// become ready. The caller owns cleanup via (*PGContainer).Stop.
// Readiness timeout: 60s (matches Makefile convention).
func StartPG(version string) (*PGContainer, error) {
	out, err := exec.Command("docker", "run", "--rm", "-d",
		"-e", "POSTGRES_PASSWORD=test",
		fmt.Sprintf("postgres:%s-alpine", version)).Output()
	if err != nil {
		return nil, fmt.Errorf("docker run postgres:%s: %w", version, err)
	}
	cid := strings.TrimSpace(string(out))
	c := &PGContainer{ID: cid, Version: version}
	if err := c.waitReady(); err != nil {
		c.Stop()
		return nil, err
	}
	return c, nil
}

// Stop kills the container. Idempotent: swallowed errors on already-
// dead containers.
func (c *PGContainer) Stop() {
	if c == nil || c.ID == "" {
		return
	}
	_ = exec.Command("docker", "kill", c.ID).Run()
}

func (c *PGContainer) waitReady() error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		err := exec.Command("docker", "exec", c.ID,
			"pg_isready", "-U", "postgres", "-q").Run()
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres:%s did not become ready in 60s", c.Version)
}

// CreateDB creates a fresh database + preinstalls the extensions
// every fixture may reference. Idempotent on the DB name via
// DROP+CREATE so re-runs don't leave stale state.
func (c *PGContainer) CreateDB(name string) error {
	_ = c.exec("postgres", fmt.Sprintf("DROP DATABASE IF EXISTS %s;", name))
	if err := c.exec("postgres", fmt.Sprintf("CREATE DATABASE %s;", name)); err != nil {
		return err
	}
	for _, ext := range []string{"hstore", "citext", "pg_trgm"} {
		if err := c.exec(name, fmt.Sprintf("CREATE EXTENSION IF NOT EXISTS %s;", ext)); err != nil {
			return fmt.Errorf("extension %s on %s: %w", ext, name, err)
		}
	}
	if err := c.exec(name, "CREATE SCHEMA IF NOT EXISTS reporting;"); err != nil {
		return fmt.Errorf("reporting schema on %s: %w", name, err)
	}
	return nil
}

// Apply streams a SQL string into psql against the named DB.
func (c *PGContainer) Apply(db, sql string) error {
	cmd := exec.Command("docker", "exec", "-i", c.ID,
		"psql", "-U", "postgres", "-d", db, "-v", "ON_ERROR_STOP=1")
	cmd.Stdin = strings.NewReader(sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql apply failed: %w\nstderr: %s\nsql:\n%s",
			err, stderr.String(), sql)
	}
	return nil
}

// exec runs a single SQL statement via psql -c. For DDL we prefer
// Apply (streamed); this is for bootstrap commands like CREATE
// DATABASE / CREATE EXTENSION.
func (c *PGContainer) exec(db, sql string) error {
	cmd := exec.Command("docker", "exec", c.ID,
		"psql", "-U", "postgres", "-d", db,
		"-v", "ON_ERROR_STOP=1", "-c", sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql exec failed: %w\nstderr: %s\nsql: %s",
			err, stderr.String(), sql)
	}
	return nil
}

// pgVersions returns the PG major versions the harness should
// target. Env var PG_VERSIONS overrides the default (PG 18).
func pgVersions() []string {
	v := strings.TrimSpace(envDefault("PG_VERSIONS", "18"))
	return strings.Fields(v)
}
