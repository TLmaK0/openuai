package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"openuai/internal/agent"
	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/mcpclient"
	"openuai/internal/subagents"
	"openuai/internal/tools"
)

// The main conversation can run as an agent definition (issue #70), like
// Claude Code's --agent and "agent" setting: the definition's instructions,
// tools, model, effort, permission mode, memory, MCP servers, hooks and
// skills apply to the main conversation exactly as they would to a
// sub-agent with that definition.
//
// Which agent, from highest priority to lowest:
//  1. the agent chosen for this conversation (selector in the UI, REST API);
//     saved with the session, so it survives restarts;
//  2. the project's "agent" key in .openuai/settings.local.json or
//     .openuai/settings.json (closest directory wins);
//  3. the user's "agent" key in config.json.
// An empty value at any level means the normal assistant.

const (
	mainAgentSourceSession = "session"
	mainAgentSourceProject = "project"
	mainAgentSourceUser    = "user"
)

// initialPromptDelay is how long a pending initialPrompt waits for the UI to
// send it (so the UI shows it as the user's message) before it is sent
// without it.
var initialPromptDelay = 4 * time.Second

// mainAgentState is what the app knows about the agent the main
// conversation runs as.
type mainAgentState struct {
	mu           sync.Mutex
	choice       *string // chosen for this conversation; nil: follow the defaults
	choiceLoaded bool    // choice was read from the saved session

	active *mainAgentSetup // what the current agent was built with (nil: normal assistant)
	wanted string          // agent requested by the settings (may have failed)
	source string          // where wanted comes from
	err    string          // why wanted could not be used
	shown  string          // last error reported in the conversation

	initialPending string // initialPrompt waiting to be sent
	initialRunning bool
}

// mainAgentSetup is everything the main conversation runs with when it runs
// as an agent definition.
type mainAgentSetup struct {
	name           string
	def            *subagents.Definition
	registry       *tools.Registry
	systemPrompt   string
	model          string
	effort         string
	permissionMode string
	maxTurns       int
	hooks          *hooks.Set
	mcp            *mcpclient.Manager // inline MCP servers of the definition
	notice         string
	spawnTypes     []string // sub-agent types it may launch (when restricted)
	spawnLimited   bool
}

func (s *mainAgentSetup) stop() {
	if s != nil && s.mcp != nil {
		_ = s.mcp.Stop()
		s.mcp = nil
	}
}

// wantedMainAgent returns the agent the main conversation should run as and
// where that comes from. problem is set when a settings file cannot be read.
func (a *App) wantedMainAgent() (name, source, problem string) {
	a.mainAg.mu.Lock()
	choice := a.mainAg.choice
	a.mainAg.mu.Unlock()
	if choice != nil {
		return *choice, mainAgentSourceSession, ""
	}
	if n, path, err := subagents.ProjectAgentSetting(a.GetWorkDir()); err != nil {
		problem = err.Error()
	} else if path != "" {
		return n, mainAgentSourceProject, ""
	}
	if a.cfg != nil {
		if n := strings.TrimSpace(a.cfg.Agent()); n != "" {
			return n, mainAgentSourceUser, problem
		}
	}
	return "", "", problem
}

// findMainAgentDef looks the definition up, with a clear error when it does
// not exist or its file could not be loaded.
func findMainAgentDef(defs subagents.Set, name string) (*subagents.Definition, error) {
	if d, ok := defs.Defs[name]; ok {
		return d, nil
	}
	for _, w := range defs.Warnings {
		path := w
		if i := strings.Index(w, ".md:"); i >= 0 {
			path = w[:i+3]
		}
		if strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) == name {
			return nil, fmt.Errorf("agent %q cannot be loaded: %s", name, w)
		}
	}
	return nil, fmt.Errorf("unknown agent %q; available agents: %s", name, joinOrNone(defs.Names()))
}

