package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// blockTool blocks until its context ends, telling started when it runs.
type blockTool struct{ started chan struct{} }

func (b blockTool) Definition() tools.Definition {
	return tools.Definition{Name: "wait", Description: "waits"}
}
func (b blockTool) Execute(ctx context.Context, _ map[string]string) tools.Result {
	close(b.started)
	<-ctx.Done()
	return tools.Result{Error: "killed"}
}

// waitProvider calls the wait tool once, then answers with text.
type waitProvider struct{ recProvider }

func (p *waitProvider) ChatWithTools(ctx context.Context, msgs []llm.Message, m string, d []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	for _, msg := range msgs {
		if len(msg.ToolCalls) > 0 {
			return p.recProvider.ChatWithTools(ctx, msgs, m, d)
		}
	}
	return &llm.Response{Model: "mock"}, []llm.ToolCall{{ID: "w1", Name: "wait"}}, nil
}

// A sub-agent's work is on disk while it runs (not only at the end), under
// the agent_id given by the caller, and a run stopped as a whole is saved as
// interrupted and can be resumed with that history.
func TestSubAgentCheckpointsWhileRunning(t *testing.T) {
	dir := t.TempDir()
	bt := blockTool{started: make(chan struct{})}
	reg := tools.NewRegistry()
	reg.Register(bt)
	p := &waitProvider{}
	cfg := resumeCfg(p, dir)
	cfg.Registry = reg
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan SubAgentResult, 1)
	go func() {
		done <- RunSubAgents(ctx, cfg, []SubAgentTask{{ID: "long", Description: "wait for LONG", AgentID: "a-fixed"}})[0]
	}()
	<-bt.started
	tr, err := LoadTranscript(dir, "a-fixed")
	if err != nil || tr.Status != StatusRunning {
		t.Fatalf("no checkpoint while running: %v %+v", err, tr.Status)
	}
	last := tr.Messages[len(tr.Messages)-1]
	if len(last.ToolCalls) != 1 || !strings.Contains(tr.Messages[1].Content, "wait for LONG") {
		t.Fatalf("checkpoint lacks the work so far: %+v", tr.Messages)
	}
	cancel()
	var r SubAgentResult
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if r.AgentID != "a-fixed" {
		t.Fatalf("agent id %q", r.AgentID)
	}
	if tr, _ = LoadTranscript(dir, "a-fixed"); tr.Status != StatusInterrupted || tr.Ended() {
		t.Fatalf("status %q, want interrupted", tr.Status)
	}

	r2 := RunSubAgents(context.Background(), cfg, []SubAgentTask{{ID: "long", Description: "the app restarted, continue", ResumeID: "a-fixed"}})[0]
	if r2.Error != "" || r2.AgentID != "a-fixed" {
		t.Fatalf("resume: %+v", r2)
	}
	seen := p.seen[len(p.seen)-1]
	var joined []string
	for _, m := range seen {
		joined = append(joined, m.Content)
	}
	all := strings.Join(joined, "\n")
	if !strings.Contains(all, "wait for LONG") || !strings.Contains(all, "the app restarted") {
		t.Fatalf("resumed run lost history: %s", all)
	}
	if tr, _ = LoadTranscript(dir, "a-fixed"); tr.Status != StatusFinished {
		t.Fatalf("status %q after resume", tr.Status)
	}
}

// While the app shuts down nothing is saved, so a dying run never overwrites
// the work to resume.
func TestSubAgentPersistOff(t *testing.T) {
	dir := t.TempDir()
	cfg := resumeCfg(&recProvider{}, dir)
	cfg.Persist = func() bool { return false }
	r := RunSubAgents(context.Background(), cfg, []SubAgentTask{{ID: "x", Description: "hi"}})[0]
	if _, err := LoadTranscript(dir, r.AgentID); err == nil {
		t.Fatal("transcript saved with Persist off")
	}
}
