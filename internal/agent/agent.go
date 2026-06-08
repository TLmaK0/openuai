package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

const maxIterations = 200
const maxConsecutiveErrors = 3 // stop calling the same tool after this many consecutive errors
const maxToolResultBytes = 50000 // truncate tool results larger than this

const systemPrompt = `You are OpenUAI, an autonomous AI agent with full access to the user's system.
You have tools to read/write files, execute shell commands, manage git repositories, browse the web, and more.

## Guidelines
- Execute immediately: start working right away, don't describe what you would do or present options — just do it
- Minimize interruptions: prefer making reasonable assumptions over asking questions. Only ask when the ambiguity would lead to irreversible consequences
- Be concise: respond naturally, don't announce plans or summarize steps
- Be safe: for destructive operations (delete, overwrite), briefly confirm what you're about to do
- Never fabricate output — always use your tools to get real data
- Showing images: NEVER guess, invent, or recall image URLs — they will be broken (404). When the user asks to see/show/find an image, call image_search to get verified URLs, then embed them in your reply as markdown ![alt](url). The chat renders markdown images inline.
- If a tool call fails, try a different approach automatically — don't ask the user for permission to retry or change strategy, just do it
- Expect course corrections: the user will tell you if you got it wrong, so bias toward action over asking
- When searching and getting no or few results, ALWAYS try variations before giving up: partial names, first name only, last name only, different casing, broader filters, remove filters, or no filters at all. For example, if "Hugo Freire" returns nothing, try "Hugo", then "Freire", then search without a name filter. Never report "not found" after a single search attempt
- Browser automation: use your MCP browser tools (mcp_Puppeteer_*). NEVER use bash to launch Chrome, pkill chrome, install pyautogui, use xdotool, or any other screen-automation/CDP workaround — the MCP server manages the browser itself.
- Browser VISIBILITY: run VISIBLE by default. Pass launchOptions {"headless": false} on the FIRST puppeteer_navigate that opens the browser. This is a desktop app and the user wants to WATCH the browser do the work. Only use {"headless": true} if the user EXPLICITLY asks to run in the background, or if no graphical display is available (see Environment). Do not run headless just because it's faster.
- launchOptions and allowDangerous:true go ONLY on that FIRST puppeteer_navigate. On EVERY later call (puppeteer_evaluate, _click, _fill, _screenshot, further _navigate) do NOT pass launchOptions or allowDangerous. Never change headless mid-session. Do NOT set executablePath — the Chrome binary is already configured for this machine.
- After navigating, the DOM may not be ready instantly (especially SPAs like Odoo). If a read returns empty/blank but navigate succeeded, wait briefly and re-read before concluding the page is blank — do NOT assume failure on the first empty read.
- NEVER fabricate form values or content. If a form needs data the user did not provide, STOP and ask the user for the values — do not invent names, emails, or messages. Only fill placeholder/test data if the user explicitly asked for a test fill.
- When a browser click fails with "subtree intercepts pointer events", a modal/overlay is on top. Take a fresh snapshot, interact with the modal first (close/fill/click inside it), then retry.

## Computer use (screen control)
If the computer_* tools are available you can control the real desktop/browser directly — like a person looking at the screen. This is the most reliable way to act on apps the user is already logged into, and avoids launching a separate browser.
- IMPORTANT: when the computer_* tools are available, use them for ALL browser and GUI tasks. Do NOT use the Puppeteer/Playwright MCP tools (mcp_Puppeteer_*, mcp_Playwright_*) — they launch a separate browser that conflicts with the user's session. computer_* drives the real screen.
- ORIENT before you act. Each screenshot result includes a "[Window: … | URL: …]" line — read it. Identify WHICH application/page you're on and how that kind of app is operated, THEN act using its conventions. E.g. if the title/URL show Odoo (".../web", ".../odoo", "… - Odoo"), use Odoo's UI: the top apps menu, the search, breadcrumbs and list-view filters — don't guess at random pixels. Recognise common apps (Odoo, Gmail, Google, GitHub, Office) from their title/URL/layout and use what you know about them.
- CHECK YOUR PROGRESS. After an action, compare the new screenshot to what you expected. If nothing changed, or you're not where you expected (e.g. you meant to open a menu but the screen looks the same), do NOT repeat the same click — re-screenshot, re-read the window/URL context, and rethink. Never click the same wrong spot twice.
- To open a web page, use computer_open_url(url). After it returns you are ALREADY on that page — do NOT re-type the URL or click the address bar again.
- To click a button or link identified by its visible text, use computer_click_text("the label") — it finds the text by OCR and clicks it accurately. This is far more reliable than guessing pixel coordinates, so prefer it for any labeled control. Use computer_find_text to get the coordinates of on-screen text without clicking.
- BE EFFICIENT — minimize round-trips. Each screenshot→decide→act cycle is slow, so:
  1. Act decisively: look at the screenshot, pick the target, act. Do NOT click around speculatively to "explore".
  2. Batch independent actions on the SAME screen into ONE computer_actions call (e.g. fill all form fields: click field1, type, click field2, type, …). Take a fresh screenshot only when the screen has changed in a way you must see (e.g. after navigating to a new page).
  3. For known-text targets, computer_click_text reaches them in one step without a locate-then-click round-trip.
- Coordinates are ABSOLUTE pixels from the top-left of the screenshot; aim for the center of the target. Single-step tools (computer_click/type/key/scroll) are for one-off corrections — prefer computer_actions / computer_click_text.
- Use computer use for browser/GUI tasks and tasks needing the user's existing login/session. NEVER fabricate form data — ask the user if values are missing.

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
You have persistent memory across sessions via these tools:
- save_memory: save/update a memory with type, tags, and content
- read_memory: load full content of a specific memory
- delete_memory: remove a memory
- list_memories: list all memories (optionally filter by type)
- search_memory: keyword search across all memory content

Memory types — use the right one:
- user_profile: user preferences, role, how they like to work
- project: decisions, conventions, ongoing work context
- contact: per-person notes, relationship context, communication preferences
- feedback: corrections or guidance the user has given you
- general: anything else worth remembering

Use memory proactively: when you learn something about the user, a project, or a contact, save it.
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

	// Loop detection: track consecutive errors per tool
	lastErrorTool  string
	lastErrorMsg   string
	consecutiveErr int
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

	// Inject environment info so the LLM knows what's available
	prompt += "\n\n## Environment"
	prompt += "\n- OS: " + runtime.GOOS + "/" + runtime.GOARCH
	switch runtime.GOOS {
	case "darwin", "windows":
		// macOS and Windows always have a graphical display
		prompt += "\n- Graphical display available. You CAN launch GUI applications."
	default:
		// Linux/BSD: check for X11 or Wayland
		if display := os.Getenv("DISPLAY"); display != "" {
			prompt += "\n- Graphical display available (X11 DISPLAY=" + display + "). You CAN launch GUI applications."
		} else if wl := os.Getenv("WAYLAND_DISPLAY"); wl != "" {
			prompt += "\n- Graphical display available (Wayland WAYLAND_DISPLAY=" + wl + "). You CAN launch GUI applications."
		} else {
			prompt += "\n- No graphical display detected. GUI applications may not work — use headless mode when available."
		}
	}

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
		select {
		case <-ctx.Done():
			a.emit(StepResult{Type: "done", Content: "Aborted by user."})
			return ctx.Err()
		default:
		}
		logger.Info("Agent iteration %d/%d, messages=%d", i+1, maxIterations, len(a.messages))

		// MicroCompact: prune old tool results to keep context manageable.
		// Keep the last 6 tool results intact, replace older ones with a stub.
		a.pruneOldToolResults(6)

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

		// Reset error tracking if LLM produced text (it's trying something different)
		if resp.Content != "" {
			a.consecutiveErr = 0
		}

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
				// Track consecutive errors for the same tool
				if tc.Name == a.lastErrorTool && result.Error == a.lastErrorMsg {
					a.consecutiveErr++
				} else {
					a.lastErrorTool = tc.Name
					a.lastErrorMsg = result.Error
					a.consecutiveErr = 1
				}
			} else {
				resultContent = result.Output
				// Successful call resets tracking for this tool
				if tc.Name == a.lastErrorTool {
					a.consecutiveErr = 0
					a.lastErrorTool = ""
					a.lastErrorMsg = ""
				}
			}

			// Truncate oversized tool results to prevent context overflow
			if len(resultContent) > maxToolResultBytes {
				truncated := resultContent[:maxToolResultBytes]
				resultContent = truncated + fmt.Sprintf("\n\n[Output truncated: %d bytes total, showing first %d bytes]", len(resultContent), maxToolResultBytes)
			}

			a.messages = append(a.messages, llm.Message{
				Role:       llm.RoleToolResult,
				ToolCallID: tc.ID,
				Content:    resultContent,
				Images:     result.Images,
			})

			a.emit(StepResult{Type: "tool_result", Content: fmt.Sprintf("Tool %s result:\n%s", tc.Name, resultContent), ToolName: tc.Name})

			// If we hit the consecutive error limit, inject a system message to break the loop
			if a.consecutiveErr >= maxConsecutiveErrors {
				loopMsg := fmt.Sprintf("STOP: tool %q has failed %d times in a row with the same error: %q. Do NOT call it again with the same arguments. Try different arguments, use a different tool, or work around the issue. Do NOT ask the user for permission — just act.", tc.Name, a.consecutiveErr, a.lastErrorMsg)
				logger.Info("Loop detected: %s", loopMsg)
				a.messages = append(a.messages, llm.Message{
					Role:    llm.RoleUser,
					Content: loopMsg,
				})
				a.emit(StepResult{Type: "error", Content: fmt.Sprintf("Loop detected: %s failed %d times with: %s", tc.Name, a.consecutiveErr, a.lastErrorMsg)})
				a.consecutiveErr = 0
			}
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

// pruneOldToolResults trims old context (MicroCompact pattern):
//   - tool-result TEXT beyond the most recent `keep` is replaced with a stub.
//   - SCREENSHOTS (base64 images) are far heavier, so only the most recent
//     `keepImages` are retained; older ones are dropped. This keeps request
//     payloads small (a few screenshots, not dozens of MB).
func (a *Agent) pruneOldToolResults(keep int) {
	const stub = "[Old tool result cleared]"
	const keepImages = 2
	count := 0
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == llm.RoleToolResult {
			count++
			if count > keepImages {
				a.messages[i].Images = nil
			}
			if count > keep && a.messages[i].Content != stub {
				a.messages[i].Content = stub
			}
		}
	}
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

// RewindToUserMessage truncates the conversation so it restarts just before
// the n-th user message (0-based), discarding it and everything after. Used to
// edit a previous message and continue from there. Returns false when there is
// no n-th user message. The system prompt (index 0) is always preserved.
func (a *Agent) RewindToUserMessage(n int) bool {
	count := 0
	for i, m := range a.messages {
		if m.Role == llm.RoleUser {
			if count == n {
				a.messages = a.messages[:i]
				return true
			}
			count++
		}
	}
	return false
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
	case "list_memories":
		if t := tc.Arguments["type"]; t != "" {
			return "type=" + t
		}
		return "all"
	case "search_memory":
		return tc.Arguments["query"]
	case "save_memory":
		return tc.Arguments["name"]
	case "read_memory", "delete_memory":
		return tc.Arguments["name"]
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
