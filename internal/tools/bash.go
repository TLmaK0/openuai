package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Bash executes a shell command
type Bash struct {
	WorkDir string
}

func (t Bash) Definition() Definition {
	return Definition{
		Name:        "bash",
		Description: "Execute a shell command and return its output. Use for system operations, installing packages, running scripts, etc.",
		Parameters: []Parameter{
			{Name: "command", Type: "string", Description: "The shell command to execute", Required: true},
			{Name: "timeout", Type: "string", Description: "Timeout in seconds (default: 30)", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t Bash) Execute(ctx context.Context, args map[string]string) Result {
	command := args["command"]
	if command == "" {
		return Result{Error: "command is required"}
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
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

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
		cmd = exec.CommandContext(ctx, "powershell", "-Command", "Start-Process cmd -ArgumentList '/C "+command+"' -Verb RunAs -Wait")
	} else {
		cmd = exec.CommandContext(ctx, "sudo", "sh", "-c", command)
	}

	if t.WorkDir != "" {
		cmd.Dir = t.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

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
