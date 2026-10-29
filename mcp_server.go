package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/mcpserver"
)

// mcpBackend wires the MCP server (internal/mcpserver) to the app.
func (a *App) mcpBackend() mcpserver.Backend {
	return mcpserver.Backend{
		SendMessage: a.sendMessageForMCP,
		History:     func() any { return a.GetChatHistory() },
		NewConversation: func() {
			a.ClearChat()
		},
		Stop: a.AbortAgent,
		Status: func() any {
			c := a.GetCostSummary()
			return map[string]any{
				"version":             a.version,
				"provider":            a.GetProvider(),
				"model":               a.GetDefaultModel(),
				"work_dir":            a.GetWorkDir(),
				"total_input_tokens":  c.TotalInputTokens,
				"total_output_tokens": c.TotalOutputTokens,
				"total_cost_usd":      c.TotalCostUSD,
			}
		},
	}
}

// sendMessageForMCP runs a message like the chat does (same hooks, skills
// and permission prompts) and returns the assistant's final answer.
func (a *App) sendMessageForMCP(message string) mcpserver.Reply {
	r := a.SendMessage(message)
	return mcpserver.Reply{
		Content:      r.Content,
		InputTokens:  r.InputTokens,
		OutputTokens: r.OutputTokens,
		CostUSD:      r.CostUSD,
		Model:        a.GetDefaultModel(),
		Error:        r.Error,
		Refused:      r.Refused,
	}
}

// lastAnswer returns the assistant text written since the last user message.
func lastAnswer(msgs []llm.Message) string {
	var parts []string
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == llm.RoleUser {
			break
		}
		if m.Role == llm.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			parts = append(parts, m.Content)
		}
	}
	// Collected backwards; restore the order.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n\n")
}

// runMCPCommand implements `openuai mcp`: an MCP server on stdin/stdout that
// forwards to the running app's MCP endpoint. It returns the exit code.
func runMCPCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "port of the OpenUAI local API (default: api_port from the config, or 9120)")
	url := fs.String("url", "", "full URL of the OpenUAI MCP endpoint (overrides --port)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: openuai mcp [--port N | --url URL]")
		fmt.Fprintln(stderr, "Serves the running OpenUAI app as an MCP server over stdio.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	endpoint := *url
	if endpoint == "" {
		p := *port
		if p == 0 {
			if cfg, err := config.Load(); err == nil {
				p = cfg.APIPort()
			}
		}
		if p == 0 {
			p = 9120
		}
		endpoint = fmt.Sprintf("http://127.0.0.1:%d%s", p, mcpserver.Path)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errLog := log.New(stderr, "openuai mcp: ", log.LstdFlags)
	errLog.Printf("forwarding to %s", endpoint)
	if err := mcpserver.NewProxy(endpoint, Version).Serve(ctx, stdin, stdout, errLog); err != nil && ctx.Err() == nil {
		errLog.Printf("%v", err)
		return 1
	}
	return 0
}
