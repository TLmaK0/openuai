package claudeheadless

import (
	"testing"

	"openuai/internal/llm"
)

// The main conversation rebuilds its system prompt every turn (AGENTS.md):
// an identical prompt must keep the CLI session, a changed one starts anew.
func TestUnchangedSystemPromptKeepsSession(t *testing.T) {
	turn1 := []llm.Message{{Role: llm.RoleSystem, Content: "sys\n\nRULE A"}, {Role: llm.RoleUser, Content: "hi"}}
	turn2 := append(append([]llm.Message(nil), turn1...), llm.Message{Role: llm.RoleAssistant, Content: "ok"}, llm.Message{Role: llm.RoleUser, Content: "more"})
	k1 := conversationKey("m", "model", turn1, nil)
	if k2 := conversationKey("m", "model", turn2, nil); k1 != k2 {
		t.Fatal("same system prompt must give the same conversation key")
	}
	st := newSessionStore()
	s, _ := st.acquire(k1, turn1)
	st.release(s, turn1, 1, true)
	if s2, resume := st.acquire(k1, turn2); !resume || s2.id != s.id {
		t.Fatal("unchanged prompt must resume the session")
	}

	edited := append([]llm.Message(nil), turn2...)
	edited[0].Content = "sys\n\nRULE B"
	if conversationKey("m", "model", edited, nil) == k1 {
		t.Fatal("a changed system prompt is a different conversation")
	}
}
