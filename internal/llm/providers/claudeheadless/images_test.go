package claudeheadless

import (
	"encoding/json"
	"strings"
	"testing"

	"openuai/internal/llm"
)

func TestImagesStreamJSON(t *testing.T) {
	in := invocation{prompt: "hello"}
	if in.stdin() != "hello" || strings.Contains(strings.Join(in.args(), " "), "input-format") {
		t.Fatal("text prompt must stay plain")
	}
	in.images = []string{"iVBORw0KGgo", "/9j/4AAQ"}
	if !strings.Contains(strings.Join(in.args(), " "), "--input-format stream-json") {
		t.Fatal("images need stream-json input")
	}
	var m struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					MediaType string `json:"media_type"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(in.stdin()), &m); err != nil {
		t.Fatal(err)
	}
	c := m.Message.Content
	if m.Type != "user" || len(c) != 3 || c[0].Source.MediaType != "image/png" || c[1].Source.MediaType != "image/jpeg" || c[2].Text != "hello" {
		t.Fatalf("bad message: %+v", m)
	}
}

func TestImageCap(t *testing.T) {
	// Several earlier user messages with images, then one tool turn with 8
	// screenshots: only the current turn travels, capped at maxImages.
	var msgs []llm.Message
	for i := 0; i < 3; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "old", Images: []string{"old"}}, llm.Message{Role: llm.RoleAssistant, Content: "ok"})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "now", Images: []string{"a"}})
	for i := 1; i < 8; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleToolResult, Content: "shot", Images: []string{string(rune('a' + i))}})
	}
	imgs := runImages(msgs)
	if len(imgs) != maxImages || imgs[0] != "c" {
		t.Fatalf("got %v", imgs)
	}
	p := renderPrompt(msgs, nil)
	if strings.Count(p, "omitted") != 5 || !strings.Contains(p, "image #6") {
		t.Fatalf("prompt numbering wrong:\n%s", p)
	}
}

// A screenshot from an earlier turn is never sent again as if it were new.
func TestOldImagesNotResent(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "look", Images: []string{"OLD"}},
		{Role: llm.RoleAssistant, Content: "seen"},
		{Role: llm.RoleUser, Content: "next question"},
	}
	if imgs := runImages(msgs); len(imgs) != 0 {
		t.Fatalf("old image resent: %v", imgs)
	}
}
