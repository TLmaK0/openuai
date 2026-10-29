package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"openuai/internal/agent"
	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/logger"
	"openuai/internal/mcpclient"
	"openuai/internal/procs"
	"openuai/internal/subagents"
	"openuai/internal/tools"
	"openuai/internal/tray"
)

// hookState is the app-level state of hooks: the files in effect, hooks
// registered at run time, context waiting for the model's next turn and what
// the watcher (FileChanged, CwdChanged, ConfigChange) last saw.
type hookState struct {
	loader  hooks.Loader
	session hooks.SessionHooks

	mu        sync.Mutex
	sessionID string
	notes     []string      // context for the model's next turn
	title     string        // session title from a hook, applied on the next turn
	ready     chan struct{} // closed when the SessionStart hooks are done
	watch     []string      // FileChanged paths returned by hooks
	files     map[string]fileSig
	cwd       string
	configSig string
	skillsSig string
	started   bool
}

type fileSig struct {
	exists bool
	mod    time.Time
	size   int64
}

func statSig(p string) fileSig {
	st, err := os.Stat(p)
	if err != nil {
		return fileSig{}
	}
	return fileSig{exists: true, mod: st.ModTime(), size: st.Size()}
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// loadHooks reads the user and project hooks (plus those registered while the
// app runs). It runs on every event, so edits to the hook files apply without
// restarting; a ConfigChange hook can refuse an edit.
func (a *App) loadHooks() *hooks.Set {
	return a.hk.loader.Load(a.cfg.ConfigDir(), a.GetWorkDir()).Merge(a.hk.session.Set())
}

// GetHooks lists the active hooks (and problems in the hook files) for the UI.
func (a *App) GetHooks() *hooks.Set {
	s := a.loadHooks()
	if s.Entries == nil {
		s.Entries = []hooks.Entry{}
	}
	return s
}

// registerSessionHooks adds hooks for the rest of the session (for example
// the hooks a skill declares when it is invoked); handlers with once: true
// are removed after their first successful run.
func (a *App) registerSessionHooks(cfg hooks.Config, source string) {
	for _, w := range a.hk.session.Register(cfg, source) {
		logger.Error("Hooks (%s): %s", source, w)
	}
}

func (a *App) emitStep(step agent.StepResult) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "agent_step", step)
	if a.apiServer != nil {
		a.apiServer.Broadcast("agent_step", "", step)
	}
}

func (a *App) emitHookError(msg string) {
	a.emitStep(agent.StepResult{Type: "error", Content: "Hook error: " + msg})
}

func (a *App) sessionID() string {
	a.hk.mu.Lock()
	defer a.hk.mu.Unlock()
	if a.hk.sessionID == "" {
		a.hk.sessionID = newSessionID()
	}
	return a.hk.sessionID
}

func (a *App) transcriptPath() string {
	return filepath.Join(a.cfg.ConfigDir(), "sessions", "current.json")
}

// commonHookInput is the input every app-level hook receives.
func (a *App) commonHookInput() map[string]any {
	return map[string]any{
		"session_id": a.sessionID(), "transcript_path": a.transcriptPath(),
		"cwd": a.GetWorkDir(), "permission_mode": "default",
	}
}

// runAppHook runs the hooks of an event fired by the app (outside an agent
// turn) and shows their errors and messages to the user.
func (a *App) runAppHook(ctx context.Context, event, target string, in map[string]any) hooks.Result {
	return a.runHookOn(ctx, a.loadHooks(), event, target, in)
}

