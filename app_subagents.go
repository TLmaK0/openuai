package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/logger"
)

// subAgentRuns tracks running sub-agents so each one can be cancelled on its
// own. Keys are "<run>/<task id>", unique across spawn_agents calls.
type subAgentRuns struct {
	seq     atomic.Int64
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (r *subAgentRuns) newRun() string { return fmt.Sprintf("sa%d", r.seq.Add(1)) }

func (r *subAgentRuns) taskContext(key string, parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.cancels == nil {
		r.cancels = map[string]context.CancelFunc{}
	}
	r.cancels[key] = cancel
	r.mu.Unlock()
	return ctx, func() {
		r.mu.Lock()
		delete(r.cancels, key)
		r.mu.Unlock()
		cancel()
	}
}

func (r *subAgentRuns) cancel(key string) bool {
	r.mu.Lock()
	c, ok := r.cancels[key]
	r.mu.Unlock()
	if ok {
		c()
	}
	return ok
}

// CancelSubAgent stops one running sub-agent (key from the subagent_* events)
// without touching the other sub-agents or the parent turn. It returns false
// when that sub-agent is no longer running.
func (a *App) CancelSubAgent(key string) bool {
	ok := a.subRuns.cancel(key)
	logger.Info("[sub-agent:%s] Cancel requested (running=%v)", key, ok)
	return ok
}

func (a *App) emitSubAgent(name string, payload map[string]any) {
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, name, payload)
	}
	if a.apiServer != nil {
		a.apiServer.Broadcast(name, "", payload)
	}
}
