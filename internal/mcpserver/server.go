// Package mcpserver exposes OpenUAI as an MCP server, so other MCP clients
// (Claude Code, Claude Desktop, other agents) can hand it tasks.
//
// The server is mounted on the local API (streamable HTTP at /mcp on
// 127.0.0.1) and is also reachable over stdio through `openuai mcp`, a thin
// proxy to that endpoint (see proxy.go).
//
// What is exposed is the agent as a whole: a task sent with send_message runs
// exactly like a message typed in the chat, so it goes through the same
// hooks, skills and permission model (tool approvals are asked in the
// OpenUAI window). The individual tools of the registry are not exposed one
// by one, since calling them directly would skip that pipeline.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server name announced to MCP clients.
const Name = "openuai"

// Tool names.
const (
	ToolSendMessage     = "send_message"
	ToolGetConversation = "get_conversation"
	ToolNewConversation = "new_conversation"
	ToolStopTask        = "stop_task"
	ToolGetStatus       = "get_status"
)

// Reply is the outcome of a message sent to the assistant.
type Reply struct {
	Content      string  `json:"content"`
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Error        string  `json:"error,omitempty"`
	// Refused is set when the message was not run at all (e.g. blocked by
	// a hook).
	Refused bool `json:"refused,omitempty"`
}

// Backend is what the server needs from the app. Func fields keep this
// package free of the main package.
type Backend struct {
	// SendMessage runs a message through the assistant, like the chat
	// does, and returns its final answer. It blocks until the turn ends.
	SendMessage func(message string) Reply
	// History returns the current conversation (JSON-serialisable).
	History func() any
	// NewConversation archives the current conversation and starts a new one.
	NewConversation func()
	// Stop aborts the task the assistant is running, if any.
	Stop func()
	// Status returns provider, model, cost... (JSON-serialisable).
	Status func() any
}

// ProgressInterval is how often send_message reports progress while the
// assistant works, when the client asked for progress. It keeps clients
// that reset their timeout on progress from giving up on long tasks.
var ProgressInterval = 10 * time.Second

// Tools returns the definitions of the exposed tools. The stdio proxy
// announces the same list, so clients see the tools even before the app is
// reachable.
func Tools() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool(ToolSendMessage,
			mcp.WithDescription("Send a message or task to the OpenUAI assistant and wait for its final answer. "+
				"It runs exactly like a message typed in the OpenUAI chat: same conversation, tools, connectors, memory and permission rules. "+
				"If a tool needs approval, the user is asked in the OpenUAI window and the call waits. Tasks can take minutes."),
			mcp.WithString("message", mcp.Required(), mcp.Description("The message or task for the assistant")),
		),
		mcp.NewTool(ToolGetConversation,
			mcp.WithDescription("Read the current OpenUAI conversation: user and assistant messages and the tool calls made."),
			mcp.WithNumber("limit", mcp.Description("Return only the last N entries (default 20, 0 for all)")),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		mcp.NewTool(ToolNewConversation,
			mcp.WithDescription("Archive the current OpenUAI conversation and start a new, empty one."),
			mcp.WithDestructiveHintAnnotation(false),
		),
		mcp.NewTool(ToolStopTask,
			mcp.WithDescription("Stop the task the OpenUAI assistant is running, if any."),
		),
		mcp.NewTool(ToolGetStatus,
			mcp.WithDescription("Get the OpenUAI status: active provider and model, and accumulated token usage and cost."),
			mcp.WithReadOnlyHintAnnotation(true),
		),
	}
}

// New builds the MCP server over the given backend.
func New(version string, b Backend) *server.MCPServer {
	s := server.NewMCPServer(Name, version,
		server.WithToolCapabilities(false),
		server.WithInstructions("OpenUAI is a desktop AI agent running on this computer. "+
			"Use send_message to give it a task and get its answer; it keeps one shared conversation with its user."),
		server.WithRecovery(),
	)
	handlers := map[string]server.ToolHandlerFunc{
		ToolSendMessage:     b.sendMessage,
		ToolGetConversation: b.getConversation,
		ToolNewConversation: b.newConversation,
		ToolStopTask:        b.stopTask,
		ToolGetStatus:       b.getStatus,
	}
	for _, t := range Tools() {
		s.AddTool(t, handlers[t.Name])
	}
	return s
}

var errUnavailable = errors.New("not available in this OpenUAI build")

func (b Backend) sendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if b.SendMessage == nil {
		return nil, errUnavailable
	}
	msg, err := req.RequireString("message")
	if err != nil || msg == "" {
		return mcp.NewToolResultError("message is required"), nil
	}

	done := make(chan Reply, 1)
	go func() { done <- b.SendMessage(msg) }()

	var token mcp.ProgressToken
	if req.Params.Meta != nil {
		token = req.Params.Meta.ProgressToken
	}
	tick := time.NewTicker(ProgressInterval)
	defer tick.Stop()
	start := time.Now()
	for n := 1; ; n++ {
		select {
		case r := <-done:
			return replyResult(r), nil
		case <-ctx.Done():
			// The client gave up waiting. The turn is not aborted (the call may
			// still be waiting for its turn behind another task): stop_task
			// does that.
			return nil, fmt.Errorf("request cancelled; the task keeps running in OpenUAI (use %s to stop it)", ToolStopTask)
		case <-tick.C:
			if token == nil {
				continue
			}
			if s := server.ServerFromContext(ctx); s != nil {
				_ = s.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
					"progressToken": token,
					"progress":      n,
					"message":       fmt.Sprintf("OpenUAI is working (%s)", time.Since(start).Round(time.Second)),
				})
			}
		}
	}
}

func replyResult(r Reply) *mcp.CallToolResult {
	if r.Error != "" {
		msg := "OpenUAI error: " + r.Error
		if r.Refused {
			msg = "OpenUAI did not run the message: " + r.Error
		}
		if r.Content != "" {
			msg += "\n\nPartial answer:\n" + r.Content
		}
		return mcp.NewToolResultError(msg)
	}
	text := r.Content
	if text == "" {
		text = "(OpenUAI finished without a text answer)"
	}
	return mcp.NewToolResultText(text)
}

func (b Backend) getConversation(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if b.History == nil {
		return nil, errUnavailable
	}
	limit := req.GetInt("limit", 20)
	h := b.History()
	// Trim to the last entries when the history is a list.
	if limit > 0 {
		if raw, err := json.Marshal(h); err == nil {
			var list []json.RawMessage
			if json.Unmarshal(raw, &list) == nil && len(list) > limit {
				h = list[len(list)-limit:]
			}
		}
	}
	return jsonResult(h)
}

func (b Backend) newConversation(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if b.NewConversation == nil {
		return nil, errUnavailable
	}
	b.NewConversation()
	return mcp.NewToolResultText("Started a new conversation."), nil
}

func (b Backend) stopTask(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if b.Stop == nil {
		return nil, errUnavailable
	}
	b.Stop()
	return mcp.NewToolResultText("Stop requested."), nil
}

func (b Backend) getStatus(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if b.Status == nil {
		return nil, errUnavailable
	}
	return jsonResult(b.Status())
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(raw)), nil
}
