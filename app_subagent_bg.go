package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"openuai/internal/agent"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

// BackgroundNotice is the placeholder result of a sub-agent launched in the
// background: the turn that launched it does not wait for it.
const BackgroundNotice = "Running in the background: its result will arrive later as a notification starting with \"[Background sub-agents finished\". Do not wait for it: continue with other work or end your turn. To stop it, call stop_agent with its agent_id."

// bgNotificationPrefix starts the notification carrying the result of a
// background sub-agent.
const bgNotificationPrefix = "[Background sub-agents finished: "

// maxBgRestarts is how many restarts may interrupt the same background
// sub-agent: after that it is reported as failed instead of resumed again.
const maxBgRestarts = 3

// bgPendingPrefix starts the pending-queue key of a background sub-agent's
// result (see bgSubAgent.key).
const bgPendingPrefix = "subagent:"

type backgroundRunKey struct{}

// inBackgroundRun reports whether ctx belongs to a spawn that was already
// split into foreground and background tasks.
func inBackgroundRun(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundRunKey{}).(bool)
	return v
}

// bgOwnerKey carries the agent_id of the background sub-agent a spawn runs
// (it may resume its own record).
type bgOwnerKey struct{}

func bgOwner(ctx context.Context) string {
	v, _ := ctx.Value(bgOwnerKey{}).(string)
	return v
}

