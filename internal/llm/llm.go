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
}

// ToolCallProvider extends Provider with native tool calling support
type ToolCallProvider interface {
	Provider
	ChatWithTools(ctx context.Context, messages []Message, model string, toolDefs []ToolDefinition) (*Response, []ToolCall, error)
}

type Response struct {
	Content      string `json:"content"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Model        string `json:"model"`
}

type Provider interface {
	Chat(ctx context.Context, messages []Message, model string) (*Response, error)
	Name() string
	Models() []string
}
