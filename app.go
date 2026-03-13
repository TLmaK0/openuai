package main

import (
	"context"

	"openuai/internal/config"
	"openuai/internal/llm"
)

type App struct {
	ctx         context.Context
	cfg         *config.Config
	claude      *llm.ClaudeProvider
	openai      *llm.OpenAIProvider
	oauth       *llm.OAuthFlow
	costTracker *llm.CostTracker
	messages    []llm.Message
}

func NewApp() *App {
	return &App{
		costTracker: llm.NewCostTracker(),
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

	// Claude provider
	a.claude = llm.NewClaudeProvider(cfg.ClaudeAPIKey)

	// OpenAI OAuth provider
	a.oauth = llm.NewOAuthFlow(func(tokens *llm.OAuthTokens) {
		a.cfg.OpenAITokens = &config.OAuthTokens{
			AccessToken:  tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			ExpiresAt:    tokens.ExpiresAt,
			AccountID:    tokens.AccountID,
		}
		a.cfg.Save()
	})

	// Restore saved tokens
	if cfg.OpenAITokens != nil {
		a.oauth.SetTokens(&llm.OAuthTokens{
			AccessToken:  cfg.OpenAITokens.AccessToken,
			RefreshToken: cfg.OpenAITokens.RefreshToken,
			ExpiresAt:    cfg.OpenAITokens.ExpiresAt,
			AccountID:    cfg.OpenAITokens.AccountID,
		})
	}

	a.openai = llm.NewOpenAIProvider(a.oauth)
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
	a.cfg.Provider = provider
	return a.cfg.Save()
}

func (a *App) GetProviders() []string {
	return []string{"openai", "claude"}
}

// --- OpenAI OAuth ---

func (a *App) OpenAILogin() string {
	if err := a.openai.Login(); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) OpenAIIsLoggedIn() bool {
	return a.openai.IsAuthenticated()
}

// --- Claude API Key ---

func (a *App) SetAPIKey(key string) error {
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
	a.cfg.DefaultModel = model
	return a.cfg.Save()
}

// --- Chat ---

type ChatResponse struct {
	Content      string  `json:"content"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Model        string  `json:"model"`
	Error        string  `json:"error,omitempty"`
}

func (a *App) SendMessage(content string) ChatResponse {
	a.messages = append(a.messages, llm.Message{
		Role:    llm.RoleUser,
		Content: content,
	})

	resp, err := a.activeProvider().Chat(a.ctx, a.messages, a.cfg.DefaultModel)
	if err != nil {
		return ChatResponse{Error: err.Error()}
	}

	a.messages = append(a.messages, llm.Message{
		Role:    llm.RoleAssistant,
		Content: resp.Content,
	})

	entry := a.costTracker.Track(resp)

	return ChatResponse{
		Content:      resp.Content,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostUSD:      entry.CostUSD,
		Model:        resp.Model,
	}
}

func (a *App) ClearChat() {
	a.messages = nil
}

func (a *App) GetCostSummary() llm.CostSummary {
	return a.costTracker.Summary()
}

func (a *App) ResetCosts() {
	a.costTracker.Reset()
}
