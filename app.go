package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"openuai/internal/agent"
	"openuai/internal/api"
	"openuai/internal/config"
	"openuai/internal/eventbus"
	"openuai/internal/lipreading"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/mcpclient"
	"openuai/internal/memory"
	"openuai/internal/tools"
	"openuai/internal/tray"
	"openuai/internal/updater"
	"openuai/internal/voice"
	"openuai/internal/whisper"

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

	mcpManager  *mcpclient.Manager
	apiServer   *api.Server
	recorder       *voice.Recorder
	lipRecorder    *lipreading.Recorder
	whisperVersion string
	version        string

	permMu       sync.Mutex
	permResponse chan permAnswer

	agentMu sync.Mutex // serializes all agent.Run() calls (user messages + autonomous triggers)

	memoryStore *memory.Store

	watchedChats   map[string]struct{} // JIDs being watched; all messages (including own) are processed
	watchedChatsMu sync.RWMutex

	// recentSentIDs tracks message IDs sent by the agent to prevent echo loops.
	// When the agent sends via MCP, the response includes the message ID.
	// When the same ID arrives back via Trouter, we filter it out.
	recentSentMu  sync.Mutex
	recentSentIDs map[string]int64 // message ID → unix timestamp
}

type permAnswer struct {
	level    agent.PermissionLevel
	approved bool
}

