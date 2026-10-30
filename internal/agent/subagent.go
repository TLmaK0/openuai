package agent

import (
	"context"
	"errors"
	"fmt"
	"openuai/internal/hooks"
	"strings"
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
	// Persist, when set, is asked before each save of a transcript; false
	// keeps the saved work as it is (the app is shutting down: what a dying
	// run does then must not overwrite the work to resume).
	Persist func() bool
	// Hooks returns the user/project hooks; WorkDir is where hooks run.
	Hooks   func() *hooks.Set
	WorkDir string
	// SessionID identifies the main conversation to hooks.
	SessionID string
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
	// Effort is the effort level (definition or per-task parameter).
	Effort string
	// PermissionMode is the permission mode from the definition (PermMode*).
	PermissionMode string
	// Notice is passed through to the result (e.g. a model fallback).
	Notice string
	// SetupError fails the task without running it.
	SetupError string
	// ResumeID continues a previous sub-agent (its agent_id) with its full
	// history; Description is then the new message for it.
	ResumeID string
	// AgentID, when set (and ResumeID is not), is the agent_id the new
	// sub-agent gets, so the caller knows it before the run starts.
	AgentID string
	// Hooks are the hooks of the sub-agent's definition, active only while
	// it runs; WorkDir overrides SubAgentConfig.WorkDir (isolated worktree).
	Hooks   *hooks.Set
	WorkDir string
	// ProjectDir is the directory the task was launched in when it is not
	// the app's (spawn_agents "cwd"); "" for the app's directory.
	ProjectDir string
	// ProjectHooks, when set, replaces SubAgentConfig.Hooks: the user and
	// project hooks of ProjectDir.
	ProjectHooks func() *hooks.Set
	// Allow pre-approves tool calls for the whole run (a skill's
	// allowed-tools when the skill runs in a sub-agent).
	Allow func(tool, arg string) bool
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
	} else if task.AgentID != "" {
		agentID = task.AgentID
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

	agentType := task.Agent
	if agentType == "" {
		agentType = "general-purpose"
	}
	workDir := cfg.WorkDir
	if task.WorkDir != "" {
		workDir = task.WorkDir
	}
	hookFn := func() *hooks.Set {
		var base *hooks.Set
		switch {
		case task.ProjectHooks != nil:
			base = task.ProjectHooks()
		case cfg.Hooks != nil:
			base = cfg.Hooks()
		}
		return base.Merge(task.Hooks)
	}
	// SubagentStart hooks can add context to the sub-agent's prompt.
	if hs := hookFn(); hs.Has(hooks.SubagentStart) {
		step := func(st StepResult) {
			if cfg.OnStep != nil {
				cfg.OnStep(task.ID, st)
			}
		}
		sid := cfg.SessionID
		if sid == "" {
			sid = "openuai"
		}
		hctx := hooks.WithStatus(runCtx, func(m string) { step(StepResult{Type: "notice", Content: m}) })
		r := hs.Run(hctx, hooks.SubagentStart, agentType, map[string]any{"session_id": sid, "agent_id": agentID, "agent_type": agentType}, workDir)
		logger.Info("[sub-agent:%s] SubagentStart hooks: ran=%d context=%d errors=%d", task.ID, r.Ran, len(r.Context), len(r.Errors))
		for _, e := range r.Errors {
			logger.Error("[sub-agent:%s] Hook error: %s", task.ID, e)
			step(StepResult{Type: "error", Content: "Hook error: " + e})
		}
		// Exit code 2 on SubagentStart only shows stderr (in the sub-agent's view).
		for _, e := range r.Notices {
			step(StepResult{Type: "error", Content: "SubagentStart hook error: " + e})
		}
		for _, m := range r.Messages {
			step(StepResult{Type: "notice", Content: "Hook: " + m})
		}
		if notes := hookNotes(r); len(notes) > 0 {
			prompt += "\n\n## Context from hooks\n" + strings.Join(notes, "\n")
		}
	}

	var onStep func(StepResult)
	if cfg.OnStep != nil {
		onStep = func(step StepResult) {
			cfg.OnStep(task.ID, step)
		}
	}

	// The work is saved after every step (not only at the end), so a
	// sub-agent interrupted by a restart can be resumed with its history.
	save := func(ag *Agent, status, errMsg string) {
		if cfg.TranscriptDir == "" || (cfg.Persist != nil && !cfg.Persist()) {
			return
		}
		if err := SaveTranscript(cfg.TranscriptDir, Transcript{
			ID: agentID, Agent: task.Agent, Model: model, Description: task.Description, Status: status, Error: errMsg, Dir: task.ProjectDir,
			Messages: ag.Messages(), CreatedAt: created, UpdatedAt: time.Now(),
		}); err != nil {
			logger.Error("[sub-agent:%s] Could not save its work: %s", task.ID, err.Error())
		}
	}

	ag := New(Config{
		OnCheckpoint: func(ag *Agent) { save(ag, StatusRunning, "") },
		Provider:     cfg.Provider,
		Model:        model,
		Registry:     registry,
		Permissions:  cfg.Permissions,
		CostTracker:  tracker,
		OnStep:       onStep,
		SystemPrompt: prompt,
		MaxTurns:     task.MaxTurns,
		Effort:       task.Effort,
		// A long run compacts its own context instead of dying when it fills up.
		CompactAtTokens: DefaultSubAgentCompactTokens,
		PermissionMode:  task.PermissionMode,
		Hooks:           hookFn,
		WorkDir:         workDir,
		AgentType:       agentType,
		AgentID:         agentID,
		Allow:           task.Allow,
		SessionID:       cfg.SessionID,
	})
	if history != nil {
		ag.SetHistory(history)
	}

	ag.Begin(task.Description)
	save(ag, StatusRunning, "")
	err := ag.Continue(runCtx)

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

	interrupted := false
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
	case err != nil && ctx.Err() != nil:
		interrupted = true
		result.Error = err.Error()
		logger.Info("[sub-agent:%s] Interrupted: %s", task.ID, err.Error())
	case err != nil:
		result.Error = err.Error()
		logger.Error("[sub-agent:%s] Error: %s", task.ID, err.Error())
	default:
		logger.Info("[sub-agent:%s] Complete: output_len=%d cost=$%.4f", task.ID, len(result.Output), result.CostUSD)
	}

	result.AgentID = agentID
	status := StatusFinished
	switch {
	case result.Partial:
		status = StatusPartial
	case result.Cancelled:
		status = StatusCancelled
	case interrupted:
		// The whole run was stopped (turn stopped or app closing): it can
		// be resumed later.
		status = StatusInterrupted
	case result.Error != "":
		status = StatusError
	}
	save(ag, status, result.Error)
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
