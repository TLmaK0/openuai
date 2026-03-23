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

// mockProvider implements llm.Provider (no tool calling — simple chat only).
type mockProvider struct {
	response string
	delay    time.Duration
}

func (m *mockProvider) Chat(ctx context.Context, _ []llm.Message, _ string) (*llm.Response, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &llm.Response{
		Content:      m.response,
		InputTokens:  10,
		OutputTokens: 20,
		Model:        "mock",
	}, nil
}

func (m *mockProvider) Name() string    { return "mock" }
func (m *mockProvider) Models() []string { return []string{"mock"} }

// mockToolProvider implements llm.ToolCallProvider — simulates an LLM that
// calls a tool on the first turn and then returns a final answer.
type mockToolProvider struct {
	mockProvider
	// toolToCall: if set, the first ChatWithTools call returns a tool call for this tool
	toolToCall string
	toolArgs   map[string]string
	finalReply string // returned after the tool result
	mu         sync.Mutex
	callCount  int
}

func (m *mockToolProvider) ChatWithTools(ctx context.Context, msgs []llm.Message, model string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}

	m.mu.Lock()
	m.callCount++
	call := m.callCount
	m.mu.Unlock()

	// First call: issue a tool call (if configured)
	if call == 1 && m.toolToCall != "" {
		return &llm.Response{
			Content:      "",
			InputTokens:  15,
			OutputTokens: 5,
			Model:        "mock-tools",
		}, []llm.ToolCall{
			{ID: "tc-1", Name: m.toolToCall, Arguments: m.toolArgs},
		}, nil
	}

	// Subsequent call (after tool result): return final answer
	reply := m.finalReply
	if reply == "" {
		reply = "Final answer from sub-agent"
	}
	return &llm.Response{
		Content:      reply,
		InputTokens:  20,
		OutputTokens: 30,
		Model:        "mock-tools",
	}, nil, nil
}

// mockTool is a simple tool for testing.
type mockTool struct {
	name   string
	output string
}

func (t mockTool) Definition() tools.Definition {
	return tools.Definition{
		Name:               t.name,
		Description:        "mock tool for testing",
		Parameters:         []tools.Parameter{{Name: "input", Type: "string", Description: "input", Required: false}},
		RequiresPermission: "none",
	}
}

func (t mockTool) Execute(_ context.Context, _ map[string]string) tools.Result {
	return tools.Result{Output: t.output}
}

func TestRunSubAgents_BasicResults(t *testing.T) {
	provider := &mockProvider{response: "Done with task"}
	registry := tools.NewRegistry()
	perms := NewPermissionManager("", nil)

	tasks := []SubAgentTask{
		{ID: "task-1", Description: "Do thing one"},
		{ID: "task-2", Description: "Do thing two"},
		{ID: "task-3", Description: "Do thing three"},
	}

	results := RunSubAgents(context.Background(), SubAgentConfig{
		Provider:      provider,
		Model:         "mock",
		Registry:      registry,
		Permissions:   perms,
		MaxConcurrent: 5,
	}, tasks)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for i, r := range results {
		if r.ID != tasks[i].ID {
			t.Errorf("result %d: expected ID %q, got %q", i, tasks[i].ID, r.ID)
		}
		if r.Output != "Done with task" {
			t.Errorf("result %d: expected output 'Done with task', got %q", i, r.Output)
		}
		if r.Error != "" {
			t.Errorf("result %d: unexpected error %q", i, r.Error)
		}
		if r.InputTokens != 10 || r.OutputTokens != 20 {
			t.Errorf("result %d: unexpected tokens in=%d out=%d", i, r.InputTokens, r.OutputTokens)
		}
	}
}

func TestRunSubAgents_ConcurrencyLimit(t *testing.T) {
	var concurrent int64
	var maxSeen int64

	provider := &mockProvider{response: "ok", delay: 50 * time.Millisecond}
	registry := tools.NewRegistry()
	perms := NewPermissionManager("", nil)

	tasks := make([]SubAgentTask, 10)
	for i := range tasks {
		tasks[i] = SubAgentTask{ID: "t" + string(rune('0'+i)), Description: "task"}
	}

	cfg := SubAgentConfig{
		Provider:      provider,
		Model:         "mock",
		Registry:      registry,
		Permissions:   perms,
		MaxConcurrent: 3,
		OnStep: func(taskID string, step StepResult) {
			cur := atomic.AddInt64(&concurrent, 1)
			for {
				old := atomic.LoadInt64(&maxSeen)
				if cur <= old || atomic.CompareAndSwapInt64(&maxSeen, old, cur) {
					break
				}
			}
			// simulate brief work
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&concurrent, -1)
		},
	}

	results := RunSubAgents(context.Background(), cfg, tasks)
	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}

	// maxSeen should not exceed MaxConcurrent
	if atomic.LoadInt64(&maxSeen) > 3 {
		t.Errorf("concurrency exceeded limit: max seen %d, limit 3", atomic.LoadInt64(&maxSeen))
	}
}

func TestRunSubAgents_ContextCancellation(t *testing.T) {
	provider := &mockProvider{response: "ok", delay: 5 * time.Second}
	registry := tools.NewRegistry()
	perms := NewPermissionManager("", nil)

	ctx, cancel := context.WithCancel(context.Background())

	tasks := []SubAgentTask{
		{ID: "slow", Description: "slow task"},
	}

	done := make(chan struct{})
	go func() {
		RunSubAgents(ctx, SubAgentConfig{
			Provider:      provider,
			Model:         "mock",
			Registry:      registry,
			Permissions:   perms,
			MaxConcurrent: 5,
		}, tasks)
		close(done)
	}()

	// Cancel quickly
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// good — returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("RunSubAgents did not return after context cancellation")
	}
}