// buildMainAgentSetup prepares the main conversation to run as agent name,
// reusing the sub-agent setup: tool filtering, MCP servers, memory, skills,
// model resolution. The caller owns the returned setup (stop() it when it
// is not used).
func (a *App) buildMainAgentSetup(name string) (*mainAgentSetup, error) {
	workDir := a.GetWorkDir()
	defs := a.loadSubAgentDefs(workDir, false)
	def, err := findMainAgentDef(defs, name)
	if err != nil {
		return nil, err
	}
	base := a.registry
	if base == nil {
		base = tools.NewRegistry()
	}
	reg, err := subagents.FilterMainTools(base, def)
	if err != nil {
		return nil, err
	}
	s := &mainAgentSetup{name: name, def: def, registry: reg}
	var notes []string
	if def.Isolation != "" {
		notes = append(notes, "isolation is ignored when the agent runs as the main conversation.")
	}
	// Agent(x, y) in tools: only those sub-agent types may be launched.
	if _, has := reg.Get(subagents.SpawnTool); has {
		if types, limited := subagents.SpawnAllowlist(def); limited {
			s.spawnTypes, s.spawnLimited = types, true
			reg.Register(tools.SpawnAgents{
				Fn:     a.restrictedSpawn(types),
				Agents: func() string { return a.describeAllowedSubAgents(types) },
				Models: a.describeSubAgentModels,
			})
		}
	}
	if len(def.MCPServers) > 0 {
		mgr, note := a.addSubAgentMCP(def, base, reg)
		s.mcp = mgr
		if note != "" {
			notes = append(notes, note)
		}
	}
	prompt := subagents.MainAgentPrompt(def, agent.DefaultSystemPrompt())
	cfgDir := ""
	if a.cfg != nil {
		cfgDir = a.cfg.ConfigDir()
	}
	if memDir := subagents.MemoryDir(def, workDir, filepath.Join(cfgDir, "agent-memory")); memDir != "" {
		prompt += subagents.MemoryPrompt(memDir)
		for _, n := range subagents.MemoryTools {
			if _, has := reg.Get(n); !has {
				if t, ok := base.Get(n); ok {
					reg.Register(t)
				}
			}
		}
	}
	if section, notice := a.preloadSkills(def, workDir); section != "" || notice != "" {
		prompt += section
		if notice != "" {
			notes = append(notes, notice)
		}
	}
	s.systemPrompt = prompt
	parent := ""
	if a.cfg != nil {
		parent = a.cfg.DefaultModel()
	}
	var available []string
	if m := strings.TrimSpace(def.Model); m != "" && !strings.EqualFold(m, "inherit") && m != parent {
		if p := a.activeProvider(); p != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			available = llm.AvailableModels(ctx, p)
			cancel()
		}
	}
	model, notice := subagents.ResolveModel("", def.Model, "", parent, available)
	if notice != "" {
		notes = append(notes, notice)
	}
	s.model = model
	s.effort = def.Effort
	s.permissionMode = def.PermissionMode
	s.maxTurns = def.MaxTurns
	s.hooks = subagents.MainHookSet(def)
	s.notice = strings.TrimSpace(strings.Join(notes, " "))
	return s, nil
}

// restrictedSpawn is spawn_agents for an agent whose tools list
// Agent(x, y): tasks for other sub-agent types fail with a clear error.
func (a *App) restrictedSpawn(allowed []string) tools.SpawnFunc {
	ok := map[string]bool{}
	for _, t := range allowed {
		ok[t] = true
	}
	return func(ctx context.Context, tasks []tools.SubTask) []tools.SubTaskResult {
		out := make([]tools.SubTaskResult, len(tasks))
		var run []tools.SubTask
		var at []int
		for i, t := range tasks {
			name := t.Agent
			if name == "" && t.Resume != "" && a.cfg != nil {
				if tr, err := agent.LoadTranscript(filepath.Join(a.cfg.ConfigDir(), "subagents"), t.Resume); err == nil {
					name = tr.Agent
				}
			}
			if name == "" {
				name = subagents.BuiltinGeneral
			}
			if !ok[name] {
				out[i] = tools.SubTaskResult{ID: t.ID, Agent: t.Agent, Error: fmt.Sprintf("this agent may not launch %q sub-agents; allowed agents: %s", name, joinOrNone(allowed))}
				continue
			}
			run = append(run, t)
			at = append(at, i)
		}
		if len(run) > 0 {
			res := a.spawnSubAgents(ctx, run)
			for k, r := range res {
				if k < len(at) {
					out[at[k]] = r
				}
			}
		}
		return out
	}
}

// describeAllowedSubAgents lists only the sub-agent types an agent may
// launch, for its spawn_agents description.
func (a *App) describeAllowedSubAgents(allowed []string) string {
	all := a.loadSubAgentDefs(a.GetWorkDir(), false)
	sub := subagents.Set{Defs: map[string]*subagents.Definition{}}
	for _, n := range allowed {
		if d, ok := all.Defs[n]; ok {
			sub.Defs[n] = d
		}
	}
	list := sub.Describe()
	if list == "" {
		list = "(none of the allowed agents is defined: " + joinOrNone(allowed) + ")"
	}
	return list + "\n\nOnly the agents listed here may be launched: name one of them in every task (tasks for other agents, or without an agent, fail)."
}

