package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"openuai/internal/llm"
	"openuai/internal/tools"
)

// scriptedProvider returns the scripted tool calls one per model call and
// records the model, effort and tool names each call got.
type scriptedProvider struct {
	mu      sync.Mutex
	script  [][]llm.ToolCall
	calls   int
	models  []string
	efforts []string
	tools   [][]string
}

func (p *scriptedProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{Content: "x"}, nil
}
func (p *scriptedProvider) Name() string     { return "scripted" }
func (p *scriptedProvider) Models() []string { return []string{"base"} }
func (p *scriptedProvider) ChatWithTools(ctx context.Context, _ []llm.Message, model string, defs []llm.ToolDefinition) (*llm.Response, []llm.ToolCall, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = append(p.models, model)
	p.efforts = append(p.efforts, llm.EffortFrom(ctx))
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	p.tools = append(p.tools, names)
	i := p.calls
	p.calls++
	if i < len(p.script) && p.script[i] != nil {
		return &llm.Response{}, p.script[i], nil
	}
	return &llm.Response{Content: "done"}, nil, nil
}

// fnTool is a tool backed by a function.
type fnTool struct {
	name, perm string
	fn         func(ctx context.Context, args map[string]string) tools.Result
}

func (t fnTool) Definition() tools.Definition {
	return tools.Definition{Name: t.name, RequiresPermission: t.perm}
}
func (t fnTool) Execute(ctx context.Context, args map[string]string) tools.Result {
	return t.fn(ctx, args)
}

func TestSkillTurnChangeAppliesUntilNextMessage(t *testing.T) {
	ran := map[string]int{}
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "skill", perm: "none", fn: func(ctx context.Context, _ map[string]string) tools.Result {
		turn := tools.TurnFrom(ctx)
		if turn == nil {
			return tools.Result{Error: "no turn"}
		}
		turn.Apply(tools.TurnChange{
			Skill:    "s",
			Allow:    func(tool, arg string) bool { return tool == "guarded" && strings.HasPrefix(arg, "ok") },
			Disallow: []string{"web"},
			Model:    "small", Effort: "low",
		})
		return tools.Result{Output: "SKILL CONTENT"}
	}})
	reg.Register(fnTool{name: "guarded", perm: "always", fn: func(context.Context, map[string]string) tools.Result {
		ran["guarded"]++
		return tools.Result{Output: "ran"}
	}})
	reg.Register(fnTool{name: "web", perm: "none", fn: func(context.Context, map[string]string) tools.Result {
		ran["web"]++
		return tools.Result{Output: "web"}
	}})
	asked := 0
	pm := NewPermissionManager(t.TempDir(), func(context.Context, string, string) (PermissionLevel, bool) {
		asked++
		return PermAlways, false
	})
	p := &scriptedProvider{script: [][]llm.ToolCall{
		{{ID: "1", Name: "skill", Arguments: map[string]string{}}},
		{{ID: "2", Name: "guarded", Arguments: map[string]string{"command": "ok go"}}},
		{{ID: "3", Name: "web", Arguments: map[string]string{}}},
		nil, // turn 1 ends
		{{ID: "4", Name: "guarded", Arguments: map[string]string{"command": "ok again"}}},
	}}
	ag := New(Config{Provider: p, Model: "base", Registry: reg, Permissions: pm})
	if err := ag.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if ran["guarded"] != 1 || asked != 0 {
		t.Fatalf("allowed-tools must run without asking: ran=%v asked=%d", ran, asked)
	}
	if ran["web"] != 0 {
		t.Fatal("a disallowed tool ran")
	}
	if p.models[0] != "base" || p.models[1] != "small" || p.efforts[1] != "low" || p.efforts[0] != "" {
		t.Fatalf("model/effort: %v %v", p.models, p.efforts)
	}
	for _, n := range p.tools[1] {
		if n == "web" {
			t.Fatalf("disallowed tool offered: %v", p.tools[1])
		}
	}
	if ag.Effort() != "low" {
		t.Fatalf("effort %q", ag.Effort())
	}
	// Next message: everything clears.
	if err := ag.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if asked != 1 || ran["guarded"] != 1 {
		t.Fatalf("the grant must clear with the next message: asked=%d ran=%v", asked, ran)
	}
	if last := p.models[len(p.models)-1]; last != "base" {
		t.Fatalf("model override must clear: %v", p.models)
	}
}

func TestToolContextHasAndOnToolUse(t *testing.T) {
	reg := tools.NewRegistry()
	var had bool
	reg.Register(fnTool{name: "probe", perm: "none", fn: func(ctx context.Context, _ map[string]string) tools.Result {
		had = tools.TurnFrom(ctx).Has("MARKER-123")
		return tools.Result{Output: "ok"}
	}})
	p := &scriptedProvider{script: [][]llm.ToolCall{{{ID: "1", Name: "probe", Arguments: map[string]string{"path": "a.go"}}}}}
	var seen string
	ag := New(Config{Provider: p, Model: "m", Registry: reg, OnToolUse: func(tool string, args map[string]string) string {
		seen = tool + ":" + args["path"]
		return "NOTE-FOR-MODEL"
	}})
	if err := ag.Run(context.Background(), "text with MARKER-123"); err != nil {
		t.Fatal(err)
	}
	if !had || seen != "probe:a.go" {
		t.Fatalf("had=%v seen=%q", had, seen)
	}
	found := false
	for _, m := range ag.Messages() {
		if m.Role == llm.RoleToolResult && strings.Contains(m.Content, "NOTE-FOR-MODEL") {
			found = true
		}
	}
	if !found {
		t.Fatal("the OnToolUse note must reach the tool result")
	}
}

func TestConfigAllowLastsForTheAgent(t *testing.T) {
	reg := tools.NewRegistry()
	n := 0
	reg.Register(fnTool{name: "guarded", perm: "always", fn: func(context.Context, map[string]string) tools.Result { n++; return tools.Result{Output: "x"} }})
	pm := NewPermissionManager(t.TempDir(), func(context.Context, string, string) (PermissionLevel, bool) { return PermAlways, false })
	p := &scriptedProvider{script: [][]llm.ToolCall{{{ID: "1", Name: "guarded", Arguments: map[string]string{}}}}}
	ag := New(Config{Provider: p, Model: "m", Registry: reg, Permissions: pm, Allow: func(tool, _ string) bool { return tool == "guarded" }})
	if err := ag.Run(context.Background(), "go"); err != nil || n != 1 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}