func (a *App) runHookOn(ctx context.Context, hs *hooks.Set, event, target string, in map[string]any) hooks.Result {
	if !hs.Has(event) {
		return hooks.Result{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	full := a.commonHookInput()
	for k, v := range in {
		full[k] = v
	}
	ctx = hooks.WithStatus(ctx, func(m string) { a.emitStep(agent.StepResult{Type: "notice", Content: m}) })
	r := hs.Run(ctx, event, target, full, a.GetWorkDir())
	if r.Ran > 0 {
		logger.Info("Hooks %s(%s): ran=%d decision=%q context=%d errors=%d", event, target, r.Ran, r.Decision, len(r.Context), len(r.Errors))
	}
	for _, e := range r.Errors {
		logger.Error("Hook error: %s", e)
		a.emitHookError(e)
	}
	for _, e := range r.Notices {
		logger.Info("Hook notice (%s): %s", event, e)
		a.emitStep(agent.StepResult{Type: "error", Content: event + " hook error: " + e})
	}
	for _, m := range r.Messages {
		logger.Info("Hook message: %s", m)
		a.emitStep(agent.StepResult{Type: "notice", Content: "Hook: " + m})
	}
	return r
}

// addHookNote queues context for the model's next turn (SessionStart,
// PostModelSwitch and background hooks).
func (a *App) addHookNote(notes ...string) {
	a.hk.mu.Lock()
	defer a.hk.mu.Unlock()
	for _, n := range notes {
		if strings.TrimSpace(n) != "" {
			a.hk.notes = append(a.hk.notes, n)
		}
	}
}

func (a *App) takeHookNotes() []string {
	a.hk.mu.Lock()
	defer a.hk.mu.Unlock()
	n := a.hk.notes
	a.hk.notes = nil
	return n
}

// applyHookTitle gives the agent the session title a hook set, if any.
func (a *App) applyHookTitle(ag *agent.Agent) {
	a.hk.mu.Lock()
	t := a.hk.title
	a.hk.title = ""
	a.hk.mu.Unlock()
	if t != "" && ag != nil {
		ag.SetTitle(t)
	}
}

func (a *App) setHookTitle(t string) {
	if t = strings.TrimSpace(t); t == "" {
		return
	}
	a.hk.mu.Lock()
	a.hk.title = t
	a.hk.mu.Unlock()
}

// initHooks wires hooks to the app: model calls for prompt and agent hooks,
// MCP servers for mcp_tool hooks, background hook output, ConfigChange on
// hook-file edits; then it starts the watcher.
func (a *App) initHooks() {
	project := a.GetWorkDir()
	hooks.SetEnv(hooks.Env{
		ProjectDir:  project,
		OverflowDir: filepath.Join(a.cfg.ConfigDir(), "hook-output"),
		Prompt:      a.hookPrompt,
		Agent:       a.hookAgent,
		MCP:         a.hookMCP,
		Deliver:     a.deliverHookOutput,
	})
	a.hk.loader.OnChange = func(prev *hooks.Set, source, path string) bool {
		r := a.runHookOn(context.Background(), prev, hooks.ConfigChange, source, map[string]any{"source": source, "file_path": path})
		if r.Decision == "block" {
			logger.Info("ConfigChange hook blocked the change of %s: the previous hooks stay in effect", path)
			return true
		}
		return false
	}
	a.hk.mu.Lock()
	a.hk.cwd = project
	a.hk.configSig = a.configSig()
	a.hk.skillsSig = a.skillsSig()
	a.hk.mu.Unlock()
	a.loadHooks() // what is in effect now; later edits fire ConfigChange
	go a.watchHooks()
}

// hookPrompt is the single model call of a prompt hook.
func (a *App) hookPrompt(ctx context.Context, model, system, user string) (string, error) {
	p := a.activeProvider()
	if p == nil {
		return "", errors.New("no model provider is configured")
	}
	if model == "" {
		model = a.cfg.DefaultModel()
	}
	resp, err := p.Chat(ctx, []llm.Message{{Role: llm.RoleSystem, Content: system}, {Role: llm.RoleUser, Content: user}}, model)
	if err != nil {
		return "", err
	}
	a.costTracker.Track(resp)
	return resp.Content, nil
}

// hookAgent runs the sub-agent of an agent hook: read-only tools, no hooks
// of its own, at most 50 turns.
func (a *App) hookAgent(ctx context.Context, model, system, user, dir string) (string, error) {
	p := a.activeProvider()
	if p == nil {
		return "", errors.New("no model provider is configured")
	}
	if model == "" {
		model = a.cfg.DefaultModel()
	}
	reg := tools.NewRegistry()
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ListDir{})
	reg.Register(tools.SearchFiles{})
	reg.Register(tools.GitStatus{WorkDir: dir})
	reg.Register(tools.GitDiff{WorkDir: dir})
	reg.Register(tools.GitLog{WorkDir: dir})
	tracker := llm.NewCostTracker()
	ag := agent.New(agent.Config{
		Provider: p, Model: model, Registry: reg, Permissions: a.permissions, CostTracker: tracker,
		SystemPrompt: system + "\n\nWorking directory: " + dir, MaxTurns: 50, PermissionMode: agent.PermModePlan,
		WorkDir: dir, AgentType: "hook-agent", AgentID: agent.NewAgentID(),
	})
	err := ag.Run(ctx, user)
	s := tracker.Summary()
	a.costTracker.Track(&llm.Response{Model: model, InputTokens: s.TotalInputTokens, OutputTokens: s.TotalOutputTokens, CostUSD: s.TotalCostUSD})
	if out := ag.LastAssistantContent(); out != "" {
		return out, nil
	}
	return "", err
}

// hookMCP calls an MCP tool for an mcp_tool hook.
func (a *App) hookMCP(ctx context.Context, server, tool string, args map[string]any) (string, bool, error) {
	if a.mcpManager == nil {
		return "", false, errors.New("no MCP servers are configured")
	}
	if _, ok := a.mcpManager.GetConnection(server); !ok {
		return "", false, fmt.Errorf("MCP server %q is not connected", server)
	}
	res, err := a.mcpManager.CallTool(ctx, server, tool, args)
	if err != nil {
		return "", false, err
	}
	return mcpclient.ContentToString(res.Content), res.IsError, nil
}

// deliverHookOutput hands a background hook's output to the model: queued
// for its next turn, or (asyncRewake) as a notification that wakes it now.
func (a *App) deliverHookOutput(text string, wake bool) {
	if !wake {
		a.addHookNote(text)
		return
	}
	if a.pending != nil && a.pending.Add(fmt.Sprintf("hook:%d", time.Now().UnixNano()), text) {
		go a.drainPending()
	}
}

// backgroundTasks lists running background processes for Stop hooks.
func (a *App) backgroundTasks() []map[string]any {
	if a.procs == nil {
		return nil
	}
	var out []map[string]any
	for _, p := range a.procs.List() {
		if p.Status == procs.StatusExited {
			continue
		}
		cmd := p.Command
		if len(cmd) > 1000 {
			cmd = cmd[:1000]
		}
		out = append(out, map[string]any{"id": p.ID, "type": "shell", "status": string(p.Status), "description": cmd, "command": cmd})
	}
	return out
}

// startSession fires SessionStart (source startup, resume, clear) in the
// background; the next message waits for it so its context reaches the
// model. At launch MCP servers are not ready, so mcp_tool hooks are skipped.
func (a *App) startSession(source string, launch bool) {
	ready := make(chan struct{})
	a.hk.mu.Lock()
	a.hk.ready = ready
	a.hk.started = true
	a.hk.mu.Unlock()
	go func() {
		defer close(ready)
		ctx := context.Background()
		if launch {
			ctx = hooks.WithoutMCP(ctx)
		}
		in := map[string]any{"source": source, "model": a.cfg.DefaultModel()}
		r := a.runAppHook(ctx, hooks.SessionStart, source, in)
		if r.Ran == 0 {
			return
		}
		a.addHookNote(hookNotesOf(r)...)
		if source != "clear" && source != "compact" {
			a.setHookTitle(r.SessionTitle)
		}
		if r.WatchPathsSet {
			a.setWatchPaths(r.WatchPaths)
		}
		if r.InitialUserMessage != "" && a.pending != nil {
			logger.Info("SessionStart hook supplied the first message (%d chars)", len(r.InitialUserMessage))
			if a.pending.Add("hook-initial:"+a.sessionID(), r.InitialUserMessage) {
				go a.drainPending()
			}
		}
	}()
}

// waitSessionStart waits for the SessionStart hooks of the session.
func (a *App) waitSessionStart(ctx context.Context) {
	a.hk.mu.Lock()
	ready := a.hk.ready
	a.hk.mu.Unlock()
	if ready == nil {
		return
	}
	select {
	case <-ready:
	case <-ctx.Done():
	}
}

func hookNotesOf(r hooks.Result) []string {
	notes := append([]string(nil), r.Context...)
	for _, e := range r.Errors {
		notes = append(notes, "Hook error: "+e)
	}
	return notes
}

// endSession fires SessionEnd (reason clear, resume, other) and forgets the
// hooks registered during the session.
func (a *App) endSession(reason string) {
	a.hk.mu.Lock()
	started := a.hk.started
	a.hk.mu.Unlock()
	if !started {
		return
	}
	a.runAppHook(context.Background(), hooks.SessionEnd, reason, map[string]any{"reason": reason})
	a.hk.session.Clear()
	a.hk.mu.Lock()
	a.hk.sessionID = ""
	a.hk.notes = nil
	a.hk.title = ""
	a.hk.mu.Unlock()
}

// notify shows a desktop notification and fires the Notification hooks.
func (a *App) notify(ntype, title, message string) {
	tray.Notify(title, message)
	a.notifyHook(ntype, title, message)
}

// notifyHook fires the Notification hooks (they cannot block anything).
func (a *App) notifyHook(ntype, title, message string) {
	if a.cfg == nil || !a.loadHooks().Has(hooks.Notification) {
		return
	}
	go a.runAppHook(context.Background(), hooks.Notification, ntype, map[string]any{
		"message": message, "title": title, "notification_type": ntype,
	})
}

// instructionsLoaded fires InstructionsLoaded for an instruction file loaded
// into a prompt (memory index, AGENTS.md of a sub-agent).
func (a *App) instructionsLoaded(path, memoryType, reason string) {
	if !a.loadHooks().Has(hooks.InstructionsLoaded) {
		return
	}
	go a.runAppHook(context.Background(), hooks.InstructionsLoaded, reason, map[string]any{
		"file_path": path, "memory_type": memoryType, "load_reason": reason,
	})
}

// displayStep applies MessageDisplay hooks to assistant text before it is
// shown: displayContent replaces the text on screen only; the conversation
// keeps the original.
func (a *App) displayStep(step agent.StepResult) agent.StepResult {
	if (step.Type != "text" && step.Type != "done") || step.Content == "" {
		return step
	}
	hs := a.loadHooks()
	if !hs.Has(hooks.MessageDisplay) {
		return step
	}
	r := hs.Run(context.Background(), hooks.MessageDisplay, "", map[string]any{
		"session_id": a.sessionID(), "transcript_path": a.transcriptPath(), "cwd": a.GetWorkDir(),
		"turn_id": newSessionID(), "message_id": newSessionID(), "index": 0, "final": true, "delta": step.Content,
	}, a.GetWorkDir())
	for _, e := range r.Errors {
		logger.Error("MessageDisplay hook: %s", e) // the original text is shown
	}
	if r.DisplayContent != nil {
		step.Content = *r.DisplayContent
	}
	return step
}

// canonicalModel is the model name matchers of model-switch hooks compare.
func canonicalModel(m string) string {
	return strings.TrimSpace(strings.TrimSuffix(m, "[1m]"))
}

func (a *App) modelSwitchInput(from, to, requested, source string) map[string]any {
	in := map[string]any{
		"from_model": from, "to_model": to, "source": source,
		"context_tokens": 0, "prompt_cache_warm": false, "cache_ttl": "5m",
		"estimated_cache_write_usd": 0, "pricing": "default",
	}
	if requested != "" {
		in["requested_model"] = requested
	} else {
		in["requested_model"] = nil
	}
	return in
}

// preModelSwitch runs PreModelSwitch; an error means a hook refused the
// switch ("ask" counts as a refusal: settings cannot show a confirmation).
func (a *App) preModelSwitch(from, to, requested, source string) error {
	if from == to {
		return nil
	}
	r := a.runAppHook(context.Background(), hooks.PreModelSwitch, canonicalModel(to), a.modelSwitchInput(from, to, requested, source))
	if r.Decision == "deny" || r.Decision == "block" || r.Decision == "ask" {
		reason := r.Reason
		if reason == "" {
			reason = "a PreModelSwitch hook needs confirmation"
		}
		logger.Info("Model switch %s -> %s blocked by a hook: %s", from, to, reason)
		a.emitStep(agent.StepResult{Type: "error", Content: "Model switch blocked by a hook: " + reason})
		return fmt.Errorf("model switch blocked by a hook: %s", reason)
	}
	return nil
}

// postModelSwitch runs PostModelSwitch: its context reaches the model with
// the next request.
func (a *App) postModelSwitch(from, to, requested, source string) {
	if from == to {
		return
	}
	r := a.runAppHook(context.Background(), hooks.PostModelSwitch, canonicalModel(to), a.modelSwitchInput(from, to, requested, source))
	a.addHookNote(hookNotesOf(r)...)
}

func (a *App) setWatchPaths(paths []string) {
	a.hk.mu.Lock()
	defer a.hk.mu.Unlock()
	a.hk.watch = nil
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" && filepath.IsAbs(p) {
			a.hk.watch = append(a.hk.watch, filepath.Clean(p))
		}
	}
}

