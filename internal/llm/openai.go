package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type codexRequest struct {
	Model        string           `json:"model"`
	Instructions string           `json:"instructions,omitempty"`
	Input        []codexInputItem `json:"input"`
	Stream       bool             `json:"stream"`
	Store        bool             `json:"store"`
}

type codexInputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type codexResponse struct {
	Output []codexOutputItem `json:"output"`
	Usage  *codexUsage       `json:"usage,omitempty"`
	Model  string            `json:"model"`
	Error  *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

type codexOutputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
	Text string `json:"text,omitempty"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (o *OpenAIProvider) Chat(ctx context.Context, messages []Message, model string) (*Response, error) {
	accessToken, accountID, err := o.oauth.GetAccessToken()
	if err != nil {
		return nil, err
	}

	var instructions string
	var input []codexInputItem

	for _, m := range messages {
		if m.Role == RoleSystem {
			instructions = m.Content
			continue
		}
		input = append(input, codexInputItem{
			Type:    "message",
			Role:    string(m.Role),
			Content: m.Content,
		})
	}

	reqBody := codexRequest{
		Model:        model,
		Instructions: instructions,
		Input:        input,
		Stream:       false,
		Store:        false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", codexBaseURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var cResp codexResponse
	if err := json.Unmarshal(body, &cResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if cResp.Error != nil {
		return nil, fmt.Errorf("OpenAI error: %s", cResp.Error.Message)
	}

	content := extractContent(cResp)

	var inputTokens, outputTokens int
	if cResp.Usage != nil {
		inputTokens = cResp.Usage.InputTokens
		outputTokens = cResp.Usage.OutputTokens
	}

	return &Response{
		Content:      content,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Model:        cResp.Model,
	}, nil
}

func (o *OpenAIProvider) IsAuthenticated() bool {
	return o.oauth.IsAuthenticated()
}

func (o *OpenAIProvider) Login() error {
	return o.oauth.Login()
}

func extractContent(resp codexResponse) string {
	var parts []string
	for _, out := range resp.Output {
		if out.Text != "" {
			parts = append(parts, out.Text)
		}
		for _, c := range out.Content {
			if c.Type == "text" || c.Type == "output_text" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "")
}
