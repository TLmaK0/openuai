package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// panicStepsProvider calls the tool "boom" (which panics), then the tool
// "ok", then answers. The step is taken from the tool results already in the
// conversation, so it can be shared by concurrent sub-agents.
type panicStepsProvider struct{ mockProvider }

func (p *panicStepsProvider) ChatWithTools(_ context.Context, msgs []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleToolResult {
			n++
		}
	}
	resp := &llm.Response{InputTokens: 1, OutputTokens: 1, Model: "mock"}
	switch n {
	case 0:
		return resp, []llm.ToolCall{{ID: "tc-boom", Name: "boom", Arguments: map[string]string{}}}, nil
	case 1:
		return resp, []llm.ToolCall{{ID: "tc-ok", Name: "ok", Arguments: map[string]string{}}}, nil
	}
	resp.Content = "finished after the panic"
	return resp, nil, nil
}

func panicRegistry(okRuns *int32) *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "boom", perm: "none", fn: func(context.Context, map[string]string) tools.Result {
		var s []int
		_ = s[3] // index out of range, like the stripHTMLTags crash (#109)
		return tools.Result{Output: "unreachable"}
	}})
	reg.Register(fnTool{name: "ok", perm: "none", fn: func(context.Context, map[string]string) tools.Result {
		atomic.AddInt32(okRuns, 1)
		return tools.Result{Output: "ok output"}
	}})
	return reg
}

// A tool that panics is reported to the agent as a tool error and the agent
// loop keeps running (next tool call and final answer).
func TestToolPanicBecomesToolError(t *testing.T) {
	var okRuns int32
	var mu sync.Mutex
	var steps []StepResult
	ag := New(Config{
		Provider: &panicStepsProvider{}, Model: "mock", Registry: panicRegistry(&okRuns),
		Permissions: NewPermissionManager("", nil),
		OnStep: func(s StepResult) {
			mu.Lock()
			steps = append(steps, s)
			mu.Unlock()
		},
	})
	if err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var boom string
	for _, m := range ag.Messages() {
		if m.Role == llm.RoleToolResult && m.ToolCallID == "tc-boom" {
			boom = m.Content
		}
	}
	if !strings.HasPrefix(boom, "Error: tool boom crashed:") || !strings.Contains(boom, "index out of range") {
		t.Fatalf("panic not reported as a tool error, got %q", boom)
	}
	if atomic.LoadInt32(&okRuns) != 1 {
		t.Fatalf("the loop did not go on to the next tool call (ok ran %d times)", okRuns)
	}
	if got := ag.LastAssistantContent(); got != "finished after the panic" {
		t.Fatalf("final answer = %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	last := steps[len(steps)-1]
	if last.Type != "done" {
		t.Fatalf("last step = %+v, want done", last)
	}
}

// A tool panic inside concurrent sub-agents is a tool error too: every
// sub-agent finishes normally and the process survives.
func TestSubAgentToolPanicDoesNotCrash(t *testing.T) {
	var okRuns int32
	var done int32
	tasks := make([]SubAgentTask, 4)
	for i := range tasks {
		tasks[i] = SubAgentTask{ID: fmt.Sprintf("t%d", i), Description: "call boom then ok"}
	}
	res := RunSubAgents(context.Background(), SubAgentConfig{
		Provider: &panicStepsProvider{}, Model: "mock", Registry: panicRegistry(&okRuns),
		Permissions: NewPermissionManager("", nil), MaxConcurrent: 4,
		OnDone: func(SubAgentResult) { atomic.AddInt32(&done, 1) },
	}, tasks)

	for _, r := range res {
		if r.Error != "" || r.Output != "finished after the panic" {
			t.Errorf("sub-agent %s: %+v", r.ID, r)
		}
	}
	if okRuns != 4 || done != 4 {
		t.Fatalf("ok runs = %d, OnDone calls = %d, want 4 and 4", okRuns, done)
	}
}

// Any other panic in a sub-agent's goroutine ends only that sub-agent, with
// an error result, and OnDone is still called once for it.
func TestSubAgentGoroutinePanicIsContained(t *testing.T) {
	var mu sync.Mutex
	done := map[string]int{}
	res := RunSubAgents(context.Background(), SubAgentConfig{
		Provider: &mockProvider{response: "fine"}, Model: "mock", Registry: tools.NewRegistry(),
		Permissions: NewPermissionManager("", nil), MaxConcurrent: 2,
		Acquire: func(_ context.Context, t SubAgentTask) (func(), error) {
			if t.ID == "bad" {
				panic("acquire exploded")
			}
			return func() {}, nil
		},
		OnDone: func(r SubAgentResult) {
			mu.Lock()
			done[r.ID]++
			mu.Unlock()
		},
	}, []SubAgentTask{{ID: "bad", Description: "x"}, {ID: "good", Description: "y"}})

	if !strings.Contains(res[0].Error, "sub-agent crashed: acquire exploded") {
		t.Fatalf("bad result = %+v", res[0])
	}
	if res[1].Error != "" || res[1].Output != "fine" {
		t.Fatalf("good result = %+v", res[1])
	}
	if done["bad"] != 1 || done["good"] != 1 {
		t.Fatalf("OnDone calls = %v", done)
	}
}
