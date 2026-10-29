package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"openuai/internal/procs"
)

// runWithCancel starts cmd in its own process group and waits for it, killing
// the entire group if ctx is cancelled or times out. It returns the command's
// error, or ctx.Err() if the context ended first (so callers can distinguish
// a user abort / timeout from a normal non-zero exit).
func runWithCancel(ctx context.Context, cmd *exec.Cmd) error {
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		killProcGroup(cmd)
		<-done // reap the killed process
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// Bash executes a shell command
type Bash struct {
	WorkDir string
	// Procs, when set, lets the command run in the background (run_in_background).
	Procs *procs.Manager
}

func (t Bash) Definition() Definition {
	return Definition{
		Name:        "bash",
		Description: "Execute a shell command and return its output. Use for system operations, installing packages, running scripts, etc.",
		Parameters: []Parameter{
			{Name: "command", Type: "string", Description: "The shell command to execute", Required: true},
			{Name: "timeout", Type: "string", Description: "Timeout in seconds (default: 30)", Required: false},
			{Name: "run_in_background", Type: "string", Description: "Set to 'true' for builds, tests, installs or any command that may take long: it starts in the background, this call returns at once, and its result arrives later as a notification starting with \"[Background process\". Do not wait for it: answer the user or keep working.", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t Bash) Execute(ctx context.Context, args map[string]string) Result {
	command := args["command"]
	if command == "" {
		return Result{Error: "command is required"}
	}
	if strings.EqualFold(strings.TrimSpace(args["run_in_background"]), "true") {
		if t.Procs == nil {
			return Result{Error: "running in the background is not available here; run it without run_in_background"}
		}
		p, err := t.Procs.Start(command, t.WorkDir)
		if err != nil {
			return Result{Error: err.Error()}
		}
		return Result{Output: fmt.Sprintf("Started in the background as process %s. Do not wait for it: answer the user or continue with other work. "+
			"When it ends you will get a notification starting with \"[Background process %s\" with its final output. "+
			"Use proc_read to check progress and proc_kill to stop it.", p.ID(), p.ID())}
	}

	timeoutSec := 30
	if ts := args["timeout"]; ts != "" {
		fmt.Sscanf(ts, "%d", &timeoutSec)
	}
	if timeoutSec > 300 {
		timeoutSec = 300
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}

	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := runWithCancel(ctx, cmd)

	var output strings.Builder
	if stdout.Len() > 0 {
		output.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("stderr: ")
		output.WriteString(stderr.String())
	}

	// Truncate very long outputs
	result := output.String()
	if len(result) > 50000 {
		result = result[:50000] + "\n... (output truncated)"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Result{Error: "command timed out after " + fmt.Sprintf("%d", timeoutSec) + " seconds"}
		}
		if ctx.Err() == context.Canceled {
			return Result{Error: "command aborted by user"}
		}
		if result == "" {
			return Result{Error: err.Error()}
		}
		return Result{Output: result, Error: "exit: " + err.Error()}
	}

	if result == "" {
		result = "(no output)"
	}
	return Result{Output: result}
}

// BashSudo executes a command with elevated privileges
type BashSudo struct {
	WorkDir string
}

func (t BashSudo) Definition() Definition {
	return Definition{
		Name:        "bash_sudo",
		Description: "Execute a shell command with administrator/root privileges (sudo on Linux/macOS, runas on Windows)",
		Parameters: []Parameter{
			{Name: "command", Type: "string", Description: "The shell command to execute with elevated privileges", Required: true},
			{Name: "timeout", Type: "string", Description: "Timeout in seconds (default: 30)", Required: false},
		},
		RequiresPermission: "always",
	}
}

func (t BashSudo) Execute(ctx context.Context, args map[string]string) Result {
	command := args["command"]
	if command == "" {
		return Result{Error: "command is required"}
	}

	timeoutSec := 30
	if ts := args["timeout"]; ts != "" {
		fmt.Sscanf(ts, "%d", &timeoutSec)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-Command", "Start-Process cmd -ArgumentList '/C "+command+"' -Verb RunAs -Wait")
	} else {
		cmd = exec.Command("sudo", "sh", "-c", command)
	}

	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := runWithCancel(ctx, cmd)

	var output strings.Builder
	if stdout.Len() > 0 {
		output.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString("stderr: ")
		output.WriteString(stderr.String())
	}

	result := output.String()
	if len(result) > 50000 {
		result = result[:50000] + "\n... (output truncated)"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Result{Error: "command timed out after " + fmt.Sprintf("%d", timeoutSec) + " seconds"}
		}
		if ctx.Err() == context.Canceled {
			return Result{Error: "command aborted by user"}
		}
		if result == "" {
			return Result{Error: err.Error()}
		}
		return Result{Output: result, Error: "exit: " + err.Error()}
	}

	if result == "" {
		result = "(no output)"
	}
	return Result{Output: result}
}
