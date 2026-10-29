// Package hooks runs the user's own commands automatically around the
// assistant's work (before/after tools, when a prompt is sent, when a turn or
// a sub-agent ends). The configuration has the same shape as Claude Code's
// "hooks" setting, but lives in OpenUAI's own folders:
//
//	<config dir>/hooks.json              user (all projects)
//	<project>/.openuai/hooks.json        project (can be committed)
//	<project>/.openuai/hooks.local.json  project, not for git
//
// Each file holds {"hooks": {"PreToolUse": [{"matcher": "bash", "hooks":
// [{"type": "command", "command": "..."}]}]}}.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Events OpenUAI fires (same names as Claude Code).
const (
	PreToolUse         = "PreToolUse"
	PostToolUse        = "PostToolUse"
	PostToolUseFailure = "PostToolUseFailure"
	UserPromptSubmit   = "UserPromptSubmit"
	Stop               = "Stop"
	SubagentStart      = "SubagentStart"
	SubagentStop       = "SubagentStop"
)

// Events lists every supported event.
var Events = []string{PreToolUse, PostToolUse, PostToolUseFailure, UserPromptSubmit, Stop, SubagentStart, SubagentStop}

func validEvent(e string) bool {
	for _, k := range Events {
		if e == k {
			return true
		}
	}
	return false
}

// Events without matcher support fire on every occurrence.
var noMatcher = map[string]bool{UserPromptSubmit: true, Stop: true}

// Handler is one hook handler. Only "command" handlers are supported.
type Handler struct {
	Type          string `json:"type" yaml:"type"`
	Command       string `json:"command" yaml:"command"`
	Timeout       int    `json:"timeout,omitempty" yaml:"timeout"`
	StatusMessage string `json:"statusMessage,omitempty" yaml:"statusMessage"`
}

// Group is a matcher group: the handlers that run when the matcher matches.
type Group struct {
	Matcher string    `json:"matcher,omitempty" yaml:"matcher"`
	Hooks   []Handler `json:"hooks" yaml:"hooks"`
}

// Config maps an event name to its matcher groups.
type Config map[string][]Group

// Entry is one active hook, with where it came from.
type Entry struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	Source  string `json:"source"` // user, project, local, agent:<name>
}

// Set is the active hooks.
type Set struct {
	Entries  []Entry  `json:"entries"`
	Warnings []string `json:"warnings,omitempty"`
}

// Load reads the user and project hook files. Missing files are fine;
// unreadable ones and invalid entries become warnings and are skipped.
func Load(configDir, workDir string) *Set {
	s := &Set{}
	if configDir != "" {
		s.loadFile(filepath.Join(configDir, "hooks.json"), "user")
	}
	if workDir != "" {
		s.loadFile(filepath.Join(workDir, ".openuai", "hooks.json"), "project")
		s.loadFile(filepath.Join(workDir, ".openuai", "hooks.local.json"), "local")
	}
	return s
}

func (s *Set) loadFile(path, source string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %v", path, err))
		return
	}
	var f struct {
		Hooks Config `json:"hooks"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%s: invalid JSON: %v", path, err))
		return
	}
	s.Add(f.Hooks, source)
}

// Add adds the hooks of cfg with the given source. Identical handlers
// (same event, matcher and command) are kept once.
func (s *Set) Add(cfg Config, source string) {
	events := make([]string, 0, len(cfg))
	for e := range cfg {
		events = append(events, e)
	}
	sort.Strings(events)
	for _, ev := range events {
		if !validEvent(ev) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: event %q is not supported (use %s)", source, ev, strings.Join(Events, ", ")))
			continue
		}
		for _, g := range cfg[ev] {
			if _, err := compileMatcher(g.Matcher); err != nil {
				s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %s matcher %q: %v", source, ev, g.Matcher, err))
				continue
			}
			for _, h := range g.Hooks {
				if h.Type != "command" {
					s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %s hook type %q is not supported (only \"command\")", source, ev, h.Type))
					continue
				}
				if strings.TrimSpace(h.Command) == "" {
					s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %s hook without command", source, ev))
					continue
				}
				e := Entry{Event: ev, Matcher: g.Matcher, Command: h.Command, Timeout: h.Timeout, Source: source}
				if !s.has(e) {
					s.Entries = append(s.Entries, e)
				}
			}
		}
	}
}

func (s *Set) has(e Entry) bool {
	for _, x := range s.Entries {
		if x.Event == e.Event && x.Matcher == e.Matcher && x.Command == e.Command {
			return true
		}
	}
	return false
}

// Merge returns a new set with the entries of s and other.
func (s *Set) Merge(other *Set) *Set {
	out := &Set{}
	for _, x := range []*Set{s, other} {
		if x == nil {
			continue
		}
		out.Warnings = append(out.Warnings, x.Warnings...)
		for _, e := range x.Entries {
			if !out.has(e) {
				out.Entries = append(out.Entries, e)
			}
		}
	}
	return out
}

// Has reports whether any hook is configured for event.
func (s *Set) Has(event string) bool {
	if s == nil {
		return false
	}
	for _, e := range s.Entries {
		if e.Event == event {
			return true
		}
	}
	return false
}

var exactMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]*$`)

