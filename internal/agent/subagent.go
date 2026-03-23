package agent

import (
	"context"
	"fmt"
	"sync"

	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

const subAgentSystemPrompt = `You are a focused sub-agent of OpenUAI working on a specific task.
Complete it efficiently using your tools. Be direct and concise.`

// SubAgentConfig holds everything needed to spawn sub-agents.
type SubAgentConfig struct {
	Provider      llm.Provider
	Model         string
	Registry      *tools.Registry // should already have spawn_agents excluded
	Permissions   *PermissionManager
	MaxConcurrent int
	OnStep        func(taskID string, step StepResult)
}

// SubAgentTask is a single task to assign to a sub-agent.
type SubAgentTask struct {
	ID          string
	Description string
}

// SubAgentResult holds the outcome of a single sub-agent run.
type SubAgentResult struct {
	ID           string
	Output       string
	Error        string
	CostUSD      float64
	InputTokens  int
	OutputTokens int
}

// RunSubAgents runs multiple tasks concurrently, each in its own Agent instance.
func RunSubAgents(ctx context.Context, cfg SubAgentConfig, tasks []SubAgentTask) []SubAgentResult {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 5
	}

	results := make([]SubAgentResult, len(tasks))
	sem := make(chan struct{}, cfg.MaxConcurrent)
	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t SubAgentTask) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			result := runSingleSubAgent(ctx, cfg, t)
			results[idx] = result
		}(i, task)
	}

	wg.Wait()
	return results
}

func runSingleSubAgent(ctx context.Context, cfg SubAgentConfig, task SubAgentTask) SubAgentResult {
	logger.Info("[sub-agent:%s] Starting: %s", task.ID, task.Description)

	tracker := llm.NewCostTracker()

	var onStep func(StepResult)
	if cfg.OnStep != nil {
		onStep = func(step StepResult) {
			cfg.OnStep(task.ID, step)
		}
	}

	ag := New(Config{
		Provider:    cfg.Provider,
		Model:       cfg.Model,
		Registry:    cfg.Registry,
		Permissions: cfg.Permissions,
		CostTracker: tracker,
		OnStep:      onStep,
	})
	// Override system prompt for sub-agents
	if len(ag.messages) > 0 {
		ag.messages[0].Content = subAgentSystemPrompt
	}

	err := ag.Run(ctx, task.Description)

	summary := tracker.Summary()
	result := SubAgentResult{
		ID:           task.ID,
		Output:       ag.LastAssistantContent(),
		CostUSD:      summary.TotalCostUSD,
		InputTokens:  summary.TotalInputTokens,
		OutputTokens: summary.TotalOutputTokens,
	}

	if err != nil {
		result.Error = err.Error()
		logger.Error("[sub-agent:%s] Error: %s", task.ID, err.Error())
	} else {
		logger.Info("[sub-agent:%s] Complete: output_len=%d cost=$%.4f", task.ID, len(result.Output), result.CostUSD)
	}

	return result
}

// FormatSubAgentResults formats results into a readable string for the parent agent.
func FormatSubAgentResults(results []SubAgentResult) string {
	var out string
	for _, r := range results {
		out += fmt.Sprintf("=== Task: %s ===\n", r.ID)
		if r.Error != "" {
			out += fmt.Sprintf("Error: %s\n", r.Error)
		}
		if r.Output != "" {
			out += r.Output + "\n"
		}
		out += fmt.Sprintf("(tokens: %d in, %d out, $%.4f)\n\n", r.InputTokens, r.OutputTokens, r.CostUSD)
	}
	return out
}
