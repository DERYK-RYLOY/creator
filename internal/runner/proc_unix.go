//go:build !windows

package runner

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProc(cmd *exec.Cmd) {
	// Own process group so a timeout can kill the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
}

func killTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
	return p.Kill()
}
