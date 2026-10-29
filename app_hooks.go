package main

import (
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/agent"
	"openuai/internal/hooks"
)

// loadHooks reads the user and project hooks. It runs on every event, so
// edits to the hook files apply without restarting.
func (a *App) loadHooks() *hooks.Set {
	return hooks.Load(a.cfg.ConfigDir(), a.GetWorkDir())
}

// GetHooks lists the active hooks (and problems in the hook files) for the UI.
func (a *App) GetHooks() *hooks.Set {
	s := a.loadHooks()
	if s.Entries == nil {
		s.Entries = []hooks.Entry{}
	}
	return s
}

func (a *App) emitHookError(msg string) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "agent_step", agent.StepResult{Type: "error", Content: "Hook error: " + msg})
}
