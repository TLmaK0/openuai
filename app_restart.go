package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openuai/internal/agent"
	"openuai/internal/logger"
	"openuai/internal/procs"
)

// handledRetention is how long handled background-message keys are kept to
// recognise a message delivered again after a restart.
const handledRetention = 7 * 24 * time.Hour

// pendingItem is a background notification waiting for the agent.
type pendingItem struct {
	Key          string    `json:"key"`
	Notification string    `json:"notification"`
	CreatedAt    time.Time `json:"created_at"`
}

// pendingStore is the on-disk queue of background notifications (messages
// from watched chats) not yet handed to the agent, plus the keys already
// handled. It survives restarts, so nothing waiting is lost and nothing
// already handled is processed twice.
type pendingStore struct {
	mu   sync.Mutex
	path string
	data struct {
		Items   []pendingItem        `json:"items"`
		Handled map[string]time.Time `json:"handled"`
	}
}

func loadPendingStore(dir string) *pendingStore {
	s := &pendingStore{path: filepath.Join(dir, "pending.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			logger.Error("Pending queue unreadable, starting empty: %s", err.Error())
		}
	}
	if s.data.Handled == nil {
		s.data.Handled = map[string]time.Time{}
	}
	for k, t := range s.data.Handled {
		if time.Since(t) > handledRetention {
			delete(s.data.Handled, k)
		}
	}
	if n := len(s.data.Items); n > 0 {
		logger.Info("Pending queue: %d background message(s) waiting from before the restart", n)
	}
	return s
}

// Add queues a notification. It returns false when key was already queued or
// handled.
func (s *pendingStore) Add(key, notification string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Handled[key]; ok {
		return false
	}
	for _, it := range s.data.Items {
		if it.Key == key {
			return false
		}
	}
	s.data.Items = append(s.data.Items, pendingItem{Key: key, Notification: notification, CreatedAt: time.Now()})
	s.saveLocked()
	return true
}

// Peek returns the oldest queued notification.
func (s *pendingStore) Peek() (pendingItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Items) == 0 {
		return pendingItem{}, false
	}
	return s.data.Items[0], true
}

// Has reports whether key is queued or was already handled.
func (s *pendingStore) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Handled[key]; ok {
		return true
	}
	for _, it := range s.data.Items {
		if it.Key == key {
			return true
		}
	}
	return false
}

// Keys returns the keys of the queued notifications starting with prefix.
func (s *pendingStore) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, it := range s.data.Items {
		if strings.HasPrefix(it.Key, prefix) {
			out = append(out, it.Key)
		}
	}
	return out
}

// Done removes key from the queue and remembers it as handled.
func (s *pendingStore) Done(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.data.Items[:0]
	for _, it := range s.data.Items {
		if it.Key != key {
			items = append(items, it)
		}
	}
	s.data.Items = items
	s.data.Handled[key] = time.Now()
	s.saveLocked()
}

// Len returns how many notifications are waiting.
func (s *pendingStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Items)
}

func (s *pendingStore) saveLocked() {
	if err := writeFileAtomic(s.path, s.data); err != nil {
		logger.Error("Cannot save pending queue: %s", err.Error())
	}
}

func writeFileAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- background processes across restarts ---

var procsStateMu sync.Mutex

func procsStatePath(configDir string) string {
	return filepath.Join(configDir, "sessions", "procs.json")
}

// writeProcsState records the background processes running right now, so the
// next start can tell the agent which ones the restart left behind.
func writeProcsState(configDir string, running []string) {
	procsStateMu.Lock()
	defer procsStateMu.Unlock()
	path := procsStatePath(configDir)
	if len(running) == 0 {
		_ = os.Remove(path)
		return
	}
	if err := writeFileAtomic(path, running); err != nil {
		logger.Error("Cannot save running processes: %s", err.Error())
	}
}

