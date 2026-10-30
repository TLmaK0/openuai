package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"openuai/internal/agent"
	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

type stubTool struct{ name string }

func (t stubTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t stubTool) Execute(context.Context, map[string]string) tools.Result {
	return tools.Result{}
}

// mainAgentApp is an app with its config in a temp dir, a working directory
// of its own (no project settings) and the user agents given.
func mainAgentApp(t *testing.T, agents map[string]string) *App {
	t.Helper()
	cfgDir := t.TempDir()
	t.Chdir(t.TempDir())
	prev := initialPromptDelay
	initialPromptDelay = time.Hour // tests send it themselves
	t.Cleanup(func() { initialPromptDelay = prev })
	if err := os.MkdirAll(filepath.Join(cfgDir, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range agents {
		if err := os.WriteFile(filepath.Join(cfgDir, "agents", name+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.SetPath(filepath.Join(cfgDir, "config.json"))
	cfg.SetProviderAndModel("p", "m")
	a := appWith(map[string]llm.Provider{"p": inTreeProvider{name: "p"}})
	a.cfg = cfg
	a.registry = tools.NewRegistry()
	for _, n := range []string{"read_file", "write_file", "list_dir", "bash", "spawn_agents", "watch_chat"} {
		a.registry.Register(stubTool{n})
	}
	return a
}

const readerAgent = `---
name: reader
description: Reads, never writes
tools: read_file, list_dir
permissionMode: plan
effort: low
---
You are a read-only reviewer.`

const greeterAgent = `---
name: greeter
tools: read_file
initialPrompt: Say hello and list the files.
---
You greet.`

const coordAgent = `---
name: coord
tools: Agent(reader), read_file
---
You coordinate.`

func toolNamesOf(r *tools.Registry) []string {
	var out []string
	for _, d := range r.Definitions() {
		out = append(out, d.Name)
	}
	return out
}

func TestMainAgentSetupUsesTheDefinition(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"reader": readerAgent})
	s, err := a.buildMainAgentSetup("reader")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolNamesOf(s.registry), []string{"list_dir", "read_file"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v, want %v (no write_file)", got, want)
	}
	if s.permissionMode != agent.PermModePlan || s.effort != "low" || s.model != "m" {
		t.Fatalf("mode=%q effort=%q model=%q", s.permissionMode, s.effort, s.model)
	}
	if s.systemPrompt != "You are a read-only reviewer." {
		t.Fatalf("system prompt = %q", s.systemPrompt)
	}
}

func TestMainAgentFromUserDefault(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"reader": readerAgent})
	a.cfg.SetAgent("reader")
	ag := a.ensureAgent()
	if !strings.HasPrefix(ag.SystemPrompt(), "You are a read-only reviewer.") {
		t.Fatalf("system prompt does not come from the agent: %q", ag.SystemPrompt()[:60])
	}
	info := a.GetMainAgent()
	if info.Name != "reader" || info.Source != mainAgentSourceUser || info.Error != "" {
		t.Fatalf("info = %+v", info)
	}
}

func TestUnknownMainAgentFallsBackWithAnError(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"reader": readerAgent})
	if msg := a.SetMainAgent("nope"); !strings.Contains(msg, `unknown agent "nope"`) || !strings.Contains(msg, "reader") {
		t.Fatalf("SetMainAgent error = %q", msg)
	}
	if info := a.GetMainAgent(); info.Name != "" || info.Session {
		t.Fatalf("an unknown agent must not be selected: %+v", info)
	}
	// A default naming a missing agent: normal assistant and a clear error.
	a.cfg.SetAgent("gone")
	a.currentAgent = nil
	ag := a.ensureAgent()
	if !strings.HasPrefix(ag.SystemPrompt(), agent.DefaultSystemPrompt()) {
		t.Fatal("should run as the normal assistant")
	}
	info := a.GetMainAgent()
	if info.Name != "" || info.Wanted != "gone" || !strings.Contains(info.Error, `unknown agent "gone"`) {
		t.Fatalf("info = %+v", info)
	}
}

func TestBrokenMainAgentFileIsReported(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"bad": "---\nname: bad\neffort: extreme\n---\n"})
	if msg := a.SetMainAgent("bad"); !strings.Contains(msg, `agent "bad" cannot be loaded`) || !strings.Contains(msg, "invalid effort") {
		t.Fatalf("error = %q", msg)
	}
}

func TestSwitchingKeepsTheConversationAndSwitchesBack(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"reader": readerAgent})
	ag := a.ensureAgent()
	ag.Begin("hello")
	if msg := a.SetMainAgent("reader"); msg != "" {
		t.Fatal(msg)
	}
	ag2 := a.ensureAgent()
	if ag2 == ag || !hasUserMessages(ag2.Messages()) || !strings.HasPrefix(ag2.SystemPrompt(), "You are a read-only") {
		t.Fatal("switching must rebuild the agent and keep the conversation")
	}
	// The choice is saved with the session: a restart runs as reader again.
	if c := agent.PeekMainAgentChoice(a.cfg.ConfigDir(), "current"); c == nil || *c != "reader" {
		t.Fatalf("saved choice = %v", c)
	}
	b := appWith(map[string]llm.Provider{"p": inTreeProvider{name: "p"}})
	b.cfg, b.registry = a.cfg, a.registry
	if got := b.ensureAgent().SystemPrompt(); !strings.HasPrefix(got, "You are a read-only") {
		t.Fatalf("after restart: %q", got[:40])
	}
	if msg := a.SetMainAgent(""); msg != "" {
		t.Fatal(msg)
	}
	if got := a.ensureAgent().SystemPrompt(); !strings.HasPrefix(got, agent.DefaultSystemPrompt()) {
		t.Fatal("switching back must restore the normal assistant")
	}
}

