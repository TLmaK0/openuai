package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"openuai/internal/agent"
	"openuai/internal/logger"
	"openuai/internal/tools"
)

// stoppedByAgentError is the error of a sub-agent stopped with stop_agent
// (shown on its card, which shows it as cancelled).
const stoppedByAgentError = "stopped by the agent (stop_agent)"

// stopWait is how long stop_agent waits for a stopped sub-agent to end.
var stopWait = 2 * time.Minute

// subAgentRun is one run of a sub-agent, from the moment it starts until it
// can be resumed, so the agent can stop it by its agent_id.
type subAgentRun struct {
	key     string // "<run>/<task id>", as in the subagent_* events
	agentID string
	taskID  string
	parent  string // key of the sub-agent that launched it ("" for main)
	// bg: a background sub-agent; runBackground ends it once its record is
	// updated (see bgSettle).
	bg bool
	// stopped: stopped with stop_agent, which reports its result (a
	// background one then gets no notification).
	stopped bool
	// settled: runBackground has decided whether to notify its result.
	settled bool
	ended   bool // the run is over: result is set
	result  agent.SubAgentResult
	done    chan struct{} // closed once it can be resumed
}

// track records the run key of sub-agent agentID as it starts.
func (r *subAgentRuns) track(key, agentID, taskID, parent string, bg bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]*subAgentRun{}
	}
	r.runs[key] = &subAgentRun{key: key, agentID: agentID, taskID: taskID, parent: parent, bg: bg, done: make(chan struct{})}
}

// ended records the result of run key and returns it as it must be reported
// (a run stopped with stop_agent says so). A foreground run is over here; a
// background one when runBackground calls finish.
func (r *subAgentRuns) ended(key string, res agent.SubAgentResult) agent.SubAgentResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.runs[key]
	if e == nil {
		return res
	}
	if e.stopped && res.Cancelled {
		res.Error = stoppedByAgentError
	}
	e.result, e.ended = res, true
	if !e.bg {
		delete(r.runs, key)
		close(e.done)
	}
	return res
}

// bgSettle is called by runBackground once the background run of agentID
// returned: it reports whether stop_agent stopped it (and so reports its
// result instead of a notification). The run must then be passed to finish.
func (r *subAgentRuns) bgSettle(agentID string) (*subAgentRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.runs {
		if e.bg && e.agentID == agentID {
			e.settled = true
			return e, e.stopped
		}
	}
	return nil, false
}

// finish forgets background run e (nil is ignored) and releases a stop_agent
// call waiting for it.
func (r *subAgentRuns) finish(e *subAgentRun) {
	if e == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs[e.key] == e {
		delete(r.runs, e.key)
		close(e.done)
	}
}

// requestStop marks the run of agentID as stopped by the agent. It returns
// the run (nil when none is running), how many sub-agents it launched are
// running below it, and whether it had already ended.
func (r *subAgentRuns) requestStop(agentID string) (e *subAgentRun, nested int, ended bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.runs {
		if x.agentID == agentID && (e == nil || !x.ended) {
			e = x
		}
	}
	if e == nil {
		return nil, 0, false
	}
	if e.ended {
		return e, 0, true
	}
	e.stopped = true
	// Every run whose chain of parents reaches e.
	below := map[string]bool{e.key: true}
	for grew := true; grew; {
		grew = false
		for k, x := range r.runs {
			if !below[k] && below[x.parent] {
				below[k], grew = true, true
				if !x.ended {
					nested++
				}
			}
		}
	}
	return e, nested, false
}

// abandonStop is called when a stopped run did not end in time: a
// background one not yet settled gets its notification after all. It
// reports whether it will.
func (r *subAgentRuns) abandonStop(e *subAgentRun) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.bg && !e.settled {
		e.stopped = false
		return true
	}
	return false
}

func (r *subAgentRuns) resultOf(e *subAgentRun) agent.SubAgentResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return e.result
}

// running lists the sub-agents running now ("task (agent_id X)").
func (r *subAgentRuns) running() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.runs {
		if !e.ended {
			out = append(out, fmt.Sprintf("%s (agent_id %s)", e.taskID, e.agentID))
		}
	}
	sort.Strings(out)
	return out
}

// isRunning reports whether sub-agent agentID is running now.
func (r *subAgentRuns) isRunning(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.runs {
		if e.agentID == agentID && !e.ended {
			return true
		}
	}
	return false
}

