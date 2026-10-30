package subagents

import (
	"fmt"
	"strings"

	"openuai/internal/hooks"
)

// parseHooks validates a definition's hooks. As in Claude Code, a Stop hook
// in a sub-agent definition becomes SubagentStop (the event a sub-agent
// fires when it finishes). Claude Code events OpenUAI has no counterpart for
// are accepted and ignored.
// With sub false (the agent runs as the main conversation) Stop stays Stop.
func parseHooks(cfg hooks.Config, sub bool) (hooks.Config, error) {
	if len(cfg) == 0 {
		return nil, nil
	}
	out := hooks.Config{}
	for ev, groups := range cfg {
		if sub && ev == hooks.Stop {
			ev = hooks.SubagentStop
		}
		if hooks.SkippedEvent(ev) {
			continue
		}
		if !hooks.ValidEvent(ev) {
			return nil, fmt.Errorf("hooks: event %q is not supported (use %s)", ev, strings.Join(hooks.Events, ", "))
		}
		for _, g := range groups {
			for _, h := range g.Hooks {
				if err := h.Check(ev); err != nil {
					return nil, fmt.Errorf("hooks: %s: %v", ev, err)
				}
			}
		}
		out[ev] = append(out[ev], groups...)
	}
	return out, nil
}

// HookSet returns the hooks of d as a set (nil when it has none).
func HookSet(d *Definition) *hooks.Set {
	if d == nil || len(d.Hooks) == 0 {
		return nil
	}
	s := &hooks.Set{}
	s.Add(d.Hooks, "agent:"+d.Name)
	return s
}
