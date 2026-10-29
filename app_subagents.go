package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/logger"
	"openuai/internal/tools"
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

// subAgentScope travels in the context of a running sub-agent so that, when
// it calls spawn_agents itself, its children know their level, their parent
// and which tools they may get (never more than the parent has).
type subAgentScope struct {
	depth    int    // 0: main conversation
	key      string // key of the sub-agent that is delegating ("" for main)
	registry *tools.Registry
	// dir is the directory the delegating sub-agent works in when it is not
	// the app's (spawn_agents cwd); its children inherit it.
	dir string
}

type subAgentScopeKey struct{}

func withSubAgentScope(ctx context.Context, s subAgentScope) context.Context {
	return context.WithValue(ctx, subAgentScopeKey{}, s)
}

func subAgentScopeFrom(ctx context.Context) subAgentScope {
	if s, ok := ctx.Value(subAgentScopeKey{}).(subAgentScope); ok {
		return s
	}
	return subAgentScope{}
}

// subAgentLimiter caps how many sub-agents run at once across every level,
// so nesting never multiplies the number of agents running.
type subAgentLimiter struct {
	mu      sync.Mutex
	running int
	freed   chan struct{} // closed (and replaced) on every release
}

// acquire takes a slot. With wait it blocks until one is free (or ctx ends);
// without it, it fails at once when the limit is reached.
func (l *subAgentLimiter) acquire(ctx context.Context, max int, wait bool) (func(), error) {
	for {
		l.mu.Lock()
		if l.freed == nil {
			l.freed = make(chan struct{})
		}
		if l.running < max {
			l.running++
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(l.release) }, nil
		}
		freed := l.freed
		l.mu.Unlock()
		if !wait {
			return nil, fmt.Errorf("concurrency limit reached (%d sub-agents running at once): do this part yourself instead of delegating it", max)
		}
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (l *subAgentLimiter) release() {
	l.mu.Lock()
	l.running--
	close(l.freed)
	l.freed = make(chan struct{})
	l.mu.Unlock()
}

func (l *subAgentLimiter) inUse() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}
