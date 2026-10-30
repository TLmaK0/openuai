package agent

import (
	"context"
	"sync"
	"testing"

	"openuai/internal/tools"
)

// runModelTask runs one sub-agent (a tool call, then an answer: two model
// responses, both reporting "mock-tools") and returns the model OnStart got
// and every model OnModel reported.
func runModelTask(t *testing.T, parentModel string, task SubAgentTask) (started string, reported []string, res SubAgentResult) {
	t.Helper()
	provider := &mockToolProvider{toolToCall: "mock_tool", finalReply: "done"}
	registry := tools.NewRegistry()
	registry.Register(mockTool{name: "mock_tool", output: "ok"})
	var mu sync.Mutex
	results := RunSubAgents(context.Background(), SubAgentConfig{
		Provider:    provider,
		Model:       parentModel,
		Registry:    registry,
		Permissions: NewPermissionManager("", nil),
		OnStart: func(_ SubAgentTask, model string) {
			mu.Lock()
			started = model
			mu.Unlock()
		},
		OnModel: func(taskID, model string) {
			if taskID != task.ID {
				t.Errorf("OnModel task id = %q, want %q", taskID, task.ID)
			}
			mu.Lock()
			reported = append(reported, model)
			mu.Unlock()
		},
	}, []SubAgentTask{task})
	if provider.callCount != 2 {
		t.Fatalf("expected 2 model calls, got %d", provider.callCount)
	}
	return started, reported, results[0]
}

func TestSubAgentModel_TaskModelWinsOverParent(t *testing.T) {
	started, _, res := runModelTask(t, "parent-model", SubAgentTask{ID: "a", Description: "x", Model: "task-model"})
	if started != "task-model" || res.Model != "task-model" {
		t.Errorf("started=%q result=%q, want task-model for both", started, res.Model)
	}
}

func TestSubAgentModel_FallsBackToParent(t *testing.T) {
	started, _, res := runModelTask(t, "parent-model", SubAgentTask{ID: "a", Description: "x"})
	if started != "parent-model" || res.Model != "parent-model" {
		t.Errorf("started=%q result=%q, want parent-model for both", started, res.Model)
	}
}

// A sub-agent started with an alias reports, once, the model the provider
// says it actually runs on.
func TestSubAgentModel_ReportsActualModelOnce(t *testing.T) {
	_, reported, _ := runModelTask(t, "parent-model", SubAgentTask{ID: "a", Description: "x", Model: "default"})
	if len(reported) != 1 || reported[0] != "mock-tools" {
		t.Errorf("reported = %v, want [mock-tools]", reported)
	}
}

// Nothing is reported when the provider confirms the model it started with.
func TestSubAgentModel_NoReportWhenUnchanged(t *testing.T) {
	_, reported, _ := runModelTask(t, "parent-model", SubAgentTask{ID: "a", Description: "x", Model: "mock-tools"})
	if len(reported) != 0 {
		t.Errorf("reported = %v, want none", reported)
	}
}
