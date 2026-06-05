package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"openuai/internal/logger"
	"strings"
	"time"
)

// maxRetries is the number of additional attempts after the first on transient errors.
const maxRetries = 4

// isRetriableStatus reports whether an HTTP status warrants a retry with backoff.
func isRetriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

const codexBaseURL = "https://chatgpt.com/backend-api/codex/responses"
const codexModelsURL = "https://chatgpt.com/backend-api/codex/models"
const codexClientVersion = "0.115.0"

type OpenAIProvider struct {
	oauth      *OAuthFlow
	httpClient *http.Client
}

func NewOpenAIProvider(oauth *OAuthFlow) *OpenAIProvider {
	return &OpenAIProvider{
		oauth:      oauth,
		httpClient: &http.Client{},
	}
}

func (o *OpenAIProvider) Name() string {
	return "openai"
}

func (o *OpenAIProvider) Models() []string {
	// Static fallback only. The live set of models is gated server-side per
	// account and should come from FetchModels(); this list is used when the
	// user isn't authenticated yet or the models endpoint is unreachable.
	// As of 2026-06, ChatGPT accounts are migrated to the 5.4 family.
	return []string{
		"gpt-5.4",
		"gpt-5.4-mini",
	}
}

type codexModelEntry struct {
	Slug         string `json:"slug"`
	Visibility   string `json:"visibility"`
	SupportedAPI bool   `json:"supported_in_api"`
}

type codexModelsResponse struct {
	Models []codexModelEntry `json:"models"`
}

// FetchModels queries the Codex backend for the models actually available to
// this ChatGPT account (the set is gated server-side and changes over time, so
// hardcoding slugs goes stale and yields HTTP 400 "model not supported").
// Returns the slugs marked visibility=="list".
func (o *OpenAIProvider) FetchModels(ctx context.Context) ([]string, error) {
	accessToken, accountID, err := o.oauth.GetAccessToken()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", codexModelsURL+"?client_version="+codexClientVersion, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", "codex_cli_rs/"+codexClientVersion)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body bytes.Buffer
		body.ReadFrom(resp.Body)
		return nil, fmt.Errorf("models API error (status %d): %s", resp.StatusCode, body.String())
	}

	var parsed codexModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	var slugs []string
	for _, m := range parsed.Models {
		if m.Visibility == "list" {
			slugs = append(slugs, m.Slug)
		}
	}
	if len(slugs) == 0 {
		return nil, fmt.Errorf("models API returned no listable models")
	}
	logger.Info("Fetched %d models from Codex backend: %v", len(slugs), slugs)
	return slugs, nil
}

// Tool definition for the API
type ToolDefinition struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  []ToolParam       `json:"parameters"`
}

type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ToolCall represents a tool call from the model
type ToolCall struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

// codex API request format
type codexRequest struct {
	Model             string           `json:"model"`
	Instructions      string           `json:"instructions,omitempty"`
	Input             []interface{}    `json:"input"`
	Tools             []interface{}    `json:"tools,omitempty"`
	ToolChoice        string           `json:"tool_choice,omitempty"`
	ParallelToolCalls bool             `json:"parallel_tool_calls"`
	Stream            bool             `json:"stream"`
	Store             bool             `json:"store"`
}

type codexInputMessage struct {
	Type    string      `json:"type"`
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string, or []content-part for image messages
}

// imageContentParts builds Responses-API content parts (input_text + input_image)
// from a text body and base64 PNG images.
func imageContentParts(text string, images []string) []map[string]interface{} {
	parts := []map[string]interface{}{{"type": "input_text", "text": text}}
	for _, img := range images {
		parts = append(parts, map[string]interface{}{
			"type":      "input_image",
			"image_url": "data:image/png;base64," + img,
		})
	}
	return parts
}

type codexToolResult struct {
	Type       string `json:"type"`
	CallID     string `json:"call_id"`
	Output     string `json:"output"`
}

