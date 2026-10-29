package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Env is what hooks need from the app. Set it once with SetEnv; the zero
// value runs command and http hooks only.
type Env struct {
	// ProjectDir is the project root where the session started: the value of
	// OPENUAI_PROJECT_DIR and of the ${OPENUAI_PROJECT_DIR} placeholder.
	ProjectDir string
	// OverflowDir is where hook output over 10,000 characters is saved.
	OverflowDir string
	// Prompt makes one model call (prompt hooks). model may be empty.
	Prompt func(ctx context.Context, model, system, user string) (string, error)
	// Agent runs a read-only sub-agent and returns its final answer (agent
	// hooks). model may be empty.
	Agent func(ctx context.Context, model, system, user, dir string) (string, error)
	// MCP calls a tool of a configured MCP server (mcp_tool hooks).
	MCP func(ctx context.Context, server, tool string, args map[string]any) (text string, isError bool, err error)
	// Deliver hands the output of a background (async) hook to the model:
	// on its next turn, or at once when wake is set (asyncRewake).
	Deliver func(text string, wake bool)
}

var (
	envMu sync.RWMutex
	env   Env
)

// SetEnv sets what hooks use from the app.
func SetEnv(e Env) {
	envMu.Lock()
	env = e
	envMu.Unlock()
}

func getEnv() Env {
	envMu.RLock()
	defer envMu.RUnlock()
	return env
}

type ctxKey int

const (
	statusKey ctxKey = iota
	noMCPKey
)

// WithStatus makes Run report each handler's statusMessage to fn while it
// runs.
func WithStatus(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, statusKey, fn)
}

// WithoutMCP marks a run where MCP servers are not available yet (the
// SessionStart at launch): its mcp_tool hooks are skipped, as in Claude Code.
func WithoutMCP(ctx context.Context) context.Context {
	return context.WithValue(ctx, noMCPKey, true)
}

// Result is the combined outcome of the hooks run for one event.
type Result struct {
	// Decision: "" (go on), "allow", "ask", "deny" (PreToolUse,
	// PermissionRequest, PreModelSwitch) or "block".
	Decision     string
	Reason       string
	Context      []string       // additional context for the model
	UpdatedInput map[string]any // PreToolUse / PermissionRequest: new tool input
	// UpdatedToolOutput replaces the tool result the model sees (PostToolUse).
	UpdatedToolOutput *string
	Errors            []string // hooks that failed or timed out (non-blocking)
	Messages          []string // systemMessage values, shown to the user
	Notices           []string // exit-2 stderr that only the user sees
	StopAll           bool     // continue: false (or a PermissionRequest interrupt)
	StopReason        string
	Retry             bool // PermissionDenied: the model may retry

	// SessionStart / UserPromptSubmit / CwdChanged / FileChanged outputs.
	InitialUserMessage string
	SessionTitle       string
	WatchPaths         []string
	WatchPathsSet      bool // a hook returned watchPaths (even empty)
	SuppressPrompt     bool // suppressOriginalPrompt
	// DisplayContent replaces the text shown on screen (MessageDisplay).
	DisplayContent *string
	// WorktreePath is the directory a WorktreeCreate hook created.
	WorktreePath string

	Ran int
	ok  bool // this handler succeeded (once removal)
}

const (
	maxText     = 10000
	previewText = 2000
	maxLLMHooks = 12
)

// capText applies the 10,000-character cap of Claude Code: longer text is
// saved to a file and replaced by its path and a 2,000-character preview.
func capText(s string) string {
	n := utf8.RuneCountInString(s)
	if n <= maxText {
		return s
	}
	preview := string([]rune(s)[:previewText])
	dir := getEnv().OverflowDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "openuai-hooks")
	}
	var path string
	if err := os.MkdirAll(dir, 0o700); err == nil {
		if f, err := os.CreateTemp(dir, "hook-output-*.txt"); err == nil {
			_, werr := f.WriteString(s)
			f.Close()
			if werr == nil {
				path = f.Name()
			}
		}
	}
	if path == "" {
		return preview + " …[truncated]"
	}
	return fmt.Sprintf("[Hook output of %d characters saved to %s. First %d characters:]\n%s", n, path, previewText, preview)
}

// escapeReminder keeps hook text from opening or closing a system reminder.
func escapeReminder(s string) string {
	s = strings.ReplaceAll(s, "</system-reminder>", "&lt;/system-reminder>")
	return strings.ReplaceAll(s, "<system-reminder>", "&lt;system-reminder>")
}

