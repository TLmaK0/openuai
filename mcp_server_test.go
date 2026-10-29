package main

import (
	"testing"

	"openuai/internal/llm"
)

func TestLastAnswer(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "old question"},
		{Role: llm.RoleAssistant, Content: "old answer"},
		{Role: llm.RoleUser, Content: "new question"},
		{Role: llm.RoleAssistant, Content: "Let me check.", ToolCalls: []llm.ToolCall{{ID: "1", Name: "bash"}}},
		{Role: llm.RoleToolResult, ToolCallID: "1", Content: "ok"},
		{Role: llm.RoleAssistant, Content: "  "},
		{Role: llm.RoleAssistant, Content: "Done: 42."},
	}
	if got, want := lastAnswer(msgs), "Let me check.\n\nDone: 42."; got != want {
		t.Fatalf("lastAnswer = %q, want %q", got, want)
	}
	if got := lastAnswer([]llm.Message{{Role: llm.RoleUser, Content: "q"}}); got != "" {
		t.Fatalf("no answer yet: got %q", got)
	}
}
