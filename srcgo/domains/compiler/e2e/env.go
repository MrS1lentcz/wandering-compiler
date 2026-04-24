//go:build e2e

package e2e

import "os"

// envDefault returns the env var's value or the supplied fallback
// when unset / empty. Small helper so docker.go / harness.go stay
// focused.
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