// stopSubAgent is the stop_agent tool: it stops the running sub-agent
// agentID (and the sub-agents it launched) the same way the Cancel button on
// its card does, waits for it to end and reports the work it had done.
func (a *App) stopSubAgent(ctx context.Context, agentID string) tools.Result {
	e, nested, ended := a.subRuns.requestStop(agentID)
	if e == nil {
		return a.notRunningResult(agentID)
	}
	if ended {
		logger.Info("[sub-agent:%s] stop_agent: already finished (agent_id %s)", e.taskID, agentID)
		return tools.Result{Output: fmt.Sprintf("Nothing was stopped: sub-agent %q (agent_id %s) has already finished; its result will arrive as a notification. To continue it with new instructions, call spawn_agents with \"resume\": %q.", e.taskID, agentID, agentID)}
	}
	logger.Info("[sub-agent:%s] stop_agent requested (agent_id %s, nested running %d)", e.taskID, agentID, nested)
	a.CancelSubAgent(e.key)

	timer := time.NewTimer(stopWait)
	defer timer.Stop()
	select {
	case <-e.done:
	case <-ctx.Done():
		return a.stopNotEnded(e)
	case <-timer.C:
		return a.stopNotEnded(e)
	}

	res := a.subRuns.resultOf(e)
	var b strings.Builder
	if res.Cancelled {
		fmt.Fprintf(&b, "Stopped sub-agent %q (agent_id %s); its card shows it as cancelled.", e.taskID, agentID)
		if nested > 0 {
			fmt.Fprintf(&b, " %d sub-agent(s) it had launched were stopped too.", nested)
		}
		fmt.Fprintf(&b, " Other sub-agents and this conversation keep running. To restart it with its full history plus new instructions, call spawn_agents with a task whose \"resume\" is %q and whose \"description\" holds the new instructions (add \"background\": true to run it in the background again). Below is the work it had done so far.\n\n", agentID)
	} else {
		fmt.Fprintf(&b, "Sub-agent %q (agent_id %s) ended before it could be stopped; below is its result.\n\n", e.taskID, agentID)
	}
	b.WriteString(tools.FormatResults([]tools.SubTaskResult{{
		ID: res.ID, Agent: res.Agent, Model: res.Model, Output: res.Output, Error: res.Error, Notice: res.Notice,
		Partial: res.Partial, CostUSD: res.CostUSD, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, AgentID: res.AgentID,
	}}))
	return tools.Result{Output: b.String()}
}

// stopNotEnded reports a stop that was requested but did not end in time.
func (a *App) stopNotEnded(e *subAgentRun) tools.Result {
	logger.Info("[sub-agent:%s] stop_agent: still ending after the wait (agent_id %s)", e.taskID, e.agentID)
	msg := fmt.Sprintf("Stop requested for sub-agent %q (agent_id %s), but it has not ended yet.", e.taskID, e.agentID)
	if a.subRuns.abandonStop(e) {
		msg += " Its result will arrive as a notification when it ends."
	}
	return tools.Result{Output: msg}
}

// notRunningResult explains why agentID cannot be stopped.
func (a *App) notRunningResult(agentID string) tools.Result {
	running := "none"
	if l := a.subRuns.running(); len(l) > 0 {
		running = strings.Join(l, ", ")
	}
	if a.bgAgents != nil && a.bgAgents.has(agentID) {
		return tools.Result{Output: fmt.Sprintf("Nothing was stopped: background sub-agent %s has not started yet (it is waiting for a free slot); try again in a moment.", agentID)}
	}
	dir := ""
	if a.cfg != nil {
		dir = filepath.Join(a.cfg.ConfigDir(), "subagents")
	}
	tr, err := agent.LoadTranscript(dir, agentID)
	if err != nil {
		return tools.Result{Error: fmt.Sprintf("cannot stop %q: it is not a running sub-agent (%s). Running sub-agents: %s", agentID, err.Error(), running)}
	}
	state := "is not running (last status: " + tr.Status + ")"
	if tr.Ended() {
		state = "has already finished (status: " + tr.Status + ") and its result was already reported"
	}
	return tools.Result{Output: fmt.Sprintf("Nothing was stopped: sub-agent %s %s. To continue it with new instructions, call spawn_agents with a task whose \"resume\" is %q. Running sub-agents: %s", agentID, state, agentID, running)}
}
