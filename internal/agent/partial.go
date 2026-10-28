package agent

import (
	"fmt"
	"sort"
	"strings"

	"openuai/internal/llm"
)

const partialResultClip = 2000

// PartialWork describes what an interrupted run (e.g. stopped at maxTurns)
// did so far: its last text answer or, when it produced no text yet, the
// tool calls it made and their results. It is never empty if any work ran.
func (a *Agent) PartialWork() string {
	if s := a.LastAssistantContent(); s != "" {
		return s
	}
	var b strings.Builder
	for _, m := range a.messages {
		switch m.Role {
		case llm.RoleAssistant:
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "- Called %s(%s)\n", tc.Name, partialArgs(tc.Arguments))
			}
		case llm.RoleToolResult:
			fmt.Fprintf(&b, "  Result: %s\n", partialClip(m.Content))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "Tool calls made before stopping:\n" + b.String()
}

func partialArgs(args map[string]string) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, args[k]))
	}
	return strings.Join(parts, ", ")
}

func partialClip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > partialResultClip {
		return s[:partialResultClip] + " …[truncated]"
	}
	return strings.ReplaceAll(s, "\n", "\n    ")
}
