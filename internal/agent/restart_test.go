package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

func newRestartTestAgent(reply string) *Agent {
	return New(Config{Provider: &mockProvider{response: reply}, Model: "mock", Registry: tools.NewRegistry(), Permissions: NewPermissionManager("", nil)})
}

func TestRestartResumesInterruptedTurn(t *testing.T) {
	dir := t.TempDir()
	a := newRestartTestAgent("unused")
	a.Begin("do it")
	a.messages = append(a.messages,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "bash"}, {ID: "t2", Name: "bash"}}},
		llm.Message{Role: llm.RoleToolResult, ToolCallID: "t1", Content: "ok"})
	if err := a.SaveSession(dir); err != nil {
		t.Fatal(err)
	}

	// "Restart": a fresh agent loads the saved session.
	b := newRestartTestAgent("finished")
	if err := b.LoadSession(dir); err != nil {
		t.Fatal(err)
	}
	resume, changed := b.RecoverInterrupted([]string{"p1 (npm run dev)"}, time.Date(2026, 10, 28, 11, 30, 0, 0, time.UTC))
	if !resume || !changed {
		t.Fatalf("resume=%v changed=%v", resume, changed)
	}
	if b.messages[1].Content != "do it" {
		t.Fatalf("user message lost: %+v", b.messages[1])
	}
	closed := false
	for _, m := range b.messages {
		if m.Role == llm.RoleToolResult && m.ToolCallID == "t2" && strings.Contains(m.Content, "interrupted") {
			closed = true
		}
	}
	if !closed {
		t.Fatal("dangling tool call t2 was not closed")
	}
	last := b.messages[len(b.messages)-1]
	if last.Role != llm.RoleUser || !strings.HasPrefix(last.Content, RestartNotePrefix+" at 11:30]") || !strings.Contains(last.Content, "p1 (npm run dev)") {
		t.Fatalf("bad restart note: %q", last.Content)
	}

	if err := b.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.LastAssistantContent() != "finished" || b.RunInProgress() || b.ResumePending() {
		t.Fatalf("resume did not finish the turn")
	}

	// Once finished, a later restart must not resume (nor note) anything.
	if err := b.SaveSession(dir); err != nil {
		t.Fatal(err)
	}
	c := newRestartTestAgent("x")
	_ = c.LoadSession(dir)
	if r, ch := c.RecoverInterrupted(nil, time.Now()); r || ch {
		t.Fatalf("finished turn recovered again: resume=%v changed=%v", r, ch)
	}
}

func TestFinishedTurnIsNotResumed(t *testing.T) {
	dir := t.TempDir()
	a := newRestartTestAgent("answer")
	if err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	_ = a.SaveSession(dir)
	b := newRestartTestAgent("x")
	_ = b.LoadSession(dir)
	if r, ch := b.RecoverInterrupted(nil, time.Now()); r || ch {
		t.Fatalf("resume=%v changed=%v", r, ch)
	}
}

func TestResumeGivesUpAfterRepeatedRestarts(t *testing.T) {
	dir := t.TempDir()
	a := newRestartTestAgent("x")
	a.Begin("restart yourself")
	_ = a.SaveSession(dir)
	for i := 0; i <= maxResumeAttempts; i++ {
		b := newRestartTestAgent("x")
		_ = b.LoadSession(dir)
		resume, _ := b.RecoverInterrupted(nil, time.Now())
		if i < maxResumeAttempts {
			if !resume {
				t.Fatalf("attempt %d should resume", i+1)
			}
			b.runInProgress = true // the resumed turn is interrupted again
			_ = b.SaveSession(dir)
			continue
		}
		if resume {
			t.Fatal("must give up after maxResumeAttempts")
		}
		if last := b.messages[len(b.messages)-1].Content; !strings.Contains(last, "not resumed") {
			t.Fatalf("give-up note missing: %q", last)
		}
	}
}

// The resume decision must survive the agent being rebuilt from disk after
// startup recovery (MCP becoming ready resets the agent). Seen in a real run:
// the rebuilt agent had no pending resume and the turn was never continued.
func TestResumePendingSurvivesAgentRebuild(t *testing.T) {
	dir := t.TempDir()
	a := newRestartTestAgent("x")
	a.Begin("prueba")
	_ = a.SaveSession(dir)

	b := newRestartTestAgent("x")
	_ = b.LoadSession(dir)
	if resume, _ := b.RecoverInterrupted(nil, time.Now()); !resume {
		t.Fatal("interrupted turn should be resumed")
	}
	if err := b.SaveSession(dir); err != nil {
		t.Fatal(err)
	}

	// Agent rebuilt from disk, as the app does when MCP tools become ready.
	c := newRestartTestAgent("done")
	if err := c.LoadSession(dir); err != nil {
		t.Fatal(err)
	}
	if !c.ResumePending() {
		t.Fatal("resume decision lost when the agent was rebuilt")
	}
	if err := c.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.LastAssistantContent() != "done" || c.ResumePending() || c.resumeAttempts != 0 {
		t.Fatalf("resume did not finish cleanly: pending=%v attempts=%d", c.ResumePending(), c.resumeAttempts)
	}
	_ = c.SaveSession(dir)
	d := newRestartTestAgent("x")
	_ = d.LoadSession(dir)
	if d.ResumePending() || d.RunInProgress() {
		t.Fatal("finished resume must not be pending again after reload")
	}
}

// A new user message cancels a pending resume (the user moved on).
func TestNewMessageClearsPendingResume(t *testing.T) {
	dir := t.TempDir()
	a := newRestartTestAgent("x")
	a.Begin("one")
	_ = a.SaveSession(dir)
	b := newRestartTestAgent("x")
	_ = b.LoadSession(dir)
	b.RecoverInterrupted(nil, time.Now())
	b.Begin("two")
	_ = b.SaveSession(dir)
	c := newRestartTestAgent("x")
	_ = c.LoadSession(dir)
	if c.ResumePending() {
		t.Fatal("pending resume should be cleared by a new message")
	}
}
