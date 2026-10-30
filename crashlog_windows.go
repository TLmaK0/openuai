//go:build windows

package main

import "os"

// processAlive reports whether a process with that PID exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
