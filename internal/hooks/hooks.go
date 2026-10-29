// Package hooks runs the user's own handlers automatically at points of the
// assistant's work (before/after tools, when a prompt is sent, when a turn or
// a sub-agent ends, when the session starts...). The configuration has the
// same shape and semantics as Claude Code's "hooks" setting, but lives in
// OpenUAI's own folders:
//
//	<config dir>/hooks.json              user (all projects)
//	<project>/.openuai/hooks.json        project (can be committed)
//	<project>/.openuai/hooks.local.json  project, not for git
//
// Each file holds {"hooks": {"PreToolUse": [{"matcher": "bash", "hooks":
// [{"type": "command", "command": "..."}]}]}} and optionally
// "disableAllHooks": true.
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Events OpenUAI fires (same names as Claude Code).
const (
	PreToolUse          = "PreToolUse"
	PostToolUse         = "PostToolUse"
	PostToolUseFailure  = "PostToolUseFailure"
	PostToolBatch       = "PostToolBatch"
	PermissionRequest   = "PermissionRequest"
	PermissionDenied    = "PermissionDenied"
	UserPromptSubmit    = "UserPromptSubmit"
	UserPromptExpansion = "UserPromptExpansion"
	Stop                = "Stop"
	StopFailure         = "StopFailure"
	SubagentStart       = "SubagentStart"
	SubagentStop        = "SubagentStop"
	SessionStart        = "SessionStart"
	SessionEnd          = "SessionEnd"
	Notification        = "Notification"
	PreCompact          = "PreCompact"
	PostCompact         = "PostCompact"
	CwdChanged          = "CwdChanged"
	FileChanged         = "FileChanged"
	ConfigChange        = "ConfigChange"
	PreModelSwitch      = "PreModelSwitch"
	PostModelSwitch     = "PostModelSwitch"
	WorktreeCreate      = "WorktreeCreate"
	WorktreeRemove      = "WorktreeRemove"
	InstructionsLoaded  = "InstructionsLoaded"
	MessageDisplay      = "MessageDisplay"
)

// Events lists every supported event.
var Events = []string{
	SessionStart, InstructionsLoaded, UserPromptSubmit, UserPromptExpansion, MessageDisplay,
	PreToolUse, PermissionRequest, PermissionDenied, PostToolUse, PostToolUseFailure, PostToolBatch,
	Notification, SubagentStart, SubagentStop, Stop, StopFailure, ConfigChange, CwdChanged,
	FileChanged, WorktreeCreate, WorktreeRemove, PreCompact, PostCompact, PreModelSwitch,
	PostModelSwitch, SessionEnd,
}

// Skipped lists Claude Code events that have no counterpart in OpenUAI
// (setup flags, agent teams and tasks, extra directories, MCP elicitation).
// They are accepted in the files without a warning, and never fire.
var Skipped = []string{"Setup", "TaskCreated", "TaskCompleted", "TeammateIdle", "DirectoryAdded", "Elicitation", "ElicitationResult"}

// ValidEvent reports whether e is an event OpenUAI fires.
func ValidEvent(e string) bool { return contains(Events, e) }

// SkippedEvent reports whether e is a Claude Code event OpenUAI ignores.
func SkippedEvent(e string) bool { return contains(Skipped, e) }

func contains(list []string, v string) bool {
	for _, k := range list {
		if k == v {
			return true
		}
	}
	return false
}

// Events without matcher support fire on every occurrence.
var noMatcher = map[string]bool{
	UserPromptSubmit: true, PostToolBatch: true, Stop: true, CwdChanged: true,
	WorktreeCreate: true, WorktreeRemove: true, MessageDisplay: true,
}

// Events whose exact-match matchers only accept letters, digits, _ and |.
var narrowMatcher = map[string]bool{FileChanged: true, StopFailure: true}

// Tool events: the only ones where `if` is evaluated.
var toolEvents = map[string]bool{
	PreToolUse: true, PostToolUse: true, PostToolUseFailure: true, PermissionRequest: true, PermissionDenied: true,
}

// Events that accept every handler type (prompt and agent included).
var allTypes = map[string]bool{
	PermissionDenied: true, PostToolBatch: true, PostToolUse: true, PostToolUseFailure: true,
	PreToolUse: true, Stop: true, SubagentStop: true, UserPromptExpansion: true, UserPromptSubmit: true,
}

// TypeSupported reports whether a handler type can run on event.
func TypeSupported(event, typ string) bool {
	switch typ {
	case "command", "mcp_tool":
		return true
	case "http":
		return event != SessionStart
	case "prompt":
		return allTypes[event] || event == PermissionRequest
	case "agent":
		return allTypes[event]
	}
	return false
}

