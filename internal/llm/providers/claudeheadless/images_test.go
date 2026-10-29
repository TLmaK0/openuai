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
	var msgs []llm.Message
	for i := 0; i < 8; i++ {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "x", Images: []string{string(rune('a' + i))}})
	}
	imgs := runImages(msgs)
	if len(imgs) != maxImages || imgs[0] != "c" {
		t.Fatalf("got %v", imgs)
	}
	p := renderPrompt(msgs, nil)
	if strings.Count(p, "omitted") != 2 || !strings.Contains(p, "image #6") {
		t.Fatalf("prompt numbering wrong:\n%s", p)
	}
}
