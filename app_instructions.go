package main

import (
	"strings"
	"sync"

	"openuai/internal/logger"
	"openuai/internal/subagents"
)

// Instruction files of the main conversation: the user's AGENTS.md in
// OpenUAI's config folder, then every AGENTS.md from the repository root
// down to the working directory. They are read again at the start of every
// turn, so edits (and a new working directory) apply from the next message.

// mainInstructionFiles returns the instruction files the main conversation
// loads now.
func (a *App) mainInstructionFiles() []subagents.InstructionFile {
	userFile := ""
	if a.cfg != nil {
		userFile = subagents.UserInstructionsFile(a.cfg.ConfigDir())
	}
	return subagents.LoadInstructions(userFile, a.GetWorkDir())
}

// GetInstructionFiles lists the instruction files loaded in the main
// conversation (for Settings).
func (a *App) GetInstructionFiles() []subagents.InstructionFile {
	files := a.mainInstructionFiles()
	if files == nil {
		return []subagents.InstructionFile{}
	}
	return files
}

// instructionsLoader returns the agent's Instructions callback. Each agent
// (conversation) gets its own: InstructionsLoaded fires for every file the
// first time it is loaded (load_reason "session_start") and again when it
// changes or appears later ("file_changed").
func (a *App) instructionsLoader() func() string {
	var mu sync.Mutex
	var loaded map[string]string // path -> content, nil before the first turn
	return func() string {
		files := a.mainInstructionFiles()
		mu.Lock()
		defer mu.Unlock()
		reason := "file_changed"
		if loaded == nil {
			reason = "session_start"
		}
		next := make(map[string]string, len(files))
		var changed []string
		for _, f := range files {
			next[f.Path] = f.Content
			if old, ok := loaded[f.Path]; ok && old == f.Content {
				continue
			}
			changed = append(changed, f.Path)
			memType := "Project"
			if f.Scope == subagents.ScopeUser {
				memType = "User"
			}
			if f.Truncated {
				logger.Info("Instructions: %s is %d bytes, only the start was loaded", f.Path, f.Size)
			}
			a.instructionsLoaded(f.Path, memType, reason)
		}
		if len(changed) > 0 || len(next) != len(loaded) {
			paths := make([]string, len(files))
			for i, f := range files {
				paths[i] = f.Path
			}
			logger.Info("Instructions loaded (%s): [%s]", reason, strings.Join(paths, ", "))
		}
		loaded = next
		return subagents.MainInstructionsPrompt(files)
	}
}
