//go:build !windows

package procs

import (
	"os"
	"syscall"
)

// signalTree signals the process and its whole session/process group (the
// process is started with setsid, so its PID is the group ID).
func signalTree(p *os.Process, graceful bool) {
	if p == nil {
		return
	}
	sig := syscall.SIGKILL
	if graceful {
		sig = syscall.SIGTERM
	}
	_ = syscall.Kill(-p.Pid, sig)
	_ = p.Signal(sig)
}