// Precedence of decisions: deny/block > ask (defer is treated as ask) > allow.
var rank = map[string]int{"": 0, "allow": 1, "ask": 2, "block": 3, "deny": 3}

func defaultTimeout(event, typ string) time.Duration {
	switch typ {
	case "prompt":
		return 30 * time.Second
	case "agent":
		return 60 * time.Second
	}
	switch event {
	case UserPromptSubmit, PreModelSwitch, PostModelSwitch:
		return 30 * time.Second
	case MessageDisplay:
		return 10 * time.Second
	case SessionEnd:
		return 1500 * time.Millisecond
	}
	return 600 * time.Second
}

// Run runs the hooks of event whose matcher matches target, in parallel, with
// input as JSON (stdin, request body...), in dir. Their outcomes are
// combined: a block or deny wins over ask, which wins over allow.
func (s *Set) Run(ctx context.Context, event, target string, input map[string]any, dir string) Result {
	var res Result
	if s == nil || s.Disabled {
		return res
	}
	if input == nil {
		input = map[string]any{}
	}
	var run []Entry
	for _, e := range s.Entries {
		if e.Event != event {
			continue
		}
		if !noMatcher[event] {
			match, err := compileMatcher(event, e.Matcher)
			if err != nil || !match(target) {
				continue
			}
		}
		if e.H.If != "" && (!toolEvents[event] || !ifMatches(e.H.If, input, dir)) {
			continue
		}
		if e.H.Type == "mcp_tool" && ctx.Value(noMCPKey) != nil {
			continue
		}
		run = append(run, e)
	}
	if len(run) == 0 {
		return res
	}
	input["hook_event_name"] = event
	if _, ok := input["cwd"]; !ok {
		input["cwd"] = dir
	}
	stdin, _ := json.Marshal(input)

	if event == SessionEnd {
		// SessionEnd hooks share a 1.5 s budget, raised to the longest
		// per-hook timeout (up to 60 s).
		budget := 1500 * time.Millisecond
		for _, e := range run {
			if t := time.Duration(e.H.Timeout) * time.Second; t > budget {
				budget = t
			}
		}
		if budget > 60*time.Second {
			budget = 60 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	status, _ := ctx.Value(statusKey).(func(string))

	outs := make([]Result, len(run))
	llm := make(chan struct{}, maxLLMHooks)
	var wg sync.WaitGroup
	for i, e := range run {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			if e.H.Type == "prompt" || e.H.Type == "agent" {
				select {
				case llm <- struct{}{}:
					defer func() { <-llm }()
				case <-ctx.Done():
					return
				}
			}
			if status != nil && e.H.StatusMessage != "" {
				status(e.H.StatusMessage)
			}
			outs[i] = runOne(ctx, event, target, e, input, stdin, dir)
			if outs[i].ok && e.remove != nil {
				e.remove()
			}
		}(i, e)
	}
	wg.Wait()

	res.Ran = len(run)
	for _, o := range outs {
		if rank[o.Decision] > rank[res.Decision] {
			res.Decision, res.Reason = o.Decision, o.Reason
			if o.UpdatedInput != nil || o.Decision == "deny" || o.Decision == "block" {
				res.UpdatedInput = o.UpdatedInput
			}
		} else if o.Decision != "" && o.Decision == res.Decision && o.Reason != "" && res.Reason != o.Reason {
			res.Reason = strings.TrimSpace(res.Reason + "\n" + o.Reason)
		}
		if o.UpdatedInput != nil && res.UpdatedInput == nil && rank[o.Decision] == rank[res.Decision] {
			res.UpdatedInput = o.UpdatedInput
		}
		res.Context = append(res.Context, o.Context...)
		res.Errors = append(res.Errors, o.Errors...)
		res.Messages = append(res.Messages, o.Messages...)
		res.Notices = append(res.Notices, o.Notices...)
		if o.UpdatedToolOutput != nil {
			res.UpdatedToolOutput = o.UpdatedToolOutput
		}
		if o.StopAll {
			res.StopAll = true
			if o.StopReason != "" {
				res.StopReason = o.StopReason
			}
		}
		res.Retry = res.Retry || o.Retry
		if o.InitialUserMessage != "" {
			res.InitialUserMessage = strings.TrimSpace(res.InitialUserMessage + "\n\n" + o.InitialUserMessage)
		}
		if o.SessionTitle != "" {
			res.SessionTitle = o.SessionTitle
		}
		if o.WatchPathsSet {
			res.WatchPathsSet = true
			res.WatchPaths = append(res.WatchPaths, o.WatchPaths...)
		}
		res.SuppressPrompt = res.SuppressPrompt || o.SuppressPrompt
		if o.DisplayContent != nil {
			res.DisplayContent = o.DisplayContent
		}
		if res.WorktreePath == "" {
			res.WorktreePath = o.WorktreePath
		}
	}
	if res.Decision == "deny" || res.Decision == "block" {
		if res.Reason == "" {
			res.Reason = "blocked by a hook"
		}
	}
	return res
}

func hookName(event, target string, e Entry) string {
	label := event
	if target != "" {
		label += ":" + target
	}
	return fmt.Sprintf("%s %s hook [%s]", e.Source, label, short(e.Command))
}

func runOne(ctx context.Context, event, target string, e Entry, input map[string]any, stdin []byte, dir string) Result {
	timeout := defaultTimeout(event, e.H.Type)
	if e.H.Timeout > 0 {
		timeout = time.Duration(e.H.Timeout) * time.Second
	}
	name := hookName(event, target, e)
	switch e.H.Type {
	case "command":
		if e.H.Async || e.H.AsyncRewake {
			startAsync(event, name, e, stdin, dir, timeout)
			return Result{ok: true}
		}
		return finish(event, e, runCommand(ctx, event, name, e, stdin, dir, timeout))
	case "http":
		return finish(event, e, runHTTP(ctx, name, e, stdin, timeout))
	case "mcp_tool":
		return finish(event, e, runMCP(ctx, name, e, input, timeout))
	case "prompt", "agent":
		return runLLM(ctx, event, name, e, stdin, dir, timeout)
	}
	return Result{}
}

// outcome is what one command, http or mcp_tool handler produced.
type outcome struct {
	name      string
	cancelled bool   // the action was cancelled: no result at all
	failure   string // non-blocking error ("" when the run succeeded)
	timedOut  bool
	block     bool // exit code 2
	blockMsg  string
	parsed    *output
	plain     string // plain-text stdout
	path      string // WorktreeCreate (command): the created directory
}

// Events whose plain-text stdout (exit 0) is context for the model.
var plainContext = map[string]bool{UserPromptSubmit: true, UserPromptExpansion: true, SessionStart: true, PostModelSwitch: true}

func finish(event string, e Entry, oc outcome) Result {
	var r Result
	if oc.cancelled {
		return r
	}
	if oc.failure != "" {
		r.Errors = append(r.Errors, oc.name+" "+oc.failure)
		switch {
		case event == WorktreeCreate || event == WorktreeRemove:
			r.Decision, r.Reason = "block", oc.name+" "+oc.failure
		case event == PreModelSwitch && oc.timedOut:
			r.Decision, r.Reason = "deny", oc.name+" timed out"
		case e.H.OnFailure == "block" && event != Stop && event != SubagentStop:
			word := "failed"
			if oc.timedOut {
				word = "timed out"
			}
			reason := fmt.Sprintf("%s %s; blocking because onFailure is \"block\"\n%s", oc.name, word, oc.failure)
			if event == PermissionRequest {
				r.Decision, r.Reason = "deny", reason
			} else {
				exit2(&r, event, reason)
			}
		}
		return r
	}
	if oc.block {
		reason := oc.blockMsg
		if oc.parsed != nil {
			applyOutput(&r, event, oc.parsed, true)
			if jr := jsonBlockReason(event, oc.parsed); jr != "" {
				reason = jr
			}
		}
		exit2(&r, event, reason)
		return r
	}
	r.ok = true
	r.WorktreePath = oc.path
	if oc.parsed != nil {
		applyOutput(&r, event, oc.parsed, false)
	} else if oc.plain != "" && plainContext[event] {
		r.Context = append(r.Context, escapeReminder(capText(oc.plain)))
	}
	return r
}

// exit2 applies what exit code 2 does on event (docs: "Exit code 2 behavior
// per event").
func exit2(r *Result, event, reason string) {
	switch event {
	case PreToolUse, PreModelSwitch:
		r.Decision = "deny"
	case UserPromptSubmit, UserPromptExpansion, Stop, SubagentStop, ConfigChange, PostToolBatch,
		PreCompact, WorktreeCreate, WorktreeRemove, PostToolUse, PostToolUseFailure:
		r.Decision = "block"
	case SessionStart, SubagentStart, SessionEnd, CwdChanged, FileChanged, PostCompact, PostModelSwitch:
		r.Notices = append(r.Notices, capText(reason))
		return
	default: // PermissionRequest, PermissionDenied, Notification, StopFailure, InstructionsLoaded, MessageDisplay
		return
	}
	r.Reason = capText(reason)
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 60 {
		return string([]rune(s)[:60]) + "…"
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
