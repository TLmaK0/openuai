package agent

import (
	"context"
	"fmt"
	"strings"

	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

const maxIterations = 50

const systemPrompt = `You are OpenUAI, an autonomous AI agent with full access to the user's system.
You have tools to read/write files, execute shell commands, manage git repositories, browse the web, and more.

## Guidelines
- Act directly: use your tools to accomplish the task, don't describe what you would do
- Be concise: respond naturally, don't announce plans or summarize steps
- Be safe: for destructive operations (delete, overwrite), briefly confirm what you're about to do
- Never fabricate output — always use your tools to get real data
- If a task is ambiguous, ask a brief clarifying question instead of guessing

## Real-time message monitoring (IMPORTANT)
To receive messages in real-time you MUST call watch_chat with the chat ID. Without watch_chat, NO messages will reach you.
When the user says "when a message arrives", "monitor", "watch", "respond to messages", etc. → ALWAYS call watch_chat first.
Steps: 1) call list_chats to find the chat ID, 2) call watch_chat with that ID.
- Teams chat IDs: "19:xxx@unq.gbl.spaces". Self-chat/personal notes: "48:notes".
- WhatsApp: "34612345678@s.whatsapp.net".

Once watched, notifications arrive as: [New source message from sender (chat: chat_jid): message_body]
To reply: Teams → mcp_teams_send_chat_message, WhatsApp → mcp_whatsapp_send_message.
To stop: unwatch_chat.

## Memory
You have persistent memory across sessions via save_memory, list_memories, and delete_memory tools.
Use memory proactively: save user preferences, important context, ongoing tasks, and per-contact notes.
Memory is injected into your system prompt at the start of each new session.`

type StepResult struct {
	Type     string `json:"type"` // "text", "tool_call", "tool_result", "error", "done"
	Content  string `json:"content"`
	ToolName string `json:"tool_name,omitempty"`
}

type Agent struct {
	provider    llm.Provider
	model       string
	registry    *tools.Registry
	permissions *PermissionManager
	costTracker *llm.CostTracker
	messages    []llm.Message
	toolDefs    []llm.ToolDefinition
	onStep      func(StepResult) // callback for each step
}

type Config struct {
	Provider    llm.Provider
	Model       string
	Registry    *tools.Registry
	Permissions *PermissionManager
	CostTracker *llm.CostTracker
	OnStep      func(StepResult)
	MemoryText  string // pre-loaded memory content injected into system prompt
}

func New(cfg Config) *Agent {
	// Convert tools.Definition to llm.ToolDefinition
	toolDefs := convertToolDefs(cfg.Registry)

	prompt := systemPrompt
	if cfg.MemoryText != "" {
		prompt += "\n\n## Memory\nThe following is what you remember from previous sessions:\n\n" + cfg.MemoryText
	}

	return &Agent{
		provider:    cfg.Provider,
		model:       cfg.Model,
		registry:    cfg.Registry,
		permissions: cfg.Permissions,
		costTracker: cfg.CostTracker,
		onStep:      cfg.OnStep,
		toolDefs:    toolDefs,
		messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompt},
		},
	}
}

// InjectEvent adds an event notification to the conversation context without
// triggering the agent loop. The agent will see it on the next Run call.
func (a *Agent) InjectEvent(notification string) {
	a.messages = append(a.messages, llm.Message{
		Role:    llm.RoleUser,
		Content: notification,
	})
}

