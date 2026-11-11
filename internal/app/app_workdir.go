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

// SetWorkDir moves to another folder, as if OpenUAI had been launched
// there: its AGENTS.md, agents, skills and hooks apply. Sessions belong to a
// folder: the conversation in progress is archived with its folder and the
// most recent session of the new folder is opened (a new one if it has
// none). Returns "" on success or an error message.
func (a *App) SetWorkDir(path string) string {
	if !a.agentMu.TryLock() {
		return errWorkDirBusy
	}
	defer a.agentMu.Unlock()
	if running := a.subRuns.running(); len(running) > 0 {
		return "Sub-agents are still running (" + strings.Join(running, ", ") + "): wait for them or stop them before changing folders."
	}
	dir, err := a.resolveWorkDir(path)
	if err != nil {
		return err.Error()
	}
	if dir == filepath.Clean(a.GetWorkDir()) {
		a.rememberWorkDir(dir)
		return ""
	}
	if err := a.openFolderLocked(dir, true); err != nil {
		return err.Error()
	}
	return ""
}

// openFolderLocked moves to dir with its most recent session that can be
// restored, or a new one. The caller holds agentMu.
func (a *App) openFolderLocked(dir string, fireHooks bool) error {
	if id := a.latestSessionIn(dir); id != "" {
		err := a.openSessionLocked(id, dir, "resume", fireHooks)
		var mismatch *agent.SessionMismatchError
		if !errors.As(err, &mismatch) {
			return err
		}
		logger.Info("Session %s of %s: %s; starting a new one", id, dir, err.Error())
	}
	return a.openSessionLocked("", dir, "clear", fireHooks)
}

// latestSessionIn is the ID of the most recent archived session of dir.
func (a *App) latestSessionIn(dir string) string {
	for _, s := range agent.ListSessions(a.cfg.ConfigDir()) { // newest first
		if s.WorkDir != "" && filepath.Clean(s.WorkDir) == dir {
			return s.ID
		}
	}
	return ""
}

// openSessionLocked makes archived session id ("": a new, empty one) the
// conversation in progress, working in dir ("": stay), and archives the
// conversation that was in progress. On error nothing changes. source is the
// SessionStart source; fireHooks is false at startup, which fires its own.
// The caller holds agentMu.
func (a *App) openSessionLocked(id, dir, source string, fireHooks bool) error {
	cfgDir := a.cfg.ConfigDir()
	prevDir := a.GetWorkDir()
	if dir != "" {
		if err := a.changeWorkDir(dir); err != nil {
			return err
		}
	}
	// The new conversation runs as the agent chosen for it, if any.
	a.loadMainAgentChoice()
	var choice *string
	if id != "" {
		choice = agent.PeekMainAgentChoice(cfgDir, id)
	}
	a.mainAg.mu.Lock()
	prevChoice := a.mainAg.choice
	a.mainAg.choice = choice
	a.mainAg.mu.Unlock()
	ag := a.newMainAgent(a.resolveMainAgent())
	if id != "" {
		if err := ag.LoadSessionByID(cfgDir, id); err != nil {
			a.mainAg.mu.Lock()
			a.mainAg.choice = prevChoice
			a.mainAg.mu.Unlock()
			if dir != "" {
				_ = a.changeWorkDir(prevDir)
			}
			return err
		}
	}
	ag.SetMainAgentChoice(choice)

	if fireHooks {
		a.endSession(source)
	}
	if old := a.currentAgent; old != nil {
		a.saveSession(old)
	}
	agent.ArchiveSession(cfgDir)
	if id != "" {
		// It is the conversation in progress now, not an archive.
		if err := agent.DeleteSession(cfgDir, id); err != nil {
			logger.Error("Failed to remove archived session %s: %s", id, err.Error())
		}
	}
	a.currentAgent = ag
	a.costTracker.Reset()
	a.saveSession(ag)
	if fireHooks {
		a.startSession(source, false)
	}
	if id == "" {
		a.armInitialPromptLocked(ag)
	}
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "session_changed", map[string]string{"work_dir": a.GetWorkDir()})
	}
	return nil
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

// restoreSessionWorkDir runs at startup, before the conversation is
// restored. Launched from the home directory or the root (a desktop or menu
// launch), the app goes back to the folder of the conversation in progress.
// Launched from a specific folder, that folder wins: a conversation in
// progress that belongs to another folder is archived and the folder's most
// recent session is opened.
func (a *App) restoreSessionWorkDir() {
	launch := filepath.Clean(a.GetWorkDir())
	current := agent.PeekSessionWorkDir(a.cfg.ConfigDir(), "current")
	if !isGenericLaunchDir(launch) {
		a.rememberWorkDir(launch)
		if current == "" || filepath.Clean(current) == launch {
			return
		}
		a.agentMu.Lock()
		defer a.agentMu.Unlock()
		if err := a.openFolderLocked(launch, false); err != nil {
			logger.Error("Cannot open the sessions of %s: %s", launch, err.Error())
		}
		return
	}
	if current == "" || filepath.Clean(current) == launch {
		return
	}
	if err := a.changeWorkDir(current); err != nil {
		logger.Info("Restored session keeps working in %s: %s", launch, err.Error())
	}
}

// isGenericLaunchDir reports whether dir is where a desktop or menu launch
// starts (home or a filesystem root) rather than a folder chosen by the user.
func isGenericLaunchDir(dir string) bool {
	if dir == filepath.VolumeName(dir)+string(filepath.Separator) {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && home != "" && dir == filepath.Clean(home)
}