const hookWatchInterval = 2 * time.Second

// watchHooks polls for the changes some hooks react to: watched files
// (FileChanged), the working directory (CwdChanged), the settings and the
// skills (ConfigChange) and the hook files themselves (ConfigChange, through
// the loader).
func (a *App) watchHooks() {
	t := time.NewTicker(hookWatchInterval)
	defer t.Stop()
	for range t.C {
		if a.shuttingDown.Load() {
			return
		}
		a.checkHookChanges()
	}
}

func (a *App) checkHookChanges() {
	hs := a.loadHooks() // fires ConfigChange for edited hook files
	a.checkCwd(hs)
	a.checkConfig(hs)
	a.checkFiles(hs)
}

// checkCwd fires CwdChanged when the working directory changed.
func (a *App) checkCwd(hs *hooks.Set) {
	cwd := a.GetWorkDir()
	a.hk.mu.Lock()
	old := a.hk.cwd
	a.hk.cwd = cwd
	a.hk.mu.Unlock()
	if old == "" || old == cwd || !hs.Has(hooks.CwdChanged) {
		return
	}
	r := a.runHookOn(context.Background(), hs, hooks.CwdChanged, "", map[string]any{"old_cwd": old, "new_cwd": cwd})
	if r.WatchPathsSet {
		a.setWatchPaths(r.WatchPaths)
	}
}

