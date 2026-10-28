package agent

import (
	"context"
	"fmt"
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
