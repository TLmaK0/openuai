package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/tools"
)

// MCPTool wraps a remote MCP tool as a local tools.Tool.
type MCPTool struct {
	serverName string
	remoteName string
	localName  string
	remoteTool mcp.Tool
	manager    *Manager
}

// Definition implements tools.Tool.
func (t *MCPTool) Definition() tools.Definition {
	params := mcpSchemaToParams(t.remoteTool.InputSchema)

	return tools.Definition{
		Name:               t.localName,
		Description:        fmt.Sprintf("[MCP:%s] %s", t.serverName, t.remoteTool.Description),
		Parameters:         params,
		RequiresPermission: "always",
	}
}

// Execute implements tools.Tool.
func (t *MCPTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	// Convert string args to any for MCP
	mcpArgs := make(map[string]any, len(args))
	for k, v := range args {
		mcpArgs[k] = v
	}

	result, err := t.manager.CallTool(ctx, t.serverName, t.remoteName, mcpArgs)
	if err != nil {
		return tools.Result{Error: err.Error()}
	}

	if result.IsError {
		return tools.Result{Error: ContentToString(result.Content)}
	}

	return tools.Result{Output: ContentToString(result.Content)}
}

// RegisterMCPTools adds all discovered MCP tools to the local tool registry.
func RegisterMCPTools(registry *tools.Registry, manager *Manager) {
	allTools := manager.AllTools()
	for localName, ref := range allTools {
		tool := &MCPTool{
			serverName: ref.ServerName,
			remoteName: ref.Tool.Name,
			localName:  localName,
			remoteTool: ref.Tool,
			manager:    manager,
		}
		registry.Register(tool)
	}
}

// mcpSchemaToParams converts an MCP tool input schema to our Parameter slice.
func mcpSchemaToParams(schema mcp.ToolInputSchema) []tools.Parameter {
	var params []tools.Parameter
	requiredSet := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		requiredSet[r] = true
	}

	for name, prop := range schema.Properties {
		p := tools.Parameter{
			Name:     name,
			Required: requiredSet[name],
		}
		if m, ok := prop.(map[string]any); ok {
			if t, ok := m["type"].(string); ok {
				p.Type = t
			}
			if d, ok := m["description"].(string); ok {
				p.Description = d
			}
		}
		if p.Type == "" {
			p.Type = "string"
		}
		params = append(params, p)
	}
	return params
}

// ContentToString extracts text and images from MCP content items.
// Images are rendered as markdown data-URI images so the frontend can display them.
func ContentToString(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		switch v := c.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		case mcp.ImageContent:
			mime := v.MIMEType
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, fmt.Sprintf("![image](data:%s;base64,%s)", mime, v.Data))
		default:
			data, err := json.Marshal(v)
			if err == nil {
				parts = append(parts, string(data))
			}
		}
	}
	return strings.Join(parts, "\n")
}
