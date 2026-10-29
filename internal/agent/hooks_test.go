//go:build !windows

package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"openuai/internal/hooks"
	"openuai/internal/llm"
	"openuai/internal/tools"
)

// scriptProvider calls the tool `calls` times, then answers with text.
type scriptProvider struct {
	mockProvider
	tool  string
	args  map[string]string
	calls int
	n     int
}

func (p *scriptProvider) ChatWithTools(_ context.Context, _ []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.n++
	if p.n <= p.calls {
		return &llm.Response{Model: "mock"}, []llm.ToolCall{{ID: "t" + string(rune('0'+p.n)), Name: p.tool, Arguments: p.args}}, nil
	}
	return &llm.Response{Content: "finished", Model: "mock"}, nil, nil
}

type recTool struct {
	mu   *sync.Mutex
	args *[]map[string]string
}

func (t recTool) Definition() tools.Definition {
	return tools.Definition{Name: "bash", Description: "x", RequiresPermission: "session"}
}
func (t recTool) Execute(_ context.Context, a map[string]string) tools.Result {
	t.mu.Lock()
	*t.args = append(*t.args, a)
	t.mu.Unlock()
	return tools.Result{Output: "ran " + a["command"]}
}

func newHookAgent(t *testing.T, p llm.Provider, cfg hooks.Config, asked *int) (*Agent, *[]map[string]string) {
	t.Helper()
	s := &hooks.Set{}
	s.Add(cfg, "test")
	var mu sync.Mutex
	var got []map[string]string
	reg := tools.NewRegistry()
	reg.Register(recTool{mu: &mu, args: &got})
	pm := NewPermissionManager(t.TempDir(), func(context.Context, string, string) (PermissionLevel, bool) {
		*asked++
		return PermAlways, true
	})
	a := New(Config{Provider: p, Model: "mock", Registry: reg, Permissions: pm, Hooks: func() *hooks.Set { return s }, WorkDir: t.TempDir()})
	return a, &got
}

func lastToolResult(a *Agent) string {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == llm.RoleToolResult {
			return a.messages[i].Content
		}
	}
	return ""
}

func TestPreToolUseBlocks(t *testing.T) {
	asked := 0
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "rm -rf /tmp/x"}, calls: 1}
	a, got := newHookAgent(t, p, hooks.Config{hooks.PreToolUse: {{Matcher: "bash", Hooks: []hooks.Handler{{Type: "command", Command: `grep -q 'rm -rf' && { echo 'no rm -rf' >&2; exit 2; }; exit 0`}}}}}, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 || asked != 0 {
		t.Fatalf("tool must not run nor ask: ran=%d asked=%d", len(*got), asked)
	}
	if r := lastToolResult(a); !strings.Contains(r, "blocked by a hook: no rm -rf") {
		t.Fatalf("model must see the reason: %q", r)
	}
}

func TestPreToolUseAllowAndUpdate(t *testing.T) {
	asked := 0
	p := &scriptProvider{tool: "bash", args: map[string]string{"command": "ls"}, calls: 1}
	a, got := newHookAgent(t, p, hooks.Config{
		hooks.PreToolUse:  {{Hooks: []hooks.Handler{{Type: "command", Command: `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"command":"ls -la"}}}'`}}}},
		hooks.PostToolUse: {{Matcher: "bash", Hooks: []hooks.Handler{{Type: "command", Command: `echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"POST68"}}'`}}}},
	}, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if asked != 0 || len(*got) != 1 || (*got)[0]["command"] != "ls -la" {
		t.Fatalf("asked=%d got=%v", asked, *got)
	}
	if r := lastToolResult(a); !strings.Contains(r, "ran ls -la") || !strings.Contains(r, "POST68") {
		t.Fatalf("result: %q", r)
	}
}

func TestStopHookContinuesOnce(t *testing.T) {
	asked := 0
	flag := t.TempDir() + "/once"
	p := &scriptProvider{calls: 0}
	a, _ := newHookAgent(t, p, hooks.Config{hooks.Stop: {{Hooks: []hooks.Handler{{Type: "command", Command: `[ -f '` + flag + `' ] && exit 0; touch '` + flag + `'; echo 'run the tests first' >&2; exit 2`}}}}}, &asked)
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if p.n != 2 {
		t.Fatalf("the stop hook must make the agent answer twice, got %d", p.n)
	}
	found := false
	for _, m := range a.messages {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "run the tests first") {
			found = true
		}
	}
	if !found {
		t.Fatal("the hook's reason must reach the model")
	}
}

func TestSubagentDefinitionHookOnlyForIt(t *testing.T) {
	dir := t.TempDir()
	def := &hooks.Set{}
	def.Add(hooks.Config{hooks.SubagentStart: {{Hooks: []hooks.Handler{{Type: "command", Command: `echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"SECRET_WORD_68"}}'`}}}}}, "agent:x")
	var seen []string
	var mu sync.Mutex
	prov := &promptRecorder{seen: &seen, mu: &mu}
	res := RunSubAgents(context.Background(), SubAgentConfig{Provider: prov, Model: "mock", Registry: tools.NewRegistry(), Permissions: NewPermissionManager("", nil), MaxConcurrent: 2, WorkDir: dir},
		[]SubAgentTask{{ID: "with", Description: "a", Agent: "x", Hooks: def}, {ID: "without", Description: "b"}})
	if res[0].Error != "" || res[1].Error != "" {
		t.Fatal(res)
	}
	n := 0
	for _, s := range seen {
		if strings.Contains(s, "SECRET_WORD_68") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("definition hook must apply to its sub-agent only, seen in %d prompts", n)
	}
}

type promptRecorder struct {
	mockProvider
	seen *[]string
	mu   *sync.Mutex
}

func (p *promptRecorder) ChatWithTools(_ context.Context, msgs []llm.Message, _ string, _ []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.mu.Lock()
	*p.seen = append(*p.seen, msgs[0].Content)
	p.mu.Unlock()
	return &llm.Response{Content: "ok", Model: "mock"}, nil, nil
}
