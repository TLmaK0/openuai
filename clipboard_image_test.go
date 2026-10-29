package main

import (
	"testing"

	"openuai/internal/llm"
)

func TestParseAppleScriptData(t *testing.T) {
	b, err := parseAppleScriptData("«data PNGf89504E47»\n")
	if err != nil || len(b) != 4 || b[0] != 0x89 {
		t.Fatalf("got %v %v", b, err)
	}
	if b, _ := parseAppleScriptData("missing value"); b != nil {
		t.Fatal("no image must give nil")
	}
}

func TestHistoryKeepsImages(t *testing.T) {
	h := buildChatHistory([]llm.Message{
		{Role: llm.RoleUser, Content: "", Images: []string{"iVBOR"}},
		{Role: llm.RoleUser, Content: "hola", Images: []string{"iVBOR", "/9j/"}},
	})
	if len(h) != 2 || len(h[0].Images) != 1 || len(h[1].Images) != 2 || h[1].Content != "hola" {
		t.Fatalf("got %+v", h)
	}
}
