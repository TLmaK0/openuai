package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveDir turns the directory a sub-agent task asks to work in into a
// clean absolute path: "~" is the user's home and a relative path is taken
// from base (the directory of the agent delegating). It fails with a clear
// error when the directory does not exist, is not a directory or cannot be
// read, so the sub-agent never starts somewhere it cannot work.
func ResolveDir(dir, base string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("cwd is empty")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cwd %q: cannot find the home directory: %v", dir, err)
		}
		dir = filepath.Join(home, dir[1:])
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %v", dir, err)
	}
	st, err := os.Stat(abs)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("cwd %s does not exist; the sub-agent was not started", abs)
	case err != nil:
		return "", fmt.Errorf("cwd %s cannot be accessed (%v); the sub-agent was not started", abs, err)
	case !st.IsDir():
		return "", fmt.Errorf("cwd %s is not a directory; the sub-agent was not started", abs)
	}
	if _, err := os.ReadDir(abs); err != nil {
		return "", fmt.Errorf("cwd %s cannot be read (%v); the sub-agent was not started", abs, err)
	}
	return abs, nil
}
