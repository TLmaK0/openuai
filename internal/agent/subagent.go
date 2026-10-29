package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

// ErrMaxTurns is returned (wrapped) by Run when Config.MaxTurns is reached.
var ErrMaxTurns = errors.New("reached maxTurns")

// DefaultMaxConcurrentSubAgents is how many sub-agents run at once unless the
// config says otherwise (same default as Claude Code: 20).
const DefaultMaxConcurrentSubAgents = 20

// MaxSubAgentDepth is how many levels of sub-agents may exist below the main
// agent (same as Claude Code: 3). The deepest level cannot delegate further.
const MaxSubAgentDepth = 3

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
	// OnStart, when set, is called as each sub-agent starts (also for tasks
	// that fail during setup, so every task is visible).
	OnStart func(task SubAgentTask, model string)
	// OnDone, when set, is called with each sub-agent's result.
	OnDone func(SubAgentResult)
	// TaskContext, when set, derives the context one task runs under so it
	// can be cancelled on its own; release is called when the task ends.
	TaskContext func(taskID string, parent context.Context) (ctx context.Context, release func())
	// Acquire, when set, replaces the local MaxConcurrent limit: it takes a
	// slot of a limit shared by the whole tree of sub-agents. An error fails
	// the task without running it.
	Acquire func(ctx context.Context, task SubAgentTask) (release func(), err error)
	// TranscriptDir, when set, is where each sub-agent's work is saved so it
	// can be resumed later (see Transcript).
	TranscriptDir string
}

// SubAgentTask is a single task to assign to a sub-agent.
type SubAgentTask struct {
	ID          string
	Description string

	// Optional per-task settings, usually resolved from a subagent
	// definition. Empty values fall back to SubAgentConfig.
	Agent        string
	Model        string
	Registry     *tools.Registry
	SystemPrompt string
	MaxTurns     int
	// PermissionMode is the permission mode from the definition (PermMode*).
	PermissionMode string
	// Notice is passed through to the result (e.g. a model fallback).
	Notice string
	// SetupError fails the task without running it.
	SetupError string
	// ResumeID continues a previous sub-agent (its agent_id) with its full
	// history; Description is then the new message for it.
	ResumeID string
}

// SubAgentResult holds the outcome of a single sub-agent run.
type SubAgentResult struct {
	ID           string
	Agent        string
	Model        string
	Output       string
	Error        string
	Notice       string
	Partial      bool // stopped at maxTurns: Output is the work done so far
	Cancelled    bool // stopped on its own by the user: Output is the work done so far
	CostUSD      float64
	InputTokens  int
	OutputTokens int
	// AgentID identifies the saved work, to resume this sub-agent later.
	AgentID string
}

// RunSubAgents runs multiple tasks concurrently, each in its own Agent instance.
func RunSubAgents(ctx context.Context, cfg SubAgentConfig, tasks []SubAgentTask) []SubAgentResult {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrentSubAgents
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

			if cfg.Acquire != nil {
				if t.SetupError == "" {
					release, err := cfg.Acquire(ctx, t)
					if err != nil {
						t.SetupError = err.Error()
					} else {
						defer release()
					}
				}
			} else {
				sem <- struct{}{}
				defer func() { <-sem }()
			}

			results[idx] = runSingleSubAgent(ctx, cfg, t)
			if cfg.OnDone != nil {
				cfg.OnDone(results[idx])
			}
		}(i, task)
	}

	wg.Wait()
	return results
}

