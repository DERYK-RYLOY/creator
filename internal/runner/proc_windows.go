//go:build windows

package runner

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.WaitDelay = 5 * time.Second
}

func killTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	// taskkill /T walks children, which os.Process.Kill cannot do.
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err != nil {
		return p.Kill()
	}
	return nil
}