// resolveMainAgent decides what the next main agent is built with: the
// wanted agent's setup, or nil for the normal assistant (also when the
// wanted agent cannot be used: the error is kept for the UI and reported
// once in the conversation). The previous setup is stopped.
func (a *App) resolveMainAgent() *mainAgentSetup {
	name, source, problem := a.wantedMainAgent()
	var setup *mainAgentSetup
	errText := problem
	if name != "" {
		s, err := a.buildMainAgentSetup(name)
		if err != nil {
			errText = err.Error()
			logger.Error("Main conversation agent %q (%s): %s; using the normal assistant", name, source, errText)
		} else {
			setup = s
			logger.Info("Main conversation runs as agent %q (%s, model %s)", name, source, s.model)
			if s.notice != "" {
				logger.Info("Main conversation agent %q: %s", name, s.notice)
			}
		}
	}
	a.mainAg.mu.Lock()
	prev := a.mainAg.active
	a.mainAg.active, a.mainAg.wanted, a.mainAg.source, a.mainAg.err = setup, name, source, errText
	report := errText != "" && errText != a.mainAg.shown
	a.mainAg.shown = errText
	a.mainAg.mu.Unlock()
	if prev != setup {
		prev.stop()
	}
	if report {
		msg := "Could not run the conversation as an agent: " + errText + ". Using the normal assistant."
		if name == "" {
			msg = "Could not read the agent setting: " + errText
		}
		a.emitSubAgent("main_agent_error", map[string]any{"agent": name, "error": msg})
	}
	return setup
}

// applyMainAgent fills an agent configuration from the setup (nil: the
// normal assistant, cfg unchanged).
func (a *App) applyMainAgent(cfg *agent.Config, s *mainAgentSetup) {
	if s == nil {
		return
	}
	cfg.Registry = s.registry
	cfg.SystemPrompt = s.systemPrompt
	cfg.Model = s.model
	cfg.Effort = s.effort
	cfg.PermissionMode = s.permissionMode
	cfg.MaxTurns = s.maxTurns
	if s.hooks != nil {
		extra := s.hooks
		base := cfg.Hooks
		cfg.Hooks = func() *hooks.Set {
			var b *hooks.Set
			if base != nil {
				b = base()
			}
			return b.Merge(extra)
		}
	}
}

// mainHooks are the hooks of the main conversation: the configured ones plus
// those of the agent it runs as.
func (a *App) mainHooks() *hooks.Set {
	a.mainAg.mu.Lock()
	s := a.mainAg.active
	a.mainAg.mu.Unlock()
	base := a.loadHooks()
	if s == nil || s.hooks == nil {
		return base
	}
	return base.Merge(s.hooks)
}

// loadMainAgentChoice reads, once, the agent chosen for the conversation
// saved on disk.
func (a *App) loadMainAgentChoice() {
	a.mainAg.mu.Lock()
	defer a.mainAg.mu.Unlock()
	if a.mainAg.choiceLoaded || a.cfg == nil {
		return
	}
	a.mainAg.choiceLoaded = true
	a.mainAg.choice = agent.PeekMainAgentChoice(a.cfg.ConfigDir(), "current")
}

func (a *App) mainAgentChoice() *string {
	a.mainAg.mu.Lock()
	defer a.mainAg.mu.Unlock()
	return a.mainAg.choice
}

func hasUserMessages(msgs []llm.Message) bool {
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			return true
		}
	}
	return false
}

// --- initialPrompt ---

// armInitialPromptLocked prepares the initialPrompt of the agent the
// conversation runs as, when the conversation is just starting (no user
// message yet). The caller holds agentMu. It is sent by the UI (so it shows
// as the user's message) or, if no UI does it, after initialPromptDelay.
func (a *App) armInitialPromptLocked(ag *agent.Agent) {
	a.mainAg.mu.Lock()
	s := a.mainAg.active
	if s == nil || s.def == nil || s.def.InitialPrompt == "" || ag == nil || hasUserMessages(ag.Messages()) {
		a.mainAg.initialPending = ""
		a.mainAg.mu.Unlock()
		return
	}
	a.mainAg.initialPending = s.def.InitialPrompt
	name := s.name
	a.mainAg.mu.Unlock()
	logger.Info("initialPrompt of agent %q armed (%d chars)", name, len(s.def.InitialPrompt))
	delay := initialPromptDelay
	go func() {
		time.Sleep(delay)
		if a.ctx != nil && a.ctx.Err() != nil {
			return
		}
		a.RunInitialPrompt()
	}()
}

