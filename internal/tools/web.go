package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebFetch fetches a URL and returns its text content
type WebFetch struct{}

func (t WebFetch) Definition() Definition {
	return Definition{
		Name:        "web_fetch",
		Description: "Fetch a URL and return its text content. Useful for reading web pages, APIs, documentation, etc.",
		Parameters: []Parameter{
			{Name: "url", Type: "string", Description: "The URL to fetch", Required: true},
			{Name: "method", Type: "string", Description: "HTTP method (default: GET)", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t WebFetch) Execute(ctx context.Context, args map[string]string) Result {
	rawURL := args["url"]
	if rawURL == "" {
		return Result{Error: "url is required"}
	}

	// Basic validation
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	method := strings.ToUpper(args["method"])
	if method == "" {
		method = "GET"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return Result{Error: fmt.Sprintf("create request: %s", err)}
	}

	req.Header.Set("User-Agent", "OpenUAI/1.0")
	req.Header.Set("Accept", "text/html,application/json,text/plain,*/*")

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{Error: fmt.Sprintf("fetch failed: %s", err)}
	}
	defer resp.Body.Close()

	// Limit to 500KB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 500*1024))
	if err != nil {
		return Result{Error: fmt.Sprintf("read body: %s", err)}
	}

	content := string(body)

	// Strip HTML tags for readability if it looks like HTML
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		content = stripHTMLTags(content)
	}

	// Truncate if still too long
	if len(content) > 50000 {
		content = content[:50000] + "\n... (content truncated)"
	}

	header := fmt.Sprintf("Status: %d %s\nContent-Type: %s\n\n", resp.StatusCode, resp.Status, resp.Header.Get("Content-Type"))

	if resp.StatusCode >= 400 {
		return Result{Output: header + content, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	return Result{Output: header + content}
}

// stripHTMLTags removes HTML tags and collapses whitespace for readability
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	inScript := false
	inStyle := false
	lastWasSpace := false

	lower := strings.ToLower(s)

	for i := 0; i < len(s); i++ {
		if i+7 <= len(s) && lower[i:i+7] == "<script" {
			inScript = true
		}
		if i+8 <= len(s) && lower[i:i+8] == "</script" {
			inScript = false
			inTag = true
		}
		if i+6 <= len(s) && lower[i:i+6] == "<style" {
			inStyle = true
		}
		if i+7 <= len(s) && lower[i:i+7] == "</style" {
			inStyle = false
			inTag = true
		}

		if inScript || inStyle {
			continue
		}

		if s[i] == '<' {
			inTag = true
			// Add newline for block elements
			if i+1 < len(s) {
				next := strings.ToLower(s[i:])
				if strings.HasPrefix(next, "<br") || strings.HasPrefix(next, "<p") ||
					strings.HasPrefix(next, "<div") || strings.HasPrefix(next, "<h") ||
					strings.HasPrefix(next, "<li") || strings.HasPrefix(next, "<tr") {
					b.WriteByte('\n')
					lastWasSpace = true
				}
			}
			continue
		}
		if s[i] == '>' {
			inTag = false
			continue
		}
		if inTag {
			continue
		}

		ch := s[i]
		if ch == '\n' || ch == '\r' || ch == '\t' || ch == ' ' {
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		} else {
			b.WriteByte(ch)
			lastWasSpace = false
		}
	}

	return strings.TrimSpace(b.String())
}
