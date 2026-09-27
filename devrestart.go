package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"openuai/internal/logger"
)

// relaunchFileEnv is exported by scripts/dev-relaunch.sh. It names the file
// where the app writes the directory the next launch must build and run.
const relaunchFileEnv = "OPENUAI_RELAUNCH_FILE"

// RequestRestart exits with code 42 so scripts/dev-relaunch.sh rebuilds and
// relaunches the app. With a worktree, the relaunch happens in that directory;
// without one, in the same directory as now.
//
// It refuses when the app is not running under the relaunch loop, because
// exiting would then just close the app.
func (a *App) RequestRestart(worktree string) (map[string]any, error) {
	file := os.Getenv(relaunchFileEnv)
	if file == "" {
		return nil, errors.New("not running under scripts/dev-relaunch.sh: restarting would close the app without relaunching it")
	}

	target := ""
	if worktree != "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if target, err = resolveRestartTarget(cwd, worktree); err != nil {
			return nil, err
		}
	}
	if err := writeRelaunchTarget(file, target); err != nil {
		return nil, fmt.Errorf("cannot record restart target: %w", err)
	}

	if target == "" {
		logger.Info("Restart requested (same directory)")
	} else {
		logger.Info("Restart requested in worktree %s", target)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Exit(42)
	}()
	return map[string]any{"ok": true, "code": 42, "worktree": target}, nil
}

// resolveRestartTarget turns dir (absolute, or relative to base) into the
// absolute directory to relaunch in. It must be a checkout of the same
// repository as base (the main copy or one of its worktrees) and contain
// dev.sh, so a typo can never leave the app unable to come back.
func resolveRestartTarget(base, dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	dir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("worktree not found: %w", err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("worktree %s is not a directory", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "dev.sh")); err != nil {
		return "", fmt.Errorf("worktree %s has no dev.sh", dir)
	}

	want, err := gitCommonDir(base)
	if err != nil {
		return "", fmt.Errorf("current directory is not a git checkout: %w", err)
	}
	got, err := gitCommonDir(dir)
	if err != nil {
		return "", fmt.Errorf("worktree %s is not a git checkout: %w", dir, err)
	}
	if got != want {
		return "", fmt.Errorf("worktree %s belongs to another repository", dir)
	}
	return dir, nil
}

// gitCommonDir returns the shared .git directory of the checkout at dir; it is
// the same for a repository and all of its worktrees.
func gitCommonDir(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

// writeRelaunchTarget replaces the content of file atomically.
func writeRelaunchTarget(file, target string) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(target), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