func runSingleSubAgent(ctx context.Context, cfg SubAgentConfig, task SubAgentTask) SubAgentResult {
	if task.SetupError != "" {
		logger.Error("[sub-agent:%s] Not started: %s", task.ID, task.SetupError)
		if cfg.OnStart != nil {
			cfg.OnStart(task, task.Model)
		}
		return SubAgentResult{ID: task.ID, Agent: task.Agent, Error: task.SetupError, Notice: task.Notice}
	}

	model := cfg.Model
	if task.Model != "" {
		model = task.Model
	}
	registry := cfg.Registry
	if task.Registry != nil {
		registry = task.Registry
	}
	prompt := subAgentSystemPrompt
	if task.SystemPrompt != "" {
		prompt = task.SystemPrompt
	}

	agentID := task.ResumeID
	var history []llm.Message
	created := time.Now()
	if task.ResumeID != "" {
		tr, err := LoadTranscript(cfg.TranscriptDir, task.ResumeID)
		if err != nil {
			msg := fmt.Sprintf("cannot resume sub-agent %q: %v", task.ResumeID, err)
			logger.Error("[sub-agent:%s] %s", task.ID, msg)
			if cfg.OnStart != nil {
				cfg.OnStart(task, model)
			}
			return SubAgentResult{ID: task.ID, Agent: task.Agent, Error: msg, Notice: task.Notice, AgentID: task.ResumeID}
		}
		history, created = tr.Messages, tr.CreatedAt
		logger.Info("[sub-agent:%s] Resuming %s (%d saved messages)", task.ID, task.ResumeID, len(tr.Messages))
	} else {
		agentID = NewAgentID()
	}

	logger.Info("[sub-agent:%s] Starting (agent=%q model=%q maxTurns=%d): %s", task.ID, task.Agent, model, task.MaxTurns, task.Description)
	if cfg.OnStart != nil {
		cfg.OnStart(task, model)
	}
	runCtx := ctx
	if cfg.TaskContext != nil {
		c, release := cfg.TaskContext(task.ID, ctx)
		defer release()
		runCtx = c
	}

	tracker := llm.NewCostTracker()

	var onStep func(StepResult)
	if cfg.OnStep != nil {
		onStep = func(step StepResult) {
			cfg.OnStep(task.ID, step)
		}
	}

	ag := New(Config{
		Provider:     cfg.Provider,
		Model:        model,
		Registry:     registry,
		Permissions:  cfg.Permissions,
		CostTracker:  tracker,
		OnStep:       onStep,
		SystemPrompt: prompt,
		MaxTurns:     task.MaxTurns,
		// A long run compacts its own context instead of dying when it fills up.
		CompactAtTokens: DefaultSubAgentCompactTokens,
		PermissionMode:  task.PermissionMode,
	})
	if history != nil {
		ag.SetHistory(history)
	}

	err := ag.Run(runCtx, task.Description)

	summary := tracker.Summary()
	result := SubAgentResult{
		ID:           task.ID,
		Agent:        task.Agent,
		Model:        model,
		Notice:       task.Notice,
		Output:       ag.LastAssistantContent(),
		CostUSD:      summary.TotalCostUSD,
		InputTokens:  summary.TotalInputTokens,
		OutputTokens: summary.TotalOutputTokens,
	}

	switch {
	case errors.Is(err, ErrMaxTurns):
		result.Partial = true
		// With no text answer yet, report the tool work done so far.
		result.Output = ag.PartialWork()
		logger.Info("[sub-agent:%s] Stopped at maxTurns=%d (partial): output_len=%d", task.ID, task.MaxTurns, len(result.Output))
	case err != nil && runCtx.Err() != nil && ctx.Err() == nil:
		// Only this sub-agent was cancelled (by the user), not the whole turn.
		result.Cancelled = true
		result.Error = "cancelled by the user"
		result.Output = ag.PartialWork()
		logger.Info("[sub-agent:%s] Cancelled by the user", task.ID)
	case err != nil:
		result.Error = err.Error()
		logger.Error("[sub-agent:%s] Error: %s", task.ID, err.Error())
	default:
		logger.Info("[sub-agent:%s] Complete: output_len=%d cost=$%.4f", task.ID, len(result.Output), result.CostUSD)
	}

	result.AgentID = agentID
	if cfg.TranscriptDir != "" {
		status := "finished"
		switch {
		case result.Partial:
			status = "partial"
		case result.Cancelled:
			status = "cancelled"
		case result.Error != "":
			status = "error"
		}
		if err := SaveTranscript(cfg.TranscriptDir, Transcript{
			ID: agentID, Agent: task.Agent, Model: model, Description: task.Description, Status: status,
			Messages: ag.Messages(), CreatedAt: created, UpdatedAt: time.Now(),
		}); err != nil {
			logger.Error("[sub-agent:%s] Could not save its work: %s", task.ID, err.Error())
		}
	}
	return result
}

// FormatSubAgentResults formats results into a readable string for the parent agent.
func FormatSubAgentResults(results []SubAgentResult) string {
	out := make([]tools.SubTaskResult, len(results))
	for i, r := range results {
		out[i] = tools.SubTaskResult{
			ID: r.ID, Agent: r.Agent, Model: r.Model, Output: r.Output, Error: r.Error,
			Notice: r.Notice, Partial: r.Partial, CostUSD: r.CostUSD, AgentID: r.AgentID,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		}
	}
	return tools.FormatResults(out)
}