// takeInitialPrompt claims the pending initialPrompt ("" when none).
func (a *App) takeInitialPrompt() string {
	a.mainAg.mu.Lock()
	defer a.mainAg.mu.Unlock()
	p := a.mainAg.initialPending
	a.mainAg.initialPending = ""
	return p
}

// withInitialPrompt prepends a pending initialPrompt to the first message
// the user writes (the user wrote before it was sent).
func (a *App) withInitialPrompt(content string) string {
	p := a.takeInitialPrompt()
	if p == "" {
		return content
	}
	logger.Info("initialPrompt prepended to the first user message")
	if strings.TrimSpace(content) == "" {
		return p
	}
	return p + "\n\n" + content
}

// RunInitialPrompt sends the pending initialPrompt as the first user
// message of the conversation and returns the result. Refused (nothing to
// send) when there is none or it was already sent.
func (a *App) RunInitialPrompt() ChatResponse {
	p := a.takeInitialPrompt()
	if p == "" {
		return ChatResponse{Refused: true}
	}
	a.mainAg.mu.Lock()
	a.mainAg.initialRunning = true
	name := ""
	if a.mainAg.active != nil {
		name = a.mainAg.active.name
	}
	a.mainAg.mu.Unlock()
	logger.Info("Sending the initialPrompt of agent %q", name)
	a.emitSubAgent("initial_prompt", map[string]any{"agent": name, "content": p})
	resp := a.sendMessage(p, nil, nil)
	a.mainAg.mu.Lock()
	a.mainAg.initialRunning = false
	a.mainAg.mu.Unlock()
	a.emitSubAgent("initial_prompt_done", map[string]any{"agent": name, "error": resp.Error, "cost_usd": resp.CostUSD, "input_tokens": resp.InputTokens, "output_tokens": resp.OutputTokens})
	return resp
}

// --- Selection (UI and REST API) ---

// MainAgentOption is an agent definition the conversation can run as.
type MainAgentOption struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Scope         string `json:"scope"` // builtin, user or project
	Color         string `json:"color,omitempty"`
	Model         string `json:"model,omitempty"`
	InitialPrompt bool   `json:"initial_prompt,omitempty"`
}

// MainAgentInfo describes the agent the main conversation runs as.
type MainAgentInfo struct {
	// Name is the agent the conversation runs as now ("": normal assistant).
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color,omitempty"`
	Model       string `json:"model,omitempty"`
	Notice      string `json:"notice,omitempty"`
	// Wanted is the agent the settings ask for and Source where that comes
	// from (session, project, user); Error says why it is not running.
	Wanted string `json:"wanted"`
	Source string `json:"source"`
	Error  string `json:"error,omitempty"`
	// Session is set when an agent (or the normal assistant) was chosen for
	// this conversation, overriding the saved defaults.
	Session        bool   `json:"session"`
	UserDefault    string `json:"user_default"`
	ProjectDefault string `json:"project_default"`
	ProjectFile    string `json:"project_file,omitempty"`
	InitialPending string `json:"initial_pending,omitempty"`
	InitialRunning bool   `json:"initial_running"`
	// SpawnAgents lists the sub-agent types it may launch when restricted.
	SpawnAgents []string          `json:"spawn_agents,omitempty"`
	Agents      []MainAgentOption `json:"agents"`
}

// GetMainAgent describes the agent the main conversation runs as and the
// agents it could run as.
func (a *App) GetMainAgent() MainAgentInfo {
	if a.currentAgent == nil && a.agentMu.TryLock() {
		a.ensureAgent()
		a.agentMu.Unlock()
	}
	info := MainAgentInfo{Agents: []MainAgentOption{}}
	defs := a.loadSubAgentDefs(a.GetWorkDir(), false)
	for _, n := range defs.Names() {
		d := defs.Defs[n]
		scope := d.Scope
		if scope == "" {
			scope = "builtin"
		}
		info.Agents = append(info.Agents, MainAgentOption{Name: n, Description: d.Description, Scope: scope, Color: d.Color, Model: d.Model, InitialPrompt: d.InitialPrompt != ""})
	}
	sort.Slice(info.Agents, func(i, j int) bool { return info.Agents[i].Name < info.Agents[j].Name })
	if a.cfg != nil {
		info.UserDefault = a.cfg.Agent()
	}
	if n, p, err := subagents.ProjectAgentSetting(a.GetWorkDir()); err == nil {
		info.ProjectDefault, info.ProjectFile = n, p
	}
	a.mainAg.mu.Lock()
	defer a.mainAg.mu.Unlock()
	if s := a.mainAg.active; s != nil {
		info.Name, info.Model, info.Notice = s.name, s.model, s.notice
		if s.def != nil {
			info.Description, info.Color = s.def.Description, s.def.Color
		}
		if s.spawnLimited {
			info.SpawnAgents = append([]string{}, s.spawnTypes...)
		}
	}
	info.Wanted, info.Source, info.Error = a.mainAg.wanted, a.mainAg.source, a.mainAg.err
	info.Session = a.mainAg.choice != nil
	info.InitialPending, info.InitialRunning = a.mainAg.initialPending, a.mainAg.initialRunning
	return info
}