// bgSubAgent is a background sub-agent recorded on disk while it runs, so
// that after a restart it can be resumed from its saved transcript.
type bgSubAgent struct {
	AgentID     string `json:"agent_id"`
	TaskID      string `json:"task_id"`
	Description string `json:"description"` // the task as launched
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"` // as requested (alias, ID or "")
	Effort      string `json:"effort,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	// Parent is who launched it (only the main conversation launches
	// background sub-agents).
	Parent string `json:"parent"`
	// Restarts counts the restarts that interrupted it.
	Restarts  int       `json:"restarts"`
	StartedAt time.Time `json:"started_at"`
}

// key identifies its result in the pending queue: stable across restarts
// (so it is delivered once) and different for a later resume of the same
// agent_id (a new launch).
func (e bgSubAgent) key() string {
	return fmt.Sprintf("%s%s:%d:%s", bgPendingPrefix, e.AgentID, e.StartedAt.UnixNano(), e.TaskID)
}

// taskIDFromKey returns the task id stored in a pending key made by key().
func taskIDFromKey(k string) string {
	parts := strings.SplitN(strings.TrimPrefix(k, bgPendingPrefix), ":", 3)
	if len(parts) == 3 {
		return parts[2]
	}
	return k
}

// bgStore is the on-disk list of background sub-agents still running.
type bgStore struct {
	mu    sync.Mutex
	path  string
	items []bgSubAgent
}

func bgStorePath(configDir string) string {
	return filepath.Join(configDir, "sessions", "subagents_bg.json")
}

func loadBgStore(configDir string) *bgStore {
	s := &bgStore{path: bgStorePath(configDir)}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.items); err != nil {
			logger.Error("Background sub-agents list unreadable, starting empty: %s", err.Error())
		}
	}
	return s
}

func (s *bgStore) saveLocked() {
	if len(s.items) == 0 {
		_ = os.Remove(s.path)
		return
	}
	if err := writeFileAtomic(s.path, s.items); err != nil {
		logger.Error("Cannot save background sub-agents: %s", err.Error())
	}
}

// put adds e or replaces the record with its agent_id.
func (s *bgStore) put(e bgSubAgent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].AgentID == e.AgentID {
			s.items[i] = e
			s.saveLocked()
			return
		}
	}
	s.items = append(s.items, e)
	s.saveLocked()
}

func (s *bgStore) remove(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.items[:0]
	for _, it := range s.items {
		if it.AgentID != agentID {
			items = append(items, it)
		}
	}
	s.items = items
	s.saveLocked()
}

func (s *bgStore) has(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.AgentID == agentID {
			return true
		}
	}
	return false
}

func (s *bgStore) list() []bgSubAgent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bgSubAgent(nil), s.items...)
}

// bgBusyError is the error for a task resuming a background sub-agent that is
// still running (it must not run twice at once).
func bgBusyError(agentID string) string {
	return fmt.Sprintf("sub-agent %q is still running in the background; its result will arrive as a notification: do not launch it again (to restart it with new instructions, stop it first with stop_agent and then resume it)", agentID)
}

// spawnWithBackground runs fg now (the caller waits for them) and bg detached
// from the current turn: they keep running after the turn ends or is stopped
// (each can still be cancelled from its orb). Each background sub-agent is
// recorded on disk while it runs (so a restart resumes it) and its result is
// queued as a notification for the main agent as soon as it finishes.
func (a *App) spawnWithBackground(ctx context.Context, fg, bg []tools.SubTask) []tools.SubTaskResult {
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	bgCtx := context.WithValue(base, backgroundRunKey{}, true)
	ids := make([]string, len(bg))
	for i, t := range bg {
		ids[i] = t.ID
	}
	logger.Info("Sub-agents started in the background: %s", strings.Join(ids, ", "))

	var placeholders []tools.SubTaskResult
	for _, t := range bg {
		id := t.Resume
		if id != "" && a.bgAgents.has(id) {
			placeholders = append(placeholders, tools.SubTaskResult{ID: t.ID, Agent: t.Agent, Error: bgBusyError(id), AgentID: id})
			continue
		}
		if id == "" {
			id = agent.NewAgentID()
			t.AgentID = id
		}
		e := bgSubAgent{
			AgentID: id, TaskID: t.ID, Description: t.Description, Agent: t.Agent, Model: t.Model,
			Effort: t.Effort, Cwd: t.Cwd, Parent: "main", StartedAt: time.Now(),
		}
		a.bgAgents.put(e)
		go a.runBackground(bgCtx, t, e)
		placeholders = append(placeholders, tools.SubTaskResult{ID: t.ID, Agent: t.Agent, Notice: BackgroundNotice, AgentID: id})
	}

	var out []tools.SubTaskResult
	if len(fg) > 0 {
		out = a.spawnSubAgents(context.WithValue(ctx, backgroundRunKey{}, true), fg)
	}
	return append(out, placeholders...)
}

// runBackground runs one background sub-agent and queues its result.
func (a *App) runBackground(ctx context.Context, t tools.SubTask, e bgSubAgent) {
	ctx = context.WithValue(ctx, bgOwnerKey{}, e.AgentID)
	results := a.spawnSubAgents(ctx, []tools.SubTask{t})
	if t.OnFinish != nil {
		t.OnFinish()
	}
	// A stop_agent call may be waiting for this run: it is released once the
	// record below is updated, so the sub-agent can be resumed right away.
	run, stopped := a.subRuns.bgSettle(e.AgentID)
	defer a.subRuns.finish(run)
	if a.shuttingDown.Load() || ctx.Err() != nil {
		// The app is closing: keep its record, the next start resumes it.
		logger.Info("[sub-agent:%s] Stopped by the app closing; it will be resumed on the next start", t.ID)
		return
	}
	if stopped {
		// Stopped with stop_agent: that call reports its result, so no
		// notification is queued for it.
		logger.Info("[sub-agent:%s] Background sub-agent stopped by the agent (agent_id %s)", e.TaskID, e.AgentID)
		a.bgAgents.remove(e.AgentID)
		return
	}
	a.deliverBackground(e, bgNotificationPrefix+e.TaskID+"]\n\n"+tools.FormatResults(results))
}

// deliverBackground queues the result of background sub-agent e (on disk,
// once) and then forgets it as running.
func (a *App) deliverBackground(e bgSubAgent, notification string) {
	logger.Info("[sub-agent:%s] Background sub-agent finished (agent_id %s), notifying the agent", e.TaskID, e.AgentID)
	if a.pending != nil && a.pending.Add(e.key(), notification) {
		go a.drainPending()
	}
	a.bgAgents.remove(e.AgentID)
}

// bgRecovery is what recoverBackground decided at startup.
type bgRecovery struct {
	resume []bgSubAgent // still running before the restart: resumed
	failed []string     // could not be resumed (reported as failed)
	// failedKeys are the pending keys of the failed ones.
	failedKeys map[string]bool
	// queued are results (finished or failed) to add to the pending queue.
	queued []bgQueued
}

type bgQueued struct {
	e            bgSubAgent
	notification string
}

// recoverBackground decides, for each background sub-agent recorded before
// the restart, whether to resume it, deliver its result or report it as
// failed. It changes nothing (recoverBackgroundSubAgents applies it).
func recoverBackground(entries []bgSubAgent, transcriptDir string, delivered func(key string) bool) bgRecovery {
	r := bgRecovery{failedKeys: map[string]bool{}}
	// resumable: its history is still there (it can be resumed by hand).
	fail := func(e bgSubAgent, reason string, resumable bool) {
		logger.Error("[sub-agent:%s] Background sub-agent cannot be resumed after the restart: %s", e.TaskID, reason)
		res := tools.SubTaskResult{ID: e.TaskID, Agent: e.Agent, Error: "failed: " + reason}
		if resumable {
			res.AgentID = e.AgentID
		}
		r.failed = append(r.failed, fmt.Sprintf("%s (agent_id %s: %s)", e.TaskID, e.AgentID, reason))
		r.failedKeys[e.key()] = true
		r.queued = append(r.queued, bgQueued{e, bgNotificationPrefix + e.TaskID + "]\n\n" + tools.FormatResults([]tools.SubTaskResult{res})})
	}
	for _, e := range entries {
		if delivered(e.key()) {
			// Its result was queued just before the restart.
			r.queued = append(r.queued, bgQueued{e: e})
			continue
		}
		tr, err := agent.LoadTranscript(transcriptDir, e.AgentID)
		if err != nil {
			fail(e, "the app restarted while it was running and its saved history cannot be used to resume it ("+err.Error()+")", false)
			continue
		}
		if tr.Ended() {
			// It finished just before the restart, without being delivered.
			res := tools.SubTaskResult{ID: e.TaskID, Agent: e.Agent, Model: tr.Model, Error: tr.Error, AgentID: e.AgentID,
				Output: lastAssistant(tr.Messages), Partial: tr.Status == agent.StatusPartial}
			r.queued = append(r.queued, bgQueued{e, bgNotificationPrefix + e.TaskID + "]\n\n" + tools.FormatResults([]tools.SubTaskResult{res})})
			continue
		}
		if e.Restarts+1 >= maxBgRestarts {
			fail(e, fmt.Sprintf("it was interrupted by %d restarts, so it was not resumed again", e.Restarts+1), true)
			continue
		}
		e.Restarts++
		r.resume = append(r.resume, e)
	}
	return r
}

func lastAssistant(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// bgRestartNote tells the main agent, in the restart note, what happened to
// its background sub-agents.
func bgRestartNote(r bgRecovery, waiting []string) string {
	var parts []string
	if len(r.resume) > 0 {
		var names []string
		for _, e := range r.resume {
			names = append(names, fmt.Sprintf("%s (agent_id %s)", e.TaskID, e.AgentID))
		}
		parts = append(parts, "Background sub-agents resumed automatically after the restart (they keep working and their results will arrive as notifications; do NOT launch them again): "+strings.Join(names, ", ")+".")
	}
	if len(waiting) > 0 {
		parts = append(parts, "Background sub-agents that had already finished (their results arrive as separate notifications): "+strings.Join(waiting, ", ")+".")
	}
	if len(r.failed) > 0 {
		parts = append(parts, "Background sub-agents that could not be resumed (reported as failed): "+strings.Join(r.failed, "; ")+".")
	}
	return strings.Join(parts, " ")
}

// bgResumeMessage is the new message for a background sub-agent resumed
// after a restart.
func bgResumeMessage(at time.Time) string {
	return agent.RestartNotePrefix + " at " + at.Format("15:04") + "] The app restarted while you were working on your task, which interrupted you. " +
		"A restart is not a reason to stop: continue your task from where you left off. " +
		"Do not repeat steps that already completed; a step whose result says it was interrupted did not finish, so check its effect and run it again when needed. " +
		"Then give your final answer."
}

// recoverBackgroundSubAgents runs at startup (before the restart note): it
// queues the results of background sub-agents that ended before the restart,
// reports the ones that cannot be resumed, and returns the note for the main
// agent. The ones to resume start with startBgResumes.
func (a *App) recoverBackgroundSubAgents() string {
	r := recoverBackground(a.bgAgents.list(), filepath.Join(a.cfg.ConfigDir(), "subagents"), a.pending.Has)
	for _, q := range r.queued {
		if q.notification != "" {
			a.pending.Add(q.e.key(), q.notification)
		}
		a.bgAgents.remove(q.e.AgentID)
	}
	for _, e := range r.resume {
		a.bgAgents.put(e)
	}
	var waiting []string
	for _, k := range a.pending.Keys(bgPendingPrefix) {
		if !r.failedKeys[k] {
			waiting = append(waiting, taskIDFromKey(k))
		}
	}
	sort.Strings(waiting)
	a.bgResumes = r.resume
	if len(r.resume)+len(waiting)+len(r.failed) > 0 {
		logger.Info("Background sub-agents after the restart: resumed=%d finished=%d failed=%d", len(r.resume), len(waiting), len(r.failed))
	}
	return bgRestartNote(r, waiting)
}

// startBgResumes resumes the background sub-agents a restart interrupted.
// It runs once the UI is ready (or after the fallback delay), so their orbs
// and cards are shown.
func (a *App) startBgResumes() {
	a.bgResumeOnce.Do(func() {
		base := a.ctx
		if base == nil {
			base = context.Background()
		}
		bgCtx := context.WithValue(base, backgroundRunKey{}, true)
		now := time.Now()
		for _, e := range a.bgResumes {
			logger.Info("[sub-agent:%s] Resuming background sub-agent %s after the restart (restart %d)", e.TaskID, e.AgentID, e.Restarts)
			t := tools.SubTask{ID: e.TaskID, Description: bgResumeMessage(now), Agent: e.Agent, Model: e.Model,
				Effort: e.Effort, Cwd: e.Cwd, Resume: e.AgentID, Background: true}
			go a.runBackground(bgCtx, t, e)
		}
	})
}
