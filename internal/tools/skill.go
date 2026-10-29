package tools

import (
	"context"
	"fmt"
	"strings"

	"openuai/internal/skills"
)

// Skill loads a skill's full instructions into the conversation. The list of
// skills in its description is read from disk each time it is asked for.
type Skill struct {
	Load       func() *skills.Set
	ProjectDir func() string
}

func (t Skill) Definition() Definition {
	desc := "Load a skill: a named package of instructions for a kind of task. When the user's request matches a skill's description, or the user names a skill, call this BEFORE doing the task and then follow the instructions it returns. Do not call it for skills that are already loaded in this conversation."
	if t.Load != nil {
		if list := t.Load().Describe(); list != "" {
			desc += "\n\nAvailable skills:\n" + list
		} else {
			desc += "\n\nNo skills are available right now."
		}
	}
	return Definition{
		Name:        "skill",
		Description: desc,
		Parameters: []Parameter{
			{Name: "name", Type: "string", Description: "Name of the skill to load", Required: true},
			{Name: "arguments", Type: "string", Description: "Optional arguments for the skill", Required: false},
		},
		RequiresPermission: "none",
	}
}

func (t Skill) Execute(_ context.Context, args map[string]string) Result {
	if t.Load == nil {
		return Result{Error: "skills are not available"}
	}
	set := t.Load()
	name := strings.TrimPrefix(strings.TrimSpace(args["name"]), "/")
	sk, ok := set.Get(name)
	if !ok {
		var names []string
		for _, s := range set.Skills {
			if !s.DisableModelInvocation {
				names = append(names, s.Name)
			}
		}
		return Result{Error: fmt.Sprintf("no skill named %q (available: %s)", name, strings.Join(names, ", "))}
	}
	if sk.DisableModelInvocation {
		return Result{Error: fmt.Sprintf("skill %q can only be run by the user (typing /%s); do not reproduce its steps another way", sk.Name, sk.Name)}
	}
	dir := ""
	if t.ProjectDir != nil {
		dir = t.ProjectDir()
	}
	return Result{Output: skills.Render(sk, strings.TrimSpace(args["arguments"]), dir)}
}
