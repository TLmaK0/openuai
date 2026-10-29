package main

import (
	"strings"

	"openuai/internal/agent"
	"openuai/internal/logger"
	"openuai/internal/skills"
	"openuai/internal/subagents"
)

// loadSkills reads the user and project skills. It runs on every use, so
// edits to a skill apply without restarting.
func (a *App) loadSkills() *skills.Set {
	return skills.Load(a.cfg.ConfigDir(), a.GetWorkDir())
}

// GetSkills lists the available skills (and problems loading them) for the UI.
func (a *App) GetSkills() *skills.Set {
	return a.loadSkills()
}

// expandSkillInvocation turns a message starting with /<skill> [args] into
// the message plus the skill's instructions. Other messages are unchanged.
func (a *App) expandSkillInvocation(content string) string {
	name, args, ok := skills.ParseInvocation(content)
	if !ok {
		return content
	}
	sk, found := a.loadSkills().Get(name)
	if !found || !sk.UserInvocable {
		return content
	}
	logger.Info("Skill /%s invoked by the user", sk.Name)
	return agent.WithSkill(content, skills.Render(sk, args, a.GetWorkDir()))
}

// maxPreloadedSkills caps the skills a definition preloads (as Claude Code).
const maxPreloadedSkills = 32

// preloadSkills returns the prompt section with the full content of the
// skills a sub-agent definition lists, and a notice for those it could not
// load.
func (a *App) preloadSkills(def *subagents.Definition, workDir string) (string, string) {
	if def == nil || len(def.Skills) == 0 {
		return "", ""
	}
	set := a.loadSkills()
	var b strings.Builder
	var missing []string
	seen := map[string]bool{}
	for _, n := range def.Skills {
		n = strings.TrimPrefix(strings.TrimSpace(n), "/")
		if n == "" || seen[n] || len(seen) >= maxPreloadedSkills {
			continue
		}
		seen[n] = true
		sk, ok := set.Get(n)
		if !ok || sk.DisableModelInvocation {
			missing = append(missing, n)
			continue
		}
		b.WriteString("\n\n" + skills.Render(sk, "", workDir))
	}
	section := ""
	if b.Len() > 0 {
		section = "\n\n## Preloaded skills\nFollow these skills in your work:" + b.String()
	}
	notice := ""
	if len(missing) > 0 {
		notice = "Skills not found or not loadable by the assistant: " + strings.Join(missing, ", ") + "."
	}
	return section, notice
}