func (a *App) configSig() string {
	s := statSig(filepath.Join(a.cfg.ConfigDir(), "config.json"))
	return fmt.Sprintf("%v/%d/%d", s.exists, s.mod.UnixNano(), s.size)
}

func (a *App) skillsSig() string {
	var parts []string
	for _, d := range []string{filepath.Join(a.cfg.ConfigDir(), "skills"), filepath.Join(a.GetWorkDir(), ".openuai", "skills")} {
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			s := statSig(p)
			parts = append(parts, fmt.Sprintf("%s/%d/%d", p, s.mod.UnixNano(), s.size))
			return nil
		})
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// checkConfig fires ConfigChange when the settings (config.json, written
// when settings are saved) or the skills change. OpenUAI applies them as
// soon as they are saved, so a block here is only logged; hook-file edits,
// which can be blocked, go through the loader.
func (a *App) checkConfig(hs *hooks.Set) {
	cs, ss := a.configSig(), a.skillsSig()
	a.hk.mu.Lock()
	oldC, oldS := a.hk.configSig, a.hk.skillsSig
	a.hk.configSig, a.hk.skillsSig = cs, ss
	a.hk.mu.Unlock()
	if !hs.Has(hooks.ConfigChange) {
		return
	}
	if cs != oldC {
		r := a.runHookOn(context.Background(), hs, hooks.ConfigChange, "user_settings", map[string]any{"source": "user_settings", "file_path": filepath.Join(a.cfg.ConfigDir(), "config.json")})
		if r.Decision == "block" {
			logger.Info("ConfigChange hook blocked a settings change, but settings saved from the app are already applied")
		}
	}
	if ss != oldS {
		r := a.runHookOn(context.Background(), hs, hooks.ConfigChange, "skills", map[string]any{"source": "skills"})
		if r.Decision == "block" {
			logger.Info("ConfigChange hook blocked a skills change, but skills are read from disk on every use")
		}
	}
}

