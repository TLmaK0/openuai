package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// SubTask is a task to be executed by a sub-agent.
type SubTask struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// SubTaskResult holds the outcome of a sub-agent task.
type SubTaskResult struct {
	ID           string
	Output       string
	Error        string
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

// SpawnFunc is the function signature for spawning sub-agents.
type SpawnFunc func(ctx context.Context, tasks []SubTask) []SubTaskResult

// SpawnAgents is a tool that lets the parent agent run multiple sub-agents concurrently.
type SpawnAgents struct {
	Fn SpawnFunc
}

func (s SpawnAgents) Definition() Definition {
	return Definition{
		Name:        "spawn_agents",
		Description: "Run multiple sub-agents in parallel, each working on an independent task. Use this to decompose complex work into concurrent sub-tasks. Each sub-agent gets its own context and tools.",
		Parameters: []Parameter{
			{
				Name:        "tasks",
				Type:        "string",
				Description: `JSON array of tasks, each with "id" (unique identifier) and "description" (what the sub-agent should do). Example: [{"id":"research-go","description":"Research Go channels and summarize best practices"}]`,
				Required:    true,
			},
		},
		RequiresPermission: "session",
	}
}

func (s SpawnAgents) Execute(ctx context.Context, args map[string]string) Result {
	tasksJSON := args["tasks"]
	if tasksJSON == "" {
		return Result{Error: "tasks parameter is required"}
	}

	var tasks []SubTask
	if err := json.Unmarshal([]byte(tasksJSON), &tasks); err != nil {
		return Result{Error: fmt.Sprintf("invalid tasks JSON: %s", err.Error())}
	}

	if len(tasks) == 0 {
		return Result{Error: "at least one task is required"}
	}

	// Validate tasks
	seen := make(map[string]bool, len(tasks))
	for i, t := range tasks {
		if t.ID == "" {
			return Result{Error: fmt.Sprintf("task %d: id is required", i)}
		}
		if t.Description == "" {
			return Result{Error: fmt.Sprintf("task %q: description is required", t.ID)}
		}
		if seen[t.ID] {
			return Result{Error: fmt.Sprintf("duplicate task id: %q", t.ID)}
		}
		seen[t.ID] = true
	}

	results := s.Fn(ctx, tasks)

	// Format results
	var output string
	for _, r := range results {
		output += fmt.Sprintf("=== Task: %s ===\n", r.ID)
		if r.Error != "" {
			output += fmt.Sprintf("Error: %s\n", r.Error)
		}
		if r.Output != "" {
			output += r.Output + "\n"
		}
		output += fmt.Sprintf("(tokens: %d in, %d out, $%.4f)\n\n", r.InputTokens, r.OutputTokens, r.CostUSD)
	}

	return Result{Output: output}
}
