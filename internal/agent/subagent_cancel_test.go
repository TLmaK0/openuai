package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// Cancelling one sub-agent stops only that one; the other finishes normally,
// and every task reports start and done.
func TestCancelOneSubAgent(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(mockTool{name: "list_dir", output: "x"})
	var mu sync.Mutex
	cancels := map[string]context.CancelFunc{}
	started, done := map[string]bool{}, map[string]SubAgentResult{}
	res := RunSubAgents(context.Background(), SubAgentConfig{
		Provider: &slowLoopProvider{}, Model: "mock", Registry: reg, Permissions: NewPermissionManager("", nil), MaxConcurrent: 2,
		OnStart: func(task SubAgentTask, _ string) { mu.Lock(); started[task.ID] = true; mu.Unlock() },
		OnDone:  func(r SubAgentResult) { mu.Lock(); done[r.ID] = r; mu.Unlock() },
		TaskContext: func(id string, parent context.Context) (context.Context, func()) {
			c, cancel := context.WithCancel(parent)
			mu.Lock()
			cancels[id] = cancel
			mu.Unlock()
			if id == "loop" {
				go func() { time.Sleep(100 * time.Millisecond); cancel() }()
			}
			return c, cancel
		},
	}, []SubAgentTask{{ID: "loop", Description: "loop forever"}, {ID: "short", Description: "one turn", MaxTurns: 1}})
	if !res[0].Cancelled || res[0].Error == "" {
		t.Fatalf("loop should be cancelled: %+v", res[0])
	}
	if res[1].Cancelled || !res[1].Partial {
		t.Fatalf("short should finish on its own: %+v", res[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(started) != 2 || len(done) != 2 {
		t.Fatalf("started=%v done=%d", started, len(done))
	}
}

// slowLoopProvider only calls tools, takes a moment per call and honours
// cancellation, like a real provider waiting on the network.
type slowLoopProvider struct {
	mockProvider
	calls int32
}

func (p *slowLoopProvider) ChatWithTools(ctx context.Context, _ []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	n := atomic.AddInt32(&p.calls, 1)
	return &llm.Response{InputTokens: 1, OutputTokens: 1, Model: "mock"},
		[]llm.ToolCall{{ID: fmt.Sprintf("tc-%d", n), Name: "list_dir", Arguments: map[string]string{"path": "/x"}}}, nil
}

// A sub-agent cancelled on its own (Cancel button or stop_agent) can be
// resumed: the new run gets its whole history, with the tool call cut short
// closed, plus the new message.
func TestResumeAfterCancelKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(mockTool{name: "list_dir", output: "LISTING42"})
	cfg := SubAgentConfig{
		Provider: &slowLoopProvider{}, Model: "mock", Registry: reg, Permissions: NewPermissionManager("", nil), MaxConcurrent: 1, TranscriptDir: dir,
		TaskContext: func(_ string, parent context.Context) (context.Context, func()) {
			c, cancel := context.WithCancel(parent)
			go func() { time.Sleep(100 * time.Millisecond); cancel() }()
			return c, cancel
		},
	}
	r1 := RunSubAgents(context.Background(), cfg, []SubAgentTask{{ID: "loop", Description: "loop forever on PLATANO"}})[0]
	if !r1.Cancelled || r1.AgentID == "" || !strings.Contains(r1.Output, "LISTING42") {
		t.Fatalf("first run: %+v", r1)
	}
	if tr, err := LoadTranscript(dir, r1.AgentID); err != nil || tr.Status != StatusCancelled {
		t.Fatalf("transcript: %v %q", err, tr.Status)
	}

	p := &recProvider{}
	r2 := RunSubAgents(context.Background(), resumeCfg(p, dir), []SubAgentTask{{ID: "loop", Description: "new rule: RULE42; go on", ResumeID: r1.AgentID}})[0]
	if r2.Error != "" || r2.Cancelled || r2.AgentID != r1.AgentID {
		t.Fatalf("resume: %+v", r2)
	}
	msgs := p.seen[0]
	var all []string
	results := map[string]bool{}
	for _, m := range msgs {
		all = append(all, m.Content)
		if m.Role == llm.RoleToolResult {
			results[m.ToolCallID] = true
		}
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"PLATANO", "LISTING42", "RULE42"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("resumed run did not see %q:\n%s", want, joined)
		}
	}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if !results[tc.ID] {
				t.Fatalf("tool call %s left without a result", tc.ID)
			}
		}
	}
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser || !strings.Contains(last.Content, "RULE42") {
		t.Fatalf("the new instructions must be the last message: %+v", last)
	}
}
