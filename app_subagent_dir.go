package main

import (
	"context"
	"path/filepath"

	"openuai/internal/hooks"
	"openuai/internal/skills"
	"openuai/internal/subagents"
	"openuai/internal/tools"
)

// A sub-agent may work in another directory (spawn_agents "cwd"): it then
// behaves as if OpenUAI had been launched there. These helpers give the
// directory-dependent parts of the app (hooks, skills, definitions) for such
// a directory.

// dirOf is the directory the agent running in ctx works in: the directory of
// the sub-agent delegating, or the app's working directory.
func (a *App) dirOf(ctx context.Context) string {
	if d := subAgentScopeFrom(ctx).dir; d != "" {
		return d
	}
	return a.GetWorkDir()
}

// otherDir reports whether dir is not the app's working directory.
func (a *App) otherDir(dir string) bool {
	return dir != "" && filepath.Clean(dir) != filepath.Clean(a.GetWorkDir())
}

// loadHooksIn is loadHooks for a sub-agent working in dir: the user hooks,
// the project hooks of dir (not the app's) and the session hooks.
func (a *App) loadHooksIn(dir string) *hooks.Set {
	if !a.otherDir(dir) {
		return a.loadHooks()
	}
	return hooks.Load(a.cfg.ConfigDir(), dir).Merge(a.hk.session.Set())
}

// loadSkillsIn is loadSkills for a sub-agent working in dir.
func (a *App) loadSkillsIn(dir string) *skills.Set {
	if !a.otherDir(dir) {
		return a.loadSkills()
	}
	o := skills.Options{WorkDir: dir}
	if a.cfg != nil {
		o.ConfigDir = a.cfg.ConfigDir()
		o.Overrides = a.cfg.SkillOverrides()
	}
	return skills.LoadWith(o)
}

// skillListingIn is the skill tool's listing for a sub-agent working in dir.
func (a *App) skillListingIn(dir string) string {
	if !a.otherDir(dir) {
		return a.skillListing()
	}
	o := skills.ListOptions{Usage: a.skills.usageCopy()}
	if a.cfg != nil {
		o.Budget, o.MaxDescChars = a.cfg.SkillListingLimits()
	}
	list, _ := a.loadSkillsIn(dir).Listing(o)
	return list
}

// hookInputIn is commonHookInput with dir as the cwd.
func (a *App) hookInputIn(dir string) map[string]any {
	in := a.commonHookInput()
	if a.otherDir(dir) {
		in["cwd"] = dir
	}
	return in
}

// bindToDir binds the tools of a sub-agent working in dir to it: bash, git,
// process and file tools work there, and the skill tool lists dir's skills.
func (a *App) bindToDir(reg *tools.Registry, dir string) *tools.Registry {
	out := subagents.BindTools(reg, dir)
	if _, ok := out.Get("skill"); ok {
		out.Register(tools.Skill{Listing: func() string { return a.skillListingIn(dir) }, Invoke: a.invokeSkill})
	}
	return out
}