func NewApp() *App {
	return &App{
		costTracker:  llm.NewCostTracker(),
		permResponse: make(chan permAnswer, 1),
		watchedChats:       make(map[string]struct{}),
		recentSentIDs: make(map[string]int64),
		lipRecorder:  lipreading.NewRecorder(),
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
	logger.Info("OpenUAI %s starting up", a.version)
	logger.Info("Config dir: %s", cfg.ConfigDir())
	logger.Info("Provider: %s, Model: %s", cfg.Provider, cfg.DefaultModel)

	// Check for updates in background (delay to let frontend mount and register event listeners)
	go func() {
		time.Sleep(3 * time.Second)
		info := updater.CheckForUpdate(a.version, cfg.SkippedVersion)
		if info != nil {
			logger.Info("Emitting update_available event to frontend")
			wailsRuntime.EventsEmit(a.ctx, "update_available", info)
		}
	}()

	// Auto-download whisper-cli + model in background
	go func() {
		sttModel := cfg.STTModel
		if sttModel == "" {
			sttModel = "small"
		}
		if err := whisper.EnsureReady(cfg.ConfigDir(), a.whisperVersion, sttModel); err != nil {
			logger.Error("Whisper setup: %s", err.Error())
		}
	}()

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

	// Voice recorder (uses local parecord/arecord + whisper + espeak-ng)
	a.recorder = voice.NewRecorder()
	a.recorder.Device = cfg.AudioDevice
	a.recorder.OnLevel = func(level int) {
		wailsRuntime.EventsEmit(a.ctx, "voice_level", level)
	}

	// Memory store
	a.memoryStore = memory.New(filepath.Join(cfg.ConfigDir(), "memory"))

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
	a.registry.Register(tools.SaveMemory{Store: a.memoryStore})
	a.registry.Register(tools.ReadMemory{Store: a.memoryStore})
	a.registry.Register(tools.DeleteMemory{Store: a.memoryStore})
	a.registry.Register(tools.SpawnAgents{Fn: a.spawnSubAgents})
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
			if a.apiServer != nil {
				a.apiServer.Broadcast("permission_request", "", map[string]string{
					"tool":    tool,
					"command": command,
				})
			}
			answer := <-a.permResponse
			logger.Info("Permission response: approved=%v level=%v", answer.approved, answer.level)
			return answer.level, answer.approved
		},
	)


	// Event bus
	a.eventBus = eventbus.New()
	a.eventBus.OnAny(func(event eventbus.Event) error {
		logger.Info("Event received: source=%s type=%s payload_len=%d", event.Source, event.Type, len(event.Payload))
		wailsRuntime.EventsEmit(a.ctx, "event_received", event)
		if a.apiServer != nil {
			a.apiServer.Broadcast("event_received", "", event)
		}
		return nil
	})

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

		// Prevent echo loops: if the message ID was sent by the agent, skip it.
		msgID := event.Metadata["message_id"]
		if msgID != "" {
			a.recentSentMu.Lock()
			// Clean old entries (>5min)
			now := time.Now().Unix()
			for k, ts := range a.recentSentIDs {
				if now-ts > 300 {
					delete(a.recentSentIDs, k)
				}
			}
			if _, isEcho := a.recentSentIDs[msgID]; isEcho {
				delete(a.recentSentIDs, msgID)
				a.recentSentMu.Unlock()
				logger.Debug("Event ignored (echo, msg_id=%s): chat_jid=%q", msgID, chatJID)
				return nil
			}
			a.recentSentMu.Unlock()
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
		preview := event.Payload
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		tray.Notify(fmt.Sprintf("%s — %s", event.Source, senderName), preview)
		go a.triggerAgentWithNotification(notification)
		return nil
	})
	a.eventBus.Start(ctx)
	logger.Info("EventBus started")

	// MCP manager (if servers configured)
	if len(cfg.MCPServers) > 0 {
		a.mcpManager = mcpclient.NewManager(cfg.MCPServers)
		a.mcpManager.OnTokensSaved(func(name string, tokens *config.MCPOAuthTokens) {
			for i, s := range a.cfg.MCPServers {
				if s.Name == name {
					a.cfg.MCPServers[i].OAuthTokens = tokens
					a.cfg.Save()
					logger.Info("MCP OAuth tokens saved for %s", name)
					break
				}
			}
		})
		a.mcpManager.OnReady(func() {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
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

	// System tray
	notifyEnabled := true
	if a.cfg.NotificationsEnabled != nil {
		notifyEnabled = *a.cfg.NotificationsEnabled
	}
	tray.SetEnabled(notifyEnabled)
	tray.SetIconBytes(appIcon)
	tray.Start(tray.Config{
		Icon: appIcon,
		OnShow: func() {
			wailsRuntime.WindowShow(a.ctx)
		},
		OnQuit: func() {
			wailsRuntime.Quit(a.ctx)
		},
	})
	a.updateTrayTooltip()

	// API server (optional, disabled by default)
	if cfg.APIEnabled {
		port := cfg.APIPort
		if port == 0 {
			port = 9120
		}
		a.apiServer = api.New(api.Config{
			Host: "127.0.0.1",
			Port: port,
		}, api.Handlers{
			SendMessageSync: func(content string) any {
				return a.SendMessage(content)
			},
			GetCostSummary:    func() any { return a.GetCostSummary() },
			GetModels:         func() any { return a.GetModels() },
			GetModel:          func() any { return a.GetDefaultModel() },
			SetModel:          func(model string) any { return a.SetDefaultModel(model) },
			GetProvider:       func() any { return a.GetProvider() },
			SetProvider:       func(provider string) any { return a.SetProvider(provider) },
			GetWatchedChats:   func() any { return a.GetWatchedChats() },
			WatchChat:         func(jid string) any { return a.WatchChat(jid) },
			UnwatchChat:       func(jid string) any { return a.UnwatchChat(jid) },
			GetEventStats:     func() any { return a.GetEventStats() },
			GetMCPServers:     func() any { return a.GetMCPServers() },
			RespondPermission: func(level string, approved bool) { a.RespondPermission(level, approved) },
			ClearChat:         func() { a.ClearChat() },
			ResetCosts:        func() { a.ResetCosts() },
			GetNotifications: func() any {
				return map[string]bool{"enabled": a.GetNotificationsEnabled()}
			},
			SetNotifications: func(enabled bool) { a.ToggleNotifications(enabled) },
		})
		if err := a.apiServer.Start(); err != nil {
			logger.Error("Failed to start API server: %s", err.Error())
		}
	}

	logger.Info("Startup complete")
}

func (a *App) shutdown(ctx context.Context) {
	logger.Info("Shutting down")
	if a.apiServer != nil {
		a.apiServer.Shutdown(ctx)
	}
	tray.Stop()
}

// ToggleNotifications sets the notification enabled state and persists it.
func (a *App) ToggleNotifications(enabled bool) {
	tray.SetEnabled(enabled)
	tray.SyncNotifyCheckbox()
	a.cfg.NotificationsEnabled = &enabled
	a.cfg.Save()
	logger.Info("Notifications enabled: %v", enabled)
}

// GetNotificationsEnabled returns whether notifications are on.
func (a *App) GetNotificationsEnabled() bool {
	return tray.IsEnabled()
}

func (a *App) updateTrayTooltip() {
	a.watchedChatsMu.RLock()
	n := len(a.watchedChats)
	a.watchedChatsMu.RUnlock()
	if n == 0 {
		tray.SetTooltip("OpenUAI")
	} else {
		tray.SetTooltip(fmt.Sprintf("OpenUAI — %d chats watched", n))
	}
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
			MemoryText:  a.memoryStore.LoadIndex(),
			OnStep: func(step agent.StepResult) {
				logger.Debug("Agent step: type=%s tool=%s content_len=%d", step.Type, step.ToolName, len(step.Content))
				wailsRuntime.EventsEmit(a.ctx, "agent_step", step)
				if a.apiServer != nil {
					a.apiServer.Broadcast("agent_step", "", step)
				}
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
	tray.Notify("OpenUAI", "Agent completed autonomous run")
	logger.Info("Autonomous agent run complete")
}

// spawnSubAgents is the bridge between the spawn_agents tool and the agent package.
func (a *App) spawnSubAgents(ctx context.Context, tasks []tools.SubTask) []tools.SubTaskResult {
	maxConcurrent := a.cfg.MaxConcurrentAgents
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}

	// Convert tool tasks to agent tasks
	agentTasks := make([]agent.SubAgentTask, len(tasks))
	for i, t := range tasks {
		agentTasks[i] = agent.SubAgentTask{ID: t.ID, Description: t.Description}
	}

	// Create filtered registry (no spawn_agents to prevent nesting)
	filteredRegistry := a.registry.Without("spawn_agents")

	results := agent.RunSubAgents(ctx, agent.SubAgentConfig{
		Provider:      a.activeProvider(),
		Model:         a.cfg.DefaultModel,
		Registry:      filteredRegistry,
		Permissions:   a.permissions,
		MaxConcurrent: maxConcurrent,
		OnStep: func(taskID string, step agent.StepResult) {
			logger.Debug("[sub-agent:%s] step: type=%s tool=%s", taskID, step.Type, step.ToolName)
			subStep := agent.StepResult{
				Type:     step.Type,
				Content:  fmt.Sprintf("[sub-agent:%s] %s", taskID, step.Content),
				ToolName: step.ToolName,
			}
			wailsRuntime.EventsEmit(a.ctx, "agent_step", subStep)
			if a.apiServer != nil {
				a.apiServer.Broadcast("agent_step", "", subStep)
			}
		},
	}, agentTasks)

	// Roll up sub-agent costs into parent tracker
	for _, r := range results {
		if r.InputTokens > 0 || r.OutputTokens > 0 {
			a.costTracker.Track(&llm.Response{
				Model:        a.cfg.DefaultModel,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
			})
		}
	}

	// Convert agent results to tool results
	toolResults := make([]tools.SubTaskResult, len(results))
	for i, r := range results {
		toolResults[i] = tools.SubTaskResult{
			ID:           r.ID,
			Output:       r.Output,
			Error:        r.Error,
			CostUSD:      r.CostUSD,
			InputTokens:  r.InputTokens,
			OutputTokens: r.OutputTokens,
		}
	}
	return toolResults
}

// WatchChat adds a JID or phone number to the event watch list.
// All messages from this chat are processed, including own messages.
// Called by the agent via the watch_chat tool.
func (a *App) WatchChat(jid string) string {
	a.watchedChatsMu.Lock()
	a.watchedChats[jid] = struct{}{}
	a.watchedChatsMu.Unlock()
	a.persistWatchedChats()
	a.updateTrayTooltip()
	logger.Info("Now watching chat: %s", jid)
	return fmt.Sprintf("Now watching %s for new messages", jid)
}

// UnwatchChat removes a JID from the event watch list.
func (a *App) UnwatchChat(jid string) string {
	a.watchedChatsMu.Lock()
	delete(a.watchedChats, jid)
	a.watchedChatsMu.Unlock()
	a.persistWatchedChats()
	a.updateTrayTooltip()
	logger.Info("Stopped watching chat: %s", jid)
	return fmt.Sprintf("Stopped watching %s", jid)
}

// persistWatchedChats saves the current watched JIDs to config so they survive restarts.
// TrackSentMessageID records a message ID so it can be filtered as echo when
// it arrives back via Trouter/event bus. Called by MCP tool wrappers after sending.
func (a *App) TrackSentMessageID(msgID string) {
	if msgID == "" {
		return
	}
	a.recentSentMu.Lock()
	a.recentSentIDs[msgID] = time.Now().Unix()
	a.recentSentMu.Unlock()
	logger.Debug("Tracking sent message ID: %s", msgID)
}

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


// --- MCP Servers ---

// MCPServerStatus holds the status of an MCP server for the UI.
type MCPServerStatus struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	URL       string `json:"url,omitempty"`
	AutoStart bool   `json:"auto_start"`
	Connected bool   `json:"connected"`
	Tools     int    `json:"tools"`
	Resources int    `json:"resources"`
	HasAuth   bool   `json:"has_auth"`
	NeedsAuth bool   `json:"needs_auth"`
}

// GetMCPServers returns configured MCP servers and their status.
func (a *App) GetMCPServers() []MCPServerStatus {
	servers := make([]MCPServerStatus, 0, len(a.cfg.MCPServers))
	for _, cfg := range a.cfg.MCPServers {
		status := MCPServerStatus{
			Name:      cfg.Name,
			Command:   cfg.Command,
			URL:       cfg.URL,
			AutoStart: cfg.AutoStart,
		}
		if a.mcpManager != nil {
			conn, ok := a.mcpManager.GetConnection(cfg.Name)
			if ok && conn != nil {
				if conn.NeedsAuth() {
					status.NeedsAuth = true
				} else {
					status.Connected = true
					status.Tools = len(conn.Tools())
					status.Resources = len(conn.Resources())
					for _, t := range conn.Tools() {
						if t.Name == "get_auth_status" || t.Name == "get_qr_code" {
							status.HasAuth = true
							break
						}
					}
				}
			}
		}
		servers = append(servers, status)
	}
	return servers
}

// AddMCPServer adds a new MCP server configuration.
func (a *App) AddMCPServer(name, command string, args []string, env map[string]string, autoStart bool, subscribe []string, url string) string {
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
		URL:       url,
	})
	if err := a.cfg.Save(); err != nil {
		return err.Error()
	}
	logger.Info("MCP server %q added", name)

	// Try to start the new server immediately
	cfg := a.cfg.MCPServers[len(a.cfg.MCPServers)-1]
	if a.mcpManager != nil {
		go func() {
			if err := a.mcpManager.Reconnect(cfg); err != nil {
				logger.Info("MCP server %q: initial connect: %s", name, err.Error())
			} else {
				mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
				a.currentAgent = nil
			}
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name})
		}()
	}
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

