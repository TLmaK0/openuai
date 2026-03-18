package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/eventbus"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/mcpclient"
	"openuai/internal/rules"
	"openuai/internal/tools"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx         context.Context
	cfg         *config.Config
	claude      *llm.ClaudeProvider
	openai      *llm.OpenAIProvider
	oauth       *llm.OAuthFlow
	costTracker *llm.CostTracker
	registry    *tools.Registry
	permissions *agent.PermissionManager
	currentAgent *agent.Agent
	eventBus    *eventbus.Bus
	rulesEngine *rules.Engine
	mcpManager  *mcpclient.Manager

	permMu       sync.Mutex
	permResponse chan permAnswer

	agentMu sync.Mutex // serializes all agent.Run() calls (user messages + autonomous triggers)

	watchedChats   map[string]struct{} // JIDs being watched; all messages (including own) are processed
	watchedChatsMu sync.RWMutex
}

type permAnswer struct {
	level    agent.PermissionLevel
	approved bool
}

func NewApp() *App {
	return &App{
		costTracker:  llm.NewCostTracker(),
		permResponse: make(chan permAnswer, 1),
		watchedChats: make(map[string]struct{}),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	cfg, err := config.Load()
	if err != nil {
		println("Error loading config:", err.Error())
		cfg = &config.Config{Provider: "openai", DefaultModel: "gpt-5.1-codex"}
	}
	a.cfg = cfg

	// Init logger
	if err := logger.Init(cfg.ConfigDir()); err != nil {
		println("Error init logger:", err.Error())
	}
	logger.Info("OpenUAI starting up")
	logger.Info("Config dir: %s", cfg.ConfigDir())
	logger.Info("Provider: %s, Model: %s", cfg.Provider, cfg.DefaultModel)

	// Claude provider
	a.claude = llm.NewClaudeProvider(cfg.ClaudeAPIKey)

	// OpenAI OAuth provider
	a.oauth = llm.NewOAuthFlow(func(tokens *llm.OAuthTokens) {
		logger.Info("OAuth tokens updated")
		a.cfg.OpenAITokens = &config.OAuthTokens{
			AccessToken:  tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			ExpiresAt:    tokens.ExpiresAt,
			AccountID:    tokens.AccountID,
		}
		a.cfg.Save()
	})

	if cfg.OpenAITokens != nil {
		logger.Info("Restoring saved OAuth tokens (account: %s)", cfg.OpenAITokens.AccountID)
		a.oauth.SetTokens(&llm.OAuthTokens{
			AccessToken:  cfg.OpenAITokens.AccessToken,
			RefreshToken: cfg.OpenAITokens.RefreshToken,
			ExpiresAt:    cfg.OpenAITokens.ExpiresAt,
			AccountID:    cfg.OpenAITokens.AccountID,
		})
	}

	a.openai = llm.NewOpenAIProvider(a.oauth)

	// Tools registry
	a.registry = tools.NewRegistry()
	a.registry.Register(tools.ReadFile{})
	a.registry.Register(tools.WriteFile{})
	a.registry.Register(tools.ListDir{})
	a.registry.Register(tools.DeleteFile{})
	a.registry.Register(tools.MoveFile{})
	a.registry.Register(tools.SearchFiles{})
	a.registry.Register(tools.Bash{})
	a.registry.Register(tools.BashSudo{})
	a.registry.Register(tools.GitStatus{})
	a.registry.Register(tools.GitDiff{})
	a.registry.Register(tools.GitLog{})
	a.registry.Register(tools.GitAdd{})
	a.registry.Register(tools.GitCommit{})
	a.registry.Register(tools.GitBranch{})
	a.registry.Register(tools.WebFetch{})
	a.registry.Register(tools.WatchChat{Fn: a.WatchChat})
	a.registry.Register(tools.UnwatchChat{Fn: a.UnwatchChat})
	logger.Info("Registered %d tools", len(a.registry.Definitions()))

	// Permissions
	a.permissions = agent.NewPermissionManager(
		cfg.ConfigDir(),
		func(tool, command string) (agent.PermissionLevel, bool) {
			logger.Info("Permission request: tool=%s command=%s", tool, command)
			wailsRuntime.EventsEmit(a.ctx, "permission_request", map[string]string{
				"tool":    tool,
				"command": command,
			})
			answer := <-a.permResponse
			logger.Info("Permission response: approved=%v level=%v", answer.approved, answer.level)
			return answer.level, answer.approved
		},
	)

	// Rules engine
	rulesDir := filepath.Join(cfg.ConfigDir(), "rules")
	a.rulesEngine = rules.New(rulesDir, func(rule rules.Rule, action rules.Action, event eventbus.Event, rendered rules.RenderedAction) error {
		logger.Info("Rule %q fired: action=%s", rule.ID, action.Type)
		wailsRuntime.EventsEmit(a.ctx, "rule_fired", map[string]interface{}{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"action":    string(action.Type),
			"text":      rendered.Text,
			"source":    event.Source,
			"event_type": string(event.Type),
		})
		return nil
	})
	if err := a.rulesEngine.Load(); err != nil {
		logger.Error("Failed to load rules: %s", err.Error())
	}

	// Event bus
	a.eventBus = eventbus.New()
	a.eventBus.OnAny(func(event eventbus.Event) error {
		logger.Info("Event received: source=%s type=%s payload_len=%d", event.Source, event.Type, len(event.Payload))
		wailsRuntime.EventsEmit(a.ctx, "event_received", event)
		return nil
	})
	// Connect rules engine to event bus
	a.eventBus.OnAny(a.rulesEngine.HandleEvent)
	// Immediately trigger the agent when a message from a watched chat arrives.
	// All messages are processed (including own) — the user explicitly chose to watch this chat.
	// is_from_me messages in unwatched chats are ignored to avoid noise from normal WA activity.
	a.eventBus.On(eventbus.EventMessage, func(event eventbus.Event) error {
		chatJID := event.Metadata["chat_jid"]
		sender := event.Metadata["sender"]

		a.watchedChatsMu.RLock()
		_, watchedByChat := a.watchedChats[chatJID]
		_, watchedBySender := a.watchedChats[sender]
		a.watchedChatsMu.RUnlock()

		if !watchedByChat && !watchedBySender {
			logger.Debug("Event ignored (not watched): chat_jid=%q sender=%q", chatJID, sender)
			return nil
		}

		fromMe := event.Metadata["is_from_me"] == "true"
		senderName := event.Metadata["sender_name"]
		if senderName == "" {
			if fromMe {
				senderName = "me"
			} else {
				senderName = sender
			}
		}
		notification := fmt.Sprintf("[New %s message from %s (chat: %s): %s]", event.Source, senderName, chatJID, event.Payload)
		logger.Info("Watched event received, triggering agent: %s", notification)
		go a.triggerAgentWithNotification(notification)
		return nil
	})
	a.eventBus.Start(ctx)
	logger.Info("EventBus started")

	// MCP manager (if servers configured)
	if len(cfg.MCPServers) > 0 {
		a.mcpManager = mcpclient.NewManager(cfg.MCPServers)
		a.mcpManager.OnReady(func() {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager)
			logger.Info("MCP tools registered: %d total tools now", len(a.registry.Definitions()))
			// Reset agent so it picks up new tools
			a.currentAgent = nil
		})
		if err := a.eventBus.RegisterSource(a.mcpManager); err != nil {
			logger.Error("Failed to register MCP manager: %s", err.Error())
		} else {
			logger.Info("MCP manager registered with %d servers", len(cfg.MCPServers))
		}
	}

	// Restore watched chats from config
	for _, jid := range cfg.WatchedChats {
		a.watchedChats[jid] = struct{}{}
	}
	if len(cfg.WatchedChats) > 0 {
		logger.Info("Restored %d watched chats: %v", len(cfg.WatchedChats), cfg.WatchedChats)
	}

	logger.Info("Startup complete")
}

