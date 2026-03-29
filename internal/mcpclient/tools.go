package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"openuai/internal/tools"
)

// OnSendCallback is called when a send-type MCP tool is executed, with the message body.
type OnSendCallback func(body string)

// MCPTool wraps a remote MCP tool as a local tools.Tool.
type MCPTool struct {
	serverName string
	remoteName string
	localName  string
	remoteTool mcp.Tool
	manager    *Manager
	onSend     OnSendCallback
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
	// Convert string args to proper types based on the MCP tool schema
	mcpArgs := make(map[string]any, len(args))
	for k, v := range args {
		mcpArgs[k] = coerceArg(v, t.argType(k))
	}

	result, err := t.manager.CallTool(ctx, t.serverName, t.remoteName, mcpArgs)
	if err != nil {
		return tools.Result{Error: err.Error()}
	}

	if result.IsError {
		return tools.Result{Error: ContentToString(result.Content)}
	}

	output := ContentToString(result.Content)

	// Track sent message IDs to prevent echo loops.
	// Graph API returns "Message ID: 1773919724699" in the response.
	if t.onSend != nil && strings.Contains(t.remoteName, "send") {
		if idx := strings.Index(output, "Message ID: "); idx >= 0 {
			idStr := output[idx+len("Message ID: "):]
			if end := strings.IndexAny(idStr, " \n\r\t"); end > 0 {
				idStr = idStr[:end]
			}
			t.onSend(idStr)
		}
	}

	return tools.Result{Output: output}
}

// argType returns the JSON schema type for a parameter, or "string" if unknown.
func (t *MCPTool) argType(name string) string {
	prop, ok := t.remoteTool.InputSchema.Properties[name]
	if !ok {
		return "string"
	}
	if m, ok := prop.(map[string]any); ok {
		if typ, ok := m["type"].(string); ok {
			return typ
		}
	}
	return "string"
}

// coerceArg converts a string value to the correct Go type based on JSON schema type.
func coerceArg(val, typ string) any {
	switch typ {
	case "integer":
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			return n
		}
	case "number":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	case "array", "object":
		var parsed any
		if err := json.Unmarshal([]byte(val), &parsed); err == nil {
			return parsed
		}
	}
	return val
}

// RegisterMCPTools adds all discovered MCP tools to the local tool registry.
// onSend is called when a send-type tool is executed (for echo loop prevention).
func RegisterMCPTools(registry *tools.Registry, manager *Manager, onSend OnSendCallback) {
	allTools := manager.AllTools()
	for localName, ref := range allTools {
		tool := &MCPTool{
			serverName: ref.ServerName,
			remoteName: ref.Tool.Name,
			localName:  localName,
			remoteTool: ref.Tool,
			manager:    manager,
			onSend:     onSend,
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
