//go:build !windows

package sysproc

import "os/exec"

// HideConsole is a no-op on non-Windows platforms, where spawning a CLI
// subprocess does not create a window.
func HideConsole(cmd *exec.Cmd) {}
