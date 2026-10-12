//go:build windows

package procs

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// signalTree kills the process and all its children. Windows has no polite
// signal for console programs started this way, so both modes are forceful.
func signalTree(p *os.Process, graceful bool) {
	if p == nil {
		return
	}
	c := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = c.Run()
	_ = p.Kill()
}
