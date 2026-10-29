package main

import (
	"strings"
	"testing"

	"openuai/internal/llm"
)

func TestBuildChatHistoryKeepsTools(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "hola"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "a", Name: "bash", Arguments: map[string]string{"command": "ls"}}}},
		{Role: llm.RoleToolResult, ToolCallID: "a", Content: "x.txt"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "b", Name: "read_file", Arguments: map[string]string{"path": "x"}}, {ID: "c", Name: "bash"}}},
		{Role: llm.RoleToolResult, ToolCallID: "b", Content: "Error: nope"},
		{Role: llm.RoleAssistant, Content: "listo"},
	}
	h := buildChatHistory(msgs)
	if len(h) != 3 || h[0].Role != "user" || h[1].Role != "tools" || h[2].Role != "assistant" {
		t.Fatalf("shape: %+v", h)
	}
	s := h[1].Steps
	if len(s) != 3 || s[0].Tool != "bash" || s[0].Status != "done" || !strings.Contains(s[0].Result, "x.txt") {
		t.Fatalf("step 0: %+v", s)
	}
	if s[1].Status != "error" || s[2].Status != "interrupted" {
		t.Fatalf("statuses: %+v", s)
	}
	if p := historyPreview(strings.Repeat("l\n", 30)); !strings.Contains(p, "(31 lines)") {
		t.Fatalf("preview: %q", p)
	}
}
