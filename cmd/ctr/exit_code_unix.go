//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// exitCodeOf maps a finished child to the shell convention: signaled children
// exit with 128+signal, everything else uses the child's own code.
func exitCodeOf(ee *exec.ExitError) int {
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
