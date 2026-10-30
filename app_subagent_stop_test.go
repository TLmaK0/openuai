package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/subagents"
	"openuai/internal/tools"
)

// tickTool is a harmless tool the looping sub-agents call.
type tickTool struct{}

func (tickTool) Definition() tools.Definition {
	return tools.Definition{Name: "tick", Description: "tick", RequiresPermission: "none"}
}
func (tickTool) Execute(context.Context, map[string]string) tools.Result {
	return tools.Result{Output: "tick ok"}
}

// stopProvider keeps every sub-agent calling tick until it is stopped. A
// task containing NEST first launches a nested sub-agent ("child loop"); a
// new message containing FINISH (a resume) gets a final answer.
type stopProvider struct {
	mu       sync.Mutex
	ticks    map[string]int
	finished string // everything the FINISH call saw
}

func (p *stopProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{Content: "x"}, nil
}
func (p *stopProvider) Name() string     { return "mock" }
func (p *stopProvider) Models() []string { return []string{"mock"} }
func (p *stopProvider) ChatWithTools(ctx context.Context, msgs []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(15 * time.Millisecond):
	}
	var task, last string
	spawned := false
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			if task == "" {
				task = m.Content
			}
			last = m.Content
		}
		for _, tc := range m.ToolCalls {
			spawned = spawned || tc.Name == "spawn_agents"
		}
	}
	resp := &llm.Response{Model: "mock", InputTokens: 1, OutputTokens: 1}
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.Contains(last, "FINISH") {
		var all []string
		for _, m := range msgs {
			all = append(all, m.Content)
		}
		p.finished = strings.Join(all, "\n")
		resp.Content = "finished with the new rule"
		return resp, nil, nil
	}
	if strings.Contains(task, "NEST") && !spawned {
		return resp, []llm.ToolCall{{ID: "spawn", Name: "spawn_agents", Arguments: map[string]string{"tasks": `[{"id":"child","description":"child loop"}]`}}}, nil
	}
	p.ticks[task]++
	return resp, []llm.ToolCall{{ID: fmt.Sprintf("t%d", p.ticks[task]), Name: "tick", Arguments: map[string]string{}}}, nil
}

func (p *stopProvider) count(task string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ticks[task]
}

// stopApp is an App whose sub-agents run on stopProvider. Results of
// background sub-agents are queued but never run (agentMu is held).
func stopApp(t *testing.T) (*App, *stopProvider) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	p := &stopProvider{ticks: map[string]int{}}
	reg := tools.NewRegistry()
	reg.Register(tickTool{})
	a := &App{cfg: cfg, costTracker: llm.NewCostTracker(), registry: reg,
		providers:   map[string]llm.Provider{cfg.Provider(): p},
		permissions: agent.NewPermissionManager(cfg.ConfigDir(), func(context.Context, string, string) (agent.PermissionLevel, bool) { return agent.PermSession, true }),
		bgAgents:    loadBgStore(cfg.ConfigDir()),
		pending:     loadPendingStore(t.TempDir()),
	}
	reg.Register(tools.SpawnAgents{Fn: a.spawnSubAgents})
	reg.Register(tools.StopAgent{Fn: a.stopSubAgent})
	a.agentMu.Lock()
	t.Cleanup(func() {
		for _, k := range a.pending.Keys("") {
			a.pending.Done(k)
		}
		a.agentMu.Unlock()
	})
	return a, p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Two background sub-agents: stopping one leaves the other running; the
