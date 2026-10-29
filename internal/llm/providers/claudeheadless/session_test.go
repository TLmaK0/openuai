package claudeheadless

import (
	"strings"
	"testing"

	"openuai/internal/llm"
)

func conv() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "bash", Arguments: map[string]string{"command": "ls"}}}},
		{Role: llm.RoleToolResult, ToolCallID: "call_1", Content: "a.txt"},
	}
}

func TestSessionResumesOnlyWhenHistoryContinues(t *testing.T) {
	st := newSessionStore()
	c := conv()
	s, resume := st.acquire("k", c)
	if resume {
		t.Fatal("first call cannot resume")
	}
	id := s.id
	st.release(s, c, 1.0, true)

	c2 := append(conv(), llm.Message{Role: llm.RoleAssistant, Content: "done"}, llm.Message{Role: llm.RoleUser, Content: "more"})
	c2[3].Content = prunedStub // pruned old result is not a change
	s, resume = st.acquire("k", c2)
	if !resume || s.id != id {
		t.Fatalf("should resume same session: resume=%v", resume)
	}
	if got := callCost(1.2, s.lastTotal, resume); got < 0.199 || got > 0.201 {
		t.Fatalf("cost of resumed call = %v, want 0.2", got)
	}
	st.release(s, c2, 1.2, true)

	c3 := append(conv(), llm.Message{Role: llm.RoleUser, Content: "x"})
	c3[1].Content = "edited"
	s, resume = st.acquire("k", c3)
	if resume || s.id == id {
		t.Fatal("diverged history must start a new session")
	}
	st.release(s, c3, 0.5, true)
}

func TestBusySessionIsNotShared(t *testing.T) {
	st := newSessionStore()
	a, _ := st.acquire("k", conv())
	b, resume := st.acquire("k", conv())
	if resume || a.id == b.id {
		t.Fatal("a running session must not be reused")
	}
}

func TestCallCost(t *testing.T) {
	if callCost(3, 0, false) != 3 || callCost(0.5, 0.48, true) < 0.019 || callCost(0.1, 0.5, true) != 0.1 {
		t.Fatal("callCost")
	}
}

func TestArgsSession(t *testing.T) {
	if a := strings.Join(invocation{sessionID: "u1"}.args(), " "); !strings.Contains(a, "--session-id u1") || strings.Contains(a, "--resume") {
		t.Fatal(a)
	}
	if a := strings.Join(invocation{sessionID: "u1", resume: true}.args(), " "); !strings.Contains(a, "--resume u1") || strings.Contains(a, "--session-id") {
		t.Fatal(a)
	}
}

func TestDeltaHasOnlyNewMessages(t *testing.T) {
	d := renderDelta([]llm.Message{{Role: llm.RoleUser, Content: "NEW"}})
	if !strings.Contains(d, "NEW") || strings.Contains(d, "Conversation:\n") {
		t.Fatal(d)
	}
}
