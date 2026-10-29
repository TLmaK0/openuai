package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// recProvider records what each model call saw and answers with plain text.
type recProvider struct {
	mu        sync.Mutex
	seen      [][]llm.Message
	summaries int
}

func (p *recProvider) Chat(_ context.Context, _ []llm.Message, _ string) (*llm.Response, error) {
	p.mu.Lock()
	p.summaries++
	p.mu.Unlock()
	return &llm.Response{Content: "SUMMARY: earlier work", Model: "mock"}, nil
}
func (p *recProvider) Name() string     { return "rec" }
func (p *recProvider) Models() []string { return []string{"mock"} }
func (p *recProvider) ChatWithTools(_ context.Context, msgs []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.mu.Lock()
	p.seen = append(p.seen, append([]llm.Message(nil), msgs...))
	n := len(p.seen)
	p.mu.Unlock()
	return &llm.Response{Content: fmt.Sprintf("answer %d", n), Model: "mock"}, nil, nil
}

func resumeCfg(p llm.Provider, dir string) SubAgentConfig {
	return SubAgentConfig{Provider: p, Model: "mock", Registry: tools.NewRegistry(), Permissions: NewPermissionManager("", nil), MaxConcurrent: 1, TranscriptDir: dir}
}

func TestSubAgentResumeKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	p := &recProvider{}
	r1 := RunSubAgents(context.Background(), resumeCfg(p, dir), []SubAgentTask{{ID: "first", Description: "remember the word PLATANO"}})[0]
	if r1.Error != "" || r1.AgentID == "" {
		t.Fatalf("first run: %+v", r1)
	}
	r2 := RunSubAgents(context.Background(), resumeCfg(p, dir), []SubAgentTask{{ID: "again", Description: "which word?", ResumeID: r1.AgentID}})[0]
	if r2.Error != "" || r2.AgentID != r1.AgentID {
		t.Fatalf("resume: %+v", r2)
	}
	var all []string
	for _, m := range p.seen[len(p.seen)-1] {
		all = append(all, m.Content)
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"PLATANO", "answer 1", "which word?"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("resumed run did not see %q:\n%s", want, joined)
		}
	}
	tr, err := LoadTranscript(dir, r1.AgentID)
	if err != nil || len(tr.Messages) < 5 || tr.Status != "finished" {
		t.Fatalf("transcript: err=%v msgs=%d status=%s", err, len(tr.Messages), tr.Status)
	}
}

func TestSubAgentResumeUnknown(t *testing.T) {
	r := RunSubAgents(context.Background(), resumeCfg(&recProvider{}, t.TempDir()), []SubAgentTask{{ID: "x", Description: "go on", ResumeID: "nope"}})[0]
	if !strings.Contains(r.Error, "cannot resume") {
		t.Fatalf("expected resume error, got %+v", r)
	}
}

func TestTranscriptRejectsBadID(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadTranscript(dir, "../x"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if err := SaveTranscript(dir, Transcript{ID: "../x"}); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}

func TestPruneTranscripts(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTranscript(dir, Transcript{ID: "old", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveTranscript(dir, Transcript{ID: "new", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-40 * 24 * time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "old.json"), past, past)
	if n := PruneTranscripts(dir, TranscriptRetention); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := LoadTranscript(dir, "old"); err == nil {
		t.Fatal("old transcript should be gone")
	}
	if _, err := LoadTranscript(dir, "new"); err != nil {
		t.Fatal(err)
	}
}

func TestCompactReplacesOldContext(t *testing.T) {
	p := &recProvider{}
	a := New(Config{Provider: p, Model: "mock", Registry: tools.NewRegistry(), CompactAtTokens: 1000})
	big := strings.Repeat("x", 2000)
	a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: "task"})
	for i := 0; i < 10; i++ {
		a.messages = append(a.messages, llm.Message{Role: llm.RoleAssistant, Content: big}, llm.Message{Role: llm.RoleUser, Content: "next"})
	}
	n := len(a.messages)
	a.maybeCompact(context.Background())
	if p.summaries != 1 {
		t.Fatalf("summaries=%d", p.summaries)
	}
	if a.messages[0].Role != llm.RoleSystem || !strings.HasPrefix(a.messages[1].Content, CompactNotePrefix) || len(a.messages) >= n {
		t.Fatalf("not compacted: len %d -> %d, [1]=%q", n, len(a.messages), a.messages[1].Content[:40])
	}
}

func TestCompactCutNeverStartsOnToolResult(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem}, {Role: llm.RoleUser}, {Role: llm.RoleAssistant},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: llm.RoleToolResult, ToolCallID: "a"}, {Role: llm.RoleToolResult, ToolCallID: "b"},
		{Role: llm.RoleAssistant},
	}
	cut := compactCut(msgs, 3)
	if cut == 0 || msgs[cut].Role == llm.RoleToolResult {
		t.Fatalf("bad cut %d", cut)
	}
}
