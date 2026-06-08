package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ImageSearch finds real, working image URLs via the Wikimedia Commons API.
// The LLM has no way to know real image URLs on its own and tends to fabricate
// plausible-looking ones that 404. This tool returns verified, directly-
// embeddable thumbnail URLs so the agent can show actual images in chat.
type ImageSearch struct{}

func (t ImageSearch) Definition() Definition {
	return Definition{
		Name:        "image_search",
		Description: "Search for real, working image URLs by topic (uses Wikimedia Commons). Returns ready-to-use markdown image tags. ALWAYS use this when the user asks to see/show/find images — never guess or invent image URLs yourself, they will be broken.",
		Parameters: []Parameter{
			{Name: "query", Type: "string", Description: "What to find images of (e.g. \"Opuntia cactus\", \"Eiffel tower at night\")", Required: true},
			{Name: "limit", Type: "string", Description: "Max number of images to return (default 4, max 10)", Required: false},
		},
		RequiresPermission: "session",
	}
}

type commonsResponse struct {
	Query struct {
		Pages map[string]struct {
			Title     string `json:"title"`
			Index     int    `json:"index"`
			ImageInfo []struct {
				ThumbURL string `json:"thumburl"`
				URL      string `json:"url"`
				Mime     string `json:"mime"`
			} `json:"imageinfo"`
		} `json:"pages"`
	} `json:"query"`
}

func (t ImageSearch) Execute(ctx context.Context, args map[string]string) Result {
	query := strings.TrimSpace(args["query"])
	if query == "" {
		return Result{Error: "query is required"}
	}

	limit := 4
	if l := strings.TrimSpace(args["limit"]); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 10 {
		limit = 10
	}

	api := "https://commons.wikimedia.org/w/api.php?" + url.Values{
		"action":       {"query"},
		"format":       {"json"},
		"generator":    {"search"},
		"gsrnamespace": {"6"}, // File namespace
		"gsrsearch":    {query},
		"gsrlimit":     {strconv.Itoa(limit * 3)}, // over-fetch; we filter non-photo mimes
		"prop":         {"imageinfo"},
		"iiprop":       {"url|mime"},
		"iiurlwidth":   {"480"},
	}.Encode()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
	if err != nil {
		return Result{Error: fmt.Sprintf("create request: %s", err)}
	}
	// Wikimedia requires a descriptive User-Agent.
	req.Header.Set("User-Agent", "OpenUAI/1.0 (https://github.com/singularaircraft/openuai)")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Result{Error: fmt.Sprintf("image search failed: %s", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Result{Error: fmt.Sprintf("image search HTTP %d", resp.StatusCode)}
	}

	var data commonsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Result{Error: fmt.Sprintf("parse response: %s", err)}
	}

	type img struct {
		index int
		title string
		thumb string
	}
	var imgs []img
	for _, p := range data.Query.Pages {
		if len(p.ImageInfo) == 0 {
			continue
		}
		info := p.ImageInfo[0]
		// Only raster photo formats embed reliably; skip svg/pdf/tiff/etc.
		switch info.Mime {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			continue
		}
		thumb := info.ThumbURL
		if thumb == "" {
			thumb = info.URL
		}
		title := strings.TrimSuffix(strings.TrimPrefix(p.Title, "File:"), "")
		imgs = append(imgs, img{index: p.Index, title: title, thumb: thumb})
	}

	if len(imgs) == 0 {
		return Result{Output: fmt.Sprintf("No images found for %q. Try a simpler or more specific query.", query)}
	}

	// Preserve the API's relevance ordering (lower index = more relevant).
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].index < imgs[j].index })
	if len(imgs) > limit {
		imgs = imgs[:limit]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d real image(s) for %q. Embed these markdown tags verbatim to show them — the URLs are verified:\n\n", len(imgs), query)
	for _, im := range imgs {
		fmt.Fprintf(&b, "![%s](%s)\n", im.title, im.thumb)
	}
	return Result{Output: b.String()}
}
