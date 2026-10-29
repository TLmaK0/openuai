package agent

import (
	"context"
	"strings"

	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

// turnState is what invoked skills change for the rest of a turn: tools
// approved without a prompt (allowed-tools), tools removed from the pool
// (disallowed-tools), and the model and effort. It clears when the next
// user message begins.
type turnState struct {
	allow    []func(tool, arg string) bool
	disallow map[string]string // tool -> skill that removed it
	model    string
	effort   string
}

// ApplyTurnChange applies a skill's settings for the rest of the turn.
func (a *Agent) ApplyTurnChange(c tools.TurnChange) {
	if c.Allow != nil {
		a.turn.allow = append(a.turn.allow, c.Allow)
	}
	for _, t := range c.Disallow {
		if a.turn.disallow == nil {
			a.turn.disallow = map[string]string{}
		}
		a.turn.disallow[t] = c.Skill
	}
	if c.Model != "" {
		a.turn.model = c.Model
	}
	if c.Effort != "" {
		a.turn.effort = c.Effort
	}
	logger.Info("Skill %s active for this turn: allow=%v disallow=%v model=%q effort=%q", c.Skill, c.Allow != nil, c.Disallow, c.Model, c.Effort)
}

// clearTurn drops what skills changed in the previous turn.
func (a *Agent) clearTurn() { a.turn = turnState{} }

// turnModel is the model for the next call: a skill's override, or the agent's.
func (a *Agent) turnModel() string {
	if a.turn.model != "" {
		return a.turn.model
	}
	return a.model
}

// turnEffort is the effort for the next call.
func (a *Agent) turnEffort() string {
	if a.turn.effort != "" {
		return a.turn.effort
	}
	return a.effort
}

// Effort is the agent's effort level for the current turn ("": provider default).
func (a *Agent) Effort() string { return a.turnEffort() }

// preApproved reports whether an invoked skill (or the agent's own grant)
// approved this call without a prompt. Plan mode still denies changes.
func (a *Agent) preApproved(tool, arg string) bool {
	if a.permMode == PermModePlan {
		return false
	}
	if a.allow != nil && a.allow(tool, arg) {
		return true
	}
	for _, f := range a.turn.allow {
		if f(tool, arg) {
			return true
		}
	}
	return false
}

// turnToolDefs is the tool pool for the next call, without the tools an
// active skill removed.
func (a *Agent) turnToolDefs() []llm.ToolDefinition {
	if len(a.turn.disallow) == 0 {
		return a.toolDefs
	}
	out := make([]llm.ToolDefinition, 0, len(a.toolDefs))
	for _, d := range a.toolDefs {
		if _, gone := a.turn.disallow[d.Name]; !gone {
			out = append(out, d)
		}
	}
	return out
}

// toolContext gives a tool what it may know about, and change in, this turn.
func (a *Agent) toolContext(ctx context.Context) context.Context {
	return tools.WithTurn(ctx, &tools.Turn{
		Has: func(text string) bool {
			for _, m := range a.messages {
				if strings.Contains(m.Content, text) {
					return true
				}
			}
			return false
		},
		Apply:  a.ApplyTurnChange,
		Effort: a.turnEffort(),
		Allowed: func(tool, arg, required string) bool {
			return a.preApproved(tool, arg) || a.permissions.Allowed(tool, required)
		},
	})
}
