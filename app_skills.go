package main

import (
	"fmt"
	"strings"

	"openuai/internal/agent"
	"openuai/internal/hooks"
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
// UserPromptExpansion hooks run first: they can block the expansion (an
// error) or add context for the model (the returned notes).
func (a *App) expandSkillInvocation(content string) (string, []string, error) {
	name, args, ok := skills.ParseInvocation(content)
	if !ok {
		return content, nil, nil
	}
	sk, found := a.loadSkills().Get(name)
	if !found || !sk.UserInvocable {
		return content, nil, nil
	}
	var notes []string
	if hs := a.loadHooks(); hs.Has(hooks.UserPromptExpansion) {
		r := a.runHookOn(a.ctx, hs, hooks.UserPromptExpansion, sk.Name, map[string]any{
			"expansion_type": "slash_command", "command_name": sk.Name, "command_args": args,
			"command_source": sk.Source, "prompt": content,
		})
		if r.Decision == "block" || r.StopAll {
			reason := r.Reason
			if r.StopAll && r.StopReason != "" {
				reason = r.StopReason
			}
			logger.Info("Skill /%s blocked by a UserPromptExpansion hook: %s", sk.Name, reason)
			return content, nil, fmt.Errorf("Blocked by a hook: %s", reason)
		}
		notes = hookNotesOf(r)
	}
	logger.Info("Skill /%s invoked by the user", sk.Name)
	return agent.WithSkill(content, skills.Render(sk, args, a.GetWorkDir())), notes, nil
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