// compileMatcher follows Claude Code's rules: empty or "*" matches all;
// only letters, digits, _ - space , | is a list of exact names; anything
// else is an unanchored regular expression.
func compileMatcher(m string) (func(string) bool, error) {
	m = strings.TrimSpace(m)
	if m == "" || m == "*" {
		return func(string) bool { return true }, nil
	}
	if exactMatcher.MatchString(m) {
		names := map[string]bool{}
		for _, p := range strings.FieldsFunc(m, func(r rune) bool { return r == '|' || r == ',' }) {
			if p = strings.TrimSpace(p); p != "" {
				names[p] = true
			}
		}
		return func(v string) bool { return names[v] }, nil
	}
	re, err := regexp.Compile(m)
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

// Result is the combined outcome of the hooks run for one event.
type Result struct {
	// Decision: "" (go on), "allow", "ask", "deny" (PreToolUse) or "block".
	Decision     string
	Reason       string
	Context      []string       // additional context for the model
	UpdatedInput map[string]any // PreToolUse: replaces the tool input
	Errors       []string       // hooks that failed or timed out (non-blocking)
	Messages     []string       // systemMessage values, shown to the user
	StopAll      bool           // continue: false
	StopReason   string
	Ran          int
}

const maxText = 10000

func clip(s string) string {
	if len(s) > maxText {
		return s[:maxText] + " …[truncated]"
	}
	return s
}

var rank = map[string]int{"": 0, "allow": 1, "ask": 2, "block": 3, "deny": 3}

// Run runs the hooks of event whose matcher matches target, in parallel, with
// input as JSON on stdin, in dir. Their outcomes are combined: a block or
// deny wins over ask, which wins over allow.
func (s *Set) Run(ctx context.Context, event, target string, input map[string]any, dir string) Result {
	var res Result
	if s == nil {
		return res
	}
	var run []Entry
	for _, e := range s.Entries {
		if e.Event != event {
			continue
		}
		if !noMatcher[event] {
			match, err := compileMatcher(e.Matcher)
			if err != nil || !match(target) {
				continue
			}
		}
		run = append(run, e)
	}
	if len(run) == 0 {
		return res
	}
	if input == nil {
		input = map[string]any{}
	}
	input["hook_event_name"] = event
	if _, ok := input["cwd"]; !ok {
		input["cwd"] = dir
	}
	stdin, _ := json.Marshal(input)

	outs := make([]Result, len(run))
	var wg sync.WaitGroup
	for i, e := range run {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			outs[i] = runOne(ctx, event, e, stdin, dir)
		}(i, e)
	}
	wg.Wait()

	res.Ran = len(run)
	for _, o := range outs {
		if rank[o.Decision] > rank[res.Decision] {
			res.Decision, res.Reason = o.Decision, o.Reason
		} else if o.Decision != "" && o.Decision == res.Decision && o.Reason != "" && res.Reason != o.Reason {
			res.Reason = strings.TrimSpace(res.Reason + "\n" + o.Reason)
		}
		res.Context = append(res.Context, o.Context...)
		res.Errors = append(res.Errors, o.Errors...)
		res.Messages = append(res.Messages, o.Messages...)
		if o.UpdatedInput != nil {
			res.UpdatedInput = o.UpdatedInput
		}
		if o.StopAll {
			res.StopAll = true
			res.StopReason = o.StopReason
		}
	}
	if res.Decision == "deny" || res.Decision == "block" {
		if res.Reason == "" {
			res.Reason = "blocked by a hook"
		}
	}
	return res
}