func (a *App) activeProvider() llm.Provider {
	if a.cfg.Provider == "claude" {
		return a.claude
	}
	return a.openai
}

// --- Provider management ---

func (a *App) GetProvider() string {
	return a.cfg.Provider
}

func (a *App) SetProvider(provider string) error {
	logger.Info("Provider changed to: %s", provider)
	a.cfg.Provider = provider
	a.currentAgent = nil // reset agent for new provider
	return a.cfg.Save()
}

func (a *App) GetProviders() []string {
	return []string{"openai", "claude"}
}

// --- OpenAI OAuth ---

func (a *App) OpenAILogin() string {
	logger.Info("Starting OpenAI OAuth login")
	if err := a.openai.Login(); err != nil {
		logger.Error("OpenAI login failed: %s", err.Error())
		return err.Error()
	}
	logger.Info("OpenAI login successful")
	return ""
}

func (a *App) OpenAIIsLoggedIn() bool {
	return a.openai.IsAuthenticated()
}

// --- Claude API Key ---

func (a *App) SetAPIKey(key string) error {
	logger.Info("Claude API key updated")
	a.claude.SetAPIKey(key)
	a.cfg.ClaudeAPIKey = key
	return a.cfg.Save()
}

func (a *App) HasAPIKey() bool {
	return a.cfg.ClaudeAPIKey != ""
}

