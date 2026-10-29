package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ShellResult is the outcome of an injected command: stdout and stderr
// merged, and the exit code.
type ShellResult struct {
	Output   string
	ExitCode int
	Err      error // could not run, or timed out
}

// InjectTimeout is how long an injected command may run (the bash tool's
// default in Claude Code).
var InjectTimeout = 2 * time.Minute

// shellFor picks the program that runs an injected command: bash unless the
// skill asks for PowerShell and it is installed; PowerShell when there is
// no bash and the skill did not insist on bash.
func shellFor(shell string) (string, []string, error) {
	bash, bashErr := exec.LookPath("bash")
	ps := ""
	for _, p := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(p); err == nil {
			ps = path
			break
		}
	}
	switch {
	case shell == "powershell" && ps != "":
		return ps, []string{"-NoProfile", "-NonInteractive", "-Command"}, nil
	case bashErr == nil:
		return bash, []string{"-c"}, nil
	case shell == "bash":
		return "", nil, ErrNoBash
	case ps != "":
		return ps, []string{"-NoProfile", "-NonInteractive", "-Command"}, nil
	}
	return "", nil, errors.New("no shell found (bash or PowerShell)")
}

func shellAvailable(sk Skill) error {
	if _, _, err := shellFor(sk.Shell); err != nil {
		if errors.Is(err, ErrNoBash) {
			return fmt.Errorf("Skill %s requires bash (`shell: bash` in frontmatter) but bash was not found", sk.Name)
		}
		return fmt.Errorf("Skill %s: %v", sk.Name, err)
	}
	return nil
}

// RunShell runs an injected command in dir with the skill's shell.
func RunShell(command, shell, dir string) ShellResult {
	prog, args, err := shellFor(shell)
	if err != nil {
		return ShellResult{Err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), InjectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, append(args, command)...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	res := ShellResult{Output: out.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.Err = fmt.Errorf("timed out after %s", InjectTimeout)
		return res
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
	} else if err != nil {
		res.Err = err
	}
	return res
}

// exitOneIsResult reports whether exit code 1 from command is a normal
// result (no match, files differ) rather than a failure: search and
// comparison commands.
func exitOneIsResult(command, shell string) bool {
	f := strings.Fields(command)
	if len(f) == 0 {
		return false
	}
	if f[0] == "git" && len(f) > 1 {
		return f[1] == "diff" || (shell != "powershell" && f[1] == "grep")
	}
	switch f[0] {
	case "grep", "rg", "Select-String":
		return true
	case "egrep", "fgrep", "ag", "ack", "diff", "cmp", "test", "[", "find":
		return shell != "powershell"
	}
	return false
}