// watchedFiles is the FileChanged watch list: the literal names in the
// matchers (relative to the working directory) plus the paths hooks returned.
func (a *App) watchedFiles(hs *hooks.Set) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	wd := a.GetWorkDir()
	for _, m := range hs.Matchers(hooks.FileChanged) {
		for _, name := range strings.Split(m, "|") {
			if name = strings.TrimSpace(name); name != "" {
				if filepath.IsAbs(name) {
					add(filepath.Clean(name))
				} else {
					add(filepath.Join(wd, name))
				}
			}
		}
	}
	a.hk.mu.Lock()
	for _, p := range a.hk.watch {
		add(p)
	}
	a.hk.mu.Unlock()
	return out
}

// checkFiles fires FileChanged for watched files that changed, appeared or
// disappeared since the last check.
func (a *App) checkFiles(hs *hooks.Set) {
	if !hs.Has(hooks.FileChanged) {
		return
	}
	type change struct{ path, event string }
	var changes []change
	a.hk.mu.Lock()
	if a.hk.files == nil {
		a.hk.files = map[string]fileSig{}
	}
	a.hk.mu.Unlock()
	for _, p := range a.watchedFiles(hs) {
		sig := statSig(p)
		a.hk.mu.Lock()
		old, known := a.hk.files[p]
		a.hk.files[p] = sig
		a.hk.mu.Unlock()
		switch {
		case !known || old == sig:
		case !old.exists && sig.exists:
			changes = append(changes, change{p, "add"})
		case old.exists && !sig.exists:
			changes = append(changes, change{p, "unlink"})
		default:
			changes = append(changes, change{p, "change"})
		}
	}
	for _, c := range changes {
		r := a.runHookOn(context.Background(), hs, hooks.FileChanged, filepath.Base(c.path), map[string]any{"file_path": c.path, "event": c.event})
		if r.WatchPathsSet {
			a.setWatchPaths(r.WatchPaths)
		}
	}
}

// instructionFilesFor returns the AGENTS.md files a sub-agent prompt loads.
func instructionFilesFor(def *subagents.Definition, workDir string) []string {
	if def != nil && def.OmitAgentsMd {
		return nil
	}
	return subagents.ProjectInstructionFiles(workDir)
}