// Handler is one hook handler: command, http, mcp_tool, prompt or agent.
type Handler struct {
	Type string `json:"type" yaml:"type"`

	// Common fields.
	If            string `json:"if,omitempty" yaml:"if"`
	Timeout       int    `json:"timeout,omitempty" yaml:"timeout"`
	StatusMessage string `json:"statusMessage,omitempty" yaml:"statusMessage"`
	Once          bool   `json:"once,omitempty" yaml:"once"`

	// command
	Command     string   `json:"command,omitempty" yaml:"command"`
	Args        []string `json:"args,omitempty" yaml:"args"`
	Shell       string   `json:"shell,omitempty" yaml:"shell"`
	Async       bool     `json:"async,omitempty" yaml:"async"`
	AsyncRewake bool     `json:"asyncRewake,omitempty" yaml:"asyncRewake"`
	OnFailure   string   `json:"onFailure,omitempty" yaml:"onFailure"`

	// http
	URL            string            `json:"url,omitempty" yaml:"url"`
	Headers        map[string]string `json:"headers,omitempty" yaml:"headers"`
	AllowedEnvVars []string          `json:"allowedEnvVars,omitempty" yaml:"allowedEnvVars"`

	// mcp_tool
	Server string         `json:"server,omitempty" yaml:"server"`
	Tool   string         `json:"tool,omitempty" yaml:"tool"`
	Input  map[string]any `json:"input,omitempty" yaml:"input"`

	// prompt / agent
	Prompt          string `json:"prompt,omitempty" yaml:"prompt"`
	Model           string `json:"model,omitempty" yaml:"model"`
	ContinueOnBlock bool   `json:"continueOnBlock,omitempty" yaml:"continueOnBlock"`
}

// Check reports what is wrong with h on event, or nil.
func (h Handler) Check(event string) error {
	switch h.Type {
	case "command":
		if strings.TrimSpace(h.Command) == "" {
			return errors.New("command hook without command")
		}
		if h.Shell != "" && h.Shell != "bash" && h.Shell != "powershell" {
			return fmt.Errorf("shell %q is not supported (bash or powershell)", h.Shell)
		}
	case "http":
		if !strings.HasPrefix(h.URL, "http://") && !strings.HasPrefix(h.URL, "https://") {
			return errors.New("http hook needs an http:// or https:// url")
		}
	case "mcp_tool":
		if h.Server == "" || h.Tool == "" {
			return errors.New("mcp_tool hook needs server and tool")
		}
	case "prompt", "agent":
		if strings.TrimSpace(h.Prompt) == "" {
			return fmt.Errorf("%s hook without prompt", h.Type)
		}
	case "":
		return errors.New("hook without type")
	default:
		return fmt.Errorf("hook type %q is not supported (command, http, mcp_tool, prompt, agent)", h.Type)
	}
	if h.OnFailure != "" && h.OnFailure != "continue" && h.OnFailure != "block" {
		return fmt.Errorf("onFailure %q is not supported (continue or block)", h.OnFailure)
	}
	if !TypeSupported(event, h.Type) {
		return fmt.Errorf("%s hooks cannot run on %s", h.Type, event)
	}
	return nil
}

// Summary is a one-line description of what h runs.
func (h Handler) Summary() string {
	switch h.Type {
	case "command":
		if h.Args != nil {
			return strings.TrimSpace(h.Command + " " + strings.Join(h.Args, " "))
		}
		return h.Command
	case "http":
		return "POST " + h.URL
	case "mcp_tool":
		return h.Server + "/" + h.Tool
	default:
		return h.Prompt
	}
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
	Event   string  `json:"event"`
	Matcher string  `json:"matcher"`
	Type    string  `json:"type"`
	Command string  `json:"command"` // what it runs (Handler.Summary)
	Timeout int     `json:"timeout,omitempty"`
	Source  string  `json:"source"` // user, project, local, agent:<name>, session...
	Once    bool    `json:"once,omitempty"`
	H       Handler `json:"-"`

	id     uint64 // session hooks: identity, for once removal
	remove func()
}

// key identifies a handler for deduplication: the same handler defined in
// several files runs once.
func (e Entry) key() string {
	b, _ := json.Marshal(e.H)
	k := e.Event + "\x00" + e.Matcher + "\x00" + string(b)
	if e.id != 0 {
		k += fmt.Sprintf("\x00%d", e.id)
	}
	return k
}

