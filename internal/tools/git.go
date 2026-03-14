package tools

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

func runGit(ctx context.Context, workDir string, args ...string) Result {
	cmd := exec.CommandContext(ctx, "git", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	errOutput := strings.TrimSpace(stderr.String())

	if err != nil {
		msg := output
		if errOutput != "" {
			if msg != "" {
				msg += "\n"
			}
			msg += errOutput
		}
		if msg == "" {
			msg = err.Error()
		}
		return Result{Error: msg}
	}

	if output == "" && errOutput != "" {
		output = errOutput
	}
	if output == "" {
		output = "(no output)"
	}
	return Result{Output: output}
}

// GitStatus shows the working tree status
type GitStatus struct{ WorkDir string }

func (t GitStatus) Definition() Definition {
	return Definition{
		Name:        "git_status",
		Description: "Show the working tree status (modified, staged, untracked files)",
		Parameters:  []Parameter{},
		RequiresPermission: "none",
	}
}

func (t GitStatus) Execute(ctx context.Context, _ map[string]string) Result {
	return runGit(ctx, t.WorkDir, "status", "--short")
}

// GitDiff shows changes in working tree
type GitDiff struct{ WorkDir string }

func (t GitDiff) Definition() Definition {
	return Definition{
		Name:        "git_diff",
		Description: "Show changes in the working tree (unstaged changes). Use --staged for staged changes.",
		Parameters: []Parameter{
			{Name: "args", Type: "string", Description: "Additional git diff arguments (e.g. '--staged', a file path)", Required: false},
		},
		RequiresPermission: "none",
	}
}

func (t GitDiff) Execute(ctx context.Context, args map[string]string) Result {
	gitArgs := []string{"diff"}
	if extra := args["args"]; extra != "" {
		gitArgs = append(gitArgs, strings.Fields(extra)...)
	}
	return runGit(ctx, t.WorkDir, gitArgs...)
}

// GitLog shows commit history
type GitLog struct{ WorkDir string }

func (t GitLog) Definition() Definition {
	return Definition{
		Name:        "git_log",
		Description: "Show recent commit history",
		Parameters: []Parameter{
			{Name: "count", Type: "string", Description: "Number of commits to show (default: 10)", Required: false},
			{Name: "args", Type: "string", Description: "Additional arguments (e.g. '--oneline', a file path)", Required: false},
		},
		RequiresPermission: "none",
	}
}

func (t GitLog) Execute(ctx context.Context, args map[string]string) Result {
	count := args["count"]
	if count == "" {
		count = "10"
	}
	gitArgs := []string{"log", "-" + count, "--oneline"}
	if extra := args["args"]; extra != "" {
		gitArgs = append(gitArgs, strings.Fields(extra)...)
	}
	return runGit(ctx, t.WorkDir, gitArgs...)
}

// GitAdd stages files
type GitAdd struct{ WorkDir string }

func (t GitAdd) Definition() Definition {
	return Definition{
		Name:        "git_add",
		Description: "Stage files for commit",
		Parameters: []Parameter{
			{Name: "files", Type: "string", Description: "Files to stage (space-separated, or '.' for all)", Required: true},
		},
		RequiresPermission: "session",
	}
}

func (t GitAdd) Execute(ctx context.Context, args map[string]string) Result {
	files := strings.Fields(args["files"])
	if len(files) == 0 {
		return Result{Error: "no files specified"}
	}
	gitArgs := append([]string{"add"}, files...)
	return runGit(ctx, t.WorkDir, gitArgs...)
}

// GitCommit creates a commit
type GitCommit struct{ WorkDir string }

func (t GitCommit) Definition() Definition {
	return Definition{
		Name:        "git_commit",
		Description: "Create a git commit with the staged changes",
		Parameters: []Parameter{
			{Name: "message", Type: "string", Description: "Commit message", Required: true},
		},
		RequiresPermission: "session",
	}
}

func (t GitCommit) Execute(ctx context.Context, args map[string]string) Result {
	msg := args["message"]
	if msg == "" {
		return Result{Error: "commit message is required"}
	}
	return runGit(ctx, t.WorkDir, "commit", "-m", msg)
}

// GitBranch manages branches
type GitBranch struct{ WorkDir string }

func (t GitBranch) Definition() Definition {
	return Definition{
		Name:        "git_branch",
		Description: "List, create, or switch branches",
		Parameters: []Parameter{
			{Name: "action", Type: "string", Description: "Action: 'list', 'create', 'switch'", Required: true},
			{Name: "name", Type: "string", Description: "Branch name (for create/switch)", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t GitBranch) Execute(ctx context.Context, args map[string]string) Result {
	action := args["action"]
	name := args["name"]

	switch action {
	case "list":
		return runGit(ctx, t.WorkDir, "branch", "-a")
	case "create":
		if name == "" {
			return Result{Error: "branch name required"}
		}
		return runGit(ctx, t.WorkDir, "checkout", "-b", name)
	case "switch":
		if name == "" {
			return Result{Error: "branch name required"}
		}
		return runGit(ctx, t.WorkDir, "checkout", name)
	default:
		return Result{Error: "unknown action: " + action + " (use list, create, or switch)"}
	}
}
