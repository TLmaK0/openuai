package rules

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"openuai/internal/eventbus"
)

func writeRulesFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEngineLoadAndMatch(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "test.yaml", `
rules:
  - id: greet
    name: Greeting rule
    enabled: true
    trigger:
      source: whatsapp
      type: message
      keyword: hello
    actions:
      - type: reply
        template: "Hi {{.Sender}}!"
`)

	var executed atomic.Int64
	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		executed.Add(1)
		if rendered.Text != "Hi Alice!" {
			t.Errorf("expected 'Hi Alice!', got %q", rendered.Text)
		}
		return nil
	})

	if err := engine.Load(); err != nil {
		t.Fatal(err)
	}

	rules := engine.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ID != "greet" {
		t.Errorf("expected rule ID 'greet', got %q", rules[0].ID)
	}

	// Matching event
	engine.HandleEvent(eventbus.Event{
		Source:    "whatsapp",
		Type:      eventbus.EventMessage,
		Payload:   "Hello world",
		Metadata:  map[string]string{"sender": "Alice"},
		Timestamp: time.Now(),
	})

	if got := executed.Load(); got != 1 {
		t.Errorf("expected 1 execution, got %d", got)
	}

	// Non-matching event (wrong source)
	engine.HandleEvent(eventbus.Event{
		Source:  "telegram",
		Type:    eventbus.EventMessage,
		Payload: "Hello",
	})

	if got := executed.Load(); got != 1 {
		t.Errorf("expected still 1 execution, got %d", got)
	}
}

func TestEngineRegexMatch(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "regex.yaml", `
rules:
  - id: order
    name: Order detection
    enabled: true
    trigger:
      source: "*"
      type: message
      regex: "order\\s+#\\d+"
    actions:
      - type: notify
        template: "New order detected: {{.Payload}}"
`)

	var executed atomic.Int64
	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		executed.Add(1)
		return nil
	})

	if err := engine.Load(); err != nil {
		t.Fatal(err)
	}

	// Match
	engine.HandleEvent(eventbus.Event{
		Source:  "email",
		Type:    eventbus.EventMessage,
		Payload: "New order #1234 received",
	})
	if got := executed.Load(); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}

	// No match
	engine.HandleEvent(eventbus.Event{
		Source:  "email",
		Type:    eventbus.EventMessage,
		Payload: "Just a normal message",
	})
	if got := executed.Load(); got != 1 {
		t.Errorf("expected still 1, got %d", got)
	}
}

func TestEngineSenderFilter(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "sender.yaml", `
rules:
  - id: vip
    name: VIP sender
    enabled: true
    trigger:
      source: "*"
      type: "*"
      sender: boss@company.com
    actions:
      - type: notify
        template: "VIP message: {{.Payload}}"
`)

	var executed atomic.Int64
	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		executed.Add(1)
		return nil
	})
	engine.Load()

	// Match
	engine.HandleEvent(eventbus.Event{
		Source:   "email",
		Type:     eventbus.EventEmail,
		Payload:  "Important stuff",
		Metadata: map[string]string{"sender": "boss@company.com"},
	})
	if got := executed.Load(); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}

	// No match (different sender)
	engine.HandleEvent(eventbus.Event{
		Source:   "email",
		Type:     eventbus.EventEmail,
		Payload:  "Not important",
		Metadata: map[string]string{"sender": "random@example.com"},
	})
	if got := executed.Load(); got != 1 {
		t.Errorf("expected still 1, got %d", got)
	}
}

func TestEngineDisabledRule(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "disabled.yaml", `
rules:
  - id: off
    name: Disabled rule
    enabled: false
    trigger:
      source: "*"
      type: "*"
    actions:
      - type: notify
        template: "Should not fire"
`)

	var executed atomic.Int64
	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		executed.Add(1)
		return nil
	})
	engine.Load()

	engine.HandleEvent(eventbus.Event{Source: "any", Type: "any", Payload: "anything"})

	if got := executed.Load(); got != 0 {
		t.Errorf("disabled rule fired: got %d executions", got)
	}
}

func TestEngineMultipleActions(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "multi.yaml", `
rules:
  - id: multi
    name: Multiple actions
    enabled: true
    trigger:
      source: "*"
      type: "*"
    actions:
      - type: reply
        template: "Got it"
      - type: bash
        command: "echo {{.Payload}}"
      - type: write_file
        path: "/tmp/event-{{.Source}}.txt"
        template: "{{.Payload}}"
`)

	var executed atomic.Int64
	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		executed.Add(1)
		return nil
	})
	engine.Load()

	engine.HandleEvent(eventbus.Event{Source: "test", Type: "message", Payload: "hello"})

	if got := executed.Load(); got != 3 {
		t.Errorf("expected 3 actions, got %d", got)
	}
}

func TestEngineReload(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "rules.yaml", `
rules:
  - id: r1
    name: Rule 1
    enabled: true
    trigger:
      source: "*"
      type: "*"
    actions:
      - type: notify
        template: "v1"
`)

	engine := New(dir, nil)
	engine.Load()

	if len(engine.Rules()) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(engine.Rules()))
	}

	// Update file with a second rule
	writeRulesFile(t, dir, "rules.yaml", `
rules:
  - id: r1
    name: Rule 1
    enabled: true
    trigger:
      source: "*"
      type: "*"
    actions:
      - type: notify
        template: "v2"
  - id: r2
    name: Rule 2
    enabled: true
    trigger:
      source: whatsapp
      type: message
    actions:
      - type: reply
        template: "auto-reply"
`)

	engine.Reload()

	if len(engine.Rules()) != 2 {
		t.Fatalf("expected 2 rules after reload, got %d", len(engine.Rules()))
	}
}

func TestEngineMetadataFilter(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "meta.yaml", `
rules:
  - id: group
    name: Group filter
    enabled: true
    trigger:
      source: whatsapp
      type: message
      metadata:
        group: "work-team"
    actions:
      - type: reply
        template: "Work message: {{.Payload}}"
`)

	var executed atomic.Int64
	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		executed.Add(1)
		return nil
	})
	engine.Load()

	// Match
	engine.HandleEvent(eventbus.Event{
		Source:   "whatsapp",
		Type:     eventbus.EventMessage,
		Payload:  "hi team",
		Metadata: map[string]string{"group": "work-team"},
	})
	if got := executed.Load(); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}

	// No match (wrong group)
	engine.HandleEvent(eventbus.Event{
		Source:   "whatsapp",
		Type:     eventbus.EventMessage,
		Payload:  "hi friends",
		Metadata: map[string]string{"group": "friends"},
	})
	if got := executed.Load(); got != 1 {
		t.Errorf("expected still 1, got %d", got)
	}
}

func TestEngineStats(t *testing.T) {
	dir := t.TempDir()
	writeRulesFile(t, dir, "stats.yaml", `
rules:
  - id: s1
    name: Stats rule
    enabled: true
    trigger:
      source: "*"
      type: "*"
    actions:
      - type: notify
        template: "ok"
`)

	engine := New(dir, func(rule Rule, action Action, event eventbus.Event, rendered RenderedAction) error {
		return nil
	})
	engine.Load()

	engine.HandleEvent(eventbus.Event{Source: "a", Type: "message", Payload: "1"})
	engine.HandleEvent(eventbus.Event{Source: "b", Type: "email", Payload: "2"})

	matched, run, errors := engine.Stats()
	if matched != 2 || run != 2 || errors != 0 {
		t.Errorf("stats: matched=%d run=%d errors=%d", matched, run, errors)
	}
}
