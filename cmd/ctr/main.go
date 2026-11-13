// Command ctr is the short alias for creator: same program.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	if os.Getenv("CREATOR_CTR_EXEC") == "1" {
		fmt.Fprintln(os.Stderr, "ctr: recursion guard hit — `creator` on PATH is this same ctr binary")
		os.Exit(1)
	}
	path, err := exec.LookPath("creator")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ctr: creator not found on PATH — run `creator install` first")
		os.Exit(1)
	}
	if sameFile(path) {
		fmt.Fprintln(os.Stderr, "ctr: refusing to exec myself — put the real creator binary on PATH, or run `creator install`")
		os.Exit(1)
	}
	cmd := exec.Command(path, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "CREATOR_CTR_EXEC=1")
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// A signaled child reports ExitCode() == -1; map to 128+signal
			// instead of wrapping to 255.
			os.Exit(exitCodeOf(ee))
		}
		fmt.Fprintln(os.Stderr, "ctr:", err)
		os.Exit(1)
	}
}

func sameFile(path string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	selfResolved := resolvePath(self)
	targetResolved := resolvePath(path)
	if selfResolved == targetResolved {
		return true
	}
	selfInfo, err := os.Stat(selfResolved)
	if err != nil {
		return false
	}
	targetInfo, err := os.Stat(targetResolved)
	if err != nil {
		return false
	}
	return os.SameFile(selfInfo, targetInfo)
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
