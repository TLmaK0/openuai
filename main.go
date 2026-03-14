package main

import (
	"fmt"
	"os"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
	"openuai/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("Error loading config:", err)
		cfg = &config.Config{Provider: "openai", DefaultModel: "gpt-5.1-codex"}
	}

	if err := logger.Init(cfg.ConfigDir()); err != nil {
		fmt.Println("Error init logger:", err)
	}
	defer logger.Close()
	logger.Info("OpenUAI starting up (TUI)")

	// Claude provider
	claude := llm.NewClaudeProvider(cfg.ClaudeAPIKey)

	// OpenAI OAuth provider
	oauth := llm.NewOAuthFlow(func(tokens *llm.OAuthTokens) {
		logger.Info("OAuth tokens updated")
		cfg.OpenAITokens = &config.OAuthTokens{
			AccessToken:  tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			ExpiresAt:    tokens.ExpiresAt,
			AccountID:    tokens.AccountID,
		}
		cfg.Save()
	})

	if cfg.OpenAITokens != nil {
		logger.Info("Restoring saved OAuth tokens")
		oauth.SetTokens(&llm.OAuthTokens{
			AccessToken:  cfg.OpenAITokens.AccessToken,
			RefreshToken: cfg.OpenAITokens.RefreshToken,
			ExpiresAt:    cfg.OpenAITokens.ExpiresAt,
			AccountID:    cfg.OpenAITokens.AccountID,
		})
	}

	openai := llm.NewOpenAIProvider(oauth)

	// Tools registry
	registry := tools.NewRegistry()
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.ListDir{})
	registry.Register(tools.DeleteFile{})
	registry.Register(tools.MoveFile{})
	registry.Register(tools.SearchFiles{})
	registry.Register(tools.Bash{})
	registry.Register(tools.BashSudo{})
	registry.Register(tools.GitStatus{})
	registry.Register(tools.GitDiff{})
	registry.Register(tools.GitLog{})
	registry.Register(tools.GitAdd{})
	registry.Register(tools.GitCommit{})
	registry.Register(tools.GitBranch{})
	registry.Register(tools.WebFetch{})
	logger.Info("Registered %d tools", len(registry.Definitions()))

	costTracker := llm.NewCostTracker()

	// Permission channel for TUI <-> agent communication
	permChan := make(chan tui.PermissionResponseMsg, 1)

	// Permission manager with TUI callback
	var program *tea.Program
	permissions := agent.NewPermissionManager(
		cfg.ConfigDir(),
		func(tool, command string) (agent.PermissionLevel, bool) {
			logger.Info("Permission request: tool=%s command=%s", tool, command)
			if program != nil {
				program.Send(tui.PermissionRequestMsg{Tool: tool, Command: command})
			}
			// Block until TUI responds
			resp := <-permChan
			logger.Info("Permission response: approved=%v level=%v", resp.Approved, resp.Level)
			return resp.Level, resp.Approved
		},
	)

	model := tui.NewModel(cfg, claude, openai, oauth, costTracker, registry, permissions, permChan)
	program = tea.NewProgram(&model, tea.WithAltScreen())
	model.SetProgram(program)

	if _, err := program.Run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