func buildFunctionTool(td ToolDefinition) map[string]interface{} {
	properties := map[string]interface{}{}
	required := []string{}

	for _, p := range td.Parameters {
		prop := map[string]interface{}{
			"type":        p.Type,
			"description": p.Description,
		}
		// OpenAI requires array schemas to have an "items" field
		if p.Type == "array" {
			prop["items"] = map[string]interface{}{}
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}

	return map[string]interface{}{
		"type": "function",
		"name": td.Name,
		"description": td.Description,
		"strict": false,
		"parameters": map[string]interface{}{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
	}
}

// ChatWithTools sends a message with native tool support
func (o *OpenAIProvider) ChatWithTools(ctx context.Context, messages []Message, model string, toolDefs []ToolDefinition) (*Response, []ToolCall, error) {
	logger.Info("OpenAI ChatWithTools: model=%s messages=%d tools=%d", model, len(messages), len(toolDefs))
	accessToken, accountID, err := o.oauth.GetAccessToken()
	if err != nil {
		logger.Error("OpenAI GetAccessToken failed: %s", err.Error())
		return nil, nil, err
	}

	var instructions string
	var input []interface{}

	for _, m := range messages {
		if m.Role == RoleSystem {
			instructions = m.Content
			continue
		}

		if m.Role == RoleToolResult {
			input = append(input, codexToolResult{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
			// function_call_output can't carry images, so attach any screenshots
			// as a following user message (input_image parts).
			if len(m.Images) > 0 {
				input = append(input, codexInputMessage{
					Type:    "message",
					Role:    "user",
					Content: imageContentParts("Screenshot after the action:", m.Images),
				})
			}
		} else {
			// Add the message itself (skip empty assistant messages that only had tool calls)
			if m.Content != "" || len(m.ToolCalls) == 0 || len(m.Images) > 0 {
				var content interface{} = m.Content
				if len(m.Images) > 0 {
					content = imageContentParts(m.Content, m.Images)
				}
				input = append(input, codexInputMessage{
					Type:    "message",
					Role:    string(m.Role),
					Content: content,
				})
			}
			// Add function_call items for assistant messages with tool calls
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]interface{}{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Name,
					"arguments": marshalArgs(tc.Arguments),
				})
			}
		}
	}

	// Build tools array
	var tools []interface{}
	for _, td := range toolDefs {
		tools = append(tools, buildFunctionTool(td))
	}

	reqBody := codexRequest{
		Model:             model,
		Instructions:      instructions,
		Input:             input,
		Tools:             tools,
		ToolChoice:        "auto",
		ParallelToolCalls: false,
		Stream:            true,
		Store:             false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	logger.Debug("OpenAI request body length: %d", len(jsonBody))

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s, 8s — interruptible by ctx.
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			logger.Info("OpenAI retry %d/%d after %s (last error: %v)", attempt, maxRetries, backoff, lastErr)
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", codexBaseURL, bytes.NewReader(jsonBody))
		if err != nil {
			return nil, nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("chatgpt-account-id", accountID)
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "codex_cli_rs")

		resp, err := o.httpClient.Do(req)
		if err != nil {
			// Network/transport error — retry unless the context was cancelled.
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			lastErr = fmt.Errorf("send request: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			var body bytes.Buffer
			body.ReadFrom(resp.Body)
			resp.Body.Close()
			logger.Error("OpenAI API error: status=%d body=%s", resp.StatusCode, body.String())
			lastErr = fmt.Errorf("API error (status %d): %s", resp.StatusCode, body.String())
			if isRetriableStatus(resp.StatusCode) {
				continue
			}
			return nil, nil, lastErr
		}

		defer resp.Body.Close()
		return parseSSEResponseWithTools(resp, model)
	}

	return nil, nil, fmt.Errorf("exhausted %d retries: %w", maxRetries, lastErr)
}

// Chat implements the Provider interface (simple text, no tools)
func (o *OpenAIProvider) Chat(ctx context.Context, messages []Message, model string) (*Response, error) {
	resp, _, err := o.ChatWithTools(ctx, messages, model, nil)
	return resp, err
}

func (o *OpenAIProvider) IsAuthenticated() bool {
	return o.oauth.IsAuthenticated()
}

func (o *OpenAIProvider) Login() error {
	return o.oauth.Login()
}

func parseSSEResponseWithTools(resp *http.Response, model string) (*Response, []ToolCall, error) {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var contentParts []string
	var toolCalls []ToolCall
	var inputTokens, outputTokens int
	var responseModel string

	// Track current function call being built
	currentCallID := ""
	currentFuncName := ""
	var argParts []string

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			if delta, ok := event["delta"].(string); ok {
				contentParts = append(contentParts, delta)
			}

		case "response.output_text.done":
			if text, ok := event["text"].(string); ok {
				contentParts = []string{text}
			}

		case "response.function_call_arguments.delta":
			if delta, ok := event["delta"].(string); ok {
				argParts = append(argParts, delta)
			}

		case "response.output_item.added":
			// New output item - could be text or function call
			if item, ok := event["item"].(map[string]interface{}); ok {
				if itemType, _ := item["type"].(string); itemType == "function_call" {
					currentCallID, _ = item["call_id"].(string)
					currentFuncName, _ = item["name"].(string)
					argParts = nil
					logger.Info("SSE: function_call started: name=%s id=%s", currentFuncName, currentCallID)
				}
			}

		case "response.output_item.done":
			// Output item finished - finalize function call if any
			if item, ok := event["item"].(map[string]interface{}); ok {
				if itemType, _ := item["type"].(string); itemType == "function_call" {
					callID, _ := item["call_id"].(string)
					funcName, _ := item["name"].(string)
					argsStr, _ := item["arguments"].(string)

					if callID == "" {
						callID = currentCallID
					}
					if funcName == "" {
						funcName = currentFuncName
					}
					if argsStr == "" {
						argsStr = strings.Join(argParts, "")
					}

					var args map[string]string
					// Try parsing as map[string]string first
					if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
						// Try as map[string]interface{} and convert
						var argsRaw map[string]interface{}
						if err2 := json.Unmarshal([]byte(argsStr), &argsRaw); err2 == nil {
							args = make(map[string]string)
							for k, v := range argsRaw {
								switch val := v.(type) {
								case string:
									args[k] = val
								default:
									b, _ := json.Marshal(val)
									args[k] = string(b)
								}
							}
						} else {
							logger.Error("Failed to parse function args: %s", argsStr)
							args = map[string]string{"raw": argsStr}
						}
					}

					tc := ToolCall{
						ID:        callID,
						Name:      funcName,
						Arguments: args,
					}
					toolCalls = append(toolCalls, tc)
					logger.Info("SSE: function_call done: name=%s id=%s args=%s", funcName, callID, argsStr)

					// Reset
					currentCallID = ""
					currentFuncName = ""
					argParts = nil
				}
			}

		case "response.completed":
			if respObj, ok := event["response"].(map[string]interface{}); ok {
				if m, ok := respObj["model"].(string); ok {
					responseModel = m
				}
				if usage, ok := respObj["usage"].(map[string]interface{}); ok {
					if v, ok := usage["input_tokens"].(float64); ok {
						inputTokens = int(v)
					}
					if v, ok := usage["output_tokens"].(float64); ok {
						outputTokens = int(v)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Error("SSE scanner error: %s", err.Error())
		return nil, nil, fmt.Errorf("reading SSE stream: %w", err)
	}

	content := strings.Join(contentParts, "")
	if responseModel == "" {
		responseModel = model
	}

	logger.Info("OpenAI SSE complete: model=%s content_len=%d tool_calls=%d tokens_in=%d tokens_out=%d",
		responseModel, len(content), len(toolCalls), inputTokens, outputTokens)

	return &Response{
		Content:      content,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Model:        responseModel,
	}, toolCalls, nil
}

func marshalArgs(args map[string]string) string {
	b, _ := json.Marshal(args)
	return string(b)
}