// Set is the active hooks.
type Set struct {
	Entries  []Entry  `json:"entries"`
	Warnings []string `json:"warnings,omitempty"`
	// Disabled is set when "disableAllHooks": true wins (see Load): no hook
	// runs at all.
	Disabled bool `json:"disabled,omitempty"`
}

type file struct {
	Hooks           Config `json:"hooks"`
	DisableAllHooks *bool  `json:"disableAllHooks"`
}

// Files returns the hook files in precedence order (lowest first) with their
// source names.
func Files(configDir, workDir string) [][2]string {
	var out [][2]string
	if configDir != "" {
		out = append(out, [2]string{filepath.Join(configDir, "hooks.json"), "user"})
	}
	if workDir != "" {
		out = append(out, [2]string{filepath.Join(workDir, ".openuai", "hooks.json"), "project"},
			[2]string{filepath.Join(workDir, ".openuai", "hooks.local.json"), "local"})
	}
	return out
}

// Load reads the user and project hook files. Missing files are fine;
// unreadable ones and invalid entries become warnings and are skipped.
//
// disableAllHooks follows settings precedence (local > project > user): the
// value of the most specific file that sets it decides, so a false in the
// project overrides a true in the user file.
func Load(configDir, workDir string) *Set {
	return loadWith(configDir, workDir, os.ReadFile)
}

func loadWith(configDir, workDir string, read func(string) ([]byte, error)) *Set {
	s := &Set{}
	disabled := false
	for _, f := range Files(configDir, workDir) {
		data, err := read(f[0])
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %v", f[0], err))
			continue
		}
		if d := s.addFile(f[0], f[1], data); d != nil {
			disabled = *d
		}
	}
	if disabled {
		s.Disabled = true
		s.Entries = nil
	}
	return s
}

func (s *Set) addFile(path, source string, data []byte) *bool {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%s: invalid JSON: %v", path, err))
		return nil
	}
	s.Add(f.Hooks, source)
	return f.DisableAllHooks
}

// Add adds the hooks of cfg with the given source. Identical handlers
// (same event, matcher and handler) are kept once.
func (s *Set) Add(cfg Config, source string) {
	s.add(cfg, source, nil)
}

