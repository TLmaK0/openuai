package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// expandPath expands ~ to the user's home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

// ReadFile reads a file's content
type ReadFile struct{}

func (t ReadFile) Definition() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read the contents of a file at the given path",
		Parameters: []Parameter{
			{Name: "path", Type: "string", Description: "Absolute or relative file path", Required: true},
		},
		RequiresPermission: "none",
	}
}

func (t ReadFile) Execute(_ context.Context, args map[string]string) Result {
	path := expandPath(args["path"])
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: string(data)}
}

// WriteFile writes content to a file
type WriteFile struct{}

func (t WriteFile) Definition() Definition {
	return Definition{
		Name:        "write_file",
		Description: "Write content to a file, creating it if it doesn't exist",
		Parameters: []Parameter{
			{Name: "path", Type: "string", Description: "Absolute or relative file path", Required: true},
			{Name: "content", Type: "string", Description: "Content to write to the file", Required: true},
		},
		RequiresPermission: "session",
	}
}

func (t WriteFile) Execute(_ context.Context, args map[string]string) Result {
	path := expandPath(args["path"])
	content := args["content"]

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{Error: err.Error()}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "File written successfully: " + path}
}

// ListDir lists files in a directory
type ListDir struct{}

func (t ListDir) Definition() Definition {
	return Definition{
		Name:        "list_dir",
		Description: "List files and directories at the given path",
		Parameters: []Parameter{
			{Name: "path", Type: "string", Description: "Directory path (defaults to current directory)", Required: false},
		},
		RequiresPermission: "none",
	}
}

func (t ListDir) Execute(_ context.Context, args map[string]string) Result {
	path := expandPath(args["path"])
	if path == "" {
		path = "."
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return Result{Error: err.Error()}
	}

	var lines []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	return Result{Output: strings.Join(lines, "\n")}
}

// DeleteFile deletes a file or empty directory
type DeleteFile struct{}

func (t DeleteFile) Definition() Definition {
	return Definition{
		Name:        "delete_file",
		Description: "Delete a file or empty directory",
		Parameters: []Parameter{
			{Name: "path", Type: "string", Description: "Path to delete", Required: true},
		},
		RequiresPermission: "always",
	}
}

func (t DeleteFile) Execute(_ context.Context, args map[string]string) Result {
	path := expandPath(args["path"])
	if err := os.Remove(path); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "Deleted: " + path}
}

// MoveFile moves/renames a file
type MoveFile struct{}

func (t MoveFile) Definition() Definition {
	return Definition{
		Name:        "move_file",
		Description: "Move or rename a file",
		Parameters: []Parameter{
			{Name: "source", Type: "string", Description: "Source path", Required: true},
			{Name: "destination", Type: "string", Description: "Destination path", Required: true},
		},
		RequiresPermission: "session",
	}
}

func (t MoveFile) Execute(_ context.Context, args map[string]string) Result {
	src := expandPath(args["source"])
	dst := expandPath(args["destination"])

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{Error: err.Error()}
	}

	if err := os.Rename(src, dst); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{Output: "Moved: " + src + " -> " + dst}
}

// SearchFiles searches for files matching a pattern
type SearchFiles struct{}

func (t SearchFiles) Definition() Definition {
	return Definition{
		Name:        "search_files",
		Description: "Search for files matching a glob pattern recursively",
		Parameters: []Parameter{
			{Name: "pattern", Type: "string", Description: "Glob pattern (e.g. '*.go', '**/*.json')", Required: true},
			{Name: "path", Type: "string", Description: "Starting directory (defaults to current)", Required: false},
		},
		RequiresPermission: "none",
	}
}

func (t SearchFiles) Execute(_ context.Context, args map[string]string) Result {
	pattern := args["pattern"]
	root := expandPath(args["path"])
	if root == "" {
		root = "."
	}

	var matches []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		if matched {
			matches = append(matches, path)
		}
		if len(matches) > 100 {
			return filepath.SkipAll
		}
		return nil
	})

	if len(matches) == 0 {
		return Result{Output: "No files found matching: " + pattern}
	}
	return Result{Output: strings.Join(matches, "\n")}
}