// --- Voice ---

// StartRecording begins capturing audio from the system microphone.
func (a *App) StartRecording() string {
	if err := a.recorder.Start(); err != nil {
		return err.Error()
	}
	return ""
}

// StopRecording stops capturing and transcribes the audio via Whisper.
func (a *App) StopRecording() map[string]interface{} {
	audioBase64, err := a.recorder.Stop()
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}
	return a.TranscribeAudio(audioBase64)
}

// TranscribeAudio transcribes base64-encoded audio using local Whisper.
func (a *App) TranscribeAudio(audioBase64 string) map[string]interface{} {
	result := voice.Transcribe(audioBase64, a.cfg.STTModel, a.cfg.STTLanguage, a.cfg.ConfigDir())
	return map[string]interface{}{
		"text":  result.Text,
		"error": result.Error,
	}
}

// SpeakText converts text to speech using local espeak-ng.
func (a *App) SpeakText(text string) map[string]interface{} {
	ttsVoice := a.cfg.TTSVoice
	if ttsVoice == "" {
		ttsVoice = "es"
	}
	result := voice.Speak(text, ttsVoice)
	return map[string]interface{}{
		"audio_base64": result.AudioBase64,
		"format":       result.Format,
		"char_count":   result.CharCount,
		"error":        result.Error,
	}
}

