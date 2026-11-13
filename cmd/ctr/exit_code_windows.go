//go:build windows

package main

import (
	"os/exec"
)

// exitCodeOf on Windows: process exit codes are always non-negative there,
// so the child's own code propagates. If somehow negative, fall back to 1.
func exitCodeOf(ee *exec.ExitError) int {
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