func TestMainAgentDefaultSavedPerUserAndProject(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"reader": readerAgent, "greeter": greeterAgent})
	if msg := a.SetMainAgentDefault("reader", "user"); msg != "" {
		t.Fatal(msg)
	}
	data, _ := os.ReadFile(a.cfg.Path())
	if !strings.Contains(string(data), `"agent": "reader"`) {
		t.Fatalf("config.json: %s", data)
	}
	if info := a.GetMainAgent(); info.Name != "reader" || info.Source != mainAgentSourceUser {
		t.Fatalf("info = %+v", info)
	}
	if msg := a.SetMainAgentDefault("greeter", "project"); msg != "" {
		t.Fatal(msg)
	}
	if info := a.GetMainAgent(); info.Name != "greeter" || info.Source != mainAgentSourceProject || !strings.HasSuffix(info.ProjectFile, filepath.Join(".openuai", "settings.json")) {
		t.Fatalf("project default should win: %+v", info)
	}
	if msg := a.SetMainAgentDefault("x", "user"); !strings.Contains(msg, "unknown agent") {
		t.Fatalf("unknown default: %q", msg)
	}
}

func TestInitialPromptIsPrependedOnce(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"greeter": greeterAgent, "reader": readerAgent})
	a.cfg.SetAgent("greeter")
	a.agentMu.Lock()
	a.armInitialPromptLocked(a.ensureAgent())
	a.agentMu.Unlock()
	if got := a.GetMainAgent().InitialPending; got != "Say hello and list the files." {
		t.Fatalf("pending = %q", got)
	}
	if got := a.withInitialPrompt("and then stop"); got != "Say hello and list the files.\n\nand then stop" {
		t.Fatalf("first message = %q", got)
	}
	if got := a.withInitialPrompt("again"); got != "again" {
		t.Fatalf("second message = %q", got)
	}
	if r := a.RunInitialPrompt(); !r.Refused {
		t.Fatal("nothing left to send")
	}
	// A conversation that already has messages does not get it.
	ag := a.ensureAgent()
	ag.Begin("hi")
	a.agentMu.Lock()
	a.armInitialPromptLocked(ag)
	a.agentMu.Unlock()
	if got := a.GetMainAgent().InitialPending; got != "" {
		t.Fatalf("pending on a started conversation = %q", got)
	}
	// An agent without initialPrompt arms nothing.
	b := mainAgentApp(t, map[string]string{"reader": readerAgent})
	if msg := b.SetMainAgent("reader"); msg != "" {
		t.Fatal(msg)
	}
	if got := b.GetMainAgent().InitialPending; got != "" {
		t.Fatalf("pending = %q", got)
	}
}

func TestRestrictedSpawnRefusesOtherTypes(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"coord": coordAgent, "reader": readerAgent})
	s, err := a.buildMainAgentSetup("coord")
	if err != nil {
		t.Fatal(err)
	}
	if !s.spawnLimited || !reflect.DeepEqual(s.spawnTypes, []string{"reader"}) {
		t.Fatalf("spawn types = %v %v", s.spawnTypes, s.spawnLimited)
	}
	sp, ok := s.registry.Get("spawn_agents")
	if !ok {
		t.Fatal("coord should have spawn_agents")
	}
	if d := sp.Definition().Description; !strings.Contains(d, "Available agents:\n- reader:") || strings.Contains(d, "- general-purpose:") || !strings.Contains(d, "Only the agents listed here") {
		t.Fatalf("description should list only reader:\n%s", d)
	}
	res := sp.Execute(context.Background(), map[string]string{"tasks": `[{"id":"w","description":"write","agent":"general-purpose"},{"id":"x","description":"x"}]`})
	if strings.Count(res.Output, "may not launch") != 2 {
		t.Fatalf("both tasks should be refused:\n%s", res.Output)
	}
}

// claude-headless keys its CLI session on the system prompt and the tools:
// building the same agent again must give exactly the same ones, so the
// session is reused when nothing changed.
func TestMainAgentRebuildIsStable(t *testing.T) {
	a := mainAgentApp(t, map[string]string{"reader": readerAgent})
	a.cfg.SetAgent("reader")
	first := a.ensureAgent()
	a.currentAgent = nil
	second := a.ensureAgent()
	if first.SystemPrompt() != second.SystemPrompt() {
		t.Fatal("system prompt changed between identical builds")
	}
	s1, _ := a.buildMainAgentSetup("reader")
	s2, _ := a.buildMainAgentSetup("reader")
	if !reflect.DeepEqual(s1.registry.Definitions(), s2.registry.Definitions()) || s1.systemPrompt != s2.systemPrompt {
		t.Fatal("tools or prompt changed between identical builds")
	}
}
