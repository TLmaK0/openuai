package main

import (
	"context"
	"sync"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/logger"
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

	permMu       sync.Mutex
	permResponse chan permAnswer
}

type permAnswer struct {
	level    agent.PermissionLevel
	approved bool
}

func NewApp() *App {
	return &App{
		costTracker:  llm.NewCostTracker(),
		permResponse: make(chan permAnswer, 1),
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
