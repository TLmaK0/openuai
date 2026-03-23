package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSpawnAgents_ValidJSON(t *testing.T) {
	spawner := SpawnAgents{
		Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult {
			results := make([]SubTaskResult, len(tasks))
			for i, task := range tasks {
				results[i] = SubTaskResult{
					ID:           task.ID,
					Output:       "Completed: " + task.Description,
					InputTokens:  100,
					OutputTokens: 200,
					CostUSD:      0.01,
				}
			}
			return results
		},
	}

	result := spawner.Execute(context.Background(), map[string]string{
		"tasks": `[{"id":"t1","description":"Research Go"},{"id":"t2","description":"Research Svelte"}]`,
	})

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "Task: t1") {
		t.Error("output should contain task t1")
	}
	if !strings.Contains(result.Output, "Task: t2") {
		t.Error("output should contain task t2")
	}
	if !strings.Contains(result.Output, "Completed: Research Go") {
		t.Error("output should contain t1 result")
	}
	if !strings.Contains(result.Output, "Completed: Research Svelte") {
		t.Error("output should contain t2 result")
	}
}

func TestSpawnAgents_InvalidJSON(t *testing.T) {
	spawner := SpawnAgents{Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult { return nil }}

	result := spawner.Execute(context.Background(), map[string]string{
		"tasks": `not json`,
	})
	if result.Error == "" {
		t.Error("expected error for invalid JSON")
	}
}

func TestSpawnAgents_EmptyTasks(t *testing.T) {
	spawner := SpawnAgents{Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult { return nil }}

	result := spawner.Execute(context.Background(), map[string]string{
		"tasks": `[]`,
	})
	if result.Error == "" || !strings.Contains(result.Error, "at least one task") {
		t.Errorf("expected 'at least one task' error, got: %q", result.Error)
	}
}

func TestSpawnAgents_MissingID(t *testing.T) {
	spawner := SpawnAgents{Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult { return nil }}

	result := spawner.Execute(context.Background(), map[string]string{
		"tasks": `[{"id":"","description":"do stuff"}]`,
	})
	if result.Error == "" || !strings.Contains(result.Error, "id is required") {
		t.Errorf("expected 'id is required' error, got: %q", result.Error)
	}
}

func TestSpawnAgents_DuplicateID(t *testing.T) {
	spawner := SpawnAgents{Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult { return nil }}

	result := spawner.Execute(context.Background(), map[string]string{
		"tasks": `[{"id":"x","description":"a"},{"id":"x","description":"b"}]`,
	})
	if result.Error == "" || !strings.Contains(result.Error, "duplicate") {
		t.Errorf("expected 'duplicate' error, got: %q", result.Error)
	}
}

func TestSpawnAgents_MissingParam(t *testing.T) {
	spawner := SpawnAgents{Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult { return nil }}

	result := spawner.Execute(context.Background(), map[string]string{})
	if result.Error == "" || !strings.Contains(result.Error, "required") {
		t.Errorf("expected 'required' error, got: %q", result.Error)
	}
}

func TestSpawnAgents_WithErrors(t *testing.T) {
	spawner := SpawnAgents{
		Fn: func(ctx context.Context, tasks []SubTask) []SubTaskResult {
			return []SubTaskResult{
				{ID: "ok", Output: "success"},
				{ID: "fail", Error: "something broke"},
			}
		},
	}

	result := spawner.Execute(context.Background(), map[string]string{
		"tasks": `[{"id":"ok","description":"good"},{"id":"fail","description":"bad"}]`,
	})

	if result.Error != "" {
		t.Fatalf("unexpected top-level error: %s", result.Error)
	}
	if !strings.Contains(result.Output, "success") {
		t.Error("output should contain success result")
	}
	if !strings.Contains(result.Output, "something broke") {
		t.Error("output should contain error from failed task")
	}
}

func TestSpawnAgents_Definition(t *testing.T) {
	spawner := SpawnAgents{}
	def := spawner.Definition()

	if def.Name != "spawn_agents" {
		t.Errorf("expected name 'spawn_agents', got %q", def.Name)
	}
	if def.RequiresPermission != "session" {
		t.Errorf("expected permission 'session', got %q", def.RequiresPermission)
	}
	if len(def.Parameters) != 1 || def.Parameters[0].Name != "tasks" {
		t.Error("expected single 'tasks' parameter")
	}
}
