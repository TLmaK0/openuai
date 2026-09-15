package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"openuai/internal/llm"
)

// FetchModels lists every page of models accessible with this provider's key.
// https://platform.claude.com/docs/en/api/models/list
func (c *ClaudeProvider) FetchModels(ctx context.Context) ([]string, error) {
	key := c.key()
	if key == "" {
		return nil, fmt.Errorf("Claude API key is missing")
	}
	var models []string
	seen := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		query := url.Values{"limit": {"1000"}}
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch Claude models: %w", err)
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("fetch Claude models: HTTP %d", resp.StatusCode)
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode Claude models: %w", err)
		}
		for _, m := range page.Data {
			if m.ID != "" && !seen[m.ID] {
				models = append(models, m.ID)
				seen[m.ID] = true
			}
		}
		if !page.HasMore {
			break
		}
		if page.LastID == "" || cursors[page.LastID] {
			return nil, fmt.Errorf("Claude models pagination did not advance")
		}
		cursor = page.LastID
		cursors[cursor] = true
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Claude API returned no models")
	}
	return models, nil
}

var _ llm.ModelFetcher = (*ClaudeProvider)(nil)