// GetTTSVoice returns the configured TTS voice name.
func (a *App) GetTTSVoice() string {
	if a.cfg.TTSVoice == "" {
		return "es"
	}
	return a.cfg.TTSVoice
}

// SetTTSVoice sets the TTS voice and persists.
func (a *App) SetTTSVoice(v string) error {
	a.cfg.TTSVoice = v
	return a.cfg.Save()
}

// GetSTTLanguage returns the configured STT language (empty or "auto" = auto-detect).
func (a *App) GetSTTLanguage() string {
	if a.cfg.STTLanguage == "" {
		return "auto"
	}
	return a.cfg.STTLanguage
}

// SetSTTLanguage sets the STT language and persists.
func (a *App) SetSTTLanguage(lang string) error {
	a.cfg.STTLanguage = lang
	return a.cfg.Save()
}

// GetVoiceEnabled returns whether voice features are enabled.
func (a *App) GetVoiceEnabled() bool {
	if a.cfg.VoiceEnabled == nil {
		return true // enabled by default when OpenAI is logged in
	}
	return *a.cfg.VoiceEnabled
}

// GetAudioDevices returns available microphone devices.
func (a *App) GetAudioDevices() []voice.AudioDevice {
	return voice.ListDevices()
}

// GetAudioDevice returns the configured audio device ID.
func (a *App) GetAudioDevice() string {
	return a.cfg.AudioDevice
}

// SetAudioDevice sets the audio input device and persists.
func (a *App) SetAudioDevice(deviceID string) error {
	a.cfg.AudioDevice = deviceID
	a.recorder.Device = deviceID
	return a.cfg.Save()
}