// takeKilledProcs returns the processes that were running before this start
// and forgets them.
func takeKilledProcs(configDir string) []string {
	procsStateMu.Lock()
	defer procsStateMu.Unlock()
	path := procsStatePath(configDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_ = os.Remove(path)
	var list []string
	_ = json.Unmarshal(b, &list)
	return list
}

func (a *App) saveRunningProcs() {
	if a.shuttingDown.Load() {
		return
	}
	var running []string
	for _, i := range a.procs.List() {
		if i.Status != procs.StatusExited {
			running = append(running, fmt.Sprintf("%s (%s)", i.ID, i.Command))
		}
	}
	writeProcsState(a.cfg.ConfigDir(), running)
}

// --- resuming after a restart ---

// resumeFallbackDelay is how long to wait for the UI before resuming the
// interrupted work without it.
const resumeFallbackDelay = 20 * time.Second

// recoverAfterRestart runs at startup: it repairs the turn a restart
// interrupted, notes the restart (and the processes it left behind) in the
// conversation, and schedules the resume.
func (a *App) recoverAfterRestart() {
	a.agentMu.Lock()
	ag := a.ensureAgent()
	killed := takeKilledProcs(a.cfg.ConfigDir())
	note := a.recoverBackgroundSubAgents()
	if lastCrashTrace != "" {
		logger.Error("The previous run crashed; trace saved in %s", lastCrashTrace)
		note = strings.TrimSpace(crashNote(lastCrashTrace) + " " + note)
	}
	resume, changed := ag.RecoverInterruptedWithNote(killed, note, time.Now())
	if changed {
		a.saveSession(ag)
	}
	a.agentMu.Unlock()
	if resume || a.pending.Len() > 0 || len(a.bgResumes) > 0 {
		logger.Info("Restart recovery: resume=%v pending=%d killed_procs=%d bg_subagents_resumed=%d", resume, a.pending.Len(), len(killed), len(a.bgResumes))
		go func() {
			time.Sleep(resumeFallbackDelay)
			a.ResumeInterrupted()
		}()
	}
}

func (a *App) saveSession(ag *agent.Agent) {
	if err := ag.SaveSession(a.cfg.ConfigDir()); err != nil {
		logger.Error("Failed to save session: %s", err.Error())
	}
}

// drainPending hands every queued background notification to the agent, one
// turn each, oldest first.
func (a *App) drainPending() {
	for {
		a.agentMu.Lock()
		item, ok := a.pending.Peek()
		if !ok {
			a.agentMu.Unlock()
			return
		}
		a.runNotificationLocked(item.Notification, func() { a.pending.Done(item.Key) })
		a.agentMu.Unlock()
	}
}

// HasInterruptedWork reports whether there is a turn interrupted by a restart
// or background messages waiting, so the UI shows the agent as busy while
// ResumeInterrupted runs.
func (a *App) HasInterruptedWork() bool {
	ag := a.ensureAgent()
	return ag.ResumePending() || a.pending.Len() > 0 || len(a.bgResumes) > 0
}

// ResumeInterrupted finishes the turn a restart interrupted and then handles
// the background messages still waiting. Only the first call does anything:
// the UI calls it once it is ready, and a timer calls it in case no UI does.
func (a *App) ResumeInterrupted() ChatResponse {
	var resp ChatResponse
	a.resumeOnce.Do(func() {
		// Background sub-agents first: they run on their own, with the UI
		// ready to show their orbs.
		a.startBgResumes()
		a.agentMu.Lock()
		ag := a.ensureAgent()
		if ag.ResumePending() {
			ctx, cancel := context.WithCancel(a.ctx)
			a.agentCancel = cancel
			logger.Info("Resuming the turn interrupted by the restart")
			err := ag.Resume(ctx)
			a.agentCancel = nil
			cancel()
			a.saveSession(ag)
			if err != nil {
				logger.Error("Resumed run error: %s", err.Error())
				resp.Error = err.Error()
			}
		}
		a.agentMu.Unlock()
		a.drainPending()
	})
	summary := a.costTracker.Summary()
	resp.InputTokens = summary.TotalInputTokens
	resp.OutputTokens = summary.TotalOutputTokens
	resp.CostUSD = summary.TotalCostUSD
	return resp
}