func (s *Set) add(cfg Config, source string, wrap func(*Entry)) {
	events := make([]string, 0, len(cfg))
	for e := range cfg {
		events = append(events, e)
	}
	sort.Strings(events)
	for _, ev := range events {
		if SkippedEvent(ev) {
			continue // a Claude Code event OpenUAI has no counterpart for
		}
		if !ValidEvent(ev) {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: event %q is not supported (use %s)", source, ev, strings.Join(Events, ", ")))
			continue
		}
		for _, g := range cfg[ev] {
			if _, err := compileMatcher(ev, g.Matcher); err != nil {
				s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %s matcher %q: %v", source, ev, g.Matcher, err))
				continue
			}
			for _, h := range g.Hooks {
				if err := h.Check(ev); err != nil {
					s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %s: %v", source, ev, err))
					continue
				}
				if h.Type == "command" && h.Args != nil && !strings.ContainsAny(h.Command, `/\`) && strings.ContainsAny(h.Command, " \t") {
					s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %s: exec-form command %q contains spaces: move the extra words into args", source, ev, h.Command))
				}
				e := Entry{Event: ev, Matcher: g.Matcher, Type: h.Type, Command: h.Summary(), Timeout: h.Timeout, Source: source, H: h}
				if wrap != nil {
					wrap(&e)
				}
				if !s.has(e) {
					s.Entries = append(s.Entries, e)
				}
			}
		}
	}
}

func (s *Set) has(e Entry) bool {
	k := e.key()
	for _, x := range s.Entries {
		if x.key() == k {
			return true
		}
	}
	return false
}

// Merge returns a new set with the entries of s and other. A disabled set
// disables the result.
func (s *Set) Merge(other *Set) *Set {
	out := &Set{}
	for _, x := range []*Set{s, other} {
		if x == nil {
			continue
		}
		out.Warnings = append(out.Warnings, x.Warnings...)
		out.Disabled = out.Disabled || x.Disabled
		for _, e := range x.Entries {
			if !out.has(e) {
				out.Entries = append(out.Entries, e)
			}
		}
	}
	if out.Disabled {
		out.Entries = nil
	}
	return out
}

// Has reports whether any hook is configured for event.
func (s *Set) Has(event string) bool {
	if s == nil || s.Disabled {
		return false
	}
	for _, e := range s.Entries {
		if e.Event == event {
			return true
		}
	}
	return false
}

// Matchers returns the matchers of the groups configured for event.
func (s *Set) Matchers(event string) []string {
	if s == nil || s.Disabled {
		return nil
	}
	var out []string
	for _, e := range s.Entries {
		if e.Event == event {
			out = append(out, e.Matcher)
		}
	}
	return out
}

var exactMatcher = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]*$`)
var narrowExact = regexp.MustCompile(`^[A-Za-z0-9_|]*$`)

// compileMatcher follows Claude Code's rules: empty or "*" matches all;
// only letters, digits, _ - space , | is a list of exact names (only
// letters, digits, _ and | for FileChanged and StopFailure); anything else
// is an unanchored regular expression.
func compileMatcher(event, m string) (func(string) bool, error) {
	m = strings.TrimSpace(m)
	if m == "" || m == "*" {
		return func(string) bool { return true }, nil
	}
	exact, sep := exactMatcher, func(r rune) bool { return r == '|' || r == ',' }
	if narrowMatcher[event] {
		exact, sep = narrowExact, func(r rune) bool { return r == '|' }
	}
	if exact.MatchString(m) {
		names := map[string]bool{}
		for _, p := range strings.FieldsFunc(m, sep) {
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

// SessionHooks are hooks registered while the app runs (for example by a
// skill when it is invoked), on top of the hook files. Handlers with
// once: true are removed after their first successful run.
type SessionHooks struct {
	mu      sync.Mutex
	set     Set
	counter uint64
}

// Register adds the hooks of cfg under source (e.g. "skill:deploy"). It
// returns the warnings for invalid entries. Every registration is kept
// separate, even when identical to a handler from a file.
func (r *SessionHooks) Register(cfg Config, source string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	before := len(r.set.Warnings)
	r.set.add(cfg, source, func(e *Entry) {
		r.counter++
		id := r.counter
		e.id = id
		e.Once = e.H.Once
		if e.H.Once {
			e.remove = func() { r.removeID(id) }
		}
	})
	return append([]string(nil), r.set.Warnings[before:]...)
}

func (r *SessionHooks) removeID(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.set.Entries {
		if e.id == id {
			r.set.Entries = append(r.set.Entries[:i:i], r.set.Entries[i+1:]...)
			return
		}
	}
}

// Set returns a snapshot of the registered hooks.
func (r *SessionHooks) Set() *Set {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return &Set{Entries: append([]Entry(nil), r.set.Entries...), Warnings: append([]string(nil), r.set.Warnings...)}
}

// Clear removes every registered hook (the session ended).
func (r *SessionHooks) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set = Set{}
}

// Loader loads the hook files like Load, and fires ConfigChange when one of
// them changes: a blocking ConfigChange hook keeps the previous contents of
// that file in effect.
type Loader struct {
	mu       sync.Mutex
	accepted map[string][]byte // path -> contents in effect (nil: missing)
	seen     map[string]bool
	rejected map[string]string // path -> contents a ConfigChange hook blocked
	last     *Set
	// OnChange is called with the hooks in effect before the change; it
	// returns true to block the change.
	OnChange func(prev *Set, source, path string) bool
}

// ConfigSource is the ConfigChange source of a hook file source.
func ConfigSource(source string) string {
	switch source {
	case "user":
		return "user_settings"
	case "project":
		return "project_settings"
	case "local":
		return "local_settings"
	}
	return source
}

// Load reads the hook files, firing ConfigChange for the ones that changed
// since the previous call.
func (l *Loader) Load(configDir, workDir string) *Set {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.accepted == nil {
		l.accepted, l.seen, l.rejected = map[string][]byte{}, map[string]bool{}, map[string]string{}
	}
	contents := map[string][]byte{}
	for _, f := range Files(configDir, workDir) {
		path, source := f[0], f[1]
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			continue // reported as a warning by loadWith
		}
		if err != nil {
			data = nil
		}
		if l.seen[path] && string(data) != string(l.accepted[path]) && l.OnChange != nil && l.last != nil {
			if r, ok := l.rejected[path]; (ok && r == string(data)) || l.OnChange(l.last, ConfigSource(source), path) {
				l.rejected[path] = string(data)
				contents[path] = l.accepted[path] // blocked: keep the old contents
				continue
			}
		}
		l.seen[path] = true
		l.accepted[path] = data
		contents[path] = data
	}
	s := loadWith(configDir, workDir, func(p string) ([]byte, error) {
		d, ok := contents[p]
		if !ok {
			return os.ReadFile(p)
		}
		if d == nil {
			return nil, os.ErrNotExist
		}
		return d, nil
	})
	l.last = s
	return s
}