func (a *Agent) Run(ctx context.Context, userMessage string) error {
	a.messages = append(a.messages, llm.Message{
		Role:    llm.RoleUser,
		Content: userMessage,
	})
	logger.Debug("System prompt length: %d", len(a.messages[0].Content))
	logger.Debug("User message: %s", userMessage)

	// Check if provider supports native tool calling
	toolProvider, hasNativeTools := a.provider.(llm.ToolCallProvider)
	if !hasNativeTools {
		logger.Info("Provider does not support native tool calling, running simple chat")
		return a.runSimpleChat(ctx)
	}

	for i := 0; i < maxIterations; i++ {
		logger.Info("Agent iteration %d/%d, messages=%d", i+1, maxIterations, len(a.messages))

		resp, toolCalls, err := toolProvider.ChatWithTools(ctx, a.messages, a.model, a.toolDefs)
		if err != nil {
			logger.Error("LLM call failed: %s", err.Error())
			a.emit(StepResult{Type: "error", Content: err.Error()})
			return err
		}
		logger.Info("LLM response: model=%s tokens_in=%d tokens_out=%d content_len=%d tool_calls=%d",
			resp.Model, resp.InputTokens, resp.OutputTokens, len(resp.Content), len(toolCalls))

		if a.costTracker != nil {
			a.costTracker.Track(resp)
		}

		// If there's text content, emit it
		if resp.Content != "" {
			if len(toolCalls) == 0 {
				// No tool calls — agent is done
				a.messages = append(a.messages, llm.Message{
					Role:    llm.RoleAssistant,
					Content: resp.Content,
				})
				a.emit(StepResult{Type: "done", Content: resp.Content})
				return nil
			}
			a.emit(StepResult{Type: "text", Content: resp.Content})
		}

		if len(toolCalls) == 0 {
			// No content and no tool calls — done
			logger.Info("Agent done (no tool calls, no content)")
			a.emit(StepResult{Type: "done", Content: resp.Content})
			return nil
		}

		// Add assistant message to history (include tool calls so they appear in the API input)
		a.messages = append(a.messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: toolCalls,
		})

		// Process each tool call
		for _, tc := range toolCalls {
			tool, ok := a.registry.Get(tc.Name)
			if !ok {
				errMsg := fmt.Sprintf("Unknown tool: %s", tc.Name)
				logger.Error("%s", errMsg)
				a.messages = append(a.messages, llm.Message{
					Role:       llm.RoleToolResult,
					ToolCallID: tc.ID,
					Content:    "Error: " + errMsg,
				})
				a.emit(StepResult{Type: "error", Content: errMsg})
				continue
			}

			def := tool.Definition()
			commandDesc := formatNativeToolCallDesc(tc)

			logger.Info("Tool call: %s id=%s args=%v", tc.Name, tc.ID, tc.Arguments)
			a.emit(StepResult{Type: "tool_call", Content: commandDesc, ToolName: tc.Name})

			if !a.permissions.Check(def.Name, def.RequiresPermission, commandDesc) {
				errMsg := "Permission denied for: " + tc.Name
				a.messages = append(a.messages, llm.Message{
					Role:       llm.RoleToolResult,
					ToolCallID: tc.ID,
					Content:    "Error: " + errMsg + ". The user denied this action.",
				})
				a.emit(StepResult{Type: "error", Content: errMsg})
				continue
			}

			// Execute tool
			logger.Info("Executing tool: %s", tc.Name)
			result := tool.Execute(ctx, tc.Arguments)
			logger.Info("Tool result: output_len=%d error=%q", len(result.Output), result.Error)

			var resultContent string
			if result.Error != "" {
				resultContent = fmt.Sprintf("Error: %s", result.Error)
				if result.Output != "" {
					resultContent += "\nOutput: " + result.Output
				}
			} else {
				resultContent = result.Output
			}

			a.messages = append(a.messages, llm.Message{
				Role:       llm.RoleToolResult,
				ToolCallID: tc.ID,
				Content:    resultContent,
			})

			a.emit(StepResult{Type: "tool_result", Content: fmt.Sprintf("Tool %s result:\n%s", tc.Name, resultContent), ToolName: tc.Name})
		}
	}

	a.emit(StepResult{Type: "error", Content: "Agent reached maximum iterations"})
	return fmt.Errorf("agent reached maximum iterations (%d)", maxIterations)
}

// runSimpleChat handles providers without native tool calling (fallback)
func (a *Agent) runSimpleChat(ctx context.Context) error {
	resp, err := a.provider.Chat(ctx, a.messages, a.model)
	if err != nil {
		a.emit(StepResult{Type: "error", Content: err.Error()})
		return err
	}
	if a.costTracker != nil {
		a.costTracker.Track(resp)
	}
	a.messages = append(a.messages, llm.Message{
		Role:    llm.RoleAssistant,
		Content: resp.Content,
	})
	a.emit(StepResult{Type: "done", Content: resp.Content})
	return nil
}

// LastAssistantContent scans messages backwards and returns the content
// of the last assistant message. Used by sub-agent result collection.
func (a *Agent) LastAssistantContent() string {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == llm.RoleAssistant && a.messages[i].Content != "" {
			return a.messages[i].Content
		}
	}
	return ""
}

func (a *Agent) emit(step StepResult) {
	if a.onStep != nil {
		a.onStep(step)
	}
}

func convertToolDefs(registry *tools.Registry) []llm.ToolDefinition {
	var defs []llm.ToolDefinition
	for _, td := range registry.Definitions() {
		var params []llm.ToolParam
		for _, p := range td.Parameters {
			params = append(params, llm.ToolParam{
				Name:        p.Name,
				Type:        p.Type,
				Description: p.Description,
				Required:    p.Required,
			})
		}
		defs = append(defs, llm.ToolDefinition{
			Name:        td.Name,
			Description: td.Description,
			Parameters:  params,
		})
	}
	return defs
}

func formatNativeToolCallDesc(tc llm.ToolCall) string {
	// Show the most relevant argument as plain text
	// e.g. bash: ls -la, read_file: /etc/hosts, git_status
	switch tc.Name {
	case "bash", "bash_sudo":
		if cmd := tc.Arguments["command"]; cmd != "" {
			return cmd
		}
	case "read_file", "write_file", "delete_file", "list_dir", "search_files":
		if p := tc.Arguments["path"]; p != "" {
			return p
		}
		if p := tc.Arguments["pattern"]; p != "" {
			return p
		}
	case "move_file":
		return tc.Arguments["source"] + " → " + tc.Arguments["destination"]
	case "git_commit":
		return tc.Arguments["message"]
	case "git_add":
		return tc.Arguments["files"]
	case "git_branch":
		if n := tc.Arguments["name"]; n != "" {
			return tc.Arguments["action"] + " " + n
		}
		return tc.Arguments["action"]
	case "git_diff":
		return tc.Arguments["args"]
	case "web_fetch":
		return tc.Arguments["url"]
	}
	// Fallback: join all values
	var parts []string
	for _, v := range tc.Arguments {
		if v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return ""
}
