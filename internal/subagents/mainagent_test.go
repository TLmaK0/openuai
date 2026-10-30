package subagents

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"openuai/internal/hooks"
	"openuai/internal/tools"
)

type namedTool struct{ name string }

func (t namedTool) Definition() tools.Definition { return tools.Definition{Name: t.name} }
func (t namedTool) Execute(context.Context, map[string]string) tools.Result {
	return tools.Result{}
}

func registryOf(names ...string) *tools.Registry {
	r := tools.NewRegistry()
	for _, n := range names {
		r.Register(namedTool{n})
	}
	return r
}

func toolNames(r *tools.Registry) []string {
	var out []string
	for _, d := range r.Definitions() {
		out = append(out, d.Name)
	}
	return out
}

func TestSplitListKeepsParentheses(t *testing.T) {
	got := splitList("Agent(worker, researcher), read_file ,bash")
	want := []string{"Agent(worker, researcher)", "read_file", "bash"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitList = %q, want %q", got, want)
	}
}

func TestParseInitialPromptAndMainHooks(t *testing.T) {
	d, err := Parse([]byte(`---
name: coord
initialPrompt: "  Review the open PRs  "
hooks:
  Stop:
    - hooks:
        - type: command
          command: echo done
---
Body`))
	if err != nil {
		t.Fatal(err)
	}
	if d.InitialPrompt != "Review the open PRs" {
		t.Fatalf("InitialPrompt = %q", d.InitialPrompt)
	}
	if _, ok := d.Hooks[hooks.SubagentStop]; !ok {
		t.Fatalf("sub-agent hooks should turn Stop into SubagentStop: %v", d.Hooks)
	}
	if _, ok := d.MainHooks[hooks.Stop]; !ok {
		t.Fatalf("main-conversation hooks should keep Stop: %v", d.MainHooks)
	}
	if s := MainHookSet(d); !s.Has(hooks.Stop) {
		t.Fatal("MainHookSet should have the Stop hook")
	}
}

func TestSpawnAllowlist(t *testing.T) {
	cases := []struct {
		tools      string
		want       []string
		restricted bool
	}{
		{"tools: Agent(worker, researcher), read_file", []string{"researcher", "worker"}, true},
		{"tools: spawn_agents(explore)", []string{"explore"}, true},
		{"tools: Task(a), Agent", nil, false},
		{"tools: read_file", nil, false},
		{"", nil, false},
	}
	for _, c := range cases {
		d, err := Parse([]byte("---\nname: x\n" + c.tools + "\n---\n"))
		if err != nil {
			t.Fatal(err)
		}
		got, restricted := SpawnAllowlist(d)
		if restricted != c.restricted || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %v %v, want %v %v", c.tools, got, restricted, c.want, c.restricted)
		}
	}
}

func TestFilterMainTools(t *testing.T) {
	reg := registryOf("read_file", "write_file", "spawn_agents", "watch_chat", "bash")
	d, _ := Parse([]byte("---\nname: x\ntools: Agent(worker), read_file, watch_chat\n---\n"))
	got, err := FilterMainTools(reg, d)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"read_file", "spawn_agents", "watch_chat"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Fatalf("main tools = %v, want %v", toolNames(got), want)
	}
	// As a sub-agent, Agent(...) also means spawn_agents; watch_chat stays out.
	sub, err := FilterTools(reg, d)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"read_file", "spawn_agents"}; !reflect.DeepEqual(toolNames(sub), want) {
		t.Fatalf("sub-agent tools = %v, want %v", toolNames(sub), want)
	}
	// Without Agent in tools, no spawn_agents.
	ro, _ := Parse([]byte("---\nname: ro\ntools: read_file, list_dir\n---\n"))
	got, err = FilterMainTools(registryOf("read_file", "list_dir", "write_file", "spawn_agents"), ro)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"list_dir", "read_file"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Fatalf("read-only tools = %v, want %v", toolNames(got), want)
	}
}

func TestProjectAgentSetting(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if n, p, err := ProjectAgentSetting(sub); n != "" || p != "" || err != nil {
		t.Fatalf("no settings: got %q %q %v", n, p, err)
	}
	write := func(dir, file, content string) {
		t.Helper()
		p := filepath.Join(dir, file)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(root, ProjectSettingsFile, `{"agent": "root-agent", "other": 1}`)
	if n, _, _ := ProjectAgentSetting(sub); n != "root-agent" {
		t.Fatalf("root setting: got %q", n)
	}
	write(root, ProjectLocalSettingsFile, `{"agent": "local-agent"}`)
	if n, _, _ := ProjectAgentSetting(sub); n != "local-agent" {
		t.Fatalf("local wins over shared: got %q", n)
	}
	write(sub, ProjectSettingsFile, `{"agent": ""}`)
	if n, p, _ := ProjectAgentSetting(sub); n != "" || p != filepath.Join(sub, ProjectSettingsFile) {
		t.Fatalf("closest wins, empty = normal assistant: got %q %q", n, p)
	}
	write(sub, ProjectSettingsFile, `{broken`)
	if _, _, err := ProjectAgentSetting(sub); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("broken file: err = %v", err)
	}
}

func TestSaveProjectAgentSettingKeepsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ProjectSettingsFile)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(`{"other": {"x": 1}}`), 0o644)
	if _, err := SaveProjectAgentSetting(dir, "reviewer"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `"agent": "reviewer"`) || !strings.Contains(string(data), `"other"`) {
		t.Fatalf("saved: %s", data)
	}
	if n, _, _ := ProjectAgentSetting(dir); n != "reviewer" {
		t.Fatalf("read back %q", n)
	}
	if _, err := SaveProjectAgentSetting(dir, ""); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	if strings.Contains(string(data), `"agent"`) || !strings.Contains(string(data), `"other"`) {
		t.Fatalf("after removal: %s", data)
	}
}

func TestMainAgentPrompt(t *testing.T) {
	if got := MainAgentPrompt(&Definition{Prompt: "Be a reviewer."}, "default"); got != "Be a reviewer." {
		t.Fatalf("body replaces the default prompt: %q", got)
	}
	if got := MainAgentPrompt(&Definition{}, "default"); got != "default" {
		t.Fatalf("empty body keeps the default prompt: %q", got)
	}
}
