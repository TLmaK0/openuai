package subagents

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"openuai/internal/config"
)

// MCPServerSpec is one entry of a definition's mcpServers list: either a
// reference to an MCP server already configured in OpenUAI (Inline nil) or
// an inline server that exists only while the sub-agent runs.
type MCPServerSpec struct {
	Name   string
	Inline *config.MCPServerConfig
}

var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// parseMCPServers reads the mcpServers list (same shape as Claude Code):
//
//	mcpServers:
//	  - github                 # reference by name
//	  - playwright:            # inline, scoped to this sub-agent
//	      type: stdio
//	      command: npx
//	      args: ["-y", "@playwright/mcp@latest"]
func parseMCPServers(nodes []yaml.Node) ([]MCPServerSpec, error) {
	var out []MCPServerSpec
	for _, n := range nodes {
		switch n.Kind {
		case yaml.ScalarNode:
			name := strings.TrimSpace(n.Value)
			if !mcpNameRe.MatchString(name) {
				return nil, fmt.Errorf("invalid MCP server name %q", n.Value)
			}
			out = append(out, MCPServerSpec{Name: name})
		case yaml.MappingNode:
			if len(n.Content) != 2 {
				return nil, errors.New("each inline MCP server must be a single entry: name: {definition}")
			}
			name := strings.TrimSpace(n.Content[0].Value)
			if !mcpNameRe.MatchString(name) {
				return nil, fmt.Errorf("invalid MCP server name %q", name)
			}
			var spec struct {
				Type    string            `yaml:"type"`
				Command string            `yaml:"command"`
				Args    []string          `yaml:"args"`
				Env     map[string]string `yaml:"env"`
				URL     string            `yaml:"url"`
			}
			if err := n.Content[1].Decode(&spec); err != nil {
				return nil, fmt.Errorf("MCP server %q: %v", name, err)
			}
			typ := strings.ToLower(strings.TrimSpace(spec.Type))
			if typ == "" {
				typ = "stdio"
				if spec.URL != "" {
					typ = "http"
				}
			}
			cfg := config.MCPServerConfig{Name: name, AutoStart: true, Env: spec.Env}
			switch typ {
			case "stdio":
				if strings.TrimSpace(spec.Command) == "" {
					return nil, fmt.Errorf("MCP server %q: stdio needs a command", name)
				}
				cfg.Command, cfg.Args = spec.Command, spec.Args
			case "http":
				if strings.TrimSpace(spec.URL) == "" {
					return nil, fmt.Errorf("MCP server %q: http needs a url", name)
				}
				cfg.URL = spec.URL
			default:
				return nil, fmt.Errorf("MCP server %q: type %q is not supported (use stdio or http)", name, spec.Type)
			}
			out = append(out, MCPServerSpec{Name: name, Inline: &cfg})
		default:
			return nil, errors.New("mcpServers entries must be a server name or name: {definition}")
		}
	}
	return out, nil
}

// Disallowed reports whether tool name matches one of d's disallowedTools.
func Disallowed(d *Definition, name string) bool {
	if d == nil {
		return false
	}
	for _, p := range d.DisallowedTools {
		if match(p, name) {
			return true
		}
	}
	return false
}
