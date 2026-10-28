package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SubTask is a task to be executed by a sub-agent.
type SubTask struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	// Agent optionally names a subagent definition to run the task with.
	Agent string `json:"agent,omitempty"`
	// Model optionally overrides the model (alias, full ID or "inherit").
	Model string `json:"model,omitempty"`
}

// SubTaskResult holds the outcome of a sub-agent task.
type SubTaskResult struct {
	ID           string
	Agent        string
	Model        string
	Output       string
	Error        string
	Notice       string
	Partial      bool
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

// PartialNote heads the output of a sub-agent stopped by its maxTurns limit.
const PartialNote = "[PARTIAL] The sub-agent reached its maxTurns limit before finishing; below is the work done so far."

// SpawnFunc is the function signature for spawning sub-agents.
type SpawnFunc func(ctx context.Context, tasks []SubTask) []SubTaskResult

// SpawnAgents is a tool that lets the parent agent run multiple sub-agents concurrently.
type SpawnAgents struct {
	Fn SpawnFunc
	// Agents, when set, lists the subagent definitions available (one
	// "- name: description" per line); it is added to the tool description.
	Agents func() string
}

func (s SpawnAgents) Definition() Definition {
	desc := "Run multiple sub-agents in parallel, each working on an independent task. Use this to decompose complex work into concurrent sub-tasks. Each sub-agent starts with a fresh context (it does NOT see this conversation), so describe the task fully. A task may name an \"agent\" (a subagent definition with its own instructions, tools and model) and a \"model\"; without an agent the task runs with the general-purpose agent.\n\nDelegate proactively: when a task matches an agent's description, use that agent without waiting to be asked. Use \"explore\" for searches and investigations that only need reading, \"general-purpose\" for self-contained tasks that also need to act, and more specific agents when they fit better. Launch independent tasks together in one call so they run in parallel."
	if s.Agents != nil {
		if list := s.Agents(); list != "" {
			desc += "\n\nAvailable agents:\n" + list
		}
	}
	return Definition{
		Name:        "spawn_agents",
		Description: desc,
		Parameters: []Parameter{
			{
				Name:        "tasks",
				Type:        "string",
				Description: `JSON array of tasks, each with "id" (unique identifier), "description" (what the sub-agent should do) and optionally "agent" (name of a subagent definition) and "model" (e.g. "sonnet", "haiku", a full model ID, or "inherit"). Example: [{"id":"review","agent":"code-reviewer","description":"Review the changes in internal/api"}]`,
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

	return Result{Output: FormatResults(s.Fn(ctx, tasks))}
}

// FormatResults renders sub-agent results for the parent agent.
func FormatResults(results []SubTaskResult) string {
	var b strings.Builder
	for _, r := range results {
		b.WriteString("=== Task: " + r.ID)
		var tags []string
		if r.Agent != "" {
			tags = append(tags, "agent: "+r.Agent)
		}
		if r.Model != "" {
			tags = append(tags, "model: "+r.Model)
		}
		if len(tags) > 0 {
			b.WriteString(" (" + strings.Join(tags, ", ") + ")")
		}
		b.WriteString(" ===\n")
		if r.Partial {
			b.WriteString(PartialNote + "\n")
		}
		if r.Notice != "" {
			b.WriteString("Note: " + r.Notice + "\n")
		}
		if r.Error != "" {
			b.WriteString("Error: " + r.Error + "\n")
		}
		if r.Output != "" {
			b.WriteString(r.Output + "\n")
		}
		fmt.Fprintf(&b, "(tokens: %d in, %d out, $%.4f)\n\n", r.InputTokens, r.OutputTokens, r.CostUSD)
	}
	return b.String()
}
