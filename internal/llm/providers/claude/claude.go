package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"openuai/internal/llm"
	"openuai/internal/logger"
)

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"
const anthropicAPIVersion = "2023-06-01"

// secretKey is where the API key lives in the provider's own store.
const secretKey = "api_key"

func init() {
	llm.Register(llm.Descriptor{
		Name:              "claude",
		DisplayName:       "Claude (API key)",
		Credential:        llm.CredentialSecret,
		SecretPlaceholder: "sk-ant-...",
		DefaultModel:      "claude-sonnet-4-20250514",
		New:               func(store llm.Store) llm.Provider { return New(store) },
	})

	// Prices per million input/output tokens, declared here so the core ships
	// no knowledge of this provider's models.
	for model, price := range map[string][2]float64{
		"claude-sonnet-4-20250514":   {3.0, 15.0},
		"claude-opus-4-20250514":     {15.0, 75.0},
		"claude-haiku-4-20250506":    {0.80, 4.0},
		"claude-3-5-sonnet-20241022": {3.0, 15.0},
		"claude-3-5-haiku-20241022":  {0.80, 4.0},
	} {
		llm.SetModelPricing(model, price[0], price[1])
	}
}

type ClaudeProvider struct {
	store      llm.Store
	httpClient *http.Client

	mu     sync.RWMutex
	apiKey string
}

// New builds the provider, taking its API key from store.
func New(store llm.Store) *ClaudeProvider {
	return &ClaudeProvider{
		store:      store,
		httpClient: &http.Client{},
		apiKey:     store.Get(secretKey),
	}
}

// Ready reports whether an API key has been supplied.
func (c *ClaudeProvider) Ready() bool {
	return c.key() != ""
}

func (c *ClaudeProvider) key() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
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

// Claude API request types
type claudeRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []claudeAPIMessage `json:"messages"`
	Tools     []claudeToolDef    `json:"tools,omitempty"`
}

type claudeAPIMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []claudeContentBlock
}

type claudeContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type claudeToolDef struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	InputSchema claudeInputSchema `json:"input_schema"`
}

type claudeInputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Required   []string               `json:"required,omitempty"`
}

// Claude API response types
type claudeResponse struct {
	ID      string `json:"id"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Chat implements Provider interface (simple text, no tools)
func (c *ClaudeProvider) Chat(ctx context.Context, messages []llm.Message, model string) (*llm.Response, error) {
	resp, _, err := c.ChatWithTools(ctx, messages, model, nil)
	return resp, err
}

// ChatWithTools implements ToolCallProvider for Claude
func (c *ClaudeProvider) ChatWithTools(ctx context.Context, messages []llm.Message, model string, toolDefs []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	if c.key() == "" {
		return nil, nil, fmt.Errorf("claude API key not set")
	}

	logger.Info("Claude ChatWithTools: model=%s messages=%d tools=%d", model, len(messages), len(toolDefs))

	var systemPrompt string
	var apiMessages []claudeAPIMessage

	for _, m := range messages {
		if m.Role == llm.RoleSystem {
			systemPrompt = m.Content
			continue
		}

		if m.Role == llm.RoleToolResult {
			// Tool result → user message with tool_result content block
			block := claudeContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			apiMessages = append(apiMessages, claudeAPIMessage{
				Role:    "user",
				Content: []claudeContentBlock{block},
			})
			continue
		}

		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			// Assistant message with tool calls → content blocks
			var blocks []claudeContentBlock
			if m.Content != "" {
				blocks = append(blocks, claudeContentBlock{
					Type: "text",
					Text: m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, claudeContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Arguments,
				})
			}
			apiMessages = append(apiMessages, claudeAPIMessage{
				Role:    "assistant",
				Content: blocks,
			})
			continue
		}

		// Regular text message
		apiMessages = append(apiMessages, claudeAPIMessage{
			Role:    string(m.Role),
			Content: m.Content,
		})
	}

	reqBody := claudeRequest{
		Model:     model,
		MaxTokens: 8192,
		System:    systemPrompt,
		Messages:  apiMessages,
	}

	// Add tool definitions
	if len(toolDefs) > 0 {
		for _, td := range toolDefs {
			properties := map[string]interface{}{}
			var required []string
			for _, p := range td.Parameters {
				properties[p.Name] = map[string]interface{}{
					"type":        p.Type,
					"description": p.Description,
				}
				if p.Required {
					required = append(required, p.Name)
				}
			}
			reqBody.Tools = append(reqBody.Tools, claudeToolDef{
				Name:        td.Name,
				Description: td.Description,
				InputSchema: claudeInputSchema{
					Type:       "object",
					Properties: properties,
					Required:   required,
				},
			})
		}
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	logger.Debug("Claude request body length: %d", len(jsonBody))

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.key())
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error("Claude API error: status=%d body=%s", resp.StatusCode, string(body))
		return nil, nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var cResp claudeResponse
	if err := json.Unmarshal(body, &cResp); err != nil {
		return nil, nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if cResp.Error != nil {
		return nil, nil, fmt.Errorf("claude API error: %s: %s", cResp.Error.Type, cResp.Error.Message)
	}

	// Extract text content and tool calls
	var textContent string
	var toolCalls []llm.ToolCall

	for _, block := range cResp.Content {
		switch block.Type {
		case "text":
			textContent += block.Text
		case "tool_use":
			// Parse input as map[string]string
			var argsRaw map[string]interface{}
			if err := json.Unmarshal(block.Input, &argsRaw); err != nil {
				logger.Error("Failed to parse tool_use input: %s", err.Error())
				continue
			}
			args := make(map[string]string)
			for k, v := range argsRaw {
				switch val := v.(type) {
				case string:
					args[k] = val
				default:
					b, _ := json.Marshal(val)
					args[k] = string(b)
				}
			}
			toolCalls = append(toolCalls, llm.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
			logger.Info("Claude tool_use: name=%s id=%s", block.Name, block.ID)
		}
	}

	logger.Info("Claude response: model=%s content_len=%d tool_calls=%d tokens_in=%d tokens_out=%d stop=%s",
		cResp.Model, len(textContent), len(toolCalls), cResp.Usage.InputTokens, cResp.Usage.OutputTokens, cResp.StopReason)

	return &llm.Response{
		Content:      textContent,
		InputTokens:  cResp.Usage.InputTokens,
		OutputTokens: cResp.Usage.OutputTokens,
		Model:        cResp.Model,
	}, toolCalls, nil
}

// SetSecret stores the API key and persists it.
func (c *ClaudeProvider) SetSecret(secret string) error {
	c.mu.Lock()
	c.apiKey = secret
	c.mu.Unlock()
	return c.store.Set(secretKey, secret)
}