func TestRunSubAgents_WithToolCalling(t *testing.T) {
	// Sub-agent that uses a tool (read_file) before answering
	provider := &mockToolProvider{
		toolToCall: "read_file",
		toolArgs:   map[string]string{"path": "/etc/hostname"},
		finalReply: "The hostname is test-machine",
	}

	registry := tools.NewRegistry()
	registry.Register(mockTool{name: "read_file", output: "test-machine"})

	perms := NewPermissionManager("", nil)

	tasks := []SubAgentTask{
		{ID: "read-host", Description: "Read the hostname"},
	}

	var steps []string
	results := RunSubAgents(context.Background(), SubAgentConfig{
		Provider:      provider,
		Model:         "mock-tools",
		Registry:      registry,
		Permissions:   perms,
		MaxConcurrent: 5,
		OnStep: func(taskID string, step StepResult) {
			steps = append(steps, fmt.Sprintf("[%s] %s: %s", taskID, step.Type, step.ToolName))
		},
	}, tasks)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.ID != "read-host" {
		t.Errorf("expected ID 'read-host', got %q", r.ID)
	}
	if r.Output != "The hostname is test-machine" {
		t.Errorf("expected output about hostname, got %q", r.Output)
	}
	if r.Error != "" {
		t.Errorf("unexpected error: %s", r.Error)
	}
	// Should have 35 input tokens (15 + 20) and 35 output tokens (5 + 30)
	if r.InputTokens != 35 {
		t.Errorf("expected 35 input tokens, got %d", r.InputTokens)
	}
	if r.OutputTokens != 35 {
		t.Errorf("expected 35 output tokens, got %d", r.OutputTokens)
	}

	// Verify we saw tool-related steps
	hasToolCall := false
	hasToolResult := false
	for _, s := range steps {
		if strings.Contains(s, "tool_call") {
			hasToolCall = true
		}
		if strings.Contains(s, "tool_result") {
			hasToolResult = true
		}
	}
	if !hasToolCall {
		t.Error("expected to see tool_call step")
	}
	if !hasToolResult {
		t.Error("expected to see tool_result step")
	}
}

func TestRunSubAgents_NoSpawnNesting(t *testing.T) {
	// Verify that Registry.Without works — sub-agents shouldn't see spawn_agents
	registry := tools.NewRegistry()
	registry.Register(mockTool{name: "bash", output: "ok"})
	registry.Register(mockTool{name: "read_file", output: "data"})
	registry.Register(mockTool{name: "spawn_agents", output: "should be excluded"})

	filtered := registry.Without("spawn_agents")

	if _, ok := filtered.Get("spawn_agents"); ok {
		t.Error("spawn_agents should be excluded from filtered registry")
	}
	if _, ok := filtered.Get("bash"); !ok {
		t.Error("bash should still be in filtered registry")
	}
	if _, ok := filtered.Get("read_file"); !ok {
		t.Error("read_file should still be in filtered registry")
	}

	// Original registry should be unaffected
	if _, ok := registry.Get("spawn_agents"); !ok {
		t.Error("spawn_agents should still be in original registry")
	}
}

func TestRunSubAgents_ParallelExecution(t *testing.T) {
	// Verify that tasks actually run in parallel (not serial)
	provider := &mockProvider{response: "done", delay: 100 * time.Millisecond}
	registry := tools.NewRegistry()
	perms := NewPermissionManager("", nil)

	tasks := make([]SubAgentTask, 5)
	for i := range tasks {
		tasks[i] = SubAgentTask{ID: fmt.Sprintf("p%d", i), Description: "parallel task"}
	}

	start := time.Now()
	results := RunSubAgents(context.Background(), SubAgentConfig{
		Provider:      provider,
		Model:         "mock",
		Registry:      registry,
		Permissions:   perms,
		MaxConcurrent: 5,
	}, tasks)
	elapsed := time.Since(start)

	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	// If serial, would take ~500ms. Parallel should be ~100ms (+overhead).
	if elapsed > 300*time.Millisecond {
		t.Errorf("expected parallel execution (~100ms), took %v — likely serial", elapsed)
	}
}

func TestRunSubAgents_StepsEmittedPerTask(t *testing.T) {
	provider := &mockProvider{response: "result"}
	registry := tools.NewRegistry()
	perms := NewPermissionManager("", nil)

	tasks := []SubAgentTask{
		{ID: "alpha", Description: "task A"},
		{ID: "beta", Description: "task B"},
	}

	var mu sync.Mutex
	stepsByTask := make(map[string]int)

	results := RunSubAgents(context.Background(), SubAgentConfig{
		Provider:      provider,
		Model:         "mock",
		Registry:      registry,
		Permissions:   perms,
		MaxConcurrent: 5,
		OnStep: func(taskID string, step StepResult) {
			mu.Lock()
			stepsByTask[taskID]++
			mu.Unlock()
		},
	}, tasks)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Each task should emit at least a "done" step
	if stepsByTask["alpha"] == 0 {
		t.Error("expected steps for task 'alpha'")
	}
	if stepsByTask["beta"] == 0 {
		t.Error("expected steps for task 'beta'")
	}
}