// --- Models ---

func (a *App) GetModels() []string {
	return a.activeProvider().Models()
}

func (a *App) GetDefaultModel() string {
	return a.cfg.DefaultModel
}

func (a *App) SetDefaultModel(model string) error {
	logger.Info("Model changed to: %s", model)
	a.cfg.DefaultModel = model
	a.currentAgent = nil // reset agent for new model
	return a.cfg.Save()
}

// --- Permission response from UI ---

func (a *App) RespondPermission(level string, approved bool) {
	var pl agent.PermissionLevel
	switch level {
	case "session":
		pl = agent.PermSession
	case "forever":
		pl = agent.PermForever
	default:
		pl = agent.PermAlways
	}
	a.permResponse <- permAnswer{level: pl, approved: approved}
}

// --- Agent Chat ---

type ChatResponse struct {
	Content      string  `json:"content"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Model        string  `json:"model"`
	Error        string  `json:"error,omitempty"`
}

func (a *App) ensureAgent() *agent.Agent {
	if a.currentAgent == nil {
		a.currentAgent = agent.New(agent.Config{
			Provider:    a.activeProvider(),
			Model:       a.cfg.DefaultModel,
			Registry:    a.registry,
			Permissions: a.permissions,
			CostTracker: a.costTracker,
			OnStep: func(step agent.StepResult) {
				logger.Debug("Agent step: type=%s tool=%s content_len=%d", step.Type, step.ToolName, len(step.Content))
				wailsRuntime.EventsEmit(a.ctx, "agent_step", step)
			},
		})
		// Try to restore previous session
		if err := a.currentAgent.LoadSession(a.cfg.ConfigDir()); err != nil {
			logger.Error("Failed to load session: %s", err.Error())
		}
	}
	return a.currentAgent
}

func (a *App) SendMessage(content string) ChatResponse {
	logger.Info("SendMessage: %s", content)

	a.agentMu.Lock()
	defer a.agentMu.Unlock()

	ag := a.ensureAgent()
	err := ag.Run(a.ctx, content)

	// Save session after each interaction (success or failure)
	if saveErr := ag.SaveSession(a.cfg.ConfigDir()); saveErr != nil {
		logger.Error("Failed to save session: %s", saveErr.Error())
	}

	if err != nil {
		logger.Error("Agent run error: %s", err.Error())
		return ChatResponse{Error: err.Error()}
	}

	summary := a.costTracker.Summary()
	logger.Info("Agent completed: tokens_in=%d tokens_out=%d cost=$%.4f",
		summary.TotalInputTokens, summary.TotalOutputTokens, summary.TotalCostUSD)

	return ChatResponse{
		Content:      "",
		InputTokens:  summary.TotalInputTokens,
		OutputTokens: summary.TotalOutputTokens,
		CostUSD:      summary.TotalCostUSD,
	}
}

// triggerAgentWithNotification immediately runs the agent with an event notification.
// It acquires agentMu so it serializes with SendMessage and other autonomous triggers.
func (a *App) triggerAgentWithNotification(notification string) {
	a.agentMu.Lock()
	defer a.agentMu.Unlock()

	ag := a.ensureAgent()
	logger.Info("Autonomous agent run starting: %s", notification)
	if err := ag.Run(a.ctx, notification); err != nil {
		logger.Error("Autonomous agent run error: %s", err.Error())
	}
	if saveErr := ag.SaveSession(a.cfg.ConfigDir()); saveErr != nil {
		logger.Error("Failed to save session after autonomous run: %s", saveErr.Error())
	}
	logger.Info("Autonomous agent run complete")
}

// WatchChat adds a JID or phone number to the event watch list.
// All messages from this chat are processed, including own messages.
// Called by the agent via the watch_chat tool.
func (a *App) WatchChat(jid string) string {
	a.watchedChatsMu.Lock()
	a.watchedChats[jid] = struct{}{}
	a.watchedChatsMu.Unlock()
	a.persistWatchedChats()
	logger.Info("Now watching chat: %s", jid)
	return fmt.Sprintf("Now watching %s for new messages", jid)
}

// UnwatchChat removes a JID from the event watch list.
func (a *App) UnwatchChat(jid string) string {
	a.watchedChatsMu.Lock()
	delete(a.watchedChats, jid)
	a.watchedChatsMu.Unlock()
	a.persistWatchedChats()
	logger.Info("Stopped watching chat: %s", jid)
	return fmt.Sprintf("Stopped watching %s", jid)
}

// persistWatchedChats saves the current watched JIDs to config so they survive restarts.
func (a *App) persistWatchedChats() {
	a.watchedChatsMu.RLock()
	jids := make([]string, 0, len(a.watchedChats))
	for jid := range a.watchedChats {
		jids = append(jids, jid)
	}
	a.watchedChatsMu.RUnlock()
	a.cfg.WatchedChats = jids
	a.cfg.Save()
}

// GetWatchedChats returns the list of watched JIDs.
func (a *App) GetWatchedChats() []string {
	a.watchedChatsMu.RLock()
	defer a.watchedChatsMu.RUnlock()
	result := make([]string, 0, len(a.watchedChats))
	for jid := range a.watchedChats {
		result = append(result, jid)
	}
	return result
}


func (a *App) ClearChat() {
	logger.Info("Chat cleared")
	a.currentAgent = nil
	a.costTracker.Reset()
	agent.ClearSession(a.cfg.ConfigDir())
}

func (a *App) GetCostSummary() llm.CostSummary {
	return a.costTracker.Summary()
}

func (a *App) ResetCosts() {
	a.costTracker.Reset()
}

// GetLogPath returns the log file path for debugging
func (a *App) GetLogPath() string {
	return logger.Path()
}

// --- Event Bus ---

// GetEventSources returns the names of all registered event sources.
func (a *App) GetEventSources() []string {
	return a.eventBus.Sources()
}

// GetEventStats returns event bus statistics.
func (a *App) GetEventStats() eventbus.Stats {
	return a.eventBus.GetStats()
}

// --- Rules Engine ---

// GetRules returns all loaded rules.
func (a *App) GetRules() []rules.Rule {
	return a.rulesEngine.Rules()
}

// ReloadRules hot-reloads rules from disk.
func (a *App) ReloadRules() string {
	if err := a.rulesEngine.Reload(); err != nil {
		return err.Error()
	}
	return ""
}

// --- MCP Servers ---

// MCPServerStatus holds the status of an MCP server for the UI.
type MCPServerStatus struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	AutoStart bool   `json:"auto_start"`
	Connected bool   `json:"connected"`
	Tools     int    `json:"tools"`
	Resources int    `json:"resources"`
}

// GetMCPServers returns configured MCP servers and their status.
func (a *App) GetMCPServers() []MCPServerStatus {
	servers := make([]MCPServerStatus, 0, len(a.cfg.MCPServers))
	for _, cfg := range a.cfg.MCPServers {
		status := MCPServerStatus{
			Name:      cfg.Name,
			Command:   cfg.Command,
			AutoStart: cfg.AutoStart,
		}
		if a.mcpManager != nil {
			conn, ok := a.mcpManager.GetConnection(cfg.Name)
			if ok {
				status.Connected = conn != nil
				status.Tools = len(conn.Tools())
				status.Resources = len(conn.Resources())
			}
		}
		servers = append(servers, status)
	}
	return servers
}

// AddMCPServer adds a new MCP server configuration.
func (a *App) AddMCPServer(name, command string, args []string, env map[string]string, autoStart bool, subscribe []string) string {
	for _, s := range a.cfg.MCPServers {
		if s.Name == name {
			return "server with that name already exists"
		}
	}
	a.cfg.MCPServers = append(a.cfg.MCPServers, config.MCPServerConfig{
		Name:      name,
		Command:   command,
		Args:      args,
		Env:       env,
		AutoStart: autoStart,
		Subscribe: subscribe,
	})
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	logger.Info("MCP server %q added", name)
	return ""
}

// --- Sessions ---

// GetSessions returns all archived sessions.
func (a *App) GetSessions() []agent.SessionInfo {
	return agent.ListSessions(a.cfg.ConfigDir())
}

// ResumeSession loads an archived session by ID.
func (a *App) ResumeSession(id string) string {
	logger.Info("Resuming session: %s", id)
	a.currentAgent = nil
	a.costTracker.Reset()
	ag := a.ensureAgent()
	if err := ag.LoadSessionByID(a.cfg.ConfigDir(), id); err != nil {
		return err.Error()
	}
	return ""
}

// DeleteSession removes an archived session.
func (a *App) DeleteSession(id string) string {
	if err := agent.DeleteSession(a.cfg.ConfigDir(), id); err != nil {
		return err.Error()
	}
	return ""
}

// CallMCPTool calls a tool on a specific MCP server directly (without going through the agent).
// Returns the tool output as a string. Used for UI flows like WhatsApp QR pairing.
func (a *App) CallMCPTool(serverName, toolName string, args map[string]string) string {
	if a.mcpManager == nil {
		return `{"error":"no MCP manager"}`
	}
	// Convert string args to any
	mcpArgs := make(map[string]any, len(args))
	for k, v := range args {
		mcpArgs[k] = v
	}
	result, err := a.mcpManager.CallTool(a.ctx, serverName, toolName, mcpArgs)
	if err != nil {
		logger.Error("CallMCPTool %s.%s error: %s", serverName, toolName, err.Error())
		return `{"error":"` + err.Error() + `"}`
	}
	// Extract content
	output := mcpclient.ContentToString(result.Content)
	logger.Info("CallMCPTool %s.%s result: %s", serverName, toolName, output[:min(len(output), 120)])
	if result.IsError {
		return `{"error":"` + output + `"}`
	}
	return output
}

// RemoveMCPServer removes an MCP server configuration by name.
func (a *App) RemoveMCPServer(name string) string {
	idx := -1
	for i, s := range a.cfg.MCPServers {
		if s.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return "server not found"
	}
	a.cfg.MCPServers = append(a.cfg.MCPServers[:idx], a.cfg.MCPServers[idx+1:]...)
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	logger.Info("MCP server %q removed", name)
	return ""
}