// SetVoiceEnabled toggles voice features.
func (a *App) SetVoiceEnabled(enabled bool) error {
	a.cfg.VoiceEnabled = &enabled
	return a.cfg.Save()
}

// --- Update ---

// GetVersion returns the current app version.
func (a *App) GetVersion() string {
	return a.version
}

// ApplyUpdate downloads and installs the update, then signals the frontend.
func (a *App) ApplyUpdate(downloadURL string) string {
	if err := updater.DownloadAndApply(downloadURL); err != nil {
		logger.Error("Update failed: %s", err.Error())
		return err.Error()
	}
	return ""
}

// SkipVersion saves the given version as skipped so it won't prompt again.
func (a *App) SkipVersion(version string) {
	a.cfg.SkippedVersion = version
	a.cfg.Save()
	logger.Info("Skipped update version: %s", version)
}

// --- Beta Features ---

// GetBetaLipReading returns whether the lip reading beta is enabled.
func (a *App) GetBetaLipReading() bool {
	return a.cfg.BetaLipReading
}

// SetBetaLipReading enables or disables the lip reading beta.
func (a *App) SetBetaLipReading(enabled bool) error {
	a.cfg.BetaLipReading = enabled
	logger.Info("Beta lip reading: %v", enabled)
	return a.cfg.Save()
}

// --- Lip Reading ---

// LipReadingModelReady returns whether the lip reading model is downloaded.
func (a *App) LipReadingModelReady() bool {
	return lipreading.IsModelReady(a.cfg.ConfigDir())
}

// DownloadLipReadingModel downloads the model and emits progress events.
func (a *App) DownloadLipReadingModel() string {
	// First ensure repo and python deps
	if err := lipreading.EnsureRepo(a.cfg.ConfigDir()); err != nil {
		return err.Error()
	}
	if err := lipreading.EnsurePythonDeps(); err != nil {
		return err.Error()
	}
	// Download model with progress
	err := lipreading.DownloadModel(a.cfg.ConfigDir(), func(downloaded int64) {
		wailsRuntime.EventsEmit(a.ctx, "lipreading_download_progress", downloaded)
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// StartLipRecording begins capturing video from the webcam.
func (a *App) StartLipRecording() string {
	if err := a.lipRecorder.Start(); err != nil {
		return err.Error()
	}
	return ""
}

// StopLipRecording stops video capture and runs lip reading inference.
func (a *App) StopLipRecording() map[string]interface{} {
	videoBase64, err := a.lipRecorder.Stop()
	if err != nil {
		return map[string]interface{}{"text": "", "error": err.Error()}
	}
	transcript, err := lipreading.Transcribe(videoBase64, a.cfg.ConfigDir())
	if err != nil {
		return map[string]interface{}{"text": "", "error": err.Error()}
	}
	return map[string]interface{}{"text": transcript, "error": ""}
}

// AuthMCPServer triggers the OAuth flow for a server that needs authentication.
func (a *App) AuthMCPServer(name string) string {
	if a.mcpManager == nil {
		return "MCP manager not running"
	}
	go func() {
		if err := a.mcpManager.AuthenticateServer(name); err != nil {
			logger.Error("MCP auth %q failed: %s", name, err.Error())
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name, "error": err.Error()})
		} else {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
			a.currentAgent = nil
			logger.Info("MCP server %q authenticated, %d total tools", name, len(a.registry.Definitions()))
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name})
		}
	}()
	return ""
}

// ReauthMCPServer clears saved tokens and reconnects an HTTP MCP server, triggering OAuth.
func (a *App) ReauthMCPServer(name string) string {
	var cfg config.MCPServerConfig
	found := false
	for i, s := range a.cfg.MCPServers {
		if s.Name == name {
			a.cfg.MCPServers[i].OAuthTokens = nil
			a.cfg.Save()
			cfg = a.cfg.MCPServers[i]
			found = true
			break
		}
	}
	if !found {
		return "server not found"
	}

	if a.mcpManager == nil {
		return "MCP manager not running"
	}

	logger.Info("MCP server %q: clearing tokens and reconnecting", name)
	go func() {
		if err := a.mcpManager.Reconnect(cfg); err != nil {
			logger.Error("MCP server %q reconnect failed: %s", name, err.Error())
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name, "error": err.Error()})
		} else {
			mcpclient.RegisterMCPTools(a.registry, a.mcpManager, a.TrackSentMessageID)
			a.currentAgent = nil
			logger.Info("MCP server %q reconnected, %d total tools", name, len(a.registry.Definitions()))
			wailsRuntime.EventsEmit(a.ctx, "mcp_auth_done", map[string]string{"name": name})
		}
	}()
	return ""
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