const errMainAgentBusy = "The assistant is busy: wait until it finishes (or stop it) before switching agents."

// SetMainAgent makes the main conversation run as agent name from now on
// ("": the normal assistant), for this conversation only. The conversation
// so far is kept. An agent that does not exist or cannot be loaded is not
// selected: the error is returned and nothing changes.
func (a *App) SetMainAgent(name string) string {
	name = strings.TrimSpace(name)
	if !a.agentMu.TryLock() {
		return errMainAgentBusy
	}
	defer a.agentMu.Unlock()
	if name != "" {
		if _, err := findMainAgentDef(a.loadSubAgentDefs(a.GetWorkDir(), false), name); err != nil {
			return err.Error()
		}
	}
	a.loadMainAgentChoice()
	a.mainAg.mu.Lock()
	prev := a.mainAg.choice
	a.mainAg.choice = &name
	a.mainAg.mu.Unlock()
	if errText := a.rebuildMainAgentLocked(); errText != "" && name != "" {
		// It exists but cannot run (e.g. no tools left): keep what was there.
		a.mainAg.mu.Lock()
		a.mainAg.choice = prev
		a.mainAg.mu.Unlock()
		a.rebuildMainAgentLocked()
		return errText
	}
	logger.Info("Main conversation agent chosen for this conversation: %q", name)
	return ""
}

// SetMainAgentDefault saves agent name ("": the normal assistant) as the
// default for the main conversation, for the user (scope "user", config.json)
// or for this project (scope "project", .openuai/settings.json). The choice
// made for this conversation is dropped so the default applies now.
func (a *App) SetMainAgentDefault(name, scope string) string {
	name = strings.TrimSpace(name)
	if !a.agentMu.TryLock() {
		return errMainAgentBusy
	}
	defer a.agentMu.Unlock()
	if name != "" {
		if _, err := findMainAgentDef(a.loadSubAgentDefs(a.GetWorkDir(), false), name); err != nil {
			return err.Error()
		}
	}
	switch scope {
	case mainAgentSourceUser, "":
		a.cfg.SetAgent(name)
		if err := a.cfg.Save(); err != nil {
			return err.Error()
		}
	case mainAgentSourceProject:
		if _, err := subagents.SaveProjectAgentSetting(a.GetWorkDir(), name); err != nil {
			return err.Error()
		}
	default:
		return fmt.Sprintf("unknown scope %q: use user or project", scope)
	}
	logger.Info("Default main conversation agent (%s) saved: %q", scope, name)
	a.loadMainAgentChoice()
	a.mainAg.mu.Lock()
	a.mainAg.choice = nil
	a.mainAg.mu.Unlock()
	return a.rebuildMainAgentLocked()
}

// rebuildMainAgentLocked rebuilds the main agent with the current agent
// selection, keeping the conversation. The caller holds agentMu. It returns
// the error that kept the wanted agent from running ("" when none).
func (a *App) rebuildMainAgentLocked() string {
	old := a.currentAgent
	setup := a.resolveMainAgent()
	ag := a.newMainAgent(setup)
	if old != nil {
		ag.SetHistory(old.Messages())
	} else if a.cfg != nil {
		if err := ag.LoadSession(a.cfg.ConfigDir()); err != nil {
			logger.Error("Failed to load session: %s", err.Error())
		}
	}
	ag.SetMainAgentChoice(a.mainAgentChoice())
	a.currentAgent = ag
	if a.cfg != nil {
		a.saveSession(ag)
	}
	a.armInitialPromptLocked(ag)
	info := a.mainAgentInfoLocked()
	a.emitSubAgent("main_agent", map[string]any{"name": info.name, "error": info.err})
	return info.err
}

type mainAgentBrief struct{ name, err string }

func (a *App) mainAgentInfoLocked() mainAgentBrief {
	a.mainAg.mu.Lock()
	defer a.mainAg.mu.Unlock()
	b := mainAgentBrief{err: a.mainAg.err}
	if a.mainAg.active != nil {
		b.name = a.mainAg.active.name
	}
	return b
}