// stopped one reports its work, gets no notification and can be resumed with
// its history plus new instructions.
func TestStopAgentStopsOnlyThatOneAndResumes(t *testing.T) {
	a, p := stopApp(t)
	ctx := context.Background()
	res := a.spawnSubAgents(ctx, []tools.SubTask{
		{ID: "one", Description: "loop one", Background: true},
		{ID: "two", Description: "loop two", Background: true},
	})
	idOne, idTwo := res[0].AgentID, res[1].AgentID
	if idOne == "" || idTwo == "" || res[0].ID != "one" {
		t.Fatalf("placeholders: %+v", res)
	}
	waitFor(t, "both running", func() bool { return p.count("loop one") >= 2 && p.count("loop two") >= 2 })

	out := a.stopSubAgent(ctx, idOne)
	if out.Error != "" {
		t.Fatalf("stop failed: %s", out.Error)
	}
	for _, want := range []string{`Stopped sub-agent "one"`, "Called tick", stoppedByAgentError, "agent_id: " + idOne, "keep running"} {
		if !strings.Contains(out.Output, want) {
			t.Fatalf("stop result lacks %q:\n%s", want, out.Output)
		}
	}
	if a.subRuns.isRunning(idOne) || a.bgAgents.has(idOne) {
		t.Fatal("the stopped sub-agent must be gone")
	}
	tr, err := agent.LoadTranscript(filepath.Join(a.cfg.ConfigDir(), "subagents"), idOne)
	if err != nil || tr.Status != agent.StatusCancelled {
		t.Fatalf("transcript: %v status=%q", err, tr.Status)
	}

	// The other one keeps working.
	if !a.subRuns.isRunning(idTwo) || !a.bgAgents.has(idTwo) {
		t.Fatal("the other sub-agent must keep running")
	}
	n := p.count("loop two")
	waitFor(t, "two keeps ticking", func() bool { return p.count("loop two") >= n+3 })

	// No notification for the stopped one: stop_agent already reported it.
	for _, k := range a.pending.Keys(bgPendingPrefix) {
		if strings.Contains(k, idOne) {
			t.Fatalf("stopped sub-agent queued a notification: %s", k)
		}
	}

	// Restart it right away with its history plus a new instruction.
	r := a.spawnSubAgents(ctx, []tools.SubTask{{ID: "one", Resume: idOne, Description: "FINISH. New rule: RULE42"}})[0]
	if r.Error != "" || r.AgentID != idOne || r.Output != "finished with the new rule" {
		t.Fatalf("resume: %+v", r)
	}
	p.mu.Lock()
	seen := p.finished
	p.mu.Unlock()
	for _, want := range []string{"loop one", "tick ok", "RULE42"} {
		if !strings.Contains(seen, want) {
			t.Fatalf("resumed run did not see %q:\n%s", want, seen)
		}
	}

	// Finished now: stopping it says so.
	out = a.stopSubAgent(ctx, idOne)
	if out.Error != "" || !strings.Contains(out.Output, "Nothing was stopped") || !strings.Contains(out.Output, "already finished") {
		t.Fatalf("stopping a finished sub-agent: %+v", out)
	}
	// Unknown id: an error listing what is running.
	out = a.stopSubAgent(ctx, "nope")
	if !strings.Contains(out.Error, "not a running sub-agent") || !strings.Contains(out.Error, "two (agent_id "+idTwo+")") {
		t.Fatalf("unknown id: %+v", out)
	}

	if out := a.stopSubAgent(ctx, idTwo); !strings.Contains(out.Output, `Stopped sub-agent "two"`) {
		t.Fatalf("stop two: %+v", out)
	}
	if l := a.subRuns.running(); len(l) != 0 {
		t.Fatalf("still running: %v", l)
	}
}

// Stopping a sub-agent stops the sub-agents it launched too.
func TestStopAgentStopsNested(t *testing.T) {
	a, p := stopApp(t)
	ctx := context.Background()
	res := a.spawnSubAgents(ctx, []tools.SubTask{{ID: "parent", Description: "NEST then loop", Background: true}})
	pid := res[0].AgentID
	waitFor(t, "the child running", func() bool { return p.count("child loop") >= 2 })
	var childID string
	re := regexp.MustCompile(`^child \(agent_id (\S+)\)$`)
	for _, s := range a.subRuns.running() {
		if m := re.FindStringSubmatch(s); m != nil {
			childID = m[1]
		}
	}
	if childID == "" {
		t.Fatalf("child not tracked: %v", a.subRuns.running())
	}

	out := a.stopSubAgent(ctx, pid)
	if !strings.Contains(out.Output, `Stopped sub-agent "parent"`) || !strings.Contains(out.Output, "1 sub-agent(s) it had launched were stopped too") {
		t.Fatalf("stop parent: %+v", out)
	}
	if a.subRuns.isRunning(pid) || a.subRuns.isRunning(childID) {
		t.Fatalf("still running: %v", a.subRuns.running())
	}
	n := p.count("child loop")
	time.Sleep(100 * time.Millisecond)
	if p.count("child loop") != n {
		t.Fatal("the nested sub-agent kept working")
	}
	if tr, err := agent.LoadTranscript(filepath.Join(a.cfg.ConfigDir(), "subagents"), childID); err != nil || !tr.Ended() && tr.Status != agent.StatusInterrupted {
		t.Fatalf("child transcript: %v status=%q", err, tr.Status)
	}
}

// Only the main conversation stops sub-agents: sub-agents never get the tool.
func TestStopAgentNotGivenToSubAgents(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.StopAgent{Fn: func(context.Context, string) tools.Result { return tools.Result{} }})
	reg.Register(tickTool{})
	if _, ok := reg.Without(subagents.AlwaysExcluded...).Get("stop_agent"); ok {
		t.Fatal("stop_agent must not reach sub-agents")
	}
}
