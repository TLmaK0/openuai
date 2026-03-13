package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"
const anthropicAPIVersion = "2023-06-01"

type ClaudeProvider struct {
	apiKey     string
	httpClient *http.Client
}

func NewClaudeProvider(apiKey string) *ClaudeProvider {
	return &ClaudeProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (c *ClaudeProvider) Name() string {
	return "claude"
}

func (c *ClaudeProvider) Models() []string {
	return []string{
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-haiku-4-20250506",
		"claude-3-5-sonnet-20241022",
		"claude-3-5-haiku-20241022",
	}
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *ClaudeProvider) Chat(ctx context.Context, messages []Message, model string) (*Response, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("claude API key not set")
	}

	var systemPrompt string
	var claudeMessages []claudeMessage

	for _, m := range messages {
		if m.Role == RoleSystem {
			systemPrompt = m.Content
			continue
		}
		claudeMessages = append(claudeMessages, claudeMessage{
			Role:    string(m.Role),
			Content: m.Content,
		})
	}

	reqBody := claudeRequest{
		Model:     model,
		MaxTokens: 4096,
		System:    systemPrompt,
		Messages:  claudeMessages,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var cResp claudeResponse
	if err := json.Unmarshal(body, &cResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if cResp.Error != nil {
		return nil, fmt.Errorf("claude API error: %s: %s", cResp.Error.Type, cResp.Error.Message)
	}

	var content string
	for _, c := range cResp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	return &Response{
		Content:      content,
		InputTokens:  cResp.Usage.InputTokens,
		OutputTokens: cResp.Usage.OutputTokens,
		Model:        cResp.Model,
	}, nil
}

func (c *ClaudeProvider) SetAPIKey(key string) {
	c.apiKey = key
}
