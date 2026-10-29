package tools

import (
	"context"
	"strings"
)

// Skill loads a skill's full instructions into the conversation. The listing
// in its description is read from disk each time it is asked for, and the
// work of an invocation (rendering, running it in a sub-agent, applying its
// tool and model settings) is done by Invoke.
type Skill struct {
	// Listing returns the skills the assistant may load ("" for none).
	Listing func() string
	// Invoke runs the skill called name with args for the agent calling it.
	Invoke func(ctx context.Context, name, args string) Result
}

func (t Skill) Definition() Definition {
	desc := "Load a skill: a named package of instructions for a kind of task. When the user's request matches a skill's description, or the user names a skill, call this BEFORE doing the task and then follow the instructions it returns. Do not call it for skills that are already loaded in this conversation."
	if t.Listing != nil {
		if list := t.Listing(); list != "" {
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

func (t Skill) Execute(ctx context.Context, args map[string]string) Result {
	if t.Invoke == nil {
		return Result{Error: "skills are not available"}
	}
	name := strings.TrimPrefix(strings.TrimSpace(args["name"]), "/")
	if name == "" {
		return Result{Error: "name is required"}
	}
	return t.Invoke(ctx, name, strings.TrimSpace(args["arguments"]))
}

// TurnChange changes how the agent that invoked a skill works for the rest
// of the current turn (until the next user message).
type TurnChange struct {
	Skill string
	// Allow reports tool calls that run without a permission prompt
	// (allowed-tools); arg is the call's main argument (command, path...).
	Allow func(tool, arg string) bool
	// Disallow removes tools from the pool (disallowed-tools).
	Disallow []string
	// Model and Effort override the agent's for the rest of the turn.
	Model  string
	Effort string
}

// Turn is what a tool may know about, and change in, the agent's turn that
// is calling it.
type Turn struct {
	// Has reports whether text is already in the agent's conversation.
	Has func(text string) bool
	// Apply applies a change for the rest of the turn.
	Apply func(TurnChange)
	// Effort is the agent's effort level for this turn.
	Effort string
	// Allowed reports whether a call would run without a permission prompt.
	Allowed func(tool, arg, required string) bool
}

type turnKey struct{}

// WithTurn returns ctx carrying the calling agent's turn.
func WithTurn(ctx context.Context, t *Turn) context.Context {
	return context.WithValue(ctx, turnKey{}, t)
}

// TurnFrom returns the calling agent's turn, or nil.
func TurnFrom(ctx context.Context) *Turn {
	t, _ := ctx.Value(turnKey{}).(*Turn)
	return t
}
