package claudeheadless

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"openuai/internal/llm"
)

// FetchModels reads the catalog from the CLI's SDK initialization response.
// No user message is sent, so listing models does not start an inference turn.
// Protocol: anthropics/claude-agent-sdk-python, _internal/query.py initialize.
func (p *Provider) FetchModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-p", "--input-format", "stream-json",
		"--output-format", "stream-json", "--verbose", "--tools", "",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--settings", `{"disableAllHooks":true}`, "--no-session-persistence")
	if key := p.key(); key != "" {
		cmd.Env = append(cmd.Environ(), "ANTHROPIC_API_KEY="+key)
	}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Claude Code model query: %w", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	const requestID = "openuai-models"
	if _, err := fmt.Fprintln(stdin, `{"type":"control_request","request_id":"`+requestID+`","request":{"subtype":"initialize"}}`); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		var msg struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Models []struct {
						Value string `json:"value"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return nil, fmt.Errorf("decode Claude Code model response: %w", err)
		}
		if msg.Type != "control_response" || msg.Response.RequestID != requestID {
			continue
		}
		if msg.Response.Subtype != "success" {
			return nil, fmt.Errorf("Claude Code rejected the model query")
		}
		var models []string
		seen := map[string]bool{}
		for _, m := range msg.Response.Response.Models {
			if m.Value != "" && !seen[m.Value] {
				models = append(models, m.Value)
				seen[m.Value] = true
			}
		}
		if len(models) == 0 {
			return nil, fmt.Errorf("Claude Code returned no models")
		}
		return models, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Claude Code models: %w", err)
	}
	return nil, fmt.Errorf("Claude Code exited without a model catalog")
}

var _ llm.ModelFetcher = (*Provider)(nil)
