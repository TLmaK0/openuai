//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the command in its own process group so the whole tree
// (the shell plus any children it spawns) can be killed together.
func setProcGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcGroup SIGKILLs the entire process group led by cmd. Killing the
// group (negative PID) is what stops grandchildren that would otherwise keep
// the output pipe open and hang cmd.Wait().
func killProcGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
