package rules

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"

	"openuai/internal/eventbus"
	"openuai/internal/logger"

	"gopkg.in/yaml.v3"
)

// ActionExecutor is called when a rule action needs to run.
// The engine itself doesn't execute actions — it delegates to a callback.
type ActionExecutor func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error

// RenderedAction contains the action with templates already resolved.
type RenderedAction struct {
	Text    string // rendered template/prompt
	Path    string // rendered file path
	Command string // rendered bash command
	URL     string // rendered webhook URL
}

// Engine loads rules, matches events, and triggers actions.
type Engine struct {
	mu       sync.RWMutex
	rules    []Rule
	compiled map[string]*regexp.Regexp // cached compiled regexes by rule ID
	dir      string                    // directory to watch for YAML files
	executor ActionExecutor

	// stats
	statsMu      sync.Mutex
	RulesMatched int64
	ActionsRun   int64
	Errors       int64
}

// New creates a rules engine that loads from the given directory.
func New(rulesDir string, executor ActionExecutor) *Engine {
	return &Engine{
		compiled: make(map[string]*regexp.Regexp),
		dir:      rulesDir,
		executor: executor,
	}
}

// Load reads all YAML files from the rules directory.
func (e *Engine) Load() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := os.MkdirAll(e.dir, 0755); err != nil {
		return fmt.Errorf("create rules dir: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(e.dir, "*.yaml"))
	if err != nil {
		return fmt.Errorf("glob rules: %w", err)
	}
	ymlFiles, _ := filepath.Glob(filepath.Join(e.dir, "*.yml"))
	files = append(files, ymlFiles...)

	var allRules []Rule
	compiled := make(map[string]*regexp.Regexp)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			logger.Error("Rules: failed to read %s: %s", file, err.Error())
			continue
		}

		var rf RulesFile
		if err := yaml.Unmarshal(data, &rf); err != nil {
			logger.Error("Rules: failed to parse %s: %s", file, err.Error())
			continue
		}

		for _, r := range rf.Rules {
			if r.Trigger.Regex != "" {
				re, err := regexp.Compile(r.Trigger.Regex)
				if err != nil {
					logger.Error("Rules: invalid regex in rule %q: %s", r.ID, err.Error())
					continue
				}
				compiled[r.ID] = re
			}
			allRules = append(allRules, r)
		}

		logger.Info("Rules: loaded %d rules from %s", len(rf.Rules), filepath.Base(file))
	}

	e.rules = allRules
	e.compiled = compiled
	logger.Info("Rules: total %d rules loaded", len(allRules))
	return nil
}

// Reload hot-reloads rules from disk.
func (e *Engine) Reload() error {
	logger.Info("Rules: reloading...")
	return e.Load()
}

// HandleEvent is the event bus handler — checks all rules against the event.
func (e *Engine) HandleEvent(event eventbus.Event) error {
	e.mu.RLock()
	rules := e.rules
	compiled := e.compiled
	e.mu.RUnlock()

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}

		if !e.matches(rule, compiled, event) {
			continue
		}

		e.statsMu.Lock()
		e.RulesMatched++
		e.statsMu.Unlock()

		logger.Info("Rules: rule %q matched event %s/%s", rule.ID, event.Source, event.Type)

		for _, action := range rule.Actions {
			rendered, err := e.render(action, event)
			if err != nil {
				logger.Error("Rules: render error in rule %q action %s: %s", rule.ID, action.Type, err.Error())
				e.statsMu.Lock()
				e.Errors++
				e.statsMu.Unlock()
				continue
			}

			if e.executor != nil {
				if err := e.executor(rule, action, event, rendered); err != nil {
					logger.Error("Rules: action %s failed in rule %q: %s", action.Type, rule.ID, err.Error())
					e.statsMu.Lock()
					e.Errors++
					e.statsMu.Unlock()
				} else {
					e.statsMu.Lock()
					e.ActionsRun++
					e.statsMu.Unlock()
				}
			}
		}
	}

	return nil
}

// Rules returns a copy of all loaded rules.
func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// Stats returns engine statistics.
func (e *Engine) Stats() (matched, run, errors int64) {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return e.RulesMatched, e.ActionsRun, e.Errors
}

// matches checks if a rule's trigger matches an event.
func (e *Engine) matches(rule Rule, compiled map[string]*regexp.Regexp, event eventbus.Event) bool {
	t := rule.Trigger

	// Source filter
	if t.Source != "" && t.Source != "*" && t.Source != event.Source {
		return false
	}

	// Type filter
	if t.Type != "" && t.Type != "*" && t.Type != string(event.Type) {
		return false
	}

	// Sender filter
	if t.Sender != "" {
		if sender, ok := event.Metadata["sender"]; !ok || sender != t.Sender {
			return false
		}
	}

	// Keyword filter (case-insensitive substring)
	if t.Keyword != "" {
		if !strings.Contains(strings.ToLower(event.Payload), strings.ToLower(t.Keyword)) {
			return false
		}
	}

	// Regex filter
	if t.Regex != "" {
		re, ok := compiled[rule.ID]
		if !ok {
			return false
		}
		if !re.MatchString(event.Payload) {
			return false
		}
	}

	// Metadata filter
	for key, val := range t.Metadata {
		if event.Metadata[key] != val {
			return false
		}
	}

	return true
}

// templateContext is passed to Go templates when rendering actions.
type templateContext struct {
	Event   eventbus.Event
	Payload string
	Source  string
	Sender  string
}

// render resolves Go templates in an action using event data.
func (e *Engine) render(action Action, event eventbus.Event) (RenderedAction, error) {
	ctx := templateContext{
		Event:   event,
		Payload: event.Payload,
		Source:  event.Source,
		Sender:  event.Metadata["sender"],
	}

	var rendered RenderedAction
	var err error

	if action.Template != "" {
		rendered.Text, err = execTemplate(action.Template, ctx)
		if err != nil {
			return rendered, err
		}
	}
	if action.Prompt != "" {
		rendered.Text, err = execTemplate(action.Prompt, ctx)
		if err != nil {
			return rendered, err
		}
	}
	if action.Path != "" {
		rendered.Path, err = execTemplate(action.Path, ctx)
		if err != nil {
			return rendered, err
		}
	}
	if action.Command != "" {
		rendered.Command, err = execTemplate(action.Command, ctx)
		if err != nil {
			return rendered, err
		}
	}
	if action.URL != "" {
		rendered.URL, err = execTemplate(action.URL, ctx)
		if err != nil {
			return rendered, err
		}
	}

	return rendered, nil
}

func execTemplate(tmpl string, data interface{}) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("exec template: %w", err)
	}
	return buf.String(), nil
}
