package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"openuai/internal/logger"
	"openuai/internal/tools"
)

// BackgroundNotice is the placeholder result of a sub-agent launched in the
// background: the turn that launched it does not wait for it.
const BackgroundNotice = "Running in the background: its result will arrive later as a notification starting with \"[Background sub-agents finished\". Do not wait for it: continue with other work or end your turn."

type backgroundRunKey struct{}

// inBackgroundRun reports whether ctx belongs to a spawn that was already
// split into foreground and background tasks.
func inBackgroundRun(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundRunKey{}).(bool)
	return v
}

// spawnWithBackground runs fg now (the caller waits for them) and bg detached
// from the current turn: they keep running after the turn ends or is stopped
// (each can still be cancelled from its orb), and their results are queued as
// a background notification for the main agent, which handles it as soon as
// it is free.
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
	go func() {
		results := a.spawnSubAgents(bgCtx, bg)
		for _, t := range bg {
			if t.OnFinish != nil {
				t.OnFinish()
			}
		}
		notification := "[Background sub-agents finished: " + strings.Join(ids, ", ") + "]\n\n" + tools.FormatResults(results)
		key := fmt.Sprintf("subagents:%d", time.Now().UnixNano())
		logger.Info("Background sub-agents finished: %s", strings.Join(ids, ", "))
		if a.pending != nil && a.pending.Add(key, notification) {
			go a.drainPending()
		}
	}()

	var out []tools.SubTaskResult
	if len(fg) > 0 {
		out = a.spawnSubAgents(context.WithValue(ctx, backgroundRunKey{}, true), fg)
	}
	for _, t := range bg {
		out = append(out, tools.SubTaskResult{ID: t.ID, Agent: t.Agent, Notice: BackgroundNotice})
	}
	return out
}
