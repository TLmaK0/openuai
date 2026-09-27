//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// startDetached runs the relaunch loop in its own session so it survives this
// process exiting. It waits for this PID to exit before launching the app.
func startDetached(script, target, logPath string) error {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()

	cmd := exec.Command("bash", script, "--wait-pid", strconv.Itoa(os.Getpid()), "--worktree", target)
	cmd.Dir = target
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
