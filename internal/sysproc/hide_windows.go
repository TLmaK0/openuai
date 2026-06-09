//go:build windows

package sysproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the Windows CREATE_NO_WINDOW process creation flag. It stops
// a console subprocess (whisper-cli, espeak, nvidia-smi, …) from allocating its
// own console, which otherwise flashes a terminal window when spawned from the
// GUI app.
const createNoWindow = 0x08000000

// HideConsole configures cmd so it runs without popping up a console window on
// Windows. Call it before Start/Run/Output.
func HideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