func defaultTimeout(event string) time.Duration {
	if event == UserPromptSubmit {
		return 30 * time.Second
	}
	return 600 * time.Second
}

func runOne(parent context.Context, event string, e Entry, stdin []byte, dir string) Result {
	var r Result
	name := e.Source + " " + event + " hook `" + short(e.Command) + "`"
	timeout := defaultTimeout(event)
	if e.Timeout > 0 {
		timeout = time.Duration(e.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", e.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", e.Command)
	}
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			cmd.Dir = dir
		}
	}
	cmd.Env = append(os.Environ(), "OPENUAI_PROJECT_DIR="+dir, "OPENUAI_HOOK_EVENT="+event)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()

	code := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			r.Errors = append(r.Errors, fmt.Sprintf("%s timed out after %s", name, timeout))
			return r
		case parent.Err() != nil:
			return r
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			r.Errors = append(r.Errors, fmt.Sprintf("%s could not run: %v", name, err))
			return r
		}
	}
	out := strings.TrimSpace(stdout.String())
	errText := strings.TrimSpace(stderr.String())
	isJSON := strings.HasPrefix(out, "{") && strings.HasSuffix(out, "}")
	var parsed *output
	if isJSON {
		var o output
		if jerr := json.Unmarshal([]byte(out), &o); jerr != nil {
			if code != 2 {
				r.Errors = append(r.Errors, fmt.Sprintf("%s printed invalid JSON: %v", name, jerr))
				return r
			}
		} else {
			parsed = &o
		}
	}

	if code == 2 {
		if errText == "" {
			errText = "blocked by " + name
		}
		r.Decision, r.Reason = blockWord(event), clip(errText)
		if parsed != nil {
			applyOutput(&r, event, parsed, true)
		}
		return r
	}
	if parsed != nil {
		applyOutput(&r, event, parsed, false)
		return r
	}
	if code != 0 {
		first := errText
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		r.Errors = append(r.Errors, fmt.Sprintf("%s failed (exit %d): %s", name, code, first))
		return r
	}
	// Plain stdout is context for the model on UserPromptSubmit only.
	if out != "" && event == UserPromptSubmit {
		r.Context = append(r.Context, clip(out))
	}
	return r
}

func blockWord(event string) string {
	if event == PreToolUse {
		return "deny"
	}
	return "block"
}

type output struct {
	Continue           *bool  `json:"continue"`
	StopReason         string `json:"stopReason"`
	SystemMessage      string `json:"systemMessage"`
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason"`
		UpdatedInput             map[string]any `json:"updatedInput"`
		AdditionalContext        string         `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func applyOutput(r *Result, event string, o *output, blocked bool) {
	if o.Continue != nil && !*o.Continue {
		r.StopAll = true
		r.StopReason = clip(o.StopReason)
	}
	if o.SystemMessage != "" {
		r.Messages = append(r.Messages, clip(o.SystemMessage))
	}
	if c := o.HookSpecificOutput.AdditionalContext; c != "" {
		r.Context = append(r.Context, clip(c))
	}
	if blocked {
		return // exit 2 already decided; JSON cannot override the block
	}
	if event == PreToolUse {
		d := o.HookSpecificOutput.PermissionDecision
		reason := o.HookSpecificOutput.PermissionDecisionReason
		switch strings.ToLower(o.Decision) { // deprecated top-level fields
		case "approve":
			if d == "" {
				d = "allow"
			}
		case "block":
			if d == "" {
				d = "deny"
			}
		}
		if reason == "" {
			reason = o.Reason
		}
		switch d {
		case "allow", "ask", "deny":
			r.Decision, r.Reason = d, clip(reason)
		}
		if o.HookSpecificOutput.UpdatedInput != nil {
			r.UpdatedInput = o.HookSpecificOutput.UpdatedInput
		}
		return
	}
	if strings.EqualFold(o.Decision, "block") {
		r.Decision, r.Reason = "block", clip(o.Reason)
	}
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
