package main

import (
	"fmt"
	"strings"

	"openuai/internal/config"
	"openuai/internal/mcpclient"
	"openuai/internal/subagents"
	"openuai/internal/tools"
)

// addSubAgentMCP gives a sub-agent the MCP servers of its definition:
// referenced servers contribute their already-registered tools, inline
// servers are connected now (only for this sub-agent; the caller stops the
// returned manager when the run ends). disallowedTools still apply. Problems
// are reported in the returned note and never stop the sub-agent.
func (a *App) addSubAgentMCP(def *subagents.Definition, base, reg *tools.Registry) (*mcpclient.Manager, string) {
	var notes []string
	var inline []config.MCPServerConfig
	for _, s := range def.MCPServers {
		if s.Inline != nil {
			inline = append(inline, *s.Inline)
			continue
		}
		prefix := "mcp_" + s.Name + "_"
		found := false
		for _, t := range base.All() {
			n := t.Definition().Name
			if strings.HasPrefix(n, prefix) {
				found = true
				if !subagents.Disallowed(def, n) {
					reg.Register(t)
				}
			}
		}
		if !found {
			notes = append(notes, fmt.Sprintf("MCP server %q is not configured or has no tools.", s.Name))
		}
	}
	if len(inline) == 0 {
		return nil, strings.Join(notes, " ")
	}
	mgr, errs := mcpclient.StartScoped(inline)
	for _, e := range errs {
		notes = append(notes, "Inline MCP server failed to start: "+e.Error()+".")
	}
	scoped := tools.NewRegistry()
	mcpclient.RegisterMCPTools(scoped, mgr, a.TrackSentMessageID)
	for _, t := range scoped.All() {
		if !subagents.Disallowed(def, t.Definition().Name) {
			reg.Register(t)
		}
	}
	return mgr, strings.Join(notes, " ")
}
