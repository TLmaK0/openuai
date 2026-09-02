// Package llm holds the contract between the core and the model providers.
// No code in this package talks to a model API: providers live behind the
// boundary, in their own packages, and register themselves (see registry.go).
package llm

import "context"

type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleSystem     Role = "system"
	RoleToolResult Role = "tool_result"
)

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	// Images holds base64-encoded PNG screenshots attached to this message
	// (used by computer-use: the model sees them as image inputs).
	Images []string `json:"images,omitempty"`
}

type Response struct {
	Content      string `json:"content"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Model        string `json:"model"`
}

// ToolDefinition describes a tool the model may call.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []ToolParam `json:"parameters"`
}

type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ToolCall represents a tool call from the model.
type ToolCall struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

type Provider interface {
	Chat(ctx context.Context, messages []Message, model string) (*Response, error)
	Name() string
	Models() []string
}

// ToolCallProvider extends Provider with native tool calling support
type ToolCallProvider interface {
	Provider
	ChatWithTools(ctx context.Context, messages []Message, model string, toolDefs []ToolDefinition) (*Response, []ToolCall, error)
}
