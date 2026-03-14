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
)

const codexBaseURL = "https://chatgpt.com/backend-api/codex/responses"

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
	return []string{
		"gpt-5.1-codex",
		"gpt-5-codex",
		"gpt-5.2",
		"gpt-5.1",
		"gpt-5",
	}
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
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
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
		properties[p.Name] = map[string]interface{}{
			"type":        p.Type,
			"description": p.Description,
		}
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
		} else {
			// Add the message itself (skip empty assistant messages that only had tool calls)
			if m.Content != "" || len(m.ToolCalls) == 0 {
				input = append(input, codexInputMessage{
					Type:    "message",
					Role:    string(m.Role),
					Content: m.Content,
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
		return nil, nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body bytes.Buffer
		body.ReadFrom(resp.Body)
		logger.Error("OpenAI API error: status=%d body=%s", resp.StatusCode, body.String())
		return nil, nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, body.String())
	}

	return parseSSEResponseWithTools(resp, model)
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
