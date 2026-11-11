package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"openuai/internal/agent"
	"openuai/internal/hooks"
	"openuai/internal/logger"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// The main conversation works in the process working directory (GetWorkDir),
// so every part of the app that depends on the project (AGENTS.md, skills,
// hooks, agent definitions, bash, git, relative paths) follows it. Changing
// the conversation's folder changes that directory (issue #114); it is saved
// with the session.

const errWorkDirBusy = "The assistant is busy: wait until it finishes (or stop it) before changing folders."

// launchWorkDir is the directory the process was started in.
func (a *App) launchWorkDir() (string, error) {
	if a.launchDir != "" {
		return a.launchDir, nil
	}
	return os.Getwd()
}

// resolveWorkDir turns what the user typed into an absolute directory:
// "~" is the home directory and relative paths start at the current folder.
func (a *App) resolveWorkDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("no folder given")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.GetWorkDir(), path)
	}
	path = filepath.Clean(path)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("folder %s does not exist", path)
		}
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a folder", path)
	}
	return path, nil
}

// changeWorkDir moves the process to dir and resets what was tied to the
// previous project. The caller rebuilds the main agent if it needs to.
func (a *App) changeWorkDir(path string) error {
	dir, err := a.resolveWorkDir(path)
	if err != nil {
		return err
	}
	if dir != filepath.Clean(a.GetWorkDir()) {
		if err := os.Chdir(dir); err != nil {
			return err
		}
		hooks.SetProjectDir(dir)
		a.skills.reset() // nested skill folders found in the previous project
		logger.Info("Working directory changed to %s", dir)
	}
	a.rememberWorkDir(dir)
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "work_dir_changed", dir)
	}
	return nil
}

func (a *App) rememberWorkDir(dir string) {
	if a.cfg == nil {
		return
	}
	a.cfg.AddRecentWorkDir(dir)
	if err := a.cfg.Save(); err != nil {
		logger.Error("Failed to save recent folders: %s", err.Error())
	}
}

// SetWorkDir moves the conversation to another folder, as if OpenUAI had
// been launched there: its AGENTS.md, agents, skills and hooks apply from the
// next turn. Returns "" on success or an error message.
func (a *App) SetWorkDir(path string) string {
	if !a.agentMu.TryLock() {
		return errWorkDirBusy
	}
	defer a.agentMu.Unlock()
	if running := a.subRuns.running(); len(running) > 0 {
		return "Sub-agents are still running (" + strings.Join(running, ", ") + "): wait for them or stop them before changing folders."
	}
	if err := a.changeWorkDir(path); err != nil {
		return err.Error()
	}
	// A new agent picks up the folder's instructions, agent definition and
	// system prompt; the conversation itself is kept.
	a.rebuildMainAgentLocked()
	return ""
}

// GetRecentWorkDirs lists the folders used recently that still exist, most
// recent first.
func (a *App) GetRecentWorkDirs() []string {
	out := []string{}
	if a.cfg == nil {
		return out
	}
	for _, d := range a.cfg.RecentWorkDirs() {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return out
}

// PickWorkDir opens the native folder picker at the current folder and
// returns the folder chosen ("" if cancelled).
func (a *App) PickWorkDir() string {
	dir, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title:                "Choose the folder to work in",
		DefaultDirectory:     a.GetWorkDir(),
		CanCreateDirectories: true,
	})
	if err != nil {
		logger.Error("Folder picker: %s", err.Error())
		return ""
	}
	return dir
}

// restoreSessionWorkDir runs at startup. Launched from the home directory or
// the root (a desktop or menu launch), the restored conversation goes back to
// its folder; launched from a specific folder, that folder wins.
func (a *App) restoreSessionWorkDir() {
	launch := filepath.Clean(a.GetWorkDir())
	home, _ := os.UserHomeDir()
	generic := launch == string(filepath.Separator) || (home != "" && launch == filepath.Clean(home)) || launch == filepath.VolumeName(launch)+string(filepath.Separator)
	if !generic {
		a.rememberWorkDir(launch)
		return
	}
	dir := agent.PeekSessionWorkDir(a.cfg.ConfigDir(), "current")
	if dir == "" || filepath.Clean(dir) == launch {
		return
	}
	if err := a.changeWorkDir(dir); err != nil {
		logger.Info("Restored session keeps working in %s: %s", launch, err.Error())
	}
}
