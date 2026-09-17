package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Content limits for web_fetch.
const (
	// defaultFetchMaxBytes is how much content is returned when the caller
	// doesn't ask for more.
	defaultFetchMaxBytes = 50000
	// maxFetchMaxBytes is the ceiling a caller can raise max_bytes to.
	maxFetchMaxBytes = 2 * 1024 * 1024
	// fetchReadLimit is how much of the response body is downloaded.
	fetchReadLimit = 5 * 1024 * 1024
)

// WebFetch fetches a URL and returns its text content
type WebFetch struct{}

func (t WebFetch) Definition() Definition {
	return Definition{
		Name:        "web_fetch",
		Description: "Fetch a URL and return its text content. Useful for reading web pages, APIs, documentation, etc. On documentation sites it automatically reads the markdown (.md) source instead of the HTML page. Long pages are paginated, not silently cut: when the output is truncated the tool reports the total size and the offset to continue from, so call it again with that offset (or a bigger max_bytes) instead of giving up.",
		Parameters: []Parameter{
			{Name: "url", Type: "string", Description: "The URL to fetch", Required: true},
			{Name: "method", Type: "string", Description: "HTTP method (default: GET)", Required: false},
			{Name: "max_bytes", Type: "string", Description: fmt.Sprintf("Max bytes of content to return (default %d, max %d). Raise it for long documentation pages.", defaultFetchMaxBytes, maxFetchMaxBytes), Required: false},
			{Name: "offset", Type: "string", Description: "Byte offset to start from (default 0). Use the offset reported in a truncation notice to read the next chunk without re-reading what you already have.", Required: false},
			{Name: "raw", Type: "string", Description: "Set to 'true' to get the response exactly as served: no markdown-source lookup, no HTML-to-text conversion.", Required: false},
		},
		RequiresPermission: "session",
	}
}

func (t WebFetch) Execute(ctx context.Context, args map[string]string) Result {
	rawURL := strings.TrimSpace(args["url"])
	if rawURL == "" {
		return Result{Error: "url is required"}
	}

	// Basic validation
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	method := strings.ToUpper(strings.TrimSpace(args["method"]))
	if method == "" {
		method = "GET"
	}

	maxBytes := defaultFetchMaxBytes
	if v := strings.TrimSpace(args["max_bytes"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Result{Error: "max_bytes must be a positive integer"}
		}
		maxBytes = n
	}
	if maxBytes > maxFetchMaxBytes {
		maxBytes = maxFetchMaxBytes
	}

	offset := 0
	if v := strings.TrimSpace(args["offset"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Result{Error: "offset must be a non-negative integer"}
		}
		offset = n
	}

	raw := strings.EqualFold(strings.TrimSpace(args["raw"]), "true")

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var (
		resp       fetchResponse
		haveResp   bool
		fetchedURL = rawURL
	)

	// Prefer the markdown source on documentation sites: it's the same page
	// without the HTML chrome, so nothing is lost converting tags to text and
	// the payload is an order of magnitude smaller.
	if !raw && method == "GET" {
		if mdURL, ok := markdownVariant(rawURL); ok {
			if r, err := doFetch(ctx, method, mdURL); err == nil && r.status < 300 && !isHTML(r.contentType) && len(r.body) > 0 {
				resp, haveResp, fetchedURL = r, true, mdURL
			}
		}
	}

	if !haveResp {
		r, err := doFetch(ctx, method, rawURL)
		if err != nil {
			return Result{Error: fmt.Sprintf("fetch failed: %s", err)}
		}
		resp, fetchedURL = r, rawURL
	}

	content := string(resp.body)

	// Strip HTML tags for readability if it looks like HTML
	if !raw && isHTML(resp.contentType) {
		content = stripHTMLTags(content)
	}

	total := len(content)

	header := fmt.Sprintf("Status: %d %s\nContent-Type: %s\n", resp.status, resp.status2, resp.contentType)
	if fetchedURL != rawURL {
		header += fmt.Sprintf("Source: %s (markdown version of the requested page)\n", fetchedURL)
	}
	header += fmt.Sprintf("Length: %d bytes\n", total)
	if resp.capped {
		header += fmt.Sprintf("Note: download stopped at the %d byte transfer limit; the page may continue beyond that.\n", fetchReadLimit)
	}

	if offset >= total && total > 0 {
		return Result{Output: header + fmt.Sprintf("\n(nothing to show: offset %d is at or past the end of the %d byte document)", offset, total)}
	}
	if offset > 0 {
		header += fmt.Sprintf("Offset: %d\n", offset)
	}

	end := offset + maxBytes
	truncated := end < total
	if !truncated {
		end = total
	}

	body := content[offset:end]
	if truncated {
		body += fmt.Sprintf(
			"\n\n... (TRUNCATED: showing bytes %d-%d of %d. %d bytes remain. To read the rest call web_fetch again on the same url with offset=%d, or with a larger max_bytes (up to %d). Do NOT assume the page ended here.)",
			offset, end, total, total-end, end, maxFetchMaxBytes)
	}

	out := header + "\n" + body

	if resp.status >= 400 {
		return Result{Output: out, Error: fmt.Sprintf("HTTP %d", resp.status)}
	}

	return Result{Output: out}
}

// fetchResponse is one HTTP response already read into memory.
type fetchResponse struct {
	status      int
	status2     string // full status line, e.g. "200 OK"
	contentType string
	body        []byte
	capped      bool // body hit fetchReadLimit
}

func doFetch(ctx context.Context, method, rawURL string) (fetchResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return fetchResponse{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", "OpenUAI/1.0")
	req.Header.Set("Accept", "text/markdown,text/plain,text/html,application/json,*/*")

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
		return fetchResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchReadLimit))
	if err != nil {
		return fetchResponse{}, fmt.Errorf("read body: %w", err)
	}

	return fetchResponse{
		status:      resp.StatusCode,
		status2:     resp.Status,
		contentType: resp.Header.Get("Content-Type"),
		body:        body,
		capped:      len(body) == fetchReadLimit,
	}, nil
}

func isHTML(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/html")
}

// markdownVariant returns the ".md" URL for documentation sites that publish a
// markdown source next to the HTML page (Mintlify-style docs such as
// docs.claude.com or code.claude.com). Reading that version avoids the lossy
// HTML-to-text conversion: the same page is ~10x smaller and keeps its
// structure.
func markdownVariant(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	if !docsHost(strings.ToLower(u.Hostname())) {
		return "", false
	}
	if u.Path == "" || u.Path == "/" || strings.HasSuffix(u.Path, "/") {
		return "", false
	}
	if strings.Contains(path.Base(u.Path), ".") {
		return "", false // already has an extension
	}
	u.Path += ".md"
	return u.String(), true
}

func docsHost(host string) bool {
	if strings.HasPrefix(host, "docs.") || strings.Contains(host, ".docs.") {
		return true
	}
	switch host {
	case "code.claude.com", "developers.cloudflare.com", "platform.openai.com", "ai.google.dev":
		return true
	}
	return false
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
